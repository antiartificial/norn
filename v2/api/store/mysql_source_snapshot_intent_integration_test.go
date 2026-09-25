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
)

func TestMySQLSourceSnapshotPayloadPreservesLargeIntegers(t *testing.T) {
	request := MySQLSourceSnapshotRequest{
		CatalogRevision: 9007199254740993,
		Source:          database.TargetIdentity{ServiceGeneration: 9007199254740993},
		JobIdentity:     MySQLSourceSnapshotJobIdentity{JobModifyIndex: "9007199254740993"},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := DecodeExactJSONObject(encoded)
	if err != nil || !sameMySQLSourceSnapshotPayload(payload, request) {
		t.Fatalf("large signed integers did not compare exactly: %v", err)
	}
	request.CatalogRevision--
	if sameMySQLSourceSnapshotPayload(payload, request) {
		t.Fatal("changed large catalog revision compared equal")
	}
}

func TestMySQLSourceSnapshotIntentReservesSignedPhysicalSource(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	db, acceptedStore := dbs[0], stores[0]
	ctx := context.Background()
	catalog := storeTestCatalog()
	catalog.Services = append(catalog.Services, database.DatabaseService{
		APIVersion: database.APIVersion, ID: "snapshot-mysql", Generation: 1,
		Purpose: database.PurposeApplication, Engine: database.EngineMySQL, EngineVersion: "8.4", ProviderRef: "local:snapshot-mysql",
		Endpoint: database.DatabaseEndpoint{Host: "mysql.internal", Port: 3306},
		Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
		TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled},
		Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilitySnapshot}},
	})
	maintenance := &database.MySQLMaintenanceCredentials{Generation: 1, RuntimeAccountHost: "%", SnapshotRole: "snapshot_reader", SnapshotAccountHost: "%", SnapshotCredentialRef: "secret:snapshot/reader", RestoreRole: "snapshot_restore", RestoreAccountHost: "%", RestoreCredentialRef: "secret:snapshot/restore", FenceRole: "snapshot_fence", FenceCredentialRef: "secret:snapshot/fence", FenceAccountHost: "%"}
	catalog.Bindings = append(catalog.Bindings, database.DatabaseBinding{APIVersion: database.APIVersion, ID: "snapshot-source", ServiceID: "snapshot-mysql", Database: "wordpress", Role: "snapshot_runtime", Generation: 1, CredentialRef: "secret:snapshot/runtime", MySQLMaintenance: maintenance, TLS: database.DatabaseTLS{Mode: database.TLSDisabled}})
	catalog.Profiles[0].DatabaseBindings["snapshot-source"] = "snapshot-source"
	active, err := db.ActivateDatabaseCatalog(ctx, 0, catalog, "operator")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	source, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication, LogicalResourceID: "snapshot-source"})
	if err != nil {
		t.Fatal(err)
	}
	request := MySQLSourceSnapshotRequest{CatalogRevision: active.Revision, ProfileID: "mini", LogicalID: "snapshot-source", Source: source.Target, Maintenance: *source.MySQLMaintenance,
		JobIdentity: MySQLSourceSnapshotJobIdentity{App: "wordpress", NomadRegion: "global", JobID: "wordpress", JobModifyIndex: "7", AllocationIDs: []string{"alloc-1"}}, DumpToolSHA256: strings.Repeat("a", 64)}
	accept := func(key string, body MySQLSourceSnapshotRequest) OperationClaim {
		t.Helper()
		input := newAcceptance(t, acceptedStore, key, "operator", "wordpress", false)
		input.Identity.Kind, input.Identity.Resource = MySQLSourceSnapshotOperationKind, "mysql/wordpress"
		input.Operation.Kind, input.Operation.MaxAttempts = MySQLSourceSnapshotOperationKind, 1
		encoded, _ := json.Marshal(body)
		input.Operation.Payload = nil
		if err := json.Unmarshal(encoded, &input.Operation.Payload); err != nil {
			t.Fatal(err)
		}
		input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := acceptedStore.Accept(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := NewOperationClaim(accepted.Operation.ID, "snapshot-worker", 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='running', attempts=1, locked_by=$2, lock_generation=1, locked_until=clock_timestamp()+interval '2 minutes' WHERE id=$1`, claim.OperationID(), claim.OwnerID()); err != nil {
			t.Fatal(err)
		}
		return claim
	}
	claim := accept("snapshot-"+uuid.NewString(), request)
	const priorLaunch = "source-snapshot-prior-launch"
	if _, err := db.ReserveMySQLRuntimeLaunch(ctx, priorLaunch, []database.TargetIdentity{source.Target}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptedStore, claim, request); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("source intent crossed existing runtime launch: %v", err)
	}
	if err := db.ReleaseMySQLRuntimeLaunchNeverStarted(ctx, priorLaunch, MySQLRuntimeLaunchNoStartProof{ObservedAt: time.Now().UTC(), Method: "disposable test inspected absent supervisor instance", EvidenceSHA256: strings.Repeat("b", 64)}); err != nil {
		t.Fatal(err)
	}
	prepared, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptedStore, claim, request)
	if err != nil || prepared.State != "quiesce-intended" || prepared.Replayed {
		t.Fatalf("first signed source intent=%+v err=%v", prepared, err)
	}
	if replay, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptedStore, claim, request); err != nil || !replay.Replayed {
		t.Fatalf("exact replay=%+v err=%v", replay, err)
	}
	changed := request
	changed.JobIdentity.JobModifyIndex = "8"
	if _, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptedStore, claim, changed); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("changed job revision accepted: %v", err)
	}
	second := accept("snapshot-"+uuid.NewString(), request)
	if _, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptedStore, second, request); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("second operation reused physical source: %v", err)
	}
	if _, err := db.ReserveMySQLRuntimeLaunch(ctx, "snapshot-race", []database.TargetIdentity{source.Target}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("runtime launch passed source fence: %v", err)
	}
	if _, err := db.ActivateDatabaseCatalog(ctx, active.Revision, catalog, "operator"); !errors.Is(err, ErrMySQLRestoreMaintenanceFence) {
		t.Fatalf("catalog activation passed source fence: %v", err)
	}
	var state string
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM mysql_source_snapshot_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&state); err != nil || state != "quiesce-intended" {
		t.Fatalf("source fence after competing operations = %q, %v", state, err)
	}
}
