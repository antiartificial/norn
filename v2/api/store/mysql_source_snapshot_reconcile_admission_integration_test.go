package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

type sourceLockInspectorFunc func(context.Context, database.ResolvedBinding, database.MySQLMaintenanceCredentials, database.SecretSource) error

func (f sourceLockInspectorFunc) InspectLocked(ctx context.Context, resolved database.ResolvedBinding,
	maintenance database.MySQLMaintenanceCredentials, secrets database.SecretSource) error {
	return f(ctx, resolved, maintenance, secrets)
}

func TestMySQLSourceReconciliationAdmissionRequiresFailedFencedPredecessor(t *testing.T) {
	for _, checkpoint := range []string{"stop-intended", "lock-intended"} {
		t.Run(checkpoint, func(t *testing.T) {
			testMySQLSourceReconciliationAdmission(t, checkpoint)
		})
	}
}

func testMySQLSourceReconciliationAdmission(t *testing.T, checkpoint string) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	acceptance, db := stores[0], dbs[0]
	ctx := context.Background()
	catalog := storeTestCatalog()
	catalog.Services = append(catalog.Services, database.DatabaseService{APIVersion: database.APIVersion,
		ID: "reconcile-mysql", Generation: 1, Purpose: database.PurposeApplication, Engine: database.EngineMySQL,
		EngineVersion: "8.4", ProviderRef: "local:reconcile-mysql",
		Endpoint: database.DatabaseEndpoint{Host: "mysql.internal", Port: 3306},
		Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
		TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled},
		Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilitySnapshot}}})
	maintenance := &database.MySQLMaintenanceCredentials{Generation: 1, RuntimeAccountHost: "%", SnapshotRole: "snapshot_reader",
		SnapshotAccountHost: "%", SnapshotCredentialRef: "secret:reconcile/reader", RestoreRole: "snapshot_restore",
		RestoreAccountHost: "%", RestoreCredentialRef: "secret:reconcile/restore", FenceRole: "snapshot_fence",
		FenceAccountHost: "%", FenceCredentialRef: "secret:reconcile/fence"}
	catalog.Bindings = append(catalog.Bindings, database.DatabaseBinding{APIVersion: database.APIVersion,
		ID: "reconcile-source", ServiceID: "reconcile-mysql", Database: "wordpress", Role: "snapshot_runtime",
		Generation: 1, CredentialRef: "secret:reconcile/runtime", MySQLMaintenance: maintenance,
		TLS: database.DatabaseTLS{Mode: database.TLSDisabled}})
	catalog.Profiles[0].DatabaseBindings["reconcile-source"] = "reconcile-source"
	active, err := db.ActivateDatabaseCatalog(ctx, 0, catalog, "operator")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication,
		LogicalResourceID: "reconcile-source"})
	if err != nil {
		t.Fatal(err)
	}
	request := MySQLSourceSnapshotRequest{CatalogRevision: active.Revision, ProfileID: "mini", LogicalID: "reconcile-source",
		Source: resolved.Target, Maintenance: *resolved.MySQLMaintenance,
		JobIdentity:    validSourceSnapshotJobIdentity("wordpress", active.Revision, "7", "alloc-1"),
		DumpToolSHA256: strings.Repeat("a", 64)}
	input := newAcceptance(t, acceptance, "source-"+uuid.NewString(), "operator", "wordpress", false)
	input.Identity.Kind, input.Identity.Resource = MySQLSourceSnapshotOperationKind, "mysql/wordpress"
	input.Operation.Kind, input.Operation.MaxAttempts = MySQLSourceSnapshotOperationKind, 1
	encoded, _ := json.Marshal(request)
	input.Operation.Payload = nil
	if err := json.Unmarshal(encoded, &input.Operation.Payload); err != nil {
		t.Fatal(err)
	}
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := acceptance.Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimPrivateMySQLOperation(ctx, prior.Operation.ID, "source-worker", MySQLSourceSnapshotOperationKind, time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim predecessor: %+v %v", claimed, err)
	}
	if _, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptance, claim, request); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureClaimedMySQLSourceRuntimeFence(ctx, acceptance, claim, request); err != nil {
		t.Fatal(err)
	}
	if err := db.setClaimedMySQLSourceStopState(ctx, claim, request, "quiesce-intended", "stop-intended"); err != nil {
		t.Fatal(err)
	}
	if checkpoint == "lock-intended" {
		if err := db.setClaimedMySQLSourceStopState(ctx, claim, request, "stop-intended", "stop-proved"); err != nil {
			t.Fatal(err)
		}
		lockFailure := errors.New("MySQL lock response lost")
		if err := db.LockClaimedMySQLSourceAccount(ctx, acceptance, claim, request, sourceSecretSource{},
			sourceAccountLockerFunc(func(context.Context, database.ResolvedBinding,
				database.MySQLMaintenanceCredentials, database.SecretSource) error {
				return lockFailure
			})); !errors.Is(err, ErrMySQLSourceAccountLockIndeterminate) || !errors.Is(err, lockFailure) {
			t.Fatalf("lock ambiguity did not persist: %v", err)
		}
	}
	successorInput := MySQLSourceReconciliationAcceptanceInput{PriorSourceOperationID: prior.Operation.ID,
		Actor: OperationActor{Issuer: "test-issuer", Subject: "operator"}, Key: "reconcile-" + uuid.NewString(),
		Audit: AcceptanceAuditContext{Source: "integration-test"}}
	originalKey := successorInput.Key
	if _, err := db.AcceptPrivateMySQLSourceReconciliation(ctx, acceptance, successorInput); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("running predecessor admitted successor: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, claim.OperationID()); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	failed, err := db.GetOperation(ctx, prior.Operation.ID)
	if err != nil || failed.Status != model.OperationFailed || failed.Metadata["manualRecoveryRequired"] != true {
		t.Fatalf("expired source was not manual recovery: %+v %v", failed, err)
	}
	successor, err := db.AcceptPrivateMySQLSourceReconciliation(ctx, acceptance, successorInput)
	if err != nil || successor.Operation.Status != model.OperationQueued || successor.Operation.MaxAttempts != 1 {
		t.Fatalf("signed successor admission: %+v %v", successor, err)
	}
	var signed MySQLSourceReconciliationLink
	if err := decodeMySQLSourceReconciliationLink(successor.Operation.Metadata, &signed); err != nil ||
		signed.PriorSourceOperationID != prior.Operation.ID || signed.PriorSourceDigest != prior.Intent.CanonicalDigest ||
		signed.Checkpoint != checkpoint || !sameMySQLSourceSnapshotPayload(successor.Operation.Payload, request) {
		t.Fatalf("successor did not bind predecessor: %+v %v", signed, err)
	}
	replayed, err := db.AcceptPrivateMySQLSourceReconciliation(ctx, acceptance, successorInput)
	if err != nil || replayed.Operation.ID != successor.Operation.ID {
		t.Fatalf("same-key successor replay: %+v %v", replayed, err)
	}
	claimedSuccessor, successorClaim, err := db.ClaimPrivateMySQLOperation(ctx, successor.Operation.ID,
		"reconciliation-worker", MySQLSourceSnapshotOperationKind, time.Minute)
	if err != nil || claimedSuccessor == nil || successorClaim.OperationID() != successor.Operation.ID ||
		claimedSuccessor.Source != "private-mysql-source-reconciliation" ||
		!sameMySQLSourceSnapshotPayload(claimedSuccessor.Payload, request) {
		t.Fatalf("signed successor was not source-claimable: %+v %+v %v", claimedSuccessor, successorClaim, err)
	}
	if _, _, err := db.ClaimPrivateMySQLOperation(ctx, successor.Operation.ID,
		"second-worker", MySQLSourceSnapshotOperationKind, time.Minute); !errors.Is(err, ErrMySQLMaintenanceClaimUnavailable) {
		t.Fatalf("successor was claimed twice: %v", err)
	}
	claimedReplay, err := db.AcceptPrivateMySQLSourceReconciliation(ctx, acceptance, successorInput)
	if err != nil || claimedReplay.Operation.ID != successor.Operation.ID {
		t.Fatalf("claimed successor identity replay: %+v %v", claimedReplay, err)
	}
	inspections := 0
	inspector := sourceLockInspectorFunc(func(context.Context, database.ResolvedBinding,
		database.MySQLMaintenanceCredentials, database.SecretSource) error {
		inspections++
		return nil
	})
	deniedObservation := sourceStoppedObserverFunc(func(context.Context, nomad.CASStopJobRequest) error {
		return errors.New("job still running")
	})
	if err := db.ReconcileClaimedMySQLSourceSnapshot(ctx, acceptance, successorClaim, deniedObservation,
		inspector, sourceSecretSource{}); !errors.Is(err, ErrMySQLSourceStopIndeterminate) {
		t.Fatalf("running job transferred source: %v", err)
	}
	var activeOperation, state string
	if err := db.Pool.QueryRow(ctx, `SELECT operation_id,state FROM mysql_source_snapshot_intents WHERE source_key=$1`,
		mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Source)).Scan(&activeOperation, &state); err != nil ||
		activeOperation != prior.Operation.ID || state != checkpoint || inspections != 0 {
		t.Fatalf("negative observation changed source: operation=%q state=%q inspections=%d err=%v", activeOperation, state, inspections, err)
	}
	failedUntransferredID := successor.Operation.ID
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`,
		failedUntransferredID); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if replay, err := db.AcceptPrivateMySQLSourceReconciliation(ctx, acceptance, successorInput); err != nil ||
		replay.Operation.ID != failedUntransferredID {
		t.Fatalf("failed untransferred successor did not replay: %+v %v", replay, err)
	}
	successorInput.Key = "replacement-" + uuid.NewString()
	originalKey = successorInput.Key
	successor, err = db.AcceptPrivateMySQLSourceReconciliation(ctx, acceptance, successorInput)
	if err != nil || successor.Operation.ID == failedUntransferredID || successor.Operation.Status != model.OperationQueued {
		t.Fatalf("failed read-only observation blocked signed replacement: %+v %v", successor, err)
	}
	claimedSuccessor, successorClaim, err = db.ClaimPrivateMySQLOperation(ctx, successor.Operation.ID,
		"replacement-worker", MySQLSourceSnapshotOperationKind, time.Minute)
	if err != nil || claimedSuccessor == nil || successorClaim.OperationID() != successor.Operation.ID {
		t.Fatalf("replacement successor was not claimable: %+v %+v %v", claimedSuccessor, successorClaim, err)
	}
	stoppedObservation := sourceStoppedObserverFunc(func(_ context.Context, got nomad.CASStopJobRequest) error {
		if got.JobID != request.JobIdentity.JobID || got.JobModifyIndex != 7 || len(got.AllocationIDs) != 1 ||
			got.AllocationIDs[0] != "alloc-1" {
			return errors.New("wrong signed job")
		}
		return nil
	})
	claimLostAfterObservation := sourceStoppedObserverFunc(func(_ context.Context, got nomad.CASStopJobRequest) error {
		if err := stoppedObservation.ObserveStoppedMySQLSourceJob(ctx, got); err != nil {
			return err
		}
		_, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`,
			successor.Operation.ID)
		return err
	})
	if err := db.ReconcileClaimedMySQLSourceSnapshot(ctx, acceptance, successorClaim,
		claimLostAfterObservation, inspector, sourceSecretSource{}); err == nil {
		t.Fatal("expired successor claim transferred source after observation")
	}
	if err := db.Pool.QueryRow(ctx, `SELECT operation_id,state FROM mysql_source_snapshot_intents WHERE source_key=$1`,
		mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Source)).Scan(&activeOperation, &state); err != nil ||
		activeOperation != prior.Operation.ID || state != checkpoint {
		t.Fatalf("claim loss changed source: operation=%q state=%q err=%v", activeOperation, state, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()+interval '2 minutes' WHERE id=$1`,
		successor.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if checkpoint == "lock-intended" {
		if err := db.ReconcileClaimedMySQLSourceSnapshot(ctx, acceptance, successorClaim, stoppedObservation,
			sourceLockInspectorFunc(func(context.Context, database.ResolvedBinding,
				database.MySQLMaintenanceCredentials, database.SecretSource) error {
				return errors.New("account still active")
			}), sourceSecretSource{}); !errors.Is(err, ErrMySQLSourceAccountLockIndeterminate) {
			t.Fatalf("unlocked account transferred source: %v", err)
		}
	}
	if err := db.ReconcileClaimedMySQLSourceSnapshot(ctx, acceptance, successorClaim, stoppedObservation,
		inspector, sourceSecretSource{}); err != nil {
		t.Fatalf("proved stopped source did not transfer: %v", err)
	}
	var fenceOwner string
	wantState := "stop-proved"
	wantInspections := 0
	if checkpoint == "lock-intended" {
		wantState, wantInspections = "lock-proved", 2
	}
	if err := db.Pool.QueryRow(ctx, `SELECT i.operation_id,i.state,f.owner FROM mysql_source_snapshot_intents i
		CROSS JOIN runtime_mutation_fence f WHERE i.source_key=$1 AND f.singleton=true`,
		mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Source)).Scan(&activeOperation, &state, &fenceOwner); err != nil ||
		activeOperation != successor.Operation.ID || state != wantState ||
		fenceOwner != "mysql-source-snapshot:"+successor.Operation.ID || inspections != wantInspections {
		t.Fatalf("source and fence did not transfer atomically: operation=%q state=%q fence=%q inspections=%d err=%v",
			activeOperation, state, fenceOwner, inspections, err)
	}
	var priorState, archivedSuccessor string
	if err := db.Pool.QueryRow(ctx, `SELECT checkpoint,successor_operation_id FROM mysql_source_snapshot_reconciliations
		WHERE prior_operation_id=$1`, prior.Operation.ID).Scan(&priorState, &archivedSuccessor); err != nil ||
		priorState != checkpoint || archivedSuccessor != successor.Operation.ID {
		t.Fatalf("predecessor proof not retained: state=%q successor=%q err=%v", priorState, archivedSuccessor, err)
	}
	priorInspection, err := db.InspectPrivateMySQLSourceSnapshot(ctx, acceptance, prior.Operation.ID)
	if err != nil || priorInspection.IntentState != checkpoint ||
		priorInspection.ReconciledByOperationID != successor.Operation.ID || !priorInspection.RuntimeFenceHeld {
		t.Fatalf("failed predecessor no longer inspectable: %+v %v", priorInspection, err)
	}
	var proofCanonical []byte
	if err := db.Pool.QueryRow(ctx, `SELECT proof_canonical FROM mysql_source_snapshot_reconciliations
		WHERE prior_operation_id=$1`, prior.Operation.ID).Scan(&proofCanonical); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE mysql_source_snapshot_reconciliations
		SET proof_canonical=proof_canonical || decode('20','hex') WHERE prior_operation_id=$1`, prior.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InspectPrivateMySQLSourceSnapshot(ctx, acceptance, prior.Operation.ID); !errors.Is(err, ErrMySQLSourceSnapshotInspection) {
		t.Fatalf("tampered reconciliation proof was accepted: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE mysql_source_snapshot_reconciliations SET proof_canonical=$2
		WHERE prior_operation_id=$1`, prior.Operation.ID, proofCanonical); err != nil {
		t.Fatal(err)
	}
	if checkpoint == "stop-intended" {
		if err := db.LockClaimedMySQLSourceAccount(ctx, acceptance, successorClaim, request, sourceSecretSource{},
			sourceAccountLockerFunc(func(context.Context, database.ResolvedBinding,
				database.MySQLMaintenanceCredentials, database.SecretSource) error {
				return nil
			})); err != nil {
			t.Fatalf("successor could not continue account lock: %v", err)
		}
	}
	stageDirectory := t.TempDir()
	stageBytes := []byte("-- reconciled source SQL fixture\n")
	stageDigest := sha256.Sum256(stageBytes)
	stageArtifact := database.MySQLSQLArtifact{Format: database.MySQLSQLArtifactV2, Source: request.Source,
		Bytes: int64(len(stageBytes)), SHA256: hex.EncodeToString(stageDigest[:]),
		Expectation: database.MySQLRestoreExpectation{SchemaSHA256: strings.Repeat("a", 64),
			DataSHA256: strings.Repeat("b", 64)}}
	staged, err := db.StageClaimedMySQLSourceArtifact(ctx, acceptance, successorClaim, request,
		sourceSecretSource{}, "/usr/bin/true", stageDirectory,
		sourceStagerFunc(func(_ context.Context, _ database.ResolvedBinding, _ database.TargetIdentity,
			_ database.SecretSource, _, _, directory string) (string, database.MySQLSQLArtifact, error) {
			path := filepath.Join(directory, "reconciled.sql")
			return path, stageArtifact, os.WriteFile(path, stageBytes, 0o600)
		}))
	if err != nil || staged.Receipt.OperationID != successor.Operation.ID || staged.Receipt.Artifact != stageArtifact {
		t.Fatalf("successor could not produce signed stage receipt: %+v %v", staged, err)
	}
	if err := db.ReconcileClaimedMySQLSourceSnapshot(ctx, acceptance, successorClaim,
		sourceStoppedObserverFunc(func(context.Context, nomad.CASStopJobRequest) error {
			return errors.New("replay must use signed durable proof")
		}), inspector, sourceSecretSource{}); err != nil {
		t.Fatalf("lost transfer response could not replay without external effect: %v", err)
	}
	successorInput.Key = "competing-" + uuid.NewString()
	if _, err := db.AcceptPrivateMySQLSourceReconciliation(ctx, acceptance, successorInput); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("competing successor admitted: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, successor.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	successorInput.Key = originalKey
	failedReplay, err := db.AcceptPrivateMySQLSourceReconciliation(ctx, acceptance, successorInput)
	if err != nil || failedReplay.Operation.ID != successor.Operation.ID {
		t.Fatalf("failed successor identity replay: %+v %v", failedReplay, err)
	}
	successorInput.Key = "fence-missing-" + uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `UPDATE runtime_mutation_fence SET active=false,owner='',reason='',released_at=clock_timestamp() WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptPrivateMySQLSourceReconciliation(ctx, acceptance, successorInput); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("lost source fence admitted successor: %v", err)
	}
}
