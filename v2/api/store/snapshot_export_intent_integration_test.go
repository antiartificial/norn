package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/internal/pgtest"
)

func TestSnapshotExportIntentSurvivesClaimTurnover(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_snapshot_export_intent")
	db, err := Connect(server.URL("norn_snapshot_export_intent"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	op := insertOperationFixture(t, db, "app.snapshot-export", 2, map[string]interface{}{})
	claimed, first, err := db.ClaimNextOperation(ctx, "first-export-worker", time.Minute, []string{"app.snapshot-export"})
	if err != nil || claimed == nil {
		t.Fatalf("first claim = %+v, %v", claimed, err)
	}
	want := SnapshotExportIntent{OperationID: op.ID, Bucket: "retained", ObjectKey: "snapshots/demo/operations/" + op.ID + "/dump",
		DumpSHA256: strings.Repeat("a", 64), DumpSize: 17, ManifestSHA256: strings.Repeat("b", 64)}
	if err := db.PrepareSnapshotExportIntent(ctx, first, want); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM snapshot_export_intents WHERE operation_id=$1 AND object_key=$2`, op.ID, want.ObjectKey).Scan(&state); err != nil || state != "prepared" {
		t.Fatalf("remote-write reservation = %q, %v", state, err)
	}
	changed := want
	changed.DumpSHA256 = strings.Repeat("c", 64)
	if err := db.PrepareSnapshotExportIntent(ctx, first, changed); err == nil {
		t.Fatal("changed bytes reused the existing export reservation")
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='queued', locked_by='', locked_until=NULL, next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	claimed, second, err := db.ClaimNextOperation(ctx, "second-export-worker", time.Minute, []string{"app.snapshot-export"})
	if err != nil || claimed == nil || second.Generation() == first.Generation() {
		t.Fatalf("successor claim = %+v, %+v, %v", claimed, second, err)
	}
	if err := db.PrepareSnapshotExportIntent(ctx, first, want); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale claimant reservation = %v", err)
	}
	if err := db.PrepareSnapshotExportIntent(ctx, second, want); err != nil {
		t.Fatalf("successor could not adopt exact export: %v", err)
	}
	if err := db.RecordSnapshotExportReceipt(ctx, changed); err == nil {
		t.Fatal("changed bytes recorded a publication receipt")
	}
	if err := db.RecordSnapshotExportReceipt(ctx, want); err != nil {
		t.Fatalf("verified publication receipt: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM snapshot_export_intents WHERE operation_id=$1 AND object_key=$2`, op.ID, want.ObjectKey).Scan(&state); err != nil || state != "published" {
		t.Fatalf("completed export = %q, %v", state, err)
	}
}
