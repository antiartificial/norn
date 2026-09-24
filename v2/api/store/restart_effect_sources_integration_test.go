package store

import (
	"context"
	"testing"
	"time"

	"norn/v2/api/nomad"
)

func TestRestartEffectSourcesFenceAttemptsAndPreserveAcknowledgement(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	op, claim := claimEffectOperation(t, dbs[0], "restart-worker", time.Minute)
	source := nomad.RestartAllocation{ID: "source-a", JobID: "demo", CreateIndex: 7}
	if err := dbs[0].EnsureRestartEffectSources(context.Background(), claim, []nomad.RestartAllocation{source}); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].MarkRestartSourceAttempted(context.Background(), claim, source); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].MarkRestartSourceAcknowledged(context.Background(), claim, source); err != nil {
		t.Fatal(err)
	}
	rows, err := dbs[0].RestartEffectSources(context.Background(), op.ID)
	if err != nil || len(rows) != 1 || !rows[0].Attempted || !rows[0].Acknowledged {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	stale, err := NewOperationClaim(op.ID, claim.OwnerID(), claim.Generation()+1)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].MarkRestartSourceAttempted(context.Background(), stale, source); err == nil {
		t.Fatal("stale generation recorded an attempt")
	}
}
