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
)

func TestMySQLRestoreTransfersSignedSourceFenceWithoutGap(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	acceptance, db := stores[0], dbs[0]
	ctx := context.Background()
	catalog := storeTestCatalog()
	catalog.Services = append(catalog.Services, database.DatabaseService{APIVersion: database.APIVersion, ID: "transfer-mysql", Generation: 1,
		Purpose: database.PurposeApplication, Engine: database.EngineMySQL, EngineVersion: "8.4", ProviderRef: "local:transfer-mysql",
		Endpoint: database.DatabaseEndpoint{Host: "mysql.internal", Port: 3306},
		Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
		TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled},
		Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilitySnapshot, database.CapabilityRestore}}})
	maintenance := &database.MySQLMaintenanceCredentials{Generation: 1, RuntimeAccountHost: "%", RestoreRole: "restore", RestoreAccountHost: "%", RestoreCredentialRef: "secret:restore", FenceRole: "fence", FenceAccountHost: "%", FenceCredentialRef: "secret:fence"}
	catalog.Bindings = append(catalog.Bindings,
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "transfer-source", ServiceID: "transfer-mysql", Database: "source_db", Role: "source", Generation: 1, CredentialRef: "secret:source", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "transfer-target", ServiceID: "transfer-mysql", Database: "target_db", Role: "target", Generation: 1, CredentialRef: "secret:target", MySQLMaintenance: maintenance, TLS: database.DatabaseTLS{Mode: database.TLSDisabled}})
	catalog.Profiles[0].DatabaseBindings["transfer-source"] = "transfer-source"
	catalog.Profiles[0].DatabaseBindings["transfer-target"] = "transfer-target"
	active, err := db.ActivateDatabaseCatalog(ctx, 0, catalog, "operator")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	source, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "transfer-source"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "transfer-target"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.sql")
	data := []byte("-- bounded signed transfer fixture\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(data)
	artifact := database.MySQLSQLArtifact{Format: database.MySQLSQLArtifactV2, Source: source.Target, Bytes: int64(len(data)), SHA256: hex.EncodeToString(checksum[:]),
		Expectation: database.MySQLRestoreExpectation{SchemaSHA256: strings.Repeat("a", 64), DataSHA256: strings.Repeat("b", 64)}}
	receipt := testMySQLSourceArtifactReceipt(t, db, acceptance, active.Revision, source.Target, path, artifact)
	sourceFence := testBindMySQLSourceFence(t, db, receipt)
	request := MySQLRestoreRequest{CatalogRevision: active.Revision, ProfileID: "mini", LogicalID: "transfer-target", Target: target.Target,
		Maintenance: *target.MySQLMaintenance, Artifact: artifact, ArtifactPath: path, SourceArtifact: receipt}
	input := newAcceptance(t, acceptance, "transfer-"+uuid.NewString(), "operator", "transfer-target", false)
	input.Identity.Kind, input.Identity.Resource = MySQLRestoreOperationKind, "mysql/target_db"
	input.Operation.Kind, input.Operation.MaxAttempts = MySQLRestoreOperationKind, 1
	encoded, _ := json.Marshal(request)
	input.Operation.Payload = nil
	if err := json.Unmarshal(encoded, &input.Operation.Payload); err != nil {
		t.Fatal(err)
	}
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := acceptance.Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := NewOperationClaim(accepted.Operation.ID, "worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='running',attempts=1,locked_by=$2,lock_generation=1,
		locked_until=now()+interval '2 minutes' WHERE id=$1`, claim.OperationID(), claim.OwnerID()); err != nil {
		t.Fatal(err)
	}
	verified, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil {
		t.Fatal(err)
	}
	targetJSON, _ := json.Marshal(target.Target)
	artifactJSON, _ := json.Marshal(artifact)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO mysql_restore_intents
		(operation_id,acceptance_intent_id,catalog_revision,profile_id,logical_id,target_key,target,artifact,artifact_path,state)
		VALUES ($1,$2,$3,'mini','transfer-target',$4,$5,$6,$7,'prepared')`, claim.OperationID(), verified.AcceptanceIntentID,
		active.Revision, mysqlRestoreTargetKey(target.Target), targetJSON, artifactJSON, path); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO mysql_restore_maintenance_fences
		(operation_id,catalog_revision,source_artifact_operation_id,source_artifact_receipt_sha256) VALUES ($1,$2,$3,$4)`,
		claim.OperationID(), active.Revision, receipt.OperationID, receipt.ReceiptSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := db.IntendClaimedMySQLRestoreRuntimeLock(ctx, acceptance, claim, mysqlIntentSecrets{}); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("destination lock could start before source fence transfer: %v", err)
	}
	transferred, err := db.TransferClaimedMySQLRestoreRuntimeFence(ctx, acceptance, claim)
	if err != nil || transferred.Epoch != sourceFence.Epoch || transferred.Owner != "mysql-restore:"+claim.OperationID() {
		t.Fatalf("atomic transfer: %+v %v", transferred, err)
	}
	if replay, err := db.TransferClaimedMySQLRestoreRuntimeFence(ctx, acceptance, claim); err != nil || replay.Epoch != transferred.Epoch || replay.Owner != transferred.Owner {
		t.Fatalf("idempotent exact-claim replay: %+v %v", replay, err)
	}
	if intended, err := db.IntendClaimedMySQLRestoreRuntimeLock(ctx, acceptance, claim, mysqlIntentSecrets{}); err != nil || intended.State != "lock-intended" {
		t.Fatalf("destination lock could not begin under transferred fence: %+v %v", intended, err)
	}
	if err := db.ReleaseRuntimeMutationFence(ctx, transferred); !errors.Is(err, ErrRuntimeMutationFenceOwnershipLost) {
		t.Fatalf("generic release cleared transferred fence: %v", err)
	}
	if err := db.ReleaseRuntimeMutationFence(ctx, sourceFence); !errors.Is(err, ErrRuntimeMutationFenceOwnershipLost) {
		t.Fatalf("stale source released transferred fence: %v", err)
	}
	if active, err := db.RuntimeMutationFenceActive(ctx); err != nil || !active {
		t.Fatalf("claim gate opened during transfer: active=%t err=%v", active, err)
	}
	stolen, err := NewOperationClaim(claim.OperationID(), "new-worker", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_by=$2,lock_generation=2,locked_until=$3 WHERE id=$1`, claim.OperationID(), stolen.OwnerID(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.TransferClaimedMySQLRestoreRuntimeFence(ctx, acceptance, stolen); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("successor claim resumed transferred fence: %v", err)
	}
	if _, err := db.AssessCompletedMySQLRestoreRecovery(ctx, acceptance, claim.OperationID()); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("unfinished restore appeared recovery ready: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE mysql_restore_runtime_locks SET state='verified-lock',verified_at=clock_timestamp() WHERE operation_id=$1`, claim.OperationID()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE mysql_restore_intents SET state='completed',started_at=clock_timestamp(),completed_at=clock_timestamp() WHERE operation_id=$1`, claim.OperationID()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='succeeded',locked_by='',locked_until=NULL,finished_at=clock_timestamp() WHERE id=$1`, claim.OperationID()); err != nil {
		t.Fatal(err)
	}
	ready, err := db.AssessCompletedMySQLRestoreRecovery(ctx, acceptance, claim.OperationID())
	if err != nil || ready.Fence.Epoch != transferred.Epoch || ready.Request.Target != target.Target {
		t.Fatalf("completed signed restore recovery assessment: %+v %v", ready, err)
	}
	recoveryInput := MySQLRestoreRecoveryAcceptanceInput{RestoreOperationID: claim.OperationID(),
		Actor: OperationActor{Issuer: "test-issuer", Subject: "operator"}, Key: "recover-" + uuid.NewString(),
		Audit: AcceptanceAuditContext{RequestID: "request-" + uuid.NewString(), CredentialID: "token-one", DeviceID: "device-one", Source: "integration-test", Scopes: []string{"write"}}}
	recovery, err := db.AcceptPrivateMySQLRestoreRecovery(ctx, acceptance, recoveryInput)
	if err != nil {
		t.Fatalf("accept signed recovery: %v", err)
	}
	verifiedRecovery, err := acceptance.VerifyAcceptedOperation(ctx, recovery.Operation.ID)
	var signedRecovery MySQLRestoreRecoveryRequest
	if err != nil || decodeMySQLRestoreRecoveryPayload(verifiedRecovery.Operation.Payload, &signedRecovery) != nil ||
		signedRecovery.RestoreOperationID != claim.OperationID() || signedRecovery.RuntimeFenceEpoch != transferred.Epoch ||
		signedRecovery.Target != target.Target || signedRecovery.SourceArtifactOperationID != receipt.OperationID {
		t.Fatalf("signed recovery did not bind exact lineage: %+v %v", signedRecovery, err)
	}
	replayedRecovery, err := db.AcceptPrivateMySQLRestoreRecovery(ctx, acceptance, recoveryInput)
	if err != nil || replayedRecovery.Operation.ID != recovery.Operation.ID {
		t.Fatalf("recovery identity replay: %+v %v", replayedRecovery, err)
	}
	recoveryClaim, err := NewOperationClaim(recovery.Operation.ID, "recovery-worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='running',attempts=1,locked_by=$2,
		lock_generation=1,locked_until=now()+interval '2 minutes' WHERE id=$1`, recoveryClaim.OperationID(), recoveryClaim.OwnerID()); err != nil {
		t.Fatal(err)
	}
	prepared, err := db.PrepareClaimedMySQLRestoreRecovery(ctx, acceptance, recoveryClaim)
	if err != nil || prepared.Replayed || prepared.RestoreOperationID != claim.OperationID() {
		t.Fatalf("prepare claimed recovery: %+v %v", prepared, err)
	}
	if replay, err := db.PrepareClaimedMySQLRestoreRecovery(ctx, acceptance, recoveryClaim); err != nil || !replay.Replayed {
		t.Fatalf("exact-claim recovery prepare replay: %+v %v", replay, err)
	}
	stolenRecoveryClaim, err := NewOperationClaim(recovery.Operation.ID, "other-recovery-worker", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_by=$2,lock_generation=2 WHERE id=$1`, recoveryClaim.OperationID(), stolenRecoveryClaim.OwnerID()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PrepareClaimedMySQLRestoreRecovery(ctx, acceptance, stolenRecoveryClaim); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("successor claim replayed prepared recovery: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE runtime_mutation_fence SET owner='replacement-owner' WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AssessCompletedMySQLRestoreRecovery(ctx, acceptance, claim.OperationID()); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("replaced fence appeared recovery ready: %v", err)
	}
	recoveryInput.Key = "new-recovery-" + uuid.NewString()
	if _, err := db.AcceptPrivateMySQLRestoreRecovery(ctx, acceptance, recoveryInput); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("replaced fence admitted a new recovery: %v", err)
	}
}
