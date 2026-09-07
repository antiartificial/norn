package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"norn/v2/api/model"
)

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
	if _, err := db.Pool.Exec(context.Background(), `ALTER TABLE fleet_runner_attempts DROP COLUMN IF EXISTS pilot_run_id`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("upgrade did not restore pilot_run_id: %v", err)
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
	legacy := &model.FleetRunnerAttempt{ID: legacyID, PlanID: legacyPlanID, RunnerAttemptID: "ordinary-run", Status: model.FleetRunnerAttemptFailed, CurrentPhase: "infrastructure_applied", CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256, SourceDispatchRunID: 94, HeartbeatTimeoutSeconds: 120, Revision: 1, StartedAt: now, HeartbeatAt: now, UpdatedAt: now}
	if err := db.CreateFleetRunnerAttempt(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	legacyStored, err := db.GetFleetRunnerAttempt(ctx, legacy.ID)
	if err != nil || legacyStored.PilotRunID != "" {
		t.Fatalf("legacy ordinary attempt did not retain empty pilot run: %+v, %v", legacyStored, err)
	}
}
