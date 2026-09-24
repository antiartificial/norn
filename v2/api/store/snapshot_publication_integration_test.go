package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
)

// A SELECT FOR UPDATE fence vanishes when its backend is terminated. The
// durable intent must survive that exact failure, be adopted by the successor,
// and permit completion only after the successor has recorded the receipt.
func TestSnapshotPublicationIntentSurvivesTerminatedBackendAndClaimTurnover(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_snapshot_intent")
	db, err := Connect(server.URL("norn_snapshot_intent"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	op := insertOperationFixture(t, db, "app.snapshot", 2, map[string]interface{}{})
	claimed, first, err := db.ClaimNextOperation(ctx, "first-worker", time.Minute, []string{"app.snapshot"})
	if err != nil || claimed == nil {
		t.Fatalf("first claim = %+v, %v", claimed, err)
	}
	var authority string
	if err := db.Pool.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity WHERE singleton`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	effects, err := NewPGEffectStore(db)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effectReservation(t, authority, "app/demo/snapshot", "snapshot-intent-execution", first)
	reservation.Stage = "app.snapshot"
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		t.Fatal(err)
	}
	reserved, err := effects.Reserve(ctx, reservation)
	if err != nil {
		t.Fatal(err)
	}
	want := SnapshotPublicationIntent{OperationID: op.ID, EffectID: reserved.Record.Token.EffectID, InputDigest: reservation.InputDigest,
		SupervisorRootID: "snapshot-root", SupervisorExecutionID: reservation.SupervisorExecutionID, Target: []byte(`{}`), CatalogRevision: 1,
		Namespace: "snapshots/targets/demo", Filename: "demo_effect-stable_20260924T120000.dump", SHA256: strings.Repeat("a", 64), Size: 1, OriginClaimGeneration: first.Generation()}
	if _, err := db.PrepareSnapshotPublicationIntent(ctx, first, want); err != nil {
		t.Fatal(err)
	}

	// Terminate the pool's former backend(s), precisely the failure that releases
	// a row lock held across Link. The direct expiry is the deterministic lease
	// turnover seam; production obtains it from the database wall clock.
	if _, err := db.Pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=current_database() AND pid <> pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	db.Pool.Reset()
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='queued', locked_by='', locked_until=NULL, next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	claimed, second, err := db.ClaimNextOperation(ctx, "second-worker", time.Minute, []string{"app.snapshot"})
	if err != nil || claimed == nil || second.Generation() == first.Generation() {
		t.Fatalf("successor claim = %+v, %+v, %v", claimed, second, err)
	}
	if _, err := db.PrepareSnapshotPublicationIntent(ctx, second, want); err != nil {
		t.Fatalf("successor did not adopt exact intent: %v", err)
	}
	if err := db.RecordSnapshotPublicationReceipt(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishClaimedOperation(ctx, second, model.OperationSucceeded, "snapshot published", nil); err != nil {
		t.Fatal(err)
	}
}
