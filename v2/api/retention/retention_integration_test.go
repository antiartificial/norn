package retention

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/archive"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

type retentionFixture struct {
	t          *testing.T
	db         *store.DB
	pipe       *pipeline.Pipeline
	operations *store.PGOperationStore
	request    pipeline.EnqueueRequest
	signer     store.AcceptanceSigner
	hot        *saga.PostgresStore
	objects    *archive.LocalStore
	root       string
	archiver   *Archiver
}

// retentionServer starts a private scoped PostgreSQL server: pruning holds
// on every undeclared control-role session of the database, so pruning
// tests cannot share a server with concurrently running test packages.
func retentionServer(t *testing.T) (*pgtest.Server, string) {
	t.Helper()
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_control")
	return server, server.URL("norn_control")
}

// retentionPool opens a pool on the private server declaring the given
// reader contract component (as every Norn binary's pool does).
func retentionPool(t *testing.T, databaseURL string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	store.DeclareReaderContract(config, "retention-test")
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newRetentionFixture(t *testing.T) *retentionFixture {
	t.Helper()
	_, databaseURL := retentionServer(t)
	return newRetentionFixtureAt(t, databaseURL)
}

func newRetentionFixtureAt(t *testing.T, databaseURL string) *retentionFixture {
	t.Helper()
	ctx := context.Background()
	pool := retentionPool(t, databaseURL)
	db := &store.DB{Pool: pool}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	signer, err := store.NewHMACAcceptanceSigner("retention-test-signing-key-000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := operations.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hot := saga.NewPostgresStore(pool)
	root := filepath.Join(t.TempDir(), "evidence")
	objects, err := archive.OpenLocal(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { objects.Close() })
	pipe := &pipeline.Pipeline{DB: db, SagaStore: hot, OperationStore: operations, AppsDir: t.TempDir()}
	request := pipeline.EnqueueRequest{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/test", Subject: "operator"}, Key: "k", Audit: store.AcceptanceAuditContext{Source: "retention-test"}}
	return &retentionFixture{t: t, db: db, pipe: pipe, operations: operations, request: request, signer: signer, hot: hot, objects: objects, root: root,
		archiver: &Archiver{DB: db, Archive: objects, Signer: signer, Mode: ModeShadow, Quiet: 0, BackfillAfter: time.Hour, MinAge: 0, BatchSize: 20}}
}

func (f *retentionFixture) terminalFleetGitHubReceipt() store.AcceptedOperation {
	f.t.Helper()
	accepted, err := f.operations.Accept(context.Background(), f.terminalFleetGitHubAcceptance())
	if err != nil {
		f.t.Fatal(err)
	}
	return accepted
}

func (f *retentionFixture) terminalFleetGitHubAcceptance() store.OperationAcceptance {
	f.t.Helper()
	now := time.Now().UTC()
	finished := now
	op := model.Operation{ID: uuid.NewString(), Kind: "fleet.github.pull-request", Ref: uuid.NewString(), Status: model.OperationSucceeded,
		Source: "control-api", Message: "fleet pull request opened", Payload: map[string]interface{}{"planId": "plan-1", "url": "https://github.example.test/acme/fleet/pull/1"},
		Metadata: map[string]interface{}{}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1}
	identity := store.OperationRequestIdentity{Authority: f.request.Authority, Actor: f.request.Actor, Kind: op.Kind, Resource: op.Ref, Key: "fleet-receipt-" + uuid.NewString()}
	acceptance := store.OperationAcceptance{Identity: identity, Operation: op, Audit: store.AcceptanceAuditContext{Source: "retention-test"}, Semantics: map[string]interface{}{"fleetGitHub": map[string]interface{}{"planId": op.Ref, "kind": op.Kind}}}
	var err error
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		f.t.Fatal(err)
	}
	return acceptance
}

// finishedSaga accepts a signed operation, logs saga events, and finishes it
// through the claimed-operation path (which creates the outbox intent).
func (f *retentionFixture) finishedSaga(app string, events int) *model.Operation {
	f.t.Helper()
	ctx := context.Background()
	request := f.request
	request.Key = "retention-" + uuid.NewString()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: app, SagaID: uuid.NewString(), Status: model.OperationQueued, Source: "control-api",
		Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, MaxAttempts: 1}
	accepted, err := f.pipe.QueueOperation(ctx, op, request)
	if err != nil {
		f.t.Fatal(err)
	}
	log := saga.NewWithID(f.hot, accepted.Operation.SagaID, app, "pipeline", "snapshot")
	for index := range events {
		if err := log.Log(ctx, "step.progress", "evidence event "+string(rune('a'+index)), map[string]string{"n": string(rune('0' + index))}); err != nil {
			f.t.Fatal(err)
		}
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at = CASE WHEN id=$1 THEN now() - interval '1 second' ELSE now() + interval '1 hour' END WHERE status='queued'`, accepted.Operation.ID); err != nil {
		f.t.Fatal(err)
	}
	claimed, claim, err := f.db.ClaimNextOperation(ctx, "retention-worker", time.Minute, []string{"app.preflight"})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		f.t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := f.db.FinishClaimedOperation(ctx, claim, model.OperationSucceeded, "done", nil); err != nil {
		f.t.Fatal(err)
	}
	return &accepted.Operation
}

func (f *retentionFixture) intents(sagaID string) []store.EvidenceIntent {
	f.t.Helper()
	intents, err := f.db.EvidenceIntentsForSubject(context.Background(), "saga", sagaID)
	if err != nil {
		f.t.Fatal(err)
	}
	return intents
}

func (f *retentionFixture) hotCount(sagaID string) int {
	f.t.Helper()
	var count int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM saga_events WHERE saga_id=$1`, sagaID).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func TestEvidenceManualRecoveryHoldRequiresVerifiedReconciliationArchive(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	image := "registry.example/manual@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	source := model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: "manual-recovery-app", SagaID: uuid.NewString(), Status: model.OperationQueued,
		Source: "control-api", Payload: map[string]interface{}{"deploymentId": "deployment-1", "specDigest": digest}, Metadata: map[string]interface{}{}, StartedAt: now, MaxAttempts: 1}
	sourceAcceptance := store.OperationAcceptance{
		Identity:   store.OperationRequestIdentity{Authority: f.request.Authority, Actor: f.request.Actor, Kind: source.Kind, Resource: source.App, Key: "manual-deployment-" + uuid.NewString()},
		Operation:  source,
		Deployment: &model.Deployment{ID: "deployment-1", App: source.App, CommitSHA: strings.Repeat("c", 40), SourceKind: "git_clone", SourceRef: "refs/heads/main", ImageTag: image, SpecDigest: digest, Environment: "staging", SagaID: source.SagaID, Status: model.StatusQueued, StartedAt: now},
		Regions:    []model.ResolvedRegion{{Name: "west", NomadRegion: "west", Datacenters: []string{"dc1"}, TrafficWeight: 100}},
		Audit:      store.AcceptanceAuditContext{Source: "retention-test"},
	}
	var err error
	sourceAcceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(sourceAcceptance)
	if err != nil {
		t.Fatal(err)
	}
	acceptedSource, err := f.operations.Accept(ctx, sourceAcceptance)
	if err != nil {
		t.Fatal(err)
	}
	if err := saga.NewWithID(f.hot, source.SagaID, source.App, "pipeline", "deploy").Log(ctx, "deployment.failed", "executor lease expired", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := f.db.ClaimNextOperation(ctx, "retention-worker", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil || claimed.ID != source.ID {
		t.Fatalf("source claim=%+v err=%v", claimed, err)
	}
	if err := f.db.StartDeploymentStep(ctx, model.DeploymentStep{DeploymentID: acceptedSource.Deployment.ID, App: source.App, SagaID: source.SagaID,
		Step: "submit", Kind: model.DeploymentStepMutable, Status: model.DeploymentStepComplete, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.UpdateDeploymentRegion(ctx, acceptedSource.Deployment.ID, "west", model.StatusSubmitting, "eval-source", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	candidate, err := f.operations.DeploymentReconciliationCandidate(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 {
		t.Fatalf("source archive = %+v, %v", report, err)
	}
	sourceIntent := f.intents(source.SagaID)[0]
	if holds, err := f.db.EvidenceHolds(ctx, sourceIntent, 0); err != nil || len(holds) != 1 || holds[0] != "manual-recovery" {
		t.Fatalf("source holds before reconciliation = %v, %v", holds, err)
	}

	// A normal claimed-operation completion cannot manufacture a successful
	// reconciliation receipt, even when its signed request carries a source
	// link. CompleteDeploymentReconciliation is the only success path.
	forged := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: f.request.Authority, Actor: f.request.Actor, Kind: "app.deployment-reconcile", Resource: source.App, Key: "forged-reconciliation-" + uuid.NewString()},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.deployment-reconcile", App: source.App, Ref: source.ID, Status: model.OperationQueued, Source: "operator", StartedAt: now, MaxAttempts: 1,
			Payload:  map[string]interface{}{"sourceOperationId": source.ID, "deploymentId": acceptedSource.Deployment.ID, "imageTag": image, "specDigest": digest},
			Metadata: map[string]interface{}{"sourceOperationId": source.ID, "deploymentId": acceptedSource.Deployment.ID, "imageTag": image, "specDigest": digest, "observedAt": now.Format(time.RFC3339Nano)}},
		Audit: store.AcceptanceAuditContext{Source: "retention-test"},
	}
	forged.Fingerprint, err = store.CanonicalOperationRequestFingerprint(forged)
	if err != nil {
		t.Fatal(err)
	}
	acceptedForged, err := f.operations.Accept(ctx, forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, acceptedForged.Operation.ID); err != nil {
		t.Fatal(err)
	}
	claimed, forgedClaim, err := f.db.ClaimNextOperation(ctx, "retention-worker", time.Minute, []string{"app.deployment-reconcile"})
	if err != nil || claimed == nil || claimed.ID != acceptedForged.Operation.ID {
		t.Fatalf("forged reconciliation claim=%+v err=%v", claimed, err)
	}
	if err := f.db.FinishClaimedOperation(ctx, forgedClaim, model.OperationSucceeded, "forged reconciliation success", forged.Operation.Metadata); err == nil {
		t.Fatal("generic completion forged a reconciliation success")
	}
	if err := f.db.FinishClaimedOperation(ctx, forgedClaim, model.OperationFailed, "forged reconciliation refused", nil); err != nil {
		t.Fatal(err)
	}

	reconciliation := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: f.request.Authority, Actor: f.request.Actor, Kind: "app.deployment-reconcile", Resource: source.App, Key: "deployment-reconcile-" + uuid.NewString()},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.deployment-reconcile", App: source.App, SagaID: uuid.NewString(), Ref: source.ID, Status: model.OperationQueued, Source: "operator", StartedAt: now, MaxAttempts: 1,
			Payload:  map[string]interface{}{"sourceOperationId": source.ID, "deploymentId": acceptedSource.Deployment.ID, "imageTag": candidate.ImageTag, "specDigest": digest},
			Metadata: map[string]interface{}{"sourceOperationId": source.ID, "deploymentId": acceptedSource.Deployment.ID}},
		Audit:     store.AcceptanceAuditContext{Source: "retention-test"},
		Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
	}
	reconciliation.Fingerprint, err = store.CanonicalOperationRequestFingerprint(reconciliation)
	if err != nil {
		t.Fatal(err)
	}
	acceptedReconciliation, err := f.operations.Accept(ctx, reconciliation)
	if err != nil {
		t.Fatal(err)
	}
	if err := saga.NewWithID(f.hot, reconciliation.Operation.SagaID, reconciliation.Operation.App, "pipeline", "deployment-reconcile").Log(ctx, "deployment.reconciled", "projection repaired", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, reconciliation.Operation.ID); err != nil {
		t.Fatal(err)
	}
	claimed, reconciliationClaim, err := f.db.ClaimNextOperation(ctx, "retention-worker", time.Minute, []string{"app.deployment-reconcile"})
	if err != nil || claimed == nil || claimed.ID != reconciliation.Operation.ID {
		t.Fatalf("reconciliation claim=%+v err=%v", claimed, err)
	}
	if err := f.db.CompleteDeploymentReconciliation(ctx, reconciliationClaim, candidate, time.Now()); err != nil {
		t.Fatal(err)
	}
	if holds, err := f.db.EvidenceHolds(ctx, sourceIntent, 0); err != nil || !slices.Contains(holds, "manual-recovery") {
		t.Fatalf("pending reconciliation archive released source hold: %v, %v", holds, err)
	}
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 {
		t.Fatalf("reconciliation archive = %+v, %v", report, err)
	}
	reconciliationIntent := f.intents(reconciliation.Operation.SagaID)[0]
	if reconciliationIntent.State != "verified" || reconciliationIntent.OperationID != acceptedReconciliation.Operation.ID {
		t.Fatalf("reconciliation archive proof = %+v", reconciliationIntent)
	}
	if holds, err := f.db.EvidenceHolds(ctx, sourceIntent, 0); err != nil || slices.Contains(holds, "manual-recovery") {
		t.Fatalf("verified reconciliation archive did not release manual hold: %v, %v", holds, err)
	}
	failedSource, err := f.db.GetOperation(ctx, acceptedSource.Operation.ID)
	if err != nil || failedSource.Status != model.OperationFailed || failedSource.Metadata["manualRecoveryRequired"] != true {
		t.Fatalf("source receipt changed=%+v err=%v", failedSource, err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET metadata=metadata || '{"externalEffectRecoveryPending":true}'::jsonb WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if holds, err := f.db.EvidenceHolds(ctx, sourceIntent, 0); err != nil || !slices.Contains(holds, "manual-recovery") {
		t.Fatalf("external-effect recovery hold released by reconciliation: %v, %v", holds, err)
	}
}

func TestEvidenceArchiveLifecycleWithHoldsPruningAndArchiveReads(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	op := f.finishedSaga("shop", 3)

	// The outbox intent was created atomically with the terminal transition.
	intents := f.intents(op.SagaID)
	if len(intents) != 1 || intents[0].State != "pending" || intents[0].OperationID != op.ID {
		t.Fatalf("outbox = %+v", intents)
	}

	// Shadow: publish, read back, verify, compare; never delete.
	report, err := f.archiver.RunOnce(ctx)
	if err != nil || report.Published != 1 || len(report.PublishErrors) != 0 {
		t.Fatalf("shadow pass = %+v, %v", report, err)
	}
	intents = f.intents(op.SagaID)
	if intents[0].State != "verified" || intents[0].EventCount != 3 || intents[0].ObjectSHA256 == "" {
		t.Fatalf("verified intent = %+v", intents[0])
	}
	if report, err = f.archiver.RunOnce(ctx); err != nil || report.ShadowCompared == 0 || len(report.ShadowMismatch) != 0 || f.hotCount(op.SagaID) != 3 {
		t.Fatalf("shadow comparison = %+v, %v (hot %d)", report, err, f.hotCount(op.SagaID))
	}

	// The archived bundle holds the exact signed acceptance bytes.
	bundle, err := LoadBundle(ctx, f.objects, intents[0])
	if err != nil {
		t.Fatal(err)
	}
	var canonical []byte
	if err := f.db.Pool.QueryRow(ctx, `SELECT canonical_bytes FROM operation_acceptance_intents WHERE operation_id=$1`, op.ID).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	if bundle.Acceptance == nil || string(bundle.Acceptance.CanonicalBytes) != string(canonical) {
		t.Fatal("archived acceptance bytes differ from the signed bytes")
	}
	if err := f.signer.Verify(ctx, store.AcceptanceSignature{Algorithm: bundle.Acceptance.SigningAlgorithm, KeyID: bundle.Acceptance.SigningKeyID, Value: bundle.Acceptance.Signature}, bundle.Acceptance.CanonicalBytes); err != nil {
		t.Fatalf("archived signature: %v", err)
	}

	// A late event after sealing stays hot and becomes a supplementary bundle.
	if err := saga.NewWithID(f.hot, op.SagaID, "shop", "pipeline", "snapshot").Log(ctx, "published", "late publication", nil); err != nil {
		t.Fatal(err)
	}
	if report, err = f.archiver.RunOnce(ctx); err != nil || report.Supplementary != 1 || report.Published != 1 {
		t.Fatalf("supplementary pass = %+v, %v", report, err)
	}
	intents = f.intents(op.SagaID)
	if len(intents) != 2 || intents[1].Sequence != 2 || intents[1].EventCount != 1 {
		t.Fatalf("supplementary intents = %+v", intents)
	}

	// Pruning honours the retention age hold, then deletes exactly the
	// archived events.
	f.archiver.Mode, f.archiver.MinAge = ModePrune, time.Hour
	if report, err = f.archiver.RunOnce(ctx); err != nil || report.Held != 2 || report.Pruned != 0 || f.hotCount(op.SagaID) != 4 {
		t.Fatalf("held pass = %+v, %v", report, err)
	}
	f.archiver.MinAge = 0
	if report, err = f.archiver.RunOnce(ctx); err != nil || report.Pruned != 4 || f.hotCount(op.SagaID) != 0 {
		t.Fatalf("prune pass = %+v, %v (hot %d)", report, err, f.hotCount(op.SagaID))
	}
	for _, intent := range f.intents(op.SagaID) {
		if intent.State != "pruned" || intent.PrunedAt == nil {
			t.Fatalf("pruned intent = %+v", intent)
		}
	}

	// Archive-aware reads return the full history from the archive.
	history := &HistoryStore{Hot: f.hot, DB: f.db, Archive: f.objects}
	events, err := history.ListBySaga(ctx, op.SagaID)
	if err != nil || len(events) != 4 || events[3].Action != "published" {
		t.Fatalf("archived history = %+v, %v", events, err)
	}
	if app, err := history.SagaApp(ctx, op.SagaID); err != nil || app != "shop" {
		t.Fatalf("saga app = %q, %v", app, err)
	}

	// Index recovery without the historical index rows: the archive alone
	// restores readable history.
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM evidence_archive_intents`); err != nil {
		t.Fatal(err)
	}
	recovery, err := RestoreIndex(ctx, f.db, f.objects, f.signer)
	if err != nil || recovery.Restored != 2 || len(recovery.Rejected) != 0 {
		t.Fatalf("index recovery = %+v, %v", recovery, err)
	}
	if events, err = history.ListBySaga(ctx, op.SagaID); err != nil || len(events) != 4 {
		t.Fatalf("history after index recovery = %d, %v", len(events), err)
	}
	if again, err := RestoreIndex(ctx, f.db, f.objects, f.signer); err != nil || again.Restored != 0 || again.Existing != 2 {
		t.Fatalf("repeated index recovery = %+v, %v", again, err)
	}

	// A corrupted archive object is never served as history.
	path := filepath.Join(f.root, filepath.FromSlash(f.intents(op.SagaID)[0].ObjectKey))
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "evidence event", "EVIDENCE EVENT", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var unavailable *ErrArchivedHistoryUnavailable
	if _, err := history.ListBySaga(ctx, op.SagaID); !errors.As(err, &unavailable) {
		t.Fatalf("corrupted history read = %v", err)
	}
}

func TestTerminalFleetGitHubReceiptArchivesOriginalSignedBytes(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	accepted := f.terminalFleetGitHubReceipt()

	intents, err := f.db.EvidenceIntentsForSubject(ctx, "operation", accepted.Operation.ID)
	if err != nil || len(intents) != 1 || intents[0].State != "pending" {
		t.Fatalf("atomic non-saga reservation = %+v, %v", intents, err)
	}
	if intents[0].SubjectID != accepted.Operation.ID || intents[0].OperationID != accepted.Operation.ID {
		t.Fatalf("operation subject = %+v", intents[0])
	}
	// Historical signed Fleet receipts predate this outbox shape. Simulate an
	// interrupted older writer and prove the bounded backfill restores only
	// the accepted terminal operation.
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM evidence_archive_intents WHERE id=$1`, intents[0].ID); err != nil {
		t.Fatal(err)
	}
	f.archiver.BackfillAfter = 0
	var originalCanonical, originalRequest []byte
	if err := f.db.Pool.QueryRow(ctx, `SELECT canonical_bytes,request_canonical_bytes FROM operation_acceptance_intents WHERE operation_id=$1`, accepted.Operation.ID).Scan(&originalCanonical, &originalRequest); err != nil {
		t.Fatal(err)
	}

	report, err := f.archiver.RunOnce(ctx)
	if err != nil || report.Backfilled != 1 || report.Published != 1 || len(report.PublishErrors) != 0 {
		t.Fatalf("archive pass = %+v, %v", report, err)
	}
	intents, err = f.db.EvidenceIntentsForSubject(ctx, "operation", accepted.Operation.ID)
	if err != nil || len(intents) != 1 || intents[0].State != "verified" || intents[0].ObjectKey == "" {
		t.Fatalf("verified operation archive = %+v, %v", intents, err)
	}
	bundle, err := LoadBundle(ctx, f.objects, intents[0])
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Subject.Kind != "operation" || bundle.Subject.ID != accepted.Operation.ID || bundle.Acceptance == nil ||
		string(bundle.Acceptance.CanonicalBytes) != string(originalCanonical) || string(bundle.Acceptance.RequestCanonicalBytes) != string(originalRequest) {
		t.Fatalf("archived signed receipt was not byte exact: %#v", bundle)
	}
	if err := f.archiver.verifyAcceptance(ctx, bundle); err != nil {
		t.Fatalf("archived signed receipt did not verify: %v", err)
	}
	// The saga pruner must not claim that it can remove replay-critical hot
	// acceptance state for this operation subject.
	f.archiver.Mode = ModePrune
	report, err = f.archiver.RunOnce(ctx)
	if err != nil || report.Pruned != 0 {
		t.Fatalf("operation receipt prune pass = %+v, %v", report, err)
	}
	intents, err = f.db.EvidenceIntentsForSubject(ctx, "operation", accepted.Operation.ID)
	if err != nil || intents[0].State != "verified" {
		t.Fatalf("operation receipt was incorrectly pruned: %+v, %v", intents, err)
	}
}

func TestTerminalFunctionInvocationArchivesOnlyPublicExecutionAndAttemptEvidence(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	keys, err := store.NewPrivateInvocationKeyRing("archive-key", map[string][]byte{"archive-key": bytes.Repeat([]byte{0x42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	privateBody := "function-archive-private-body-canary"
	op := model.Operation{ID: uuid.NewString(), Kind: store.PrivateInvocationOperationKind, App: "function-archive", SagaID: "saga-function-correlation", Ref: "main", Status: model.OperationQueued,
		Source: "retention-test", Risk: "write", Payload: map[string]interface{}{"process": "resize", "imageTag": "image@sha256:abcdef"}, Metadata: map[string]interface{}{}, MaxAttempts: 1}
	acceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: f.request.Authority, Actor: f.request.Actor, Kind: op.Kind, Resource: "app/function-archive/process/resize", Key: "function-archive-" + uuid.NewString()},
		Operation: op,
		Audit:     f.request.Audit,
	}
	accepted, err := f.operations.AcceptPrivateInvocation(ctx, acceptance, store.PrivateInvocationInput{Body: privateBody, Method: "POST", Path: "/private/function-archive"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	_, claim, err := f.db.ClaimNextOperation(ctx, "function-archive-worker", time.Minute, []string{store.PrivateInvocationOperationKind})
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := f.db.RecordFunctionInvocationEffectStage(ctx, claim, store.FunctionInvocationJobAttempt, "function-archive-resize", digest); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.MarkFunctionInvocationEffectAttempt(ctx, claim, store.FunctionInvocationJobAttempt, "function-archive-resize", digest); err != nil {
		t.Fatal(err)
	}
	exitCode, duration := 0, int64(12)
	execution := store.FuncExecution{ID: accepted.Operation.ID, App: accepted.Operation.App, Process: "resize", Status: "complete", ExitCode: &exitCode, DurationMs: &duration, StartedAt: time.Now().UTC().Add(-time.Second)}
	if err := f.db.FinishClaimedFunctionInvocation(ctx, claim, execution, model.OperationSucceeded, "function completed", map[string]interface{}{"jobId": "function-archive-resize"}); err != nil {
		t.Fatal(err)
	}

	report, err := f.archiver.RunOnce(ctx)
	if err != nil || report.Published != 1 || len(report.PublishErrors) != 0 {
		t.Fatalf("archive pass=%+v err=%v", report, err)
	}
	intents, err := f.db.EvidenceIntentsForSubject(ctx, "operation", accepted.Operation.ID)
	if err != nil || len(intents) != 1 || intents[0].State != "verified" {
		t.Fatalf("function archive intent=%+v err=%v", intents, err)
	}
	data, _, err := f.objects.Get(ctx, intents[0].ObjectKey, archive.MaxBundleBytes)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(privateBody)) || bytes.Contains(data, []byte("/private/function-archive")) || bytes.Contains(data, []byte(`"effects"`)) {
		t.Fatalf("function archive exposed private request or raw effects: %s", data)
	}
	bundle, err := LoadBundle(ctx, f.objects, intents[0])
	if err != nil || len(bundle.FunctionExecution) == 0 || string(bundle.FunctionEffectAttempts) == "[]" || len(bundle.Operation) == 0 || len(bundle.Effects) != 0 || bundle.Acceptance == nil ||
		bytes.Contains(bundle.Acceptance.RequestCanonicalBytes, []byte(privateBody)) || bytes.Contains(bundle.Acceptance.RequestCanonicalBytes, []byte("/private/function-archive")) {
		t.Fatalf("function bundle=%+v err=%v", bundle, err)
	}

	f.archiver.Mode = ModePrune
	report, err = f.archiver.RunOnce(ctx)
	if err != nil || report.Retired != 0 || report.Held != 1 || len(report.PublishErrors) != 0 {
		t.Fatalf("function retention pass=%+v err=%v", report, err)
	}
	var acceptances int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_acceptance_intents WHERE operation_id=$1`, accepted.Operation.ID).Scan(&acceptances); err != nil || acceptances != 1 {
		t.Fatalf("function acceptance retirement=%d err=%v", acceptances, err)
	}
}

func TestExpiredFleetGitHubReceiptRetiresHotPayloadAndReservationAtomically(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	expiring, err := store.NewPGOperationStore(f.db, f.signer, store.AcceptancePolicy{ReplayTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	acceptance := f.terminalFleetGitHubAcceptance()
	receiptID := uuid.NewString()
	now := time.Now().UTC().Add(-time.Hour)
	if err := f.db.ReserveMutationAudit(ctx, &store.MutationAuditEvent{ID: receiptID, PrincipalSubject: "operator", Method: "POST", Path: "/fleet/github", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.FinishMutationAudit(ctx, receiptID, "/fleet/github", 202, "succeeded", now.Add(time.Second), 1, "receipt-digest"); err != nil {
		t.Fatal(err)
	}
	acceptance.Audit.RequestReceiptID = receiptID
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := expiring.Accept(ctx, acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operation_request_identities SET replay_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, accepted.RequestIdentityID); err != nil {
		t.Fatal(err)
	}
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 {
		t.Fatalf("publish receipt = %+v, %v", report, err)
	}

	// A failure during retirement must leave the replay live as well as both
	// hot rows. The trigger represents a transactional failure at that boundary.
	if _, err := f.db.Pool.Exec(ctx, `CREATE FUNCTION reject_acceptance_retirement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test acceptance retirement failure'; END $$;
		CREATE TRIGGER reject_acceptance_retirement BEFORE DELETE ON operation_acceptance_intents FOR EACH ROW EXECUTE FUNCTION reject_acceptance_retirement();`); err != nil {
		t.Fatal(err)
	}
	f.archiver.Mode = ModePrune
	if report, err := f.archiver.RunOnce(ctx); err != nil || len(report.PublishErrors) != 1 || report.Retired != 0 {
		t.Fatalf("injected retirement failure = %+v, %v", report, err)
	}
	var intents, reservations int
	if err := f.db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_acceptance_intents WHERE operation_id=$1), (SELECT count(*) FROM signed_acceptance_byte_reservations WHERE operation_id=$1)`, accepted.Operation.ID).Scan(&intents, &reservations); err != nil {
		t.Fatal(err)
	}
	if intents != 1 || reservations != 1 {
		t.Fatalf("failed retirement was not atomic: intents=%d reservations=%d", intents, reservations)
	}
	var prematurelyExpired bool
	if err := f.db.Pool.QueryRow(ctx, `SELECT replay_expired_at IS NOT NULL FROM operation_request_identities WHERE id=$1`, accepted.RequestIdentityID).Scan(&prematurelyExpired); err != nil || prematurelyExpired {
		t.Fatalf("failed retirement expired replay: expired=%v err=%v", prematurelyExpired, err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operation_request_identities SET replay_expires_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, accepted.RequestIdentityID); err != nil {
		t.Fatal(err)
	}
	if replayed, err := expiring.Resolve(ctx, acceptance.Identity, acceptance.Fingerprint); err != nil || !replayed.Replayed {
		t.Fatalf("failed retirement lost replay: %+v, %v", replayed, err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operation_request_identities SET replay_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, accepted.RequestIdentityID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `DROP TRIGGER reject_acceptance_retirement ON operation_acceptance_intents; DROP FUNCTION reject_acceptance_retirement()`); err != nil {
		t.Fatal(err)
	}

	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Retired != 1 {
		t.Fatalf("retire receipt = %+v, %v", report, err)
	}
	if err := f.db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_acceptance_intents WHERE operation_id=$1), (SELECT count(*) FROM signed_acceptance_byte_reservations WHERE operation_id=$1)`, accepted.Operation.ID).Scan(&intents, &reservations); err != nil {
		t.Fatal(err)
	}
	if intents != 0 || reservations != 0 {
		t.Fatalf("retired hot rows: intents=%d reservations=%d", intents, reservations)
	}
	var fingerprint string
	var expired bool
	if err := f.db.Pool.QueryRow(ctx, `SELECT fingerprint_digest,replay_expired_at IS NOT NULL FROM operation_request_identities WHERE id=$1`, accepted.RequestIdentityID).Scan(&fingerprint, &expired); err != nil {
		t.Fatal(err)
	}
	if fingerprint != acceptance.Fingerprint.Digest || !expired {
		t.Fatalf("identity tombstone = fingerprint=%q expired=%v", fingerprint, expired)
	}
	var retiredReceiptRows int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM retired_operation_acceptances WHERE operation_id=$1`, accepted.Operation.ID).Scan(&retiredReceiptRows); err != nil || retiredReceiptRows != 1 {
		t.Fatalf("retired acceptance audit link = %d, %v", retiredReceiptRows, err)
	}
	if deleted, err := f.db.PruneMutationAudits(ctx, time.Now().Add(time.Hour)); err != nil || deleted != 0 {
		t.Fatalf("retired receipt audit prune = %d, %v", deleted, err)
	}
	if _, err := f.db.GetMutationAudit(ctx, receiptID); err != nil {
		t.Fatalf("retired receipt audit was pruned: %v", err)
	}
	if _, err := expiring.Resolve(ctx, acceptance.Identity, acceptance.Fingerprint); !errors.Is(err, store.ErrAcceptanceExpired) {
		t.Fatalf("retired replay = %v", err)
	}
	changed := acceptance.Fingerprint
	changed.Digest = strings.Repeat("f", 64)
	if _, err := expiring.Resolve(ctx, acceptance.Identity, changed); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("retired collision = %v", err)
	}
	if _, err := expiring.Accept(ctx, acceptance); !errors.Is(err, store.ErrAcceptanceExpired) {
		t.Fatalf("retired acceptance replay = %v", err)
	}
	intentsForSubject, err := f.db.EvidenceIntentsForSubject(ctx, "operation", accepted.Operation.ID)
	if err != nil || len(intentsForSubject) != 1 || intentsForSubject[0].State != "pruned" {
		t.Fatalf("archive index after retirement = %+v, %v", intentsForSubject, err)
	}
	if _, err := LoadBundle(ctx, f.objects, intentsForSubject[0]); err != nil {
		t.Fatalf("archive receipt after retirement = %v", err)
	}
	// Archive-only index recovery must fail closed when the matching control
	// database backup (including its permanent replay identity) is absent.
	fresh := newRetentionFixture(t)
	if _, err := RestoreIndex(ctx, fresh.db, f.objects, f.signer); err == nil || !strings.Contains(err.Error(), "restore the matching control database backup") {
		t.Fatalf("archive-only retired receipt recovery error = %v", err)
	}
}

func TestTerminalFleetGitHubReceiptReserveExhaustionLeavesRecoverableAcceptance(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	if err := f.db.SetEvidenceReservePolicy(ctx, store.EvidenceReservePolicy{Enabled: true, MaxPending: 1, MaxPendingAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	first := f.terminalFleetGitHubReceipt()
	candidate := f.terminalFleetGitHubAcceptance()
	_, err := f.operations.Accept(ctx, candidate)
	var exhausted *store.EvidenceReserveExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("exhausted reserve acceptance = %v", err)
	}
	var operations, identities, intents int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE id=$1`, candidate.Operation.ID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_request_identities WHERE operation_id=$1`, candidate.Operation.ID).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_acceptance_intents WHERE operation_id=$1`, candidate.Operation.ID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if operations != 0 || identities != 0 || intents != 0 {
		t.Fatalf("failed receipt left partial recovery state: operations=%d identities=%d intents=%d", operations, identities, intents)
	}
	if err := f.db.SetEvidenceReservePolicy(ctx, store.EvidenceReservePolicy{Enabled: true, MaxPending: 2, MaxPendingAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.operations.Accept(ctx, candidate)
	if err != nil {
		t.Fatalf("same external result could not be accepted after reserve recovery: %v", err)
	}
	if recovered.Operation.ID != candidate.Operation.ID || recovered.Operation.ID == first.Operation.ID {
		t.Fatalf("recovered receipt = %q, first=%q", recovered.Operation.ID, first.Operation.ID)
	}
	reserved, err := f.db.EvidenceIntentsForSubject(ctx, "operation", recovered.Operation.ID)
	if err != nil || len(reserved) != 1 || reserved[0].State != "pending" {
		t.Fatalf("recovered receipt archive reservation = %+v, %v", reserved, err)
	}
}

func TestRestoreIndexRestoresTerminalFleetGitHubReceiptBundle(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	accepted := f.terminalFleetGitHubReceipt()
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 {
		t.Fatalf("publish = %+v, %v", report, err)
	}
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM evidence_archive_intents`); err != nil {
		t.Fatal(err)
	}
	recovery, err := RestoreIndex(ctx, f.db, f.objects, f.signer)
	if err != nil || recovery.Restored != 1 || len(recovery.Rejected) != 0 {
		t.Fatalf("operation receipt index recovery = %+v, %v", recovery, err)
	}
	intents, err := f.db.EvidenceIntentsForSubject(ctx, "operation", accepted.Operation.ID)
	if err != nil || len(intents) != 1 || intents[0].SubjectKind != "operation" || intents[0].OperationID != accepted.Operation.ID || intents[0].State != "pruned" {
		t.Fatalf("restored operation receipt index = %+v, %v", intents, err)
	}
}

func TestLegacySagaEvidenceIntentToleratesMissingOperation(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	sagaID := "legacy-saga-" + uuid.NewString()
	if err := saga.NewWithID(f.hot, sagaID, "legacy", "test", "archive").Log(ctx, "completed", "legacy evidence", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO evidence_archive_intents (id,subject_kind,subject_id,app,operation_id,sequence,state)
		VALUES ($1,'saga',$2,'legacy','missing-legacy-operation',1,'pending')`, "ei-legacy-"+uuid.NewString(), sagaID); err != nil {
		t.Fatal(err)
	}
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 || len(report.PublishErrors) != 0 {
		t.Fatalf("legacy saga archive = %+v, %v", report, err)
	}
	intents, err := f.db.EvidenceIntentsForSubject(ctx, "saga", sagaID)
	if err != nil || len(intents) != 1 || intents[0].State != "verified" {
		t.Fatalf("legacy saga intent = %+v, %v", intents, err)
	}
}

func TestOperationReceiptAdoptionRejectsMissingOrSwappedAcceptance(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	first, second := f.terminalFleetGitHubReceipt(), f.terminalFleetGitHubReceipt()
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 2 {
		t.Fatalf("publish = %+v, %v", report, err)
	}
	firstIntent, secondIntent := mustOperationIntent(t, f, first.Operation.ID), mustOperationIntent(t, f, second.Operation.ID)
	firstBundle, err := LoadBundle(ctx, f.objects, firstIntent)
	if err != nil {
		t.Fatal(err)
	}
	secondBundle, err := LoadBundle(ctx, f.objects, secondIntent)
	if err != nil {
		t.Fatal(err)
	}
	missing := *firstBundle
	missing.Acceptance = nil
	if _, err := archive.Seal(&missing); err == nil {
		t.Fatal("operation bundle without signed acceptance was sealed")
	}
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM evidence_archive_intents WHERE id=$1`, firstIntent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.BackfillNonSagaFleetGitHubEvidenceIntents(ctx, 0, 1); err != nil {
		t.Fatal(err)
	}
	_, err = f.db.ProcessPendingEvidenceIntent(ctx, 0, func(ctx context.Context, intent store.EvidenceIntent, source store.EvidenceSource) (store.EvidencePublication, error) {
		forged := *firstBundle
		acceptance := *secondBundle.Acceptance
		forged.Acceptance = &acceptance
		data, err := archive.Seal(&forged)
		if err != nil {
			return store.EvidencePublication{}, err
		}
		objects, err := archive.OpenLocal(filepath.Join(t.TempDir(), "forged-operation"), 1<<20)
		if err != nil {
			return store.EvidencePublication{}, err
		}
		defer objects.Close()
		if _, err := objects.PutImmutable(ctx, firstIntent.ObjectKey, data); err != nil {
			return store.EvidencePublication{}, err
		}
		return (&Archiver{Archive: objects, Signer: f.signer}).adopt(ctx, firstIntent.ObjectKey, intent, source)
	})
	if err == nil || !strings.Contains(err.Error(), "differs from current signed receipt source") {
		t.Fatalf("swapped operation acceptance adoption = %v", err)
	}
}

func mustOperationIntent(t *testing.T, f *retentionFixture, operationID string) store.EvidenceIntent {
	t.Helper()
	intents, err := f.db.EvidenceIntentsForSubject(context.Background(), "operation", operationID)
	if err != nil || len(intents) != 1 {
		t.Fatalf("operation intent %s = %+v, %v", operationID, intents, err)
	}
	return intents[0]
}

// failingStore simulates archive outages and crash boundaries.
type failingStore struct {
	archive.Store
	failPut          bool
	failVerifyOnce   bool
	verifyFailedOnce bool
}

func (s *failingStore) PutImmutable(ctx context.Context, key string, data []byte) (archive.ObjectInfo, error) {
	if s.failPut {
		return archive.ObjectInfo{}, errors.New("archive outage")
	}
	return s.Store.PutImmutable(ctx, key, data)
}

func (s *failingStore) Verify(ctx context.Context, expected archive.ObjectInfo) error {
	if s.failVerifyOnce && !s.verifyFailedOnce {
		s.verifyFailedOnce = true
		return errors.New("crash after publication, before acknowledgement")
	}
	return s.Store.Verify(ctx, expected)
}

func TestEvidenceArchiveOutagesCrashBoundariesAndHolds(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	flaky := &failingStore{Store: f.objects, failPut: true}
	f.archiver.Archive = flaky
	f.archiver.Mode = ModePrune

	// Outage: evidence stays pending and hot, the error is visible.
	op := f.finishedSaga("outage", 2)
	if report, err := f.archiver.RunOnce(ctx); err != nil || len(report.PublishErrors) != 1 || report.Pruned != 0 {
		t.Fatalf("outage pass = %+v, %v", report, err)
	}
	health, err := ReadHealth(ctx, f.db)
	if err != nil || health.Pending != 1 || !strings.Contains(health.LastError, "archive outage") || f.hotCount(op.SagaID) != 2 {
		t.Fatalf("outage health = %+v, %v", health, err)
	}

	// Crash after publication, before acknowledgement: the object exists but
	// the intent is still pending. The retry republishes identical bytes (a
	// verified duplicate) and acknowledges.
	flaky.failPut, flaky.failVerifyOnce = false, true
	if report, _ := f.archiver.RunOnce(ctx); len(report.PublishErrors) != 1 {
		t.Fatalf("crash pass = %+v", report)
	}
	if keys, _ := f.objects.List(ctx, "evidence/saga/"); len(keys) != 1 || f.intents(op.SagaID)[0].State != "pending" {
		t.Fatalf("after crash: keys %v, intent %+v", keys, f.intents(op.SagaID))
	}
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 || report.Pruned != 2 {
		t.Fatalf("recovery pass = %+v, %v", report, err)
	}

	// Crash, then a late event before the retry: the retry's bundle differs,
	// so the existing object is adopted only because every event in it is
	// this saga's hot evidence; the late event gets its own sequence.
	crashed := f.finishedSaga("adopt", 2)
	flaky.failVerifyOnce, flaky.verifyFailedOnce = true, false
	_, _ = f.archiver.RunOnce(ctx)
	if err := saga.NewWithID(f.hot, crashed.SagaID, "adopt", "pipeline", "snapshot").Log(ctx, "published", "late", nil); err != nil {
		t.Fatal(err)
	}
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 {
		t.Fatalf("adoption pass = %+v, %v", report, err)
	}
	if intent := f.intents(crashed.SagaID)[0]; intent.EventCount != 2 {
		t.Fatalf("adopted intent = %+v", intent)
	}
	if _, err := f.archiver.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if intents := f.intents(crashed.SagaID); len(intents) != 2 || intents[1].EventCount != 1 {
		t.Fatalf("late event after adoption = %+v", intents)
	}

	// Holds: a saga whose deployment is the app's current deployment stays
	// hot even after verification.
	held := f.finishedSaga("held-app", 1)
	deployment := &model.Deployment{ID: uuid.NewString(), App: "held-app", CommitSHA: "c", ImageTag: "i", SagaID: held.SagaID, Status: model.StatusDeployed, StartedAt: time.Now().UTC()}
	if err := f.db.InsertDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE deployments SET status='deployed' WHERE id=$1`, deployment.ID); err != nil {
		t.Fatal(err)
	}
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Held == 0 || f.hotCount(held.SagaID) != 1 {
		t.Fatalf("deployment hold = %+v, %v", report, err)
	}
	intent := f.intents(held.SagaID)[0]
	if holds, err := f.db.EvidenceHolds(ctx, intent, 0); err != nil || len(holds) != 1 || holds[0] != "deployment-current-or-rollback" {
		t.Fatalf("holds = %v, %v", holds, err)
	}

	// Capacity exhaustion is an outage too: nothing is discarded.
	tiny, err := archive.OpenLocal(filepath.Join(t.TempDir(), "tiny"), 10)
	if err != nil {
		t.Fatal(err)
	}
	f.archiver.Archive = tiny
	full := f.finishedSaga("full", 1)
	if report, _ := f.archiver.RunOnce(ctx); len(report.PublishErrors) == 0 || f.hotCount(full.SagaID) != 1 {
		t.Fatalf("full archive = %+v", report)
	}
	if intent := f.intents(full.SagaID)[0]; intent.State != "pending" || !strings.Contains(intent.LastError, "capacity") {
		t.Fatalf("full archive intent = %+v", intent)
	}
}
