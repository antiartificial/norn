package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRuntimeMutationFenceKeepsQueuedAppEffectsUnclaimedUntilExactRelease(t *testing.T) {
	db := operationTestStores(t, 1)[0]
	ctx := context.Background()
	kinds := []string{"app.deploy", "app.rollback", "app.restart", "app.scale", "app.canary-promote", "app.cron-trigger", PrivateInvocationOperationKind}
	operations := make(map[string]string, len(kinds))
	for _, kind := range kinds {
		op := insertOperationFixture(t, db, kind, 2, map[string]interface{}{"process": "nightly"})
		operations[kind] = op.ID
	}

	fence, err := db.AcquireRuntimeMutationFence(ctx, "migration-28-test", "protect runtime during maintenance")
	if err != nil || fence.Epoch != 1 || fence.Owner != "migration-28-test" || fence.Reason == "" {
		t.Fatalf("acquire fence=%+v err=%v", fence, err)
	}
	if _, err := db.AcquireRuntimeMutationFence(ctx, "other-owner", "must not replace held fence"); err != ErrRuntimeMutationFenceHeld {
		t.Fatalf("second acquire err=%v, want held", err)
	}

	for _, kind := range kinds {
		claimed, _, err := db.ClaimNextOperation(ctx, "fenced-"+kind, time.Minute, []string{kind})
		if err != nil || claimed != nil {
			t.Fatalf("fenced claim %s op=%+v err=%v", kind, claimed, err)
		}
		queued, err := db.GetOperation(ctx, operations[kind])
		if err != nil || queued.Status != "queued" || queued.Attempts != 0 || queued.LockedBy != "" || queued.LockGeneration != 0 {
			t.Fatalf("fenced operation %s=%+v err=%v", kind, queued, err)
		}
	}

	if err := db.ReleaseRuntimeMutationFence(ctx, RuntimeMutationFence{Epoch: fence.Epoch + 1, Owner: fence.Owner}); err != ErrRuntimeMutationFenceOwnershipLost {
		t.Fatalf("stale release err=%v, want ownership lost", err)
	}
	if err := db.ReleaseRuntimeMutationFence(ctx, fence); err != nil {
		t.Fatal(err)
	}
	for _, kind := range kinds {
		claimed, claim, err := db.ClaimNextOperation(ctx, "released-"+kind, time.Minute, []string{kind})
		if err != nil || claimed == nil || claimed.ID != operations[kind] || claim.OwnerID() == "" || claim.Generation() != 1 {
			t.Fatalf("released claim %s op=%+v claim=%+v err=%v", kind, claimed, claim, err)
		}
		if err := db.FinishClaimedOperation(ctx, claim, "succeeded", "test complete", nil); err != nil {
			t.Fatalf("finish %s: %v", kind, err)
		}
	}
}

func TestRuntimeMutationClaimWaitsForConcurrentFenceCommit(t *testing.T) {
	db := operationTestStores(t, 1)[0]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	insertOperationFixture(t, db, "app.deploy", 1, nil)
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `UPDATE runtime_mutation_fence SET epoch=epoch+1, active=true, owner='concurrent-fence', reason='test', held_at=clock_timestamp(), released_at=NULL WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		claimed, _, err := db.ClaimNextOperation(ctx, "racing-worker", time.Minute, []string{"app.deploy"})
		if err == nil && claimed != nil {
			result <- ErrRuntimeMutationFenceHeld
			return
		}
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("claim passed uncommitted fence update: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("claim after fence commit: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("claim did not resume after fence commit")
	}
	var epoch int64
	if err := db.Pool.QueryRow(ctx, `SELECT epoch FROM runtime_mutation_fence WHERE singleton=true AND owner='concurrent-fence'`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseRuntimeMutationFence(ctx, RuntimeMutationFence{Epoch: epoch, Owner: "concurrent-fence"}); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeMutationFenceRejectsAlreadyClaimedEffect(t *testing.T) {
	db := operationTestStores(t, 1)[0]
	ctx := context.Background()
	op := insertOperationFixture(t, db, "app.restart", 2, map[string]interface{}{"process": "web"})
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='running', attempts=1, locked_by='restart-worker', lock_generation=1,
		locked_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := NewOperationClaim(op.ID, "restart-worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcquireRuntimeMutationFence(ctx, "migration-test", "drain app effects"); err != ErrRuntimeMutationFenceBusy {
		t.Fatalf("running restart did not block fence acquisition: %v", err)
	}
	if err := db.FinishClaimedOperation(ctx, claim, "succeeded", "test complete", nil); err != nil {
		t.Fatal(err)
	}
	fence, err := db.AcquireRuntimeMutationFence(ctx, "migration-test", "drained app effects")
	if err != nil {
		t.Fatalf("drained restart still blocked fence: %v", err)
	}
	if err := db.ReleaseRuntimeMutationFence(ctx, fence); err != nil {
		t.Fatal(err)
	}
}
