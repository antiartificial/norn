package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/database"
)

func TestMySQLRestoreRejectsProviderAliasSelfRestore(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	db, acceptance := dbs[0], stores[0]
	ctx := context.Background()
	catalog := storeTestCatalog()
	service := database.DatabaseService{APIVersion: database.APIVersion, ID: "physical-source", Generation: 1,
		Purpose: database.PurposeApplication, Engine: database.EngineMySQL, EngineVersion: "8.4", ProviderRef: "shared:mysql",
		Endpoint: database.DatabaseEndpoint{Host: "mysql.internal", Port: 3306},
		Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
		TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled},
		Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilitySnapshot, database.CapabilityRestore}}}
	alias := service
	alias.ID = "physical-alias"
	catalog.Services = append(catalog.Services, service, alias)
	maintenance := &database.MySQLMaintenanceCredentials{Generation: 1, RuntimeAccountHost: "%", RestoreRole: "restore", RestoreAccountHost: "%", RestoreCredentialRef: "secret:restore", FenceRole: "fence", FenceAccountHost: "%", FenceCredentialRef: "secret:fence"}
	catalog.Bindings = append(catalog.Bindings,
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "physical-source", ServiceID: service.ID, Database: "same_database", Role: "source", Generation: 1, CredentialRef: "secret:source", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "physical-target", ServiceID: alias.ID, Database: "same_database", Role: "target", Generation: 1, CredentialRef: "secret:target", MySQLMaintenance: maintenance, TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
		database.DatabaseBinding{APIVersion: database.APIVersion, ID: "distinct-target", ServiceID: alias.ID, Database: "other_database", Role: "target", Generation: 1, CredentialRef: "secret:target", MySQLMaintenance: maintenance, TLS: database.DatabaseTLS{Mode: database.TLSDisabled}})
	catalog.Profiles[0].DatabaseBindings["physical-source"] = "physical-source"
	catalog.Profiles[0].DatabaseBindings["physical-target"] = "physical-target"
	catalog.Profiles[0].DatabaseBindings["distinct-target"] = "distinct-target"
	active, err := db.ActivateDatabaseCatalog(ctx, 0, catalog, "operator")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	source, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "physical-source"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "physical-target"})
	if err != nil {
		t.Fatal(err)
	}
	if mysqlRuntimePhysicalKeyForCatalog(active.Catalog, source.Target) != mysqlRuntimePhysicalKeyForCatalog(active.Catalog, target.Target) {
		t.Fatal("provider aliases did not resolve to one physical database")
	}
	request := MySQLRestoreRequest{CatalogRevision: active.Revision, ProfileID: "mini", LogicalID: "physical-target", Target: target.Target,
		Maintenance: *target.MySQLMaintenance, Artifact: database.MySQLSQLArtifact{Source: source.Target}, ArtifactPath: "/private/stage/artifact.sql",
		SourceArtifact: MySQLRestoreSourceArtifact{OperationID: "source-op", ReceiptSHA256: hex.EncodeToString(make([]byte, 32))}}
	input := newAcceptance(t, acceptance, "alias-restore-"+uuid.NewString(), "operator", "physical-target", false)
	input.Identity.Kind, input.Identity.Resource = MySQLRestoreOperationKind, "mysql/same_database"
	input.Operation.Kind, input.Operation.MaxAttempts = MySQLRestoreOperationKind, 1
	encoded, _ := json.Marshal(request)
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
	_, err = db.Pool.Exec(ctx, `UPDATE operations SET status='running', attempts=1, locked_by=$2, lock_generation=1,
		locked_until=$3 WHERE id=$1`, claim.OperationID(), claim.OwnerID(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PrepareClaimedMySQLRestore(ctx, acceptance, claim, request, mysqlIntentSecrets{}); err != ErrMySQLRestoreFence {
		t.Fatalf("provider-alias self-restore was accepted: %v", err)
	}
	other, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "distinct-target"})
	if err != nil {
		t.Fatal(err)
	}
	request.LogicalID, request.Target = "distinct-target", other.Target
	input = newAcceptance(t, acceptance, "missing-receipt-"+uuid.NewString(), "operator", "distinct-target", false)
	input.Identity.Kind, input.Identity.Resource = MySQLRestoreOperationKind, "mysql/other_database"
	input.Operation.Kind, input.Operation.MaxAttempts = MySQLRestoreOperationKind, 1
	encoded, _ = json.Marshal(request)
	if err := json.Unmarshal(encoded, &input.Operation.Payload); err != nil {
		t.Fatal(err)
	}
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err = acceptance.Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	claim, err = NewOperationClaim(accepted.Operation.ID, "worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, `UPDATE operations SET status='running', attempts=1, locked_by=$2, lock_generation=1,
		locked_until=$3 WHERE id=$1`, claim.OperationID(), claim.OwnerID(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PrepareClaimedMySQLRestore(ctx, acceptance, claim, request, mysqlIntentSecrets{}); err != ErrMySQLRestoreFence {
		t.Fatalf("accepted restore without a source receipt was accepted: %v", err)
	}
}

// This fixture creates an accepted source operation and a service-signed
// stage-proved row. Source stop and lock behavior has separate integration
// coverage; restore tests start with an already staged artifact.
func testMySQLSourceArtifactReceipt(t *testing.T, db *DB, acceptance *PGOperationStore, revision int64, source database.TargetIdentity, path string, artifact database.MySQLSQLArtifact) MySQLRestoreSourceArtifact {
	t.Helper()
	ctx := context.Background()
	maintenance := database.MySQLMaintenanceCredentials{Generation: 1, RuntimeAccountHost: "%", SnapshotRole: "snapshot", SnapshotAccountHost: "%", SnapshotCredentialRef: "secret:test/snapshot", RestoreRole: "restore", RestoreAccountHost: "%", RestoreCredentialRef: "secret:test/restore", FenceRole: "fence", FenceAccountHost: "%", FenceCredentialRef: "secret:test/fence"}
	toolDigest := hex.EncodeToString(make([]byte, 32))
	request := MySQLSourceSnapshotRequest{CatalogRevision: revision, ProfileID: "mini", LogicalID: "fixture-source", Source: source, Maintenance: maintenance,
		JobIdentity: MySQLSourceSnapshotJobIdentity{App: "fixture", NomadRegion: "global", JobID: "fixture", JobModifyIndex: "1", AllocationIDs: []string{"fixture-alloc"}}, DumpToolSHA256: toolDigest}
	input := newAcceptance(t, acceptance, "mysql-source-fixture-"+uuid.NewString(), "operator", "fixture-source", false)
	input.Identity.Kind, input.Identity.Resource = MySQLSourceSnapshotOperationKind, "mysql/"+source.Database
	input.Operation.Kind, input.Operation.MaxAttempts = MySQLSourceSnapshotOperationKind, 1
	encoded, _ := json.Marshal(request)
	input.Operation.Payload = nil
	if err := json.Unmarshal(encoded, &input.Operation.Payload); err != nil {
		t.Fatal(err)
	}
	var err error
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := acceptance.Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := acceptance.VerifyAcceptedOperation(ctx, accepted.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := MySQLSourceArtifactReceipt{Schema: MySQLSourceArtifactReceiptSchema, OperationID: accepted.Operation.ID,
		AcceptanceIntentID: verified.AcceptanceIntentID, AcceptanceCanonicalDigest: verified.Intent.CanonicalDigest,
		CatalogRevision: revision, Source: source, DumpToolSHA256: toolDigest, ArtifactPath: path, Artifact: artifact}
	canonical, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := acceptance.signer.Sign(ctx, canonical)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	active, err := db.ActiveDatabaseCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := mysqlRuntimePhysicalKeyForCatalog(active.Catalog, source)
	sourceJSON, _ := json.Marshal(source)
	maintenanceJSON, _ := json.Marshal(maintenance)
	jobJSON, _ := json.Marshal(request.JobIdentity)
	artifactJSON, _ := json.Marshal(artifact)
	_, err = db.Pool.Exec(ctx, `INSERT INTO mysql_source_snapshot_intents
		(operation_id,acceptance_intent_id,catalog_revision,profile_id,logical_id,source_key,source,maintenance,job_identity,dump_tool_sha256,state,
		 stop_intended_at,stop_proved_at,lock_intended_at,lock_proved_at,stage_intended_at,stage_proved_at,artifact_path,artifact,
		 artifact_receipt_canonical,artifact_receipt_sha256,artifact_receipt_signing_algorithm,artifact_receipt_signing_key_id,artifact_receipt_signature)
		 VALUES ($1,$2,$3,'mini','fixture-source',$4,$5,$6,$7,$8,'stage-proved',now(),now(),now(),now(),now(),now(),$9,$10,$11,$12,$13,$14,$15)`,
		accepted.Operation.ID, verified.AcceptanceIntentID, revision, key, sourceJSON, maintenanceJSON, jobJSON, toolDigest, path, artifactJSON,
		canonical, hex.EncodeToString(digest[:]), signature.Algorithm, signature.KeyID, signature.Value)
	if err != nil {
		t.Fatal(err)
	}
	return MySQLRestoreSourceArtifact{OperationID: accepted.Operation.ID, ReceiptSHA256: hex.EncodeToString(digest[:])}
}

func testBindMySQLSourceFence(t *testing.T, db *DB, receipt MySQLRestoreSourceArtifact) RuntimeMutationFence {
	t.Helper()
	ctx := context.Background()
	fence, err := db.AcquireRuntimeMutationFence(ctx, "mysql-source-snapshot:"+receipt.OperationID, "quiesce MySQL source for signed snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET runtime_fence_epoch=$2,runtime_fence_owner=$3 WHERE operation_id=$1`, receipt.OperationID, fence.Epoch, fence.Owner); err != nil {
		t.Fatal(err)
	}
	return fence
}
