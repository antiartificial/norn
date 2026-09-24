package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestFinishCronPauseClaimedOperationIsClaimFencedAndAtomic(t *testing.T) {
	pools := schemaMigrationTestPools(t, 2)
	db := &DB{Pool: pools[0]}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	op := insertOperationFixture(t, db, "app.cron-pause", 1, map[string]interface{}{})
	claimed, claim, err := db.ClaimNextOperation(context.Background(), "cron-worker", 75*time.Millisecond, []string{"app.cron-pause"})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claim = %#v %v", claimed, err)
	}
	locker, err := pools[0].Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback(context.Background())
	if _, err = locker.Exec(context.Background(), `SELECT id FROM operations WHERE id=$1 FOR UPDATE`, op.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- (&DB{Pool: pools[1]}).FinishCronPauseClaimedOperation(context.Background(), claim, op.App, "nightly", "0 2 * * *", "paused", map[string]interface{}{"effectId": "effect"})
	}()
	time.Sleep(125 * time.Millisecond)
	if err = locker.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("finish after lock wait=%v, want ownership lost", err)
	}
	var states int
	if err = pools[0].QueryRow(context.Background(), `SELECT count(*) FROM cron_states WHERE app=$1`, op.App).Scan(&states); err != nil {
		t.Fatal(err)
	}
	if states != 0 {
		t.Fatalf("stale claimant wrote %d cron states", states)
	}
	current, err := db.GetOperation(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == model.OperationSucceeded {
		t.Fatal("stale claimant terminalized operation")
	}
}
