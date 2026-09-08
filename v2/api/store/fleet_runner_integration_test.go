package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/model"
)

// isolatedMigrationDB gives destructive migration coverage its own database.
// Never point ALTER/DROP migration tests at the package-wide integration DB:
// go test may execute packages concurrently against NORN_TEST_DATABASE_URL.
func isolatedMigrationDB(t *testing.T) *DB {
	t.Helper()
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	admin, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := "norn_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedName := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Pool.Exec(context.Background(), "CREATE DATABASE "+quotedName); err != nil {
		admin.Close()
		t.Fatalf("create isolated migration database: %v", err)
	}
	config, err := operationPoolConfig(databaseURL)
	if err != nil {
		_, _ = admin.Pool.Exec(context.Background(), "DROP DATABASE "+quotedName)
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		_, _ = admin.Pool.Exec(context.Background(), "DROP DATABASE "+quotedName)
		admin.Close()
		t.Fatalf("connect isolated migration database: %v", err)
	}
	db := &DB{Pool: pool}
	t.Cleanup(func() {
		db.Close()
		_, _ = admin.Pool.Exec(context.Background(), "DROP DATABASE "+quotedName)
		admin.Close()
	})
	return db
}

// TestMigrateSerializesParallelCalls exercises the same shared-database shape
// used by `go test ./...`: each caller must wait for the one advisory-locked
// schema program rather than interleaving ALTER/CREATE INDEX operations.
func TestMigrateSerializesParallelCalls(t *testing.T) {
	db := isolatedMigrationDB(t)
	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- Migrate(db)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("parallel Migrate: %v", err)
		}
	}
}

// TestMigrateGatesLiveDML proves that the migration transaction drains an
// already-running writer instead of interleaving DDL with it. It is purposely
// run against an isolated PostgreSQL 16 database: an advisory migration lock
// alone would not cover this separate DML transaction.
func TestMigrateGatesLiveDML(t *testing.T) {
	db := isolatedMigrationDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO control_events (type, app_id) VALUES ('migration-live-dml', 'test')`); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Migrate(db) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("migration completed while its conflicting live DML transaction was open")
		}
		t.Fatalf("migration failed instead of waiting for live DML: %v", err)
	case <-time.After(150 * time.Millisecond):
		// Expected: LOCK TABLE waits for the writer without a deadlock.
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("migration after live DML: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("migration did not complete after live DML committed")
	}
}

// TestFleetRunnerAttemptLifecycle exercises PostgreSQL uniqueness, optimistic
// revisions, evidence-gated advance, atomic failure, and retry persistence.
func TestFleetRunnerAttemptLifecycle(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC()
	finished := now
	planID := uuid.NewString()
	successID := uuid.NewString()
	failureID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM operations WHERE id IN ($1,$2,$3)`, successID, failureID, planID)
	})
	plan := &model.Operation{ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Payload: map[string]interface{}{"action": "scale"}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished}
	if err := db.InsertCompletedOperation(ctx, plan); err != nil {
		t.Fatal(err)
	}

	attempt := &model.FleetRunnerAttempt{
		ID: uuid.NewString(), PlanID: planID, RunnerAttemptID: "integration-1", Status: model.FleetRunnerAttemptRunning,
		CurrentPhase: "infrastructure_applied", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PlanSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SourceDispatchRunID:     93,
		PilotRunID:              "pilot20260907",
		HeartbeatTimeoutSeconds: 120, Revision: 1, StartedAt: now, PhaseStartedAt: now.Add(-5 * time.Minute), HeartbeatAt: now, UpdatedAt: now,
	}
	if err := db.CreateFleetRunnerAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if attempt.RootAttemptID != attempt.ID {
		t.Fatalf("initial root attempt = %q, want %q", attempt.RootAttemptID, attempt.ID)
	}
	concurrent := *attempt
	concurrent.ID, concurrent.RunnerAttemptID, concurrent.Attempt = uuid.NewString(), "integration-2", 0
	if err := db.CreateFleetRunnerAttempt(ctx, &concurrent); err == nil {
		t.Fatal("second live attempt was accepted")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("second live attempt error = %v", err)
		}
	}
	live, err := db.HeartbeatFleetRunnerAttempt(ctx, attempt.ID, attempt.CurrentPhase, 1, attempt.Revision, "integration alive")
	if err != nil || live.Revision != 2 || live.HeartbeatSequence != 1 {
		t.Fatalf("heartbeat attempt=%+v err=%v", live, err)
	}

	success := &model.Operation{
		ID: successID, Kind: "fleet.reconciliation", Ref: planID, Status: model.OperationSucceeded,
		Payload:   map[string]interface{}{"phase": "infrastructure_applied", "attemptId": attempt.ID, "commitSha": attempt.CommitSHA, "planSha256": attempt.PlanSHA256},
		StartedAt: now, UpdatedAt: now, FinishedAt: &finished,
	}
	if err := db.InsertFleetReconciliation(ctx, success, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if !success.StartedAt.Equal(attempt.PhaseStartedAt) {
		t.Fatalf("checkpoint start = %s, want server-owned phase start %s", success.StartedAt, attempt.PhaseStartedAt)
	}
	live, err = db.AdvanceFleetRunnerAttempt(ctx, attempt.ID, "infrastructure_applied", "inventory_generated", live.Revision, false)
	if err != nil || live.CurrentPhase != "inventory_generated" || !live.PhaseStartedAt.After(attempt.PhaseStartedAt) {
		t.Fatalf("advance attempt=%+v err=%v", live, err)
	}

	failure := &model.Operation{
		ID: failureID, Kind: "fleet.reconciliation", Ref: planID, Status: model.OperationFailed, Message: "inventory failed",
		Payload:   map[string]interface{}{"phase": "inventory_generated", "attemptId": attempt.ID, "commitSha": attempt.CommitSHA, "planSha256": attempt.PlanSHA256},
		StartedAt: now, UpdatedAt: now, FinishedAt: &finished,
	}
	if err := db.InsertFleetReconciliation(ctx, failure, attempt.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := db.GetFleetRunnerAttempt(ctx, attempt.ID)
	if err != nil || failed.Status != model.FleetRunnerAttemptFailed || failed.LastError != "inventory failed" {
		t.Fatalf("failed attempt=%+v err=%v", failed, err)
	}
	replacement := &model.FleetRunnerAttempt{
		ID: uuid.NewString(), PlanID: planID, RunnerAttemptID: "integration-retry", Status: model.FleetRunnerAttemptRunning,
		CurrentPhase: failed.CurrentPhase, CommitSHA: failed.CommitSHA, PlanSHA256: failed.PlanSHA256, RetryOf: failed.ID,
		SourceDispatchRunID:     failed.SourceDispatchRunID,
		PilotRunID:              failed.PilotRunID,
		HeartbeatTimeoutSeconds: 120, Revision: 1, StartedAt: now, HeartbeatAt: now, UpdatedAt: now,
	}
	if err := db.RetryFleetRunnerAttempt(ctx, failed, replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.Attempt != 2 || replacement.RetryOf != failed.ID || replacement.RootAttemptID != attempt.ID {
		t.Fatalf("replacement = %+v", replacement)
	}
	canceled, err := db.CancelFleetRunnerAttempt(ctx, replacement.ID, replacement.Revision, "operator stopped prior runner")
	if err != nil || canceled.Status != model.FleetRunnerAttemptCanceled {
		t.Fatalf("cancel before recovery = %+v, %v", canceled, err)
	}
	firstRecovery := &model.FleetRunnerAttempt{
		ID: uuid.NewString(), PlanID: planID, RunnerAttemptID: "github-actions:acme/norn-fleet:104:1",
		Status: model.FleetRunnerAttemptRunning, CurrentPhase: canceled.CurrentPhase,
		CommitSHA: canceled.CommitSHA, PlanSHA256: canceled.PlanSHA256, SourceDispatchRunID: canceled.SourceDispatchRunID, PilotRunID: canceled.PilotRunID,
		Recovery: true, RetryOf: canceled.ID, HeartbeatTimeoutSeconds: canceled.HeartbeatTimeoutSeconds,
		Revision: 1, StartedAt: now, HeartbeatAt: now, UpdatedAt: now,
	}
	if err := db.RecoverFleetRunnerAttempt(ctx, canceled, firstRecovery); err != nil {
		t.Fatal(err)
	}
	if firstRecovery.Attempt != 3 || firstRecovery.RetryOf != canceled.ID || firstRecovery.RootAttemptID != attempt.ID || firstRecovery.PilotRunID != attempt.PilotRunID || !firstRecovery.Recovery {
		t.Fatalf("first recovery = %+v", firstRecovery)
	}
	secondRecovery := &model.FleetRunnerAttempt{
		ID: uuid.NewString(), PlanID: planID, RunnerAttemptID: "github-actions:acme/norn-fleet:105:1",
		Status: model.FleetRunnerAttemptRunning, CurrentPhase: firstRecovery.CurrentPhase,
		CommitSHA: firstRecovery.CommitSHA, PlanSHA256: firstRecovery.PlanSHA256, SourceDispatchRunID: firstRecovery.SourceDispatchRunID, PilotRunID: firstRecovery.PilotRunID,
		Recovery: true, RetryOf: firstRecovery.ID, HeartbeatTimeoutSeconds: firstRecovery.HeartbeatTimeoutSeconds,
		Revision: 1, StartedAt: now, HeartbeatAt: now, UpdatedAt: now,
	}
	if err := db.RecoverFleetRunnerAttempt(ctx, firstRecovery, secondRecovery); err != nil {
		t.Fatal(err)
	}
	firstStored, err := db.GetFleetRunnerAttempt(ctx, firstRecovery.ID)
	if err != nil || firstStored.Status != model.FleetRunnerAttemptCanceled || secondRecovery.Attempt != 4 || secondRecovery.RetryOf != firstRecovery.ID || secondRecovery.RootAttemptID != attempt.ID {
		t.Fatalf("repeated recovery first=%+v second=%+v err=%v", firstStored, secondRecovery, err)
	}
}

// TestFleetRunnerPilotRunMigrationRoundTrip exercises the upgrade against a
// real PostgreSQL database. The legacy column drop is intentional: Migrate
// must restore the safe empty default before both old ordinary and new
// disposable records can be read.
func TestFleetRunnerPilotRunMigrationRoundTrip(t *testing.T) {
	db := isolatedMigrationDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(), `ALTER TABLE fleet_runner_attempts DROP COLUMN IF EXISTS pilot_run_id`); err != nil {
		t.Fatal(err)
	}
	ctx, now := context.Background(), time.Now().UTC()
	finished := now
	planID, attemptID, legacyPlanID, legacyID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM operations WHERE id = ANY($1)`, []string{planID, legacyPlanID})
	})
	for _, id := range []string{planID, legacyPlanID} {
		plan := &model.Operation{ID: id, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Payload: map[string]interface{}{"action": "scale"}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished}
		if err := db.InsertCompletedOperation(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	// Insert before the upgrade while the legacy table has no pilot_run_id. The
	// migration must backfill PostgreSQL's empty default on this real row.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO fleet_runner_attempts (
		id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, recovery,
		runner_attempt_id, status, current_phase, commit_sha, plan_sha256,
		workflow_url, principal_subject, retry_of, heartbeat_sequence,
		heartbeat_timeout_seconds, revision, started_at, phase_started_at,
		heartbeat_at, updated_at, finished_at, last_error, metadata
	) VALUES ($1,$2,1,$1,94,false,'ordinary-run','failed','infrastructure_applied',$3,$4,
		'', '', '', 0,120,1,$5,$5,$5,$5,$5,'','{}')`,
		legacyID, legacyPlanID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", now); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("upgrade did not restore pilot_run_id: %v", err)
	}
	legacyStored, err := db.GetFleetRunnerAttempt(ctx, legacyID)
	if err != nil || legacyStored.PilotRunID != "" {
		t.Fatalf("pre-migration ordinary attempt did not receive empty pilot run: %+v, %v", legacyStored, err)
	}
	attempt := &model.FleetRunnerAttempt{ID: attemptID, PlanID: planID, RunnerAttemptID: "pilot-run", Status: model.FleetRunnerAttemptRunning, CurrentPhase: "infrastructure_applied", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PlanSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", SourceDispatchRunID: 93, PilotRunID: "pilot20260907", HeartbeatTimeoutSeconds: 120, Revision: 1, StartedAt: now, HeartbeatAt: now, UpdatedAt: now}
	if err := db.CreateFleetRunnerAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := db.FailFleetRunnerAttempt(ctx, attempt.ID, attempt.CurrentPhase, "finished"); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetFleetRunnerAttempt(ctx, attempt.ID)
	if err != nil || stored.PilotRunID != attempt.PilotRunID {
		t.Fatalf("pilot attempt round trip = %+v, %v", stored, err)
	}
}
