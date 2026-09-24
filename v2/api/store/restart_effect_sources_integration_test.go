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

func TestRestartEffectSourcesAcceptSuccessorClaimForUnattemptedSource(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 2)
	ctx := context.Background()
	op := insertOperationFixture(t, dbs[0], "app.restart", 2, map[string]interface{}{"app": "demo"})
	_, first, err := dbs[0].ClaimNextOperation(ctx, "restart-one", 100*time.Millisecond, []string{"app.restart"})
	if err != nil {
		t.Fatal(err)
	}
	source := nomad.RestartAllocation{ID: "source-b", JobID: "demo", CreateIndex: 8}
	if err := dbs[0].EnsureRestartEffectSources(ctx, first, []nomad.RestartAllocation{source}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(180 * time.Millisecond)
	if err := dbs[1].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	_, successor, err := dbs[1].ClaimNextOperation(ctx, "restart-two", time.Minute, []string{"app.restart"})
	if err != nil || successor.Generation() != first.Generation()+1 {
		t.Fatalf("successor=%+v first=%+v err=%v", successor, first, err)
	}
	if err := dbs[1].MarkRestartSourceAttempted(ctx, successor, source); err != nil {
		t.Fatalf("successor could not advance unattempted source: %v", err)
	}
	rows, err := dbs[1].RestartEffectSources(ctx, op.ID)
	if err != nil || len(rows) != 1 || !rows[0].Attempted {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}
