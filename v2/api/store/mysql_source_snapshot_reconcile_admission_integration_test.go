package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

func TestMySQLSourceReconciliationAdmissionRequiresFailedFencedPredecessor(t *testing.T) {
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
		signed.Checkpoint != "stop-intended" || !sameMySQLSourceSnapshotPayload(successor.Operation.Payload, request) {
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
