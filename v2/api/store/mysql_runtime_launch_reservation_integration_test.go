package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
)

func TestMySQLRuntimeLaunchReservationSerializesWithRestoreMaintenanceFence(t *testing.T) {
	_, dbs := acceptanceIntegrationStores(t, 2)
	ctx := context.Background()
	source := database.TargetIdentity{ServiceID: "mysql-source", ServiceGeneration: 2, BindingID: "source-binding", BindingGeneration: 3, Engine: database.EngineMySQL, Database: "source", Role: "writer"}
	target := database.TargetIdentity{ServiceID: "mysql-target", ServiceGeneration: 4, BindingID: "target-binding", BindingGeneration: 5, Engine: database.EngineMySQL, Database: "target", Role: "writer"}
	seedMySQLRestoreFence(t, dbs[0], "restore-first", source, target)

	// Restore acquired first: launch waits for the same transaction gate, then
	// observes its durable source/target maintenance fence and refuses.
	if _, err := dbs[1].ReserveMySQLRuntimeLaunch(ctx, "launch-after-restore", []database.TargetIdentity{source, target}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("launch after restore fence = %v, want launch fence", err)
	}
	aliasDuringRestore := target
	aliasDuringRestore.BindingID, aliasDuringRestore.BindingGeneration, aliasDuringRestore.Role = "target-restore-alias", 6, "alternate-writer"
	if _, err := dbs[1].ReserveMySQLRuntimeLaunch(ctx, "alias-after-restore", []database.TargetIdentity{aliasDuringRestore}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("alias launch after restore fence = %v, want launch fence", err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `DELETE FROM mysql_restore_maintenance_fences WHERE operation_id='restore-first'`); err != nil {
		t.Fatal(err)
	}

	reserved, err := dbs[0].ReserveMySQLRuntimeLaunch(ctx, "launch-first", []database.TargetIdentity{target, source})
	if err != nil || reserved.Replayed || len(reserved.Identities) != 2 {
		t.Fatalf("reserve source and target = %+v, %v", reserved, err)
	}
	replay, err := dbs[1].ReserveMySQLRuntimeLaunch(ctx, "launch-first", []database.TargetIdentity{source, target})
	if err != nil || !replay.Replayed {
		t.Fatalf("exact reservation replay = %+v, %v", replay, err)
	}

	// Launch acquired first: the restore's fence acquisition observes both
	// identities before it can persist maintenance state.
	tx, err := dbs[1].Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := rejectMySQLRuntimeLaunchReservations(ctx, tx, []database.TargetIdentity{source, target}); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("restore after launch reservation = %v, want restore fence", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if err := dbs[0].ContainMySQLRuntimeLaunchForInspection(ctx, "launch-first"); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].ReleaseMySQLRuntimeLaunchNeverStarted(ctx, "launch-first", MySQLRuntimeLaunchNoStartProof{}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("ambiguous launch was released: %v", err)
	}
	tx, err = dbs[0].Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := rejectMySQLRuntimeLaunchReservations(ctx, tx, []database.TargetIdentity{source, target}); !errors.Is(err, ErrMySQLRestoreFence) {
		t.Fatalf("restore accepted ambiguous launch = %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	alias := target
	alias.BindingID, alias.BindingGeneration, alias.Role = "target-alias", 6, "alternate-writer"
	if _, err := dbs[0].ReserveMySQLRuntimeLaunch(ctx, "alias-launch", []database.TargetIdentity{alias}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("alias binding bypassed physical database reservation: %v", err)
	}
	if err := dbs[0].ReconcileMySQLRuntimeLaunch(ctx, "launch-first", MySQLRuntimeLaunchReconciliationProof{Outcome: "never-started", ObservationSource: "supervisor", ObservationExecutionID: "reconcile-launch-first", ObservedAt: time.Now().UTC(), EvidenceSHA256: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}); err != nil {
		t.Fatalf("reconcile ambiguous prelaunch reservation: %v", err)
	}
	if _, err := dbs[1].ReserveMySQLRuntimeLaunch(ctx, "launch-after-reconcile", []database.TargetIdentity{target}); err != nil {
		t.Fatalf("reconciled no-start reservation did not reopen target: %v", err)
	}

	stoppedTarget := target
	stoppedTarget.Database = "proven-stopped"
	if _, err := dbs[0].ReserveMySQLRuntimeLaunch(ctx, "proven-stopped", []database.TargetIdentity{stoppedTarget}); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].MarkMySQLRuntimeLaunchLaunched(ctx, "proven-stopped", "runtime-proven-stopped"); err != nil {
		t.Fatal(err)
	}
	proof := MySQLRuntimeLaunchStopProof{RuntimeInstanceID: "runtime-proven-stopped", ObservedAt: time.Now().UTC(), Method: "supervisor allocation absence", EvidenceSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	if err := dbs[0].StopMySQLRuntimeLaunch(ctx, "proven-stopped", proof); err != nil {
		t.Fatal(err)
	}

	ambiguousLaunched := target
	ambiguousLaunched.Database = "ambiguous-launched"
	if _, err := dbs[0].ReserveMySQLRuntimeLaunch(ctx, "ambiguous-launched", []database.TargetIdentity{ambiguousLaunched}); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].MarkMySQLRuntimeLaunchLaunched(ctx, "ambiguous-launched", "runtime-ambiguous"); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].ContainMySQLRuntimeLaunchForInspection(ctx, "ambiguous-launched"); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].ReconcileMySQLRuntimeLaunch(ctx, "ambiguous-launched", MySQLRuntimeLaunchReconciliationProof{Outcome: "stopped", RuntimeInstanceID: "wrong-runtime", ObservationSource: "supervisor", ObservationExecutionID: "reconcile-wrong-runtime", ObservedAt: time.Now().UTC(), EvidenceSHA256: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("mismatched ambiguous runtime was reconciled: %v", err)
	}
	if err := dbs[0].ReconcileMySQLRuntimeLaunch(ctx, "ambiguous-launched", MySQLRuntimeLaunchReconciliationProof{Outcome: "stopped", RuntimeInstanceID: "runtime-ambiguous", ObservationSource: "supervisor", ObservationExecutionID: "reconcile-runtime-ambiguous", ObservedAt: time.Now().UTC(), EvidenceSHA256: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[1].ReserveMySQLRuntimeLaunch(ctx, "after-ambiguous-launch", []database.TargetIdentity{ambiguousLaunched}); err != nil {
		t.Fatalf("reconciled launched reservation did not reopen target: %v", err)
	}
	tx, err = dbs[0].Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := rejectMySQLRuntimeLaunchReservations(ctx, tx, []database.TargetIdentity{stoppedTarget}); err != nil {
		t.Fatalf("proof-backed stopped launch still blocked restore: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	neverStarted := target
	neverStarted.Database = "proven-never-started"
	if _, err := dbs[0].ReserveMySQLRuntimeLaunch(ctx, "never-started", []database.TargetIdentity{neverStarted}); err != nil {
		t.Fatal(err)
	}
	noStart := MySQLRuntimeLaunchNoStartProof{ObservedAt: time.Now().UTC(), Method: "supervisor execution lookup", EvidenceSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
	if err := dbs[0].ReleaseMySQLRuntimeLaunchNeverStarted(ctx, "never-started", noStart); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[1].ReserveMySQLRuntimeLaunch(ctx, "never-started-successor", []database.TargetIdentity{neverStarted}); err != nil {
		t.Fatalf("proof-backed no-start release did not reopen target: %v", err)
	}
}

func TestMySQLRuntimeLaunchReservationFailsClosedForIncompleteIdentity(t *testing.T) {
	_, dbs := acceptanceIntegrationStores(t, 1)
	if _, err := dbs[0].ReserveMySQLRuntimeLaunch(context.Background(), "missing-identity", []database.TargetIdentity{{Engine: database.EngineMySQL}}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("incomplete identity = %v, want launch fence", err)
	}
}

func TestMySQLRuntimeLaunchReservationUsesCatalogPhysicalProviderIdentity(t *testing.T) {
	_, dbs := acceptanceIntegrationStores(t, 1)
	db := dbs[0]
	ctx := context.Background()
	service := func(id string, generation uint64) database.DatabaseService {
		return database.DatabaseService{
			APIVersion: database.APIVersion, ID: id, Generation: generation,
			Purpose: database.PurposeApplication, Engine: database.EngineMySQL,
			EngineVersion: "8.4", ProviderRef: "local:shared-mysql",
			Endpoint: database.DatabaseEndpoint{Host: "mysql.internal", Port: 3306},
			Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled},
			Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilityRuntime, database.CapabilityRestore}},
		}
	}
	catalog := database.Catalog{APIVersion: database.APIVersion, Services: []database.DatabaseService{service("mysql-primary", 1), service("mysql-alias", 1)}}
	active, err := db.ActivateDatabaseCatalog(ctx, 0, catalog, "physical-exclusion-test")
	if err != nil {
		t.Fatal(err)
	}
	primary := database.TargetIdentity{ServiceID: "mysql-primary", ServiceGeneration: 1, BindingID: "primary", BindingGeneration: 1, Engine: database.EngineMySQL, Database: "wordpress", Role: "writer"}
	alias := database.TargetIdentity{ServiceID: "mysql-alias", ServiceGeneration: 1, BindingID: "alias", BindingGeneration: 1, Engine: database.EngineMySQL, Database: "wordpress", Role: "alternate_writer"}
	if _, err := db.ReserveMySQLRuntimeLaunch(ctx, "physical-primary", []database.TargetIdentity{primary}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReserveMySQLRuntimeLaunch(ctx, "physical-alias", []database.TargetIdentity{alias}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("catalog service alias bypassed physical provider reservation: %v", err)
	}

	rotated := catalog
	rotated.Services = append([]database.DatabaseService(nil), catalog.Services...)
	rotated.Services[0].Generation = 2
	rotated.Services[0].EngineVersion = "8.4.1"
	if _, err := db.ActivateDatabaseCatalog(ctx, active.Revision, rotated, "physical-exclusion-test"); err != nil {
		t.Fatal(err)
	}
	primaryV2 := primary
	primaryV2.ServiceGeneration = 2
	if _, err := db.ReserveMySQLRuntimeLaunch(ctx, "physical-generation-2", []database.TargetIdentity{primaryV2}); !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
		t.Fatalf("service generation rotation bypassed physical provider reservation: %v", err)
	}
}

func TestMySQLRuntimeLaunchReservationWaitsForConcurrentRestoreFence(t *testing.T) {
	_, dbs := acceptanceIntegrationStores(t, 2)
	ctx := context.Background()
	source := database.TargetIdentity{ServiceID: "concurrent-source", ServiceGeneration: 1, BindingID: "source", BindingGeneration: 1, Engine: database.EngineMySQL, Database: "source", Role: "writer"}
	target := database.TargetIdentity{ServiceID: "concurrent-target", ServiceGeneration: 1, BindingID: "target", BindingGeneration: 1, Engine: database.EngineMySQL, Database: "target", Role: "writer"}
	active, err := dbs[0].ActivateDatabaseCatalog(ctx, 0, storeTestCatalog(), "launch-gate-test")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := dbs[0].Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, reserveErr := dbs[1].ReserveMySQLRuntimeLaunch(ctx, "concurrent-launch", []database.TargetIdentity{source, target})
		result <- reserveErr
	}()
	// Let the competing transaction attempt the same advisory gate before the
	// restore fence becomes visible. It must not finish while this transaction
	// owns the gate.
	time.Sleep(30 * time.Millisecond)
	select {
	case err := <-result:
		t.Fatalf("launch completed before concurrent restore committed: %v", err)
	default:
	}
	insertMySQLRestoreFence(t, ctx, tx, "concurrent-restore", active.Revision, source, target)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrMySQLRuntimeLaunchFence) {
			t.Fatalf("concurrent launch result = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("launch did not finish after restore fence committed")
	}
}

func seedMySQLRestoreFence(t *testing.T, db *DB, operationID string, source, target database.TargetIdentity) {
	t.Helper()
	ctx := context.Background()
	catalog := storeTestCatalog()
	active, err := db.ActivateDatabaseCatalog(ctx, 0, catalog, "launch-gate-test")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertMySQLRestoreFence(t, ctx, tx, operationID, active.Revision, source, target)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func insertMySQLRestoreFence(t *testing.T, ctx context.Context, tx pgx.Tx, operationID string, revision int64, source, target database.TargetIdentity) {
	t.Helper()
	artifact, _ := json.Marshal(database.MySQLSQLArtifact{Format: database.MySQLSQLArtifactV2, Source: source, Bytes: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	targetJSON, _ := json.Marshal(target)
	quiescence, _ := json.Marshal(mysqlRestoreQuiescence(source))
	if _, err := tx.Exec(ctx, `INSERT INTO operations (id, kind, status) VALUES ($1,'database.mysql-restore','running')`, operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mysql_restore_intents
		(operation_id, acceptance_intent_id, catalog_revision, profile_id, logical_id, target_key, target, artifact, artifact_path, state)
		VALUES ($1,$2,$3,'profile','logical',$4,$5,$6,'/private/artifact.sql','prepared')`, operationID, uuid.NewString(), revision, mysqlRestoreTargetKey(target), targetJSON, artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mysql_restore_maintenance_fences (operation_id, catalog_revision, source_quiescence) VALUES ($1,$2,$3)`, operationID, revision, quiescence); err != nil {
		t.Fatal(err)
	}
}
