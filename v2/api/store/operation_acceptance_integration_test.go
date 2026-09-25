package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/model"
)

const acceptanceTestKey = "acceptance-integration-signing-key-000000000000"

func acceptanceIntegrationStores(t *testing.T, count int) ([]*PGOperationStore, []*DB) {
	t.Helper()
	pools := schemaMigrationTestPools(t, count)
	dbs := make([]*DB, count)
	for index, pool := range pools {
		dbs[index] = &DB{Pool: pool}
	}
	if err := Migrate(dbs[0]); err != nil {
		t.Fatal(err)
	}
	signer, err := NewHMACAcceptanceSigner(acceptanceTestKey)
	if err != nil {
		t.Fatal(err)
	}
	stores := make([]*PGOperationStore, count)
	for index := range dbs {
		stores[index], err = NewPGOperationStore(dbs[index], signer, AcceptancePolicy{})
		if err != nil {
			t.Fatal(err)
		}
	}
	return stores, dbs
}

func newAcceptance(t *testing.T, store *PGOperationStore, key, actor, app string, withDeployment bool) OperationAcceptance {
	t.Helper()
	authority, err := store.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := OperationAcceptance{
		Identity:  OperationRequestIdentity{Authority: authority, Actor: OperationActor{Issuer: "test-issuer", Subject: actor}, Kind: "app.preflight", Resource: app, Key: key},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: app, SagaID: uuid.NewString(), Ref: "sha256:source", Status: model.OperationQueued, Risk: "read-only", Source: "integration", MaxAttempts: 3, StartedAt: now, Payload: map[string]interface{}{"app": app, "qualification": map[string]interface{}{"deploymentId": "semantic-deployment"}}, Metadata: map[string]interface{}{"candidate": "candidate-a"}},
		Audit:     AcceptanceAuditContext{RequestID: "request-" + uuid.NewString(), CredentialID: "token-one", DeviceID: "device-one", Source: "integration-test", Scopes: []string{"write", "read", "read"}},
		Semantics: map[string]interface{}{"policy": "strict"},
	}
	if withDeployment {
		a.Identity.Kind, a.Operation.Kind = "app.deploy", "app.deploy"
		a.Deployment = &model.Deployment{ID: uuid.NewString(), App: app, SagaID: a.Operation.SagaID, Status: model.StatusQueued, SourceRef: "sha256:source", SourceChanges: []string{"b", "a"}, StartedAt: now}
		a.Operation.Payload["deploymentId"] = a.Deployment.ID
		a.Regions = []model.ResolvedRegion{{Name: "west", NomadRegion: "global", Datacenters: []string{"dc2", "dc1"}, TrafficWeight: 100}}
	}
	a.Fingerprint, err = CanonicalOperationRequestFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func regeneratedAcceptance(t *testing.T, original OperationAcceptance) OperationAcceptance {
	t.Helper()
	copy := original
	copy.Operation = original.Operation
	copy.Operation.ID, copy.Operation.SagaID = uuid.NewString(), uuid.NewString()
	copy.Operation.Payload = cloneJSONMap(t, original.Operation.Payload)
	copy.Operation.Metadata = cloneJSONMap(t, original.Operation.Metadata)
	copy.Audit = original.Audit
	copy.Audit.RequestID, copy.Audit.CredentialID = "request-"+uuid.NewString(), "rotated-token"
	if original.Deployment != nil {
		deployment := *original.Deployment
		deployment.ID, deployment.SagaID = uuid.NewString(), copy.Operation.SagaID
		copy.Deployment = &deployment
		copy.Operation.Payload["deploymentId"] = deployment.ID
	}
	var err error
	copy.Fingerprint, err = CanonicalOperationRequestFingerprint(copy)
	if err != nil {
		t.Fatal(err)
	}
	return copy
}

func cloneJSONMap(t *testing.T, input map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, err := jsonMarshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]interface{}
	if err := jsonUnmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func TestVerifyAcceptedOperationForRecoveryDoesNotRewriteFailedReceipt(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	input := newAcceptance(t, stores[0], "recovery-acceptance", "operator", "recovery-app", true)
	accepted, err := stores[0].Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operations SET status='failed',last_error='operation executor lease expired',
		metadata=metadata || '{"manualRecoveryRequired":true}'::jsonb,finished_at=now() WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	verified, err := stores[0].VerifyAcceptedOperation(ctx, accepted.Operation.ID)
	if err != nil || verified.Operation.Status != model.OperationFailed || verified.Deployment == nil || verified.Deployment.ID != accepted.Deployment.ID || verified.Intent.OperationID != accepted.Operation.ID {
		t.Fatalf("verified recovery source=%+v err=%v", verified, err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operations SET payload=payload || '{"deploymentId":"forged"}'::jsonb WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].VerifyAcceptedOperation(ctx, accepted.Operation.ID); !errors.Is(err, ErrAcceptanceSignature) {
		t.Fatalf("forged recovery source err=%v", err)
	}
	if _, err := stores[0].VerifyAcceptedOperation(ctx, uuid.NewString()); !errors.Is(err, ErrAcceptanceNotFound) {
		t.Fatalf("missing recovery source err=%v", err)
	}
}

// Indirections keep this test focused while still exercising encoding/json's
// normal number and nested-map behavior.
var jsonMarshal = func(value interface{}) ([]byte, error) { return json.Marshal(value) }
var jsonUnmarshal = func(data []byte, value interface{}) error { return json.Unmarshal(data, value) }

func TestAtomicAcceptanceConcurrentReplayConflictAndActorIsolation(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 2)
	first := newAcceptance(t, stores[0], "shared-key", "actor-a", "concurrent-app", true)
	second := regeneratedAcceptance(t, first)
	if first.Fingerprint != second.Fingerprint {
		t.Fatalf("regenerated IDs changed fingerprint: %+v %+v", first.Fingerprint, second.Fingerprint)
	}
	type result struct {
		accepted AcceptedOperation
		err      error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for index, input := range []OperationAcceptance{first, second} {
		wg.Add(1)
		go func(index int, input OperationAcceptance) {
			defer wg.Done()
			accepted, err := stores[index].Accept(context.Background(), input)
			results <- result{accepted, err}
		}(index, input)
	}
	wg.Wait()
	close(results)
	var accepted []AcceptedOperation
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		accepted = append(accepted, result.accepted)
	}
	if accepted[0].Operation.ID != accepted[1].Operation.ID || accepted[0].RequestIdentityID != accepted[1].RequestIdentityID || accepted[0].AcceptanceIntentID != accepted[1].AcceptanceIntentID {
		t.Fatalf("concurrent replay diverged: %+v %+v", accepted[0], accepted[1])
	}
	var identities, operations, intents, deployments, regions int
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM operation_request_identities),(SELECT count(*) FROM operations),(SELECT count(*) FROM operation_acceptance_intents),(SELECT count(*) FROM deployments),(SELECT count(*) FROM deployment_regions)`).Scan(&identities, &operations, &intents, &deployments, &regions); err != nil {
		t.Fatal(err)
	}
	if identities != 1 || operations != 1 || intents != 1 || deployments != 1 || regions != 1 {
		t.Fatalf("row counts identity=%d op=%d intent=%d deploy=%d regions=%d", identities, operations, intents, deployments, regions)
	}

	changed := regeneratedAcceptance(t, first)
	changed.Operation.Ref = "sha256:different"
	changed.Fingerprint, _ = CanonicalOperationRequestFingerprint(changed)
	if _, err := stores[0].Accept(context.Background(), changed); !errors.Is(err, ErrAcceptanceConflict) {
		t.Fatalf("changed fingerprint error=%v", err)
	}

	foreign := regeneratedAcceptance(t, first)
	foreign.Identity.Actor = OperationActor{Issuer: "other-issuer", Subject: "actor-a"}
	foreign.Fingerprint, _ = CanonicalOperationRequestFingerprint(foreign)
	foreignAccepted, err := stores[0].Accept(context.Background(), foreign)
	if err != nil {
		t.Fatal(err)
	}
	if foreignAccepted.Operation.ID == accepted[0].Operation.ID {
		t.Fatal("foreign actor received another actor's operation")
	}
}

func TestAtomicAcceptanceRollbackSigningAndLegacyCollision(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	a := newAcceptance(t, stores[0], "rollback-key", "actor", "rollback-app", true)
	stores[0].test.beforeIntent = func() error { return errors.New("forced intent failure") }
	if _, err := stores[0].Accept(context.Background(), a); err == nil {
		t.Fatal("forced intent failure accepted")
	}
	stores[0].test.beforeIntent = nil
	var rows int
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM operation_request_identities)+(SELECT count(*) FROM operations)+(SELECT count(*) FROM deployments)+(SELECT count(*) FROM operation_acceptance_intents)`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("transaction failure left %d rows", rows)
	}

	failing := &failingAcceptanceSigner{}
	failingStore, err := NewPGOperationStore(dbs[0], failing, AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failingStore.Accept(context.Background(), a); !errors.Is(err, ErrAcceptanceSignature) {
		t.Fatalf("signing failure error=%v", err)
	}

	legacy := &model.Operation{ID: uuid.NewString(), Kind: a.Operation.Kind, App: a.Operation.App, Status: model.OperationSucceeded, StartedAt: time.Now().UTC(), MaxAttempts: 1, Metadata: map[string]interface{}{"idempotencyKey": "legacy-raw-key"}}
	finished := legacy.StartedAt
	legacy.FinishedAt = &finished
	if err := dbs[0].InsertCompletedOperation(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	legacyRequest := regeneratedAcceptance(t, a)
	legacyRequest.Identity.Key = "legacy-raw-key"
	legacyRequest.Fingerprint, _ = CanonicalOperationRequestFingerprint(legacyRequest)
	if _, err := stores[0].Accept(context.Background(), legacyRequest); !errors.Is(err, ErrLegacyReplayAmbiguous) {
		t.Fatalf("legacy collision error=%v", err)
	}
}

type failingAcceptanceSigner struct{}

func (*failingAcceptanceSigner) Sign(context.Context, []byte) (AcceptanceSignature, error) {
	return AcceptanceSignature{}, errors.New("sign unavailable")
}
func (*failingAcceptanceSigner) Verify(context.Context, AcceptanceSignature, []byte) error {
	return errors.New("verify unavailable")
}

func TestAtomicAcceptanceLostAcknowledgmentClaimVisibilityAndAdmission(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 2)
	a := newAcceptance(t, stores[0], "visibility-key", "actor", "visibility-app", false)
	reached := make(chan struct{})
	release := make(chan struct{})
	stores[0].test.beforeCommit = func() { close(reached); <-release }
	result := make(chan error, 1)
	go func() { _, err := stores[0].Accept(context.Background(), a); result <- err }()
	<-reached
	claimed, _, err := dbs[1].ClaimNextOperation(context.Background(), "early-worker", time.Minute, []string{a.Operation.Kind})
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("uncommitted accepted operation was claimable: %+v", claimed)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	stores[0].test.beforeCommit = nil
	claimed, claim, err := dbs[1].ClaimNextOperation(context.Background(), "worker", time.Minute, []string{a.Operation.Kind})
	if err != nil || claimed == nil || claimed.ID != a.Operation.ID {
		t.Fatalf("committed claim=%+v err=%v", claimed, err)
	}
	if err := dbs[1].FinishClaimedOperation(context.Background(), claim, model.OperationSucceeded, "done", map[string]interface{}{"derived": "ok"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := stores[1].Resolve(context.Background(), a.Identity, a.Fingerprint)
	if err != nil || resolved.Operation.Status != model.OperationSucceeded {
		t.Fatalf("normal lifecycle replay=%+v err=%v", resolved, err)
	}

	first := newAcceptance(t, stores[0], "admission-a", "actor", "one-active-app", false)
	first.Admission.OneActiveMutablePerApp = true
	first.Fingerprint, _ = CanonicalOperationRequestFingerprint(first)
	second := newAcceptance(t, stores[1], "admission-b", "actor", "one-active-app", false)
	second.Admission.OneActiveMutablePerApp = true
	second.Fingerprint, _ = CanonicalOperationRequestFingerprint(second)
	admissionResults := make(chan error, 2)
	go func() { _, err := stores[0].Accept(context.Background(), first); admissionResults <- err }()
	go func() { _, err := stores[1].Accept(context.Background(), second); admissionResults <- err }()
	one, two := <-admissionResults, <-admissionResults
	if (one == nil) == (two == nil) {
		t.Fatalf("admission results=%v %v, want one success", one, two)
	}
	if one != nil && !errors.Is(one, ErrAcceptanceAdmission) {
		t.Fatal(one)
	}
	if two != nil && !errors.Is(two, ErrAcceptanceAdmission) {
		t.Fatal(two)
	}

	lost := newAcceptance(t, stores[1], "lost-ack", "actor", "lost-app", false)
	lostStoreDB := &DB{Pool: dbs[0].Pool}
	lostStore, err := NewPGOperationStore(lostStoreDB, stores[0].signer, AcceptancePolicy{ResolveTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	lostStore.test.afterCommit = func() error { dbs[0].Pool.Close(); return errors.New("connection lost after commit") }
	_, err = lostStore.Accept(context.Background(), lost)
	if !errors.Is(err, ErrAcceptanceIndeterminate) {
		t.Fatalf("lost acknowledgment error=%v", err)
	}
	resolved, err = stores[1].Resolve(context.Background(), lost.Identity, lost.Fingerprint)
	if err != nil || resolved.Operation.ID != lost.Operation.ID {
		t.Fatalf("fresh resolve after indeterminate=%+v err=%v", resolved, err)
	}
}

func TestAtomicAcceptanceIntegrityRetentionAndCompletedReceipt(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	old := time.Now().UTC().AddDate(-2, 0, 0)
	linkedID, unlinkedID := uuid.NewString(), uuid.NewString()
	for _, id := range []string{linkedID, unlinkedID} {
		event := &MutationAuditEvent{ID: id, PrincipalSubject: "actor", Method: "POST", Path: "/test", StartedAt: old}
		if err := dbs[0].ReserveMutationAudit(ctx, event); err != nil {
			t.Fatal(err)
		}
		if err := dbs[0].FinishMutationAudit(ctx, id, "/test", 202, "succeeded", old.Add(time.Second), 1000, "historic-signature"); err != nil {
			t.Fatal(err)
		}
	}
	a := newAcceptance(t, stores[0], "retention-key", "actor", "retention-app", true)
	a.Audit.RequestReceiptID = linkedID
	a.Fingerprint, _ = CanonicalOperationRequestFingerprint(a)
	accepted, err := stores[0].Accept(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if deleted, err := dbs[0].PruneMutationAudits(ctx, time.Now().AddDate(-1, 0, 0)); err != nil || deleted != 1 {
		t.Fatalf("prune deleted=%d err=%v", deleted, err)
	}
	if _, err := dbs[0].GetMutationAudit(ctx, linkedID); err != nil {
		t.Fatalf("linked receipt pruned: %v", err)
	}
	if _, err := dbs[0].GetMutationAudit(ctx, unlinkedID); err == nil {
		t.Fatal("unlinked old receipt was retained")
	}

	oldSigner := stores[0].signer
	newSigner, err := NewHMACAcceptanceSigner("rotated-acceptance-signing-key-00000000000", acceptanceTestKey)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := NewPGOperationStore(dbs[0], newSigner, AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.Resolve(ctx, a.Identity, a.Fingerprint); err != nil {
		t.Fatalf("retained-key resolve: %v", err)
	}
	stores[0].signer = oldSigner
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operations SET ref='tampered' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Resolve(ctx, a.Identity, a.Fingerprint); !errors.Is(err, ErrAcceptanceSignature) {
		t.Fatalf("tampered operation error=%v", err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operations SET ref=$2 WHERE id=$1`, accepted.Operation.ID, a.Operation.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE deployment_regions SET desired_weight=99 WHERE deployment_id=$1`, accepted.Deployment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Resolve(ctx, a.Identity, a.Fingerprint); !errors.Is(err, ErrAcceptanceSignature) {
		t.Fatalf("tampered region error=%v", err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE deployment_regions SET desired_weight=100 WHERE deployment_id=$1`, accepted.Deployment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE deployments SET saga_id='tampered-saga' WHERE id=$1`, accepted.Deployment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Resolve(ctx, a.Identity, a.Fingerprint); !errors.Is(err, ErrAcceptanceSignature) {
		t.Fatalf("tampered deployment saga error=%v", err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE deployments SET saga_id=$2 WHERE id=$1`, accepted.Deployment.ID, a.Operation.SagaID); err != nil {
		t.Fatal(err)
	}

	derived := newAcceptance(t, stores[0], "derived-lifecycle", "actor", "derived-app", true)
	derived.Operation.Source = "pipeline"
	derived.Deployment.CommitSHA = "main"
	derived.Deployment.SourceRef = "main"
	derived.Deployment.SourceKind = ""
	derived.Fingerprint, _ = CanonicalOperationRequestFingerprint(derived)
	derivedAccepted, err := stores[0].Accept(ctx, derived)
	if err != nil {
		t.Fatal(err)
	}
	derivedResult := *derivedAccepted.Deployment
	derivedResult.CommitSHA = "0123456789abcdef0123456789abcdef01234567"
	derivedResult.ImageTag = "registry.example/app@sha256:artifact"
	derivedResult.SourceKind = "git_clone"
	derivedResult.SourceRef = "main"
	derivedResult.SourceDirty = false
	derivedResult.SourceChanges = []string{"README.md"}
	derivedResult.Status = model.StatusDeployed
	if err := dbs[0].UpdateDeploymentResult(ctx, &derivedResult); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Resolve(ctx, derived.Identity, derived.Fingerprint); err != nil {
		t.Fatalf("ordinary derived deployment lifecycle rejected: %v", err)
	}
	pinned := newAcceptance(t, stores[0], "pinned-lifecycle", "actor", "pinned-app", true)
	pinned.Operation.Source = "release-control-api"
	pinned.Operation.Ref = "0123456789abcdef0123456789abcdef01234567"
	pinned.Deployment.CommitSHA = pinned.Operation.Ref
	pinned.Deployment.ImageTag = "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pinned.Deployment.SourceRef = pinned.Operation.Ref
	pinned.Deployment.SourceKind = ""
	pinned.Fingerprint, _ = CanonicalOperationRequestFingerprint(pinned)
	pinnedAccepted, err := stores[0].Accept(ctx, pinned)
	if err != nil {
		t.Fatal(err)
	}
	pinnedResult := *pinnedAccepted.Deployment
	pinnedResult.SourceKind = "git_clone"
	pinnedResult.Status = model.StatusDeployed
	if err := dbs[0].UpdateDeploymentResult(ctx, &pinnedResult); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Resolve(ctx, pinned.Identity, pinned.Fingerprint); err != nil {
		t.Fatalf("pinned release source observation rejected: %v", err)
	}
	pinnedResult.CommitSHA = "ffffffffffffffffffffffffffffffffffffffff"
	if err := dbs[0].UpdateDeploymentResult(ctx, &pinnedResult); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Resolve(ctx, pinned.Identity, pinned.Fingerprint); !errors.Is(err, ErrAcceptanceSignature) {
		t.Fatalf("pinned release SHA tamper error=%v", err)
	}

	completed := newAcceptance(t, stores[0], "completed-key", "actor", "planning-app", false)
	completed.Operation.Status = model.OperationSucceeded
	finished := completed.Operation.StartedAt
	completed.Operation.FinishedAt = &finished
	completed.Fingerprint, _ = CanonicalOperationRequestFingerprint(completed)
	if _, err := stores[0].Accept(ctx, completed); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := dbs[0].ClaimNextOperation(ctx, "planning-worker", time.Minute, []string{completed.Operation.Kind})
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil && claimed.ID == completed.Operation.ID {
		t.Fatal("completed planning receipt was claimable")
	}
}

func TestAcceptedOperationRequiresIntentBeforeClaim(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	a := newAcceptance(t, stores[0], "missing-intent", "actor", "missing-intent-app", false)
	payload, _ := json.Marshal(a.Operation.Payload)
	metadata, _ := json.Marshal(a.Operation.Metadata)
	_, err := dbs[0].Pool.Exec(context.Background(), `INSERT INTO operations(id,kind,app,saga_id,ref,status,risk,source,message,payload,metadata,attempts,max_attempts,next_attempt_at,started_at,updated_at,acceptance_required) VALUES($1,$2,$3,$4,$5,'queued',$6,$7,'',$8,$9,0,3,now(),now(),now(),true)`, a.Operation.ID, a.Operation.Kind, a.Operation.App, a.Operation.SagaID, a.Operation.Ref, a.Operation.Risk, a.Operation.Source, payload, metadata)
	if err != nil {
		t.Fatal(err)
	}
	claimed, _, err := dbs[0].ClaimNextOperation(context.Background(), "worker", time.Minute, []string{a.Operation.Kind})
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("operation without acceptance intent was claimed: %+v", claimed)
	}
}

func TestControlAuthorityIsPersistedAndExpectedMatchIsEnforced(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 2)
	first, err := stores[0].Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := stores[1].Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("authorities %q %q", first, second)
	}
	signer, _ := NewHMACAcceptanceSigner(acceptanceTestKey)
	mismatched, err := NewPGOperationStore(dbs[0], signer, AcceptancePolicy{ExpectedAuthority: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mismatched.Authority(context.Background()); !errors.Is(err, ErrAcceptanceAuthority) {
		t.Fatalf("authority mismatch error=%v", err)
	}
	var version, minimumWriter int64
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT current_migration_version,minimum_writer_version FROM norn_schema_compatibility WHERE singleton=true`).Scan(&version, &minimumWriter); err != nil {
		t.Fatal(err)
	}
	if version != 33 || minimumWriter != MySQLRestoreReceiptWriterVersion {
		t.Fatalf("schema version=%d minimum writer=%d", version, minimumWriter)
	}
}

func TestAcceptanceReplayExpiryIsOptInDurableAndHoldAware(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()

	legacy := newAcceptance(t, stores[0], "indefinite-replay", "operator", "indefinite-app", false)
	legacy.Operation.Status = model.OperationSucceeded
	finished := legacy.Operation.StartedAt
	legacy.Operation.FinishedAt = &finished
	legacy.Fingerprint, _ = CanonicalOperationRequestFingerprint(legacy)
	if _, err := stores[0].Accept(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	var contract string
	var expiresAt, expiredAt *time.Time
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT replay_contract_version,replay_expires_at,replay_expired_at FROM operation_request_identities WHERE operation_id=$1`, legacy.Operation.ID).Scan(&contract, &expiresAt, &expiredAt); err != nil {
		t.Fatal(err)
	}
	if contract != "" || expiresAt != nil || expiredAt != nil {
		t.Fatalf("indefinite identity unexpectedly has expiry metadata: %q %v %v", contract, expiresAt, expiredAt)
	}
	if replayed, err := stores[0].Resolve(ctx, legacy.Identity, legacy.Fingerprint); err != nil || !replayed.Replayed {
		t.Fatalf("indefinite replay=%+v err=%v", replayed, err)
	}

	expiring, err := NewPGOperationStore(dbs[0], stores[0].signer, AcceptancePolicy{ReplayTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	a := newAcceptance(t, expiring, "expiring-replay", "operator", "expiring-app", false)
	accepted, err := expiring.Accept(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operation_request_identities SET replay_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, accepted.RequestIdentityID); err != nil {
		t.Fatal(err)
	}
	if replayed, err := expiring.Resolve(ctx, a.Identity, a.Fingerprint); err != nil || !replayed.Replayed {
		t.Fatalf("active operation must hold replay: %+v err=%v", replayed, err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operations SET status='succeeded',finished_at=clock_timestamp() WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := expiring.Resolve(ctx, a.Identity, a.Fingerprint); !errors.Is(err, ErrAcceptanceExpired) {
		t.Fatalf("eligible identity expiry error=%v", err)
	}
	if _, err := expiring.ResolveIdentity(ctx, a.Identity); !errors.Is(err, ErrAcceptanceExpired) {
		t.Fatalf("durable tombstone resolve error=%v", err)
	}
	var tombstoned bool
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT replay_contract_version=$2 AND replay_expired_at IS NOT NULL FROM operation_request_identities WHERE id=$1`, accepted.RequestIdentityID, OperationReplayContractVersion).Scan(&tombstoned); err != nil || !tombstoned {
		t.Fatalf("tombstone=%v err=%v", tombstoned, err)
	}
	changed := a.Fingerprint
	changed.Digest = strings.Repeat("f", 64)
	if changed == a.Fingerprint {
		changed.Digest = strings.Repeat("e", 64)
	}
	if _, err := expiring.Resolve(ctx, a.Identity, changed); !errors.Is(err, ErrAcceptanceConflict) {
		t.Fatalf("changed fingerprint after expiry error=%v", err)
	}
}

func TestAcceptanceReplayExpiryWaitsForEffectResolution(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	expiring, err := NewPGOperationStore(dbs[0], stores[0].signer, AcceptancePolicy{ReplayTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	a := newAcceptance(t, expiring, "effect-held-replay", "operator", "effect-held-app", false)
	a.Operation.Status = model.OperationSucceeded
	finished := a.Operation.StartedAt
	a.Operation.FinishedAt = &finished
	a.Fingerprint, _ = CanonicalOperationRequestFingerprint(a)
	accepted, err := expiring.Accept(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operation_request_identities SET replay_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, accepted.RequestIdentityID); err != nil {
		t.Fatal(err)
	}
	authority, _ := expiring.Authority(ctx)
	if _, err := dbs[0].Pool.Exec(ctx, `INSERT INTO operation_effects
		(id,generation,authority,resource,operation_id,claim_owner,claim_generation,stage,input_digest,launch_payload,supervisor,supervisor_execution_id,lifecycle)
		VALUES($1,1,$2::uuid,$3,$4,'test-owner',1,'test','sha256:'||repeat('a',64),'{}','test-supervisor',$5,'reserved')`,
		uuid.NewString(), authority, "app/effect-held-app/deploy", accepted.Operation.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if replayed, err := expiring.Resolve(ctx, a.Identity, a.Fingerprint); err != nil || !replayed.Replayed {
		t.Fatalf("unresolved effect must hold replay: %+v err=%v", replayed, err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operation_effects SET lifecycle='resolved',resolution_decision='never-launched',resolved_at=clock_timestamp(),updated_at=clock_timestamp() WHERE operation_id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := expiring.Resolve(ctx, a.Identity, a.Fingerprint); !errors.Is(err, ErrAcceptanceExpired) {
		t.Fatalf("resolved effect expiry error=%v", err)
	}
}

func TestAcceptanceReplayExpiryKeepsDeploymentRollbackCandidates(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	expiring, err := NewPGOperationStore(dbs[0], stores[0].signer, AcceptancePolicy{ReplayTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	a := newAcceptance(t, expiring, "rollback-held-replay", "operator", "rollback-held-app", true)
	accepted, err := expiring.Accept(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operation_request_identities SET replay_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, accepted.RequestIdentityID); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operations SET status='succeeded',finished_at=clock_timestamp() WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE deployments SET status='deployed',finished_at=clock_timestamp() WHERE id=$1`, accepted.Deployment.ID); err != nil {
		t.Fatal(err)
	}
	if replayed, err := expiring.Resolve(ctx, a.Identity, a.Fingerprint); err != nil || !replayed.Replayed {
		t.Fatalf("current deployment must hold replay: %+v err=%v", replayed, err)
	}
	for index := 1; index <= 2; index++ {
		if _, err := dbs[0].Pool.Exec(ctx, `INSERT INTO deployments
			(id,app,commit_sha,image_tag,environment,saga_id,status,source_kind,source_ref,source_dirty,source_changes,started_at,finished_at)
			SELECT $1,app,commit_sha,image_tag,environment,$2,'deployed',source_kind,source_ref,source_dirty,source_changes,started_at+$3*interval '1 hour',clock_timestamp()
			FROM deployments WHERE id=$4`, uuid.NewString(), uuid.NewString(), index, accepted.Deployment.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := expiring.Resolve(ctx, a.Identity, a.Fingerprint); !errors.Is(err, ErrAcceptanceExpired) {
		t.Fatalf("retired rollback candidate expiry error=%v", err)
	}
}

func TestAcceptanceReservesEvidenceAtomicallyAndReplaysDuringExhaustion(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 2)
	ctx := context.Background()
	if err := dbs[0].SetEvidenceReservePolicy(ctx, EvidenceReservePolicy{Enabled: true, MaxPending: 1, MaxPendingAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	first := newAcceptance(t, stores[0], "reserve-first", "operator", "reserve-app", false)
	accepted, err := stores[0].Accept(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	var pending, operations, reservations int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM evidence_archive_intents WHERE state = 'pending'), (SELECT count(*) FROM operations), (SELECT count(*) FROM signed_acceptance_byte_reservations)`).Scan(&pending, &operations, &reservations); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || operations != 1 || reservations != 1 {
		t.Fatalf("first acceptance pending=%d operations=%d reservations=%d", pending, operations, reservations)
	}
	// Identity resolution precedes reserve admission, so a retry remains an
	// exact replay even when the one-slot reserve is now full.
	replayed, err := stores[1].Accept(ctx, regeneratedAcceptance(t, first))
	if err != nil || !replayed.Replayed || replayed.Operation.ID != accepted.Operation.ID {
		t.Fatalf("exhausted replay=%+v err=%v", replayed, err)
	}

	second := newAcceptance(t, stores[1], "reserve-second", "operator", "reserve-app-two", false)
	var exhausted *EvidenceReserveExhaustedError
	if _, err := stores[1].Accept(ctx, second); !errors.As(err, &exhausted) {
		t.Fatalf("new acceptance under exhaustion error=%v", err)
	}
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM evidence_archive_intents WHERE state = 'pending'), (SELECT count(*) FROM operations), (SELECT count(*) FROM signed_acceptance_byte_reservations)`).Scan(&pending, &operations, &reservations); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || operations != 1 || reservations != 1 {
		t.Fatalf("exhausted acceptance wrote rows pending=%d operations=%d reservations=%d", pending, operations, reservations)
	}
}

func TestAcceptanceEvidenceReservationSerializesConcurrentNewRequests(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 2)
	ctx := context.Background()
	if err := dbs[0].SetEvidenceReservePolicy(ctx, EvidenceReservePolicy{Enabled: true, MaxPending: 1, MaxPendingAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	requests := []OperationAcceptance{
		newAcceptance(t, stores[0], "reserve-race-one", "operator", "reserve-race-one", false),
		newAcceptance(t, stores[1], "reserve-race-two", "operator", "reserve-race-two", false),
	}
	errs := make(chan error, len(requests))
	var wait sync.WaitGroup
	for index := range requests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := stores[index].Accept(ctx, requests[index])
			errs <- err
		}(index)
	}
	wait.Wait()
	close(errs)
	var acceptedCount, exhaustedCount int
	for err := range errs {
		if err == nil {
			acceptedCount++
			continue
		}
		var exhausted *EvidenceReserveExhaustedError
		if errors.As(err, &exhausted) {
			exhaustedCount++
			continue
		}
		t.Fatalf("concurrent acceptance error=%v", err)
	}
	if acceptedCount != 1 || exhaustedCount != 1 {
		t.Fatalf("concurrent outcomes accepted=%d exhausted=%d", acceptedCount, exhaustedCount)
	}
	var pending, operations int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM evidence_archive_intents WHERE state = 'pending'), (SELECT count(*) FROM operations)`).Scan(&pending, &operations); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || operations != 1 {
		t.Fatalf("concurrent reservation wrote pending=%d operations=%d", pending, operations)
	}
}

func TestSignedAcceptanceByteReserveRejectsOversizedPayloadAtomically(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	if err := dbs[0].SetEvidenceReservePolicy(ctx, EvidenceReservePolicy{
		Enabled: true, MaxPending: 100, MaxPendingAge: time.Hour,
		MaxSignedAcceptanceBytes: 1,
	}); err != nil {
		t.Fatal(err)
	}
	a := newAcceptance(t, stores[0], "byte-oversized", "operator", "byte-oversized", false)
	var exhausted *EvidenceReserveExhaustedError
	if _, err := stores[0].Accept(ctx, a); !errors.As(err, &exhausted) {
		t.Fatalf("oversized acceptance error=%v", err)
	}
	var rows int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM operation_request_identities) +
		(SELECT count(*) FROM operations) +
		(SELECT count(*) FROM operation_acceptance_intents) +
		(SELECT count(*) FROM signed_acceptance_byte_reservations) +
		(SELECT count(*) FROM evidence_archive_intents)`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("oversized acceptance left %d durable rows", rows)
	}
}

func TestSignedAcceptanceByteReserveSerializesConcurrentCapacity(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 2)
	ctx := context.Background()
	const maxSignedBytes = int64(900 << 10)
	if err := dbs[0].SetEvidenceReservePolicy(ctx, EvidenceReservePolicy{
		Enabled: true, MaxPending: 100, MaxPendingAge: time.Hour,
		MaxSignedAcceptanceBytes: maxSignedBytes,
	}); err != nil {
		t.Fatal(err)
	}
	requests := []OperationAcceptance{
		newAcceptance(t, stores[0], "byte-race-one", "operator", "byte-race-one", false),
		newAcceptance(t, stores[1], "byte-race-two", "operator", "byte-race-two", false),
	}
	for index := range requests {
		requests[index].Semantics["boundedEvidenceFixture"] = strings.Repeat("x", 600<<10)
		var err error
		requests[index].Fingerprint, err = CanonicalOperationRequestFingerprint(requests[index])
		if err != nil {
			t.Fatal(err)
		}
	}
	errs := make(chan error, len(requests))
	var wait sync.WaitGroup
	for index := range requests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := stores[index].Accept(ctx, requests[index])
			errs <- err
		}(index)
	}
	wait.Wait()
	close(errs)
	var acceptedCount, exhaustedCount int
	for err := range errs {
		switch {
		case err == nil:
			acceptedCount++
		default:
			var exhausted *EvidenceReserveExhaustedError
			if !errors.As(err, &exhausted) {
				t.Fatalf("concurrent byte admission error=%v", err)
			}
			exhaustedCount++
		}
	}
	if acceptedCount != 1 || exhaustedCount != 1 {
		t.Fatalf("concurrent byte outcomes accepted=%d exhausted=%d", acceptedCount, exhaustedCount)
	}
	var reservations int
	var reservedBytes, persistedPayloadBytes int64
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT count(*), sum(r.reserved_bytes),
		min(octet_length(i.request_canonical_bytes) + octet_length(i.canonical_bytes) + octet_length(convert_to(i.signature, 'UTF8')))
		FROM signed_acceptance_byte_reservations r
		JOIN operation_acceptance_intents i ON i.id = r.acceptance_intent_id`).Scan(&reservations, &reservedBytes, &persistedPayloadBytes); err != nil {
		t.Fatal(err)
	}
	if reservations != 1 || reservedBytes != persistedPayloadBytes || reservedBytes > maxSignedBytes {
		t.Fatalf("signed acceptance reservations count=%d reserved=%d persistedPayload=%d", reservations, reservedBytes, persistedPayloadBytes)
	}
	status, err := dbs[0].EvidenceReserve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.ReservedSignedAcceptanceBytes != reservedBytes || status.MaxSignedAcceptanceBytes != maxSignedBytes {
		t.Fatalf("reserve status=%+v", status)
	}
}

var _ OperationStore = (*PGOperationStore)(nil)
