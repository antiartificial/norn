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
		HeartbeatTimeoutSeconds: 120, Revision: 1, StartedAt: now, HeartbeatAt: now, UpdatedAt: now,
	}
	if err := db.CreateFleetRunnerAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
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
	live, err = db.AdvanceFleetRunnerAttempt(ctx, attempt.ID, "infrastructure_applied", "inventory_generated", live.Revision, false)
	if err != nil || live.CurrentPhase != "inventory_generated" {
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
		HeartbeatTimeoutSeconds: 120, Revision: 1, StartedAt: now, HeartbeatAt: now, UpdatedAt: now,
	}
	if err := db.RetryFleetRunnerAttempt(ctx, failed, replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.Attempt != 2 || replacement.RetryOf != failed.ID {
		t.Fatalf("replacement = %+v", replacement)
	}
}
