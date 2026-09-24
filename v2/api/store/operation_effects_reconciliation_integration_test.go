package store

import (
	"context"
	"testing"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
)

func TestCompletedSnapshotOperationsQueriesDurableSuccessesWithRealPostgres(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_effects")
	db, err := Connect(server.URL("norn_effects"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := NewPGEffectStore(db)
	if err != nil {
		t.Fatal(err)
	}
	var authority string
	if err := db.Pool.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity WHERE singleton`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	type completed struct {
		op       *model.Operation
		identity effect.ExecutionIdentity
	}
	complete := func(rootID, executionID string) completed {
		op := insertOperationFixture(t, db, "app.snapshot", 1, map[string]interface{}{})
		claimed, claim, err := db.ClaimNextOperation(ctx, "snapshot-worker", time.Minute, []string{"app.snapshot"})
		if err != nil || claimed == nil || claimed.ID != op.ID {
			t.Fatalf("claim = %+v, %+v, %v", claimed, claim, err)
		}
		reservation := effectReservation(t, authority, "app/demo/snapshot", executionID, claim)
		reservation.Stage = "app.snapshot"
		reservation.LaunchPayload = []byte(`{"supervisorRootId":"` + rootID + `"}`)
		if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
			t.Fatal(err)
		}
		reserved, err := store.Reserve(ctx, reservation)
		if err != nil || !reserved.Created {
			t.Fatalf("reserve = %+v, %v", reserved, err)
		}
		identity := effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: executionID + "-runtime"}
		if err := store.MarkLaunched(ctx, reserved.Record.Token, identity); err != nil {
			t.Fatal(err)
		}
		verification := effectVerification(reservation, identity.RuntimeInstanceID, effect.VerificationSucceeded)
		if err := store.Complete(ctx, reserved.Record.Token, effect.Completion{Outcome: effect.OutcomeSucceeded, Verification: verification}); err != nil {
			t.Fatal(err)
		}
		if err := db.FinishClaimedOperation(ctx, claim, model.OperationSucceeded, "snapshot published", nil); err != nil {
			t.Fatal(err)
		}
		return completed{op: op, identity: identity}
	}
	replicaA := complete("root-a", "snapshot-a")
	_ = complete("root-b", "snapshot-b")
	records, err := store.CompletedSnapshotOperations(ctx, "root-a")
	if err != nil || len(records) != 1 || records[0].Reservation.OperationClaim.OperationID != replicaA.op.ID || records[0].Execution != replicaA.identity {
		t.Fatalf("completed snapshot records = %+v, %v", records, err)
	}
}

func TestTerminalSnapshotCleanupEffectsQueriesOnlyVerifiedTerminalLocalRecordsWithRealPostgres(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_snapshot_cleanup")
	db, err := Connect(server.URL("norn_snapshot_cleanup"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := NewPGEffectStore(db)
	if err != nil {
		t.Fatal(err)
	}
	var authority string
	if err := db.Pool.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity WHERE singleton`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	reserve := func(rootID, executionID string) (effect.Reservation, effect.Record) {
		op := insertOperationFixture(t, db, "app.snapshot", 1, map[string]interface{}{})
		claimed, claim, err := db.ClaimNextOperation(ctx, "snapshot-worker", time.Minute, []string{"app.snapshot"})
		if err != nil || claimed == nil || claimed.ID != op.ID {
			t.Fatalf("claim = %+v, %+v, %v", claimed, claim, err)
		}
		reservation := effectReservation(t, authority, "app/demo/snapshot", executionID, claim)
		reservation.Stage = "app.snapshot"
		reservation.LaunchPayload = []byte(`{"supervisorRootId":"` + rootID + `"}`)
		if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
			t.Fatal(err)
		}
		reserved, err := store.Reserve(ctx, reservation)
		if err != nil || !reserved.Created {
			t.Fatalf("reserve = %+v, %v", reserved, err)
		}
		return reservation, reserved.Record
	}
	failedReservation, failed := reserve("root-a", "failed-local")
	failedIdentity := effect.ExecutionIdentity{Supervisor: failedReservation.Supervisor, SupervisorExecutionID: failedReservation.SupervisorExecutionID, RuntimeInstanceID: "failed-runtime"}
	if err := store.MarkLaunched(ctx, failed.Token, failedIdentity); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, failed.Token, effect.Completion{Outcome: effect.OutcomeFailed, Verification: effectVerification(failedReservation, failedIdentity.RuntimeInstanceID, effect.VerificationFailed)}); err != nil {
		t.Fatal(err)
	}
	abandonedReservation, abandoned := reserve("root-a", "abandoned-local")
	abandonedVerification := effectVerification(abandonedReservation, "", effect.VerificationNeverLaunched)
	if err := store.Resolve(ctx, abandoned.Token, effect.Resolution{Decision: effect.VerificationNeverLaunched, Verification: abandonedVerification}); err != nil {
		t.Fatal(err)
	}
	foreignReservation, foreign := reserve("root-b", "failed-foreign")
	foreignIdentity := effect.ExecutionIdentity{Supervisor: foreignReservation.Supervisor, SupervisorExecutionID: foreignReservation.SupervisorExecutionID, RuntimeInstanceID: "foreign-runtime"}
	if err := store.MarkLaunched(ctx, foreign.Token, foreignIdentity); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, foreign.Token, effect.Completion{Outcome: effect.OutcomeFailed, Verification: effectVerification(foreignReservation, foreignIdentity.RuntimeInstanceID, effect.VerificationFailed)}); err != nil {
		t.Fatal(err)
	}
	failedRecords, abandonedRecords, err := store.TerminalSnapshotCleanupEffects(ctx, "root-a")
	if err != nil || len(failedRecords) != 1 || len(abandonedRecords) != 1 || failedRecords[0].Reservation.SupervisorExecutionID != failedReservation.SupervisorExecutionID || abandonedRecords[0].Reservation.SupervisorExecutionID != abandonedReservation.SupervisorExecutionID {
		t.Fatalf("terminal snapshot cleanup records failed=%+v abandoned=%+v err=%v", failedRecords, abandonedRecords, err)
	}
}
