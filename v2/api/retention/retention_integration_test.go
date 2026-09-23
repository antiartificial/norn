package retention

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	op := model.Operation{ID: uuid.NewString(), Kind: "app.snapshot", App: app, SagaID: uuid.NewString(), Status: model.OperationQueued, Source: "control-api",
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
	claimed, claim, err := f.db.ClaimNextOperation(ctx, "retention-worker", time.Minute, []string{"app.snapshot"})
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
