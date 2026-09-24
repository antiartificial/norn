package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFinishScaleClaimedOperationRejectsLeaseExpiredWhileWaitingForRowLock(t *testing.T) {
	pools := schemaMigrationTestPools(t, 2)
	db := &DB{Pool: pools[0]}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	op := insertOperationFixture(t, db, "app.scale", 1, map[string]interface{}{})
	claimed, claim, err := db.ClaimNextOperation(context.Background(), "scale-worker", 75*time.Millisecond, []string{"app.scale"})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claim = %#v %v", claimed, err)
	}

	locker, err := pools[0].Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback(context.Background())
	if _, err := locker.Exec(context.Background(), `SELECT id FROM operations WHERE id=$1 FOR UPDATE`, op.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- (&DB{Pool: pools[1]}).FinishScaleClaimedOperation(context.Background(), claim, "widget", "web", "us-central", 3, "scaled", nil)
	}()
	time.Sleep(125 * time.Millisecond)
	if err := locker.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("finish after lock wait error=%v, want ownership lost", err)
	}
	var count int
	if err := pools[0].QueryRow(context.Background(), `SELECT count(*) FROM app_desired_replicas WHERE app='widget'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expired claimant persisted %d desired replica rows", count)
	}
}
