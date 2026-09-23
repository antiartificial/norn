package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/effect"
	"norn/v2/api/model"
)

func TestOperationEffectsMigrationContract(t *testing.T) {
	migration := operationEffectsMigration()
	if migration.Version != 3 || migration.Name != "external-effect-recovery" ||
		migration.MinimumReaderVersion != 1 || migration.MinimumWriterVersion != 3 {
		t.Fatalf("unexpected effect migration contract: %+v", migration)
	}
	const checksum = "8fc9df8b9732cab071e0aad5ce53efcd402c98c1507d6ceff041cbef17bff557"
	if got := MigrationChecksum(migration); got != checksum {
		t.Fatalf("effect migration checksum = %s, want frozen %s", got, checksum)
	}
}

func setupEffectStores(t *testing.T, count int) ([]*DB, []*PGEffectStore, string) {
	t.Helper()
	pools := schemaMigrationTestPools(t, count)
	dbs := make([]*DB, 0, count)
	stores := make([]*PGEffectStore, 0, count)
	for _, pool := range pools {
		db := &DB{Pool: pool}
		dbs = append(dbs, db)
		store, err := NewPGEffectStore(db)
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
	}
	if err := Migrate(dbs[0]); err != nil {
		t.Fatal(err)
	}
	var authority string
	if err := pools[0].QueryRow(context.Background(), `SELECT authority::text FROM control_plane_identity WHERE singleton`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	return dbs, stores, authority
}

func claimEffectOperation(t *testing.T, db *DB, worker string, lease time.Duration) (*model.Operation, OperationClaim) {
	t.Helper()
	op := insertOperationFixture(t, db, "app.preflight", 5, map[string]interface{}{})
	claimed, claim, err := db.ClaimNextOperation(context.Background(), worker, lease, []string{op.Kind})
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claimed operation = %+v, want %s", claimed, op.ID)
	}
	return op, claim
}

func effectReservation(t *testing.T, authority, resource, executionID string, claim OperationClaim) effect.Reservation {
	t.Helper()
	reservation := effect.Reservation{
		Authority: authority,
		Resource:  resource,
		OperationClaim: effect.OperationClaim{
			OperationID: claim.OperationID(),
			OwnerID:     claim.OwnerID(),
			Generation:  claim.Generation(),
		},
		Stage:                 "build",
		Supervisor:            "test-supervisor",
		SupervisorExecutionID: executionID,
		LaunchPayload:         json.RawMessage(`{"command":["go","test","./..."]}`),
	}
	digest, err := effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	reservation.InputDigest = digest
	return reservation
}

func effectVerification(reservation effect.Reservation, runtime string, decision effect.VerificationDecision) effect.Verification {
	verification := effect.Verification{
		Decision:              decision,
		InputDigest:           reservation.InputDigest,
		SupervisorExecutionID: reservation.SupervisorExecutionID,
		RuntimeInstanceID:     runtime,
		EvidenceSource:        "test-supervisor-journal",
		EvidenceReference:     "evidence/" + reservation.SupervisorExecutionID,
		ObservedAt:            time.Now().UTC(),
	}
	if decision == effect.VerificationSucceeded {
		verification.ResultDigest = effect.DigestInput([]byte("result"))
		verification.ResultReference = "result/" + reservation.SupervisorExecutionID
	}
	return verification
}

func TestPGEffectStoreRecoversOriginalReservationAndRequiresRepeatSafeCompletion(t *testing.T) {
	dbs, stores, authority := setupEffectStores(t, 1)
	ctx := context.Background()
	_, claim := claimEffectOperation(t, dbs[0], "worker-a", time.Minute)
	reservation := effectReservation(t, authority, "app/demo/build", "execution-original", claim)

	reserved, err := stores[0].Reserve(ctx, reservation)
	if err != nil || !reserved.Created {
		t.Fatalf("reserve = %+v, err=%v", reserved, err)
	}
	retry := reservation
	retry.SupervisorExecutionID = "execution-newly-generated"
	recovered, err := stores[0].Reserve(ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Created || recovered.Record.Token != reserved.Record.Token ||
		recovered.Record.Reservation.SupervisorExecutionID != reservation.SupervisorExecutionID {
		t.Fatalf("retry did not recover original reservation: first=%+v recovered=%+v", reserved, recovered)
	}

	identity := effect.ExecutionIdentity{
		Supervisor:            reservation.Supervisor,
		SupervisorExecutionID: reservation.SupervisorExecutionID,
		RuntimeInstanceID:     "runtime-original",
	}
	if err := stores[0].MarkLaunched(ctx, reserved.Record.Token, identity); err != nil {
		t.Fatal(err)
	}
	wrongRuntime := effectVerification(reservation, "runtime-other", effect.VerificationFailedRepeatSafe)
	if err := stores[0].Complete(ctx, reserved.Record.Token, effect.Completion{Outcome: effect.OutcomeFailed, Verification: wrongRuntime}); err == nil {
		t.Fatal("completion accepted evidence from a different runtime")
	}
	wrongDecision := effectVerification(reservation, identity.RuntimeInstanceID, effect.VerificationSucceeded)
	if err := stores[0].Complete(ctx, reserved.Record.Token, effect.Completion{Outcome: effect.OutcomeFailed, Verification: wrongDecision}); err == nil {
		t.Fatal("failed completion accepted a non-repeat-safe decision")
	}

	verified := effectVerification(reservation, identity.RuntimeInstanceID, effect.VerificationFailedRepeatSafe)
	if err := stores[0].Complete(ctx, reserved.Record.Token, effect.Completion{Outcome: effect.OutcomeFailed, Verification: verified}); err != nil {
		t.Fatal(err)
	}
	if err := stores[0].Complete(ctx, reserved.Record.Token, effect.Completion{Outcome: effect.OutcomeFailed, Verification: verified}); !errors.Is(err, effect.ErrStaleToken) {
		t.Fatalf("stale completion error = %v", err)
	}

	_, successorClaim := claimEffectOperation(t, dbs[0], "worker-b", time.Minute)
	successor := effectReservation(t, authority, reservation.Resource, "execution-successor", successorClaim)
	if result, err := stores[0].Reserve(ctx, successor); err != nil || !result.Created {
		t.Fatalf("verified failed completion did not release resource: result=%+v err=%v", result, err)
	}
}

func TestPGEffectStoreLocatesBlockingEffectAndRecordsFinalFailure(t *testing.T) {
	dbs, stores, authority := setupEffectStores(t, 1)
	ctx := context.Background()
	if got, err := stores[0].Authority(ctx); err != nil || got != authority {
		t.Fatalf("authority = %q, %v", got, err)
	}
	_, claim := claimEffectOperation(t, dbs[0], "worker-a", time.Minute)
	reservation := effectReservation(t, authority, "app/demo/build.test", "execution-final-failure", claim)
	if _, found, err := stores[0].UnresolvedForResource(ctx, authority, reservation.Resource); err != nil || found {
		t.Fatalf("free resource reported blocked: found=%v err=%v", found, err)
	}
	reserved, err := stores[0].Reserve(ctx, reservation)
	if err != nil {
		t.Fatal(err)
	}
	blocking, found, err := stores[0].UnresolvedForResource(ctx, authority, reservation.Resource)
	if err != nil || !found || blocking.Token != reserved.Record.Token || blocking.Reservation.SupervisorExecutionID != reservation.SupervisorExecutionID || len(blocking.Reservation.LaunchPayload) == 0 {
		t.Fatalf("blocking effect = %+v found=%v err=%v", blocking, found, err)
	}
	if _, found, err := stores[0].UnresolvedForResource(ctx, authority, "app/other/build.test"); err != nil || found {
		t.Fatalf("unrelated resource reported blocked: found=%v err=%v", found, err)
	}
	identity := effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: "runtime-final"}
	if err := stores[0].MarkLaunched(ctx, reserved.Record.Token, identity); err != nil {
		t.Fatal(err)
	}
	final := effectVerification(reservation, identity.RuntimeInstanceID, effect.VerificationFailed)
	final.ResultDigest = effect.DigestInput([]byte("FAIL"))
	final.ResultReference = "result/" + reservation.SupervisorExecutionID
	exit := 1
	if err := stores[0].Complete(ctx, reserved.Record.Token, effect.Completion{Outcome: effect.OutcomeFailed, ExitCode: &exit, Verification: final}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := stores[0].UnresolvedForResource(ctx, authority, reservation.Resource); err != nil || found {
		t.Fatalf("completed failure still blocks: found=%v err=%v", found, err)
	}
	replayed, err := stores[0].Reserve(ctx, reservation)
	if err != nil || replayed.Created || replayed.Record.Lifecycle != effect.LifecycleCompleted || replayed.Record.Completion == nil ||
		replayed.Record.Completion.Outcome != effect.OutcomeFailed || replayed.Record.Completion.Verification.Decision != effect.VerificationFailed ||
		replayed.Record.Completion.Verification.ResultDigest != final.ResultDigest || *replayed.Record.Completion.ExitCode != 1 {
		t.Fatalf("replay of final failure = %+v err=%v", replayed, err)
	}
}

func TestPGEffectStoreResolutionCannotEraseRecordedLaunch(t *testing.T) {
	dbs, stores, authority := setupEffectStores(t, 1)
	ctx := context.Background()
	_, claim := claimEffectOperation(t, dbs[0], "worker-a", time.Minute)
	reservation := effectReservation(t, authority, "app/demo/deploy", "execution-launched", claim)
	reserved, err := stores[0].Reserve(ctx, reservation)
	if err != nil {
		t.Fatal(err)
	}
	identity := effect.ExecutionIdentity{
		Supervisor:            reservation.Supervisor,
		SupervisorExecutionID: reservation.SupervisorExecutionID,
		RuntimeInstanceID:     "runtime-launched",
	}
	if err := stores[0].MarkLaunched(ctx, reserved.Record.Token, identity); err != nil {
		t.Fatal(err)
	}
	neverLaunched := effectVerification(reservation, "", effect.VerificationNeverLaunched)
	if err := stores[0].Resolve(ctx, reserved.Record.Token, effect.Resolution{Decision: effect.VerificationNeverLaunched, Verification: neverLaunched}); err == nil {
		t.Fatal("never-launched evidence erased a recorded launch")
	}

	_, otherClaim := claimEffectOperation(t, dbs[0], "worker-b", time.Minute)
	blocked := effectReservation(t, authority, reservation.Resource, "execution-blocked", otherClaim)
	if _, err := stores[0].Reserve(ctx, blocked); !errors.Is(err, effect.ErrResourceBlocked) {
		t.Fatalf("mismatched resolution released resource: %v", err)
	}
	unrelated := effectReservation(t, authority, "app/other/deploy", "execution-unrelated", otherClaim)
	if result, err := stores[0].Reserve(ctx, unrelated); err != nil || !result.Created {
		t.Fatalf("unrelated resource was blocked: result=%+v err=%v", result, err)
	}
}

func TestPGEffectStoreValidatesLeaseAfterWaitingForOperationLock(t *testing.T) {
	dbs, stores, authority := setupEffectStores(t, 2)
	ctx := context.Background()
	_, claim := claimEffectOperation(t, dbs[0], "worker-a", 750*time.Millisecond)
	reservation := effectReservation(t, authority, "app/demo/preflight", "execution-expired", claim)
	applicationName := "effect-lease-" + uuid.NewString()
	conn, err := dbs[1].Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('application_name',$1,false)`, applicationName); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()

	tx, err := dbs[0].Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var lockedUntil time.Time
	if err := tx.QueryRow(ctx, `SELECT locked_until FROM operations WHERE id=$1 FOR UPDATE`, claim.OperationID()).Scan(&lockedUntil); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, reserveErr := stores[1].Reserve(ctx, reservation)
		result <- reserveErr
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waiting bool
		if err := dbs[0].Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE application_name=$1 AND state='active' AND wait_event_type='Lock'
			)
		`, applicationName).Scan(&waiting); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			_ = tx.Rollback(ctx)
			t.Fatal("reserve did not observably block on the operation row")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if remaining := time.Until(lockedUntil.Add(30 * time.Millisecond)); remaining > 0 {
		time.Sleep(remaining)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrOperationOwnershipLost) {
			t.Fatalf("reserve after lease expiry error=%v, want ownership lost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reserve did not finish after operation lock was released")
	}
	var effects int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT count(*) FROM operation_effects`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatalf("expired claim created %d effect reservations", effects)
	}
}
