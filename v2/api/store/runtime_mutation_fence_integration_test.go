package store

import (
	"context"
	"testing"
	"time"
)

func TestRuntimeMutationFenceKeepsQueuedAppEffectsUnclaimedUntilExactRelease(t *testing.T) {
	db := operationTestStores(t, 1)[0]
	ctx := context.Background()
	kinds := []string{"app.deploy", "app.restart", "app.scale", "app.cron-trigger", PrivateInvocationOperationKind}
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
