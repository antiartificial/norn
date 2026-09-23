package retention

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/store"
)

// A startup reader floor cannot evict processes that are already running.
// Their control-store sessions can be seen: any session of the control role
// that declares no archive-aware reader contract (a pre-archive binary sets
// none; an older contract-1 binary declares 1) holds pruning until it is
// gone, and the hold is re-checked inside the deletion transaction.
func TestRunningOldReadersHoldPruningUntilRetired(t *testing.T) {
	server, databaseURL := retentionServer(t)
	_ = server
	f := newRetentionFixtureAt(t, databaseURL)
	ctx := context.Background()
	op := f.finishedSaga("old-readers", 2)
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 {
		t.Fatalf("publish = %+v, %v", report, err)
	}
	f.archiver.Mode = ModePrune

	// A pre-archive binary: a plain connection with no declaration.
	legacy, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	// An older contract-1 binary declaring its (hot-only) reader contract.
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["application_name"] = "norn/reader=1/control"
	older, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := older.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	names, err := f.db.UnretiredReaderSessions(ctx)
	if err != nil || !slices.Contains(names, "(unnamed)") || !slices.Contains(names, "norn/reader=1/control") {
		t.Fatalf("unretired sessions = %v, %v", names, err)
	}
	intent := f.intents(op.SagaID)[0]
	if holds, err := f.db.EvidenceHolds(ctx, intent, 0); err != nil || !slices.Contains(holds, "unretired-readers-connected") {
		t.Fatalf("holds with old readers = %v, %v", holds, err)
	}
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Held != 1 || report.Pruned != 0 || f.hotCount(op.SagaID) != 2 {
		t.Fatalf("pass with old readers = %+v, %v", report, err)
	}

	// Retiring one is not enough; both must be gone.
	older.Close()
	waitForSessions(t, f.db, 1)
	if report, _ := f.archiver.RunOnce(ctx); report.Pruned != 0 {
		t.Fatalf("pruned with a pre-archive session connected: %+v", report)
	}
	if err := legacy.Close(ctx); err != nil {
		t.Fatal(err)
	}
	waitForSessions(t, f.db, 0)
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Pruned != 2 || f.hotCount(op.SagaID) != 0 {
		t.Fatalf("pass after retirement = %+v, %v", report, err)
	}
}

// A new effect row inserted while the archived object is being verified
// (external I/O, no locks held) is seen by the deletion transaction's
// re-check: the effect's foreign key waits on the locked operation row, or
// is already committed and visible.
func TestPruneHonorsEffectCreatedDuringVerification(t *testing.T) {
	f := newRetentionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	op := f.finishedSaga("effect-hold", 2)
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 {
		t.Fatalf("publish = %+v, %v", report, err)
	}
	intent := f.intents(op.SagaID)[0]
	var authority string
	if err := f.db.Pool.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity LIMIT 1`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	pruned, holds, err := f.db.PruneVerifiedEvidence(ctx, intent.ID, 0, func(ctx context.Context, _ store.EvidenceIntent) error {
		_, err := f.db.Pool.Exec(ctx, `INSERT INTO operation_effects (id, generation, authority, resource, operation_id, claim_owner, claim_generation, stage, input_digest,
			launch_payload, supervisor, supervisor_execution_id, lifecycle) VALUES ('effect-during-verify', 1, $1, 'build/effect-hold', $2, 'worker', 1, 'build',
			'sha256:'||repeat('a', 64), '{}', 'local', 'exec-1', 'reserved')`, authority, op.ID)
		return err
	})
	if err != nil || pruned != 0 || !slices.Contains(holds, "unresolved-effect") || f.hotCount(op.SagaID) != 2 {
		t.Fatalf("prune with an effect created during verification = %d %v %v", pruned, holds, err)
	}
}

func waitForSessions(t *testing.T, db *store.DB, want int) {
	t.Helper()
	for range 100 {
		names, err := db.UnretiredReaderSessions(context.Background())
		if err == nil && len(names) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	names, err := db.UnretiredReaderSessions(context.Background())
	t.Fatalf("unretired sessions = %v, %v (want %d)", names, err, want)
}
