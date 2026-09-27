package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestReviewCheckpointRejectsLeaseExpiredDuringLockWait(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, claim := claimEffectOperation(t, dbs[0], "checkpoint-review", time.Second)
	holder, err := dbs[0].Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	var expiry time.Time
	if err := holder.QueryRow(ctx, `SELECT locked_until FROM operations WHERE id=$1 FOR UPDATE`, claim.OperationID()).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := dbs[1].RecordOperationCheckpoint(ctx, claim, CheckpointBuild, json.RawMessage(`{"image":"test"}`))
		result <- err
	}()
	// Observe this holder's blocked waiter before letting the lease expire.
	for {
		var waiting bool
		if err := holder.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pg_backend_pid() = ANY(pg_blocking_pids(pid)))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("checkpoint did not wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	for {
		var expired bool
		if err := holder.QueryRow(ctx, `SELECT clock_timestamp() > $1`, expiry).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("checkpoint accepted a claim whose lease expired while waiting for the row lock")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var count int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT count(*) FROM operation_checkpoints WHERE operation_id=$1`, claim.OperationID()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expired claim wrote %d checkpoints", count)
	}
}
