package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestFinishCronResumeClaimedOperationIsClaimFencedAndAtomic(t *testing.T) {
	pools := schemaMigrationTestPools(t, 2)
	db := &DB{Pool: pools[0]}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	op := insertOperationFixture(t, db, "app.cron-resume", 2, map[string]interface{}{"process": "nightly"})
	if err := db.UpsertCronState(ctx, op.App, "nightly", true, "0 2 * * *"); err != nil {
		t.Fatal(err)
	}
	_, oldClaim, err := db.ClaimNextOperation(ctx, "old-worker", time.Minute, []string{"app.cron-resume"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := (&DB{Pool: pools[1]}).RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	queued, err := db.GetOperation(ctx, op.ID)
	if err != nil || queued.Status != model.OperationQueued {
		t.Fatalf("recovered resume = %#v, %v", queued, err)
	}
	_, currentClaim, err := (&DB{Pool: pools[1]}).ClaimNextOperation(ctx, "new-worker", time.Minute, []string{"app.cron-resume"})
	if err != nil || currentClaim.Generation() <= oldClaim.Generation() {
		t.Fatalf("successor claim = %#v, %v", currentClaim, err)
	}
	if err := db.FinishCronResumeClaimedOperation(ctx, oldClaim, op.App, "nightly", "0 2 * * *", "resumed", nil); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale resume finish = %v", err)
	}
	state, err := db.GetCronState(ctx, op.App, "nightly")
	if err != nil || !state.Paused {
		t.Fatalf("stale claim changed cron state = %#v, %v", state, err)
	}
	if err := (&DB{Pool: pools[1]}).FinishCronResumeClaimedOperation(ctx, currentClaim, op.App, "nightly", "0 2 * * *", "resumed", map[string]interface{}{"effectId": "effect-1"}); err != nil {
		t.Fatal(err)
	}
	state, err = db.GetCronState(ctx, op.App, "nightly")
	if err != nil || state.Paused || state.Schedule != "0 2 * * *" {
		t.Fatalf("successful claim did not update cron state = %#v, %v", state, err)
	}
	finished, err := db.GetOperation(ctx, op.ID)
	if err != nil || finished.Status != model.OperationSucceeded || finished.Metadata["effectId"] != "effect-1" {
		t.Fatalf("resume receipt = %#v, %v", finished, err)
	}
}
