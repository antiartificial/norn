package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
)

// TestFleetGitHubDispatchPilotRunMigrationRoundTrip proves the pilot binding
// migration against an isolated PostgreSQL database. It deliberately creates
// a real legacy row after dropping the column, so this test cannot pass merely
// because new inserts happen to use the default.
func TestFleetGitHubDispatchPilotRunMigrationRoundTrip(t *testing.T) {
	db := isolatedMigrationDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `ALTER TABLE fleet_github_dispatches DROP COLUMN IF EXISTS pilot_run_id`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	finished := now
	legacyPlanID, disposablePlanID := uuid.NewString(), uuid.NewString()
	for _, id := range []string{legacyPlanID, disposablePlanID} {
		plan := &model.Operation{ID: id, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded,
			Payload: map[string]interface{}{"action": "scale"}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished}
		if err := db.InsertCompletedOperation(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO fleet_github_dispatches (
		plan_id, plan_run_id, plan_sha256, approved_head_sha, fleet_environment,
		allow_destructive, dispatch_nonce_sha256, dispatch_state, run_id, workflow_url
	) VALUES ($1,19,$2,$3,'staging',false,$4,'prepared',0,'')`, legacyPlanID,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("upgrade did not restore fleet_github_dispatches.pilot_run_id: %v", err)
	}
	legacy, err := db.GetFleetGitHubDispatch(ctx, legacyPlanID)
	if err != nil || legacy.PilotRunID != "" {
		t.Fatalf("pre-migration dispatch did not receive empty pilot run: %+v, %v", legacy, err)
	}
	item := FleetGitHubDispatch{
		PlanID: disposablePlanID, PlanRunID: 20,
		PlanSHA256:      "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		ApprovedHeadSHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		PilotRunID:      "pilot20260907", FleetEnvironment: "staging",
		DispatchNonceSHA256: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}
	created, err := db.CreateFleetGitHubDispatch(ctx, item)
	if err != nil || created.PilotRunID != item.PilotRunID {
		t.Fatalf("create pilot run round trip = %+v, %v", created, err)
	}
	finishedDispatch, err := db.FinishFleetGitHubDispatch(ctx, item.PlanID, item.DispatchNonceSHA256, 77, 1, "https://example.invalid/run/77")
	if err != nil || finishedDispatch.PilotRunID != item.PilotRunID || finishedDispatch.RunID != 77 {
		t.Fatalf("finish pilot run round trip = %+v, %v", finishedDispatch, err)
	}
	stored, err := db.GetFleetGitHubDispatch(ctx, item.PlanID)
	if err != nil || stored.PilotRunID != item.PilotRunID || stored.DispatchState != "dispatched" {
		t.Fatalf("get pilot run round trip = %+v, %v", stored, err)
	}
}

func TestFleetGitHubDispatchRerunGenerationsAndPreparedResetAreFenced(t *testing.T) {
	db := isolatedMigrationDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	finished := now
	planID := uuid.NewString()
	if err := db.InsertCompletedOperation(ctx, &model.Operation{ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Payload: map[string]interface{}{"action": "scale"}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	nonce, approval := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	item := FleetGitHubDispatch{PlanID: planID, PlanRunID: 20, PlanSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", ApprovedHeadSHA: "dddddddddddddddddddddddddddddddddddddddd", PilotRunID: "pilot20260907", FleetEnvironment: "disposable/external-mac/nyc3", DispatchNonceSHA256: nonce}
	if _, err := db.CreateFleetGitHubDispatch(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindFleetGitHubDispatchApproval(ctx, planID, nonce, approval); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkFleetGitHubDispatchSubmitting(ctx, planID, nonce); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishFleetGitHubDispatch(ctx, planID, nonce, 77, 1, "https://example.invalid/run/77"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkFleetGitHubDispatchRerunSubmitting(ctx, planID, nonce, 77, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkFleetGitHubDispatchRerunSubmitting(ctx, planID, nonce, 77, 1); err == nil {
		t.Fatal("second POST fence for one run attempt was accepted")
	}
	if _, err := db.FinishFleetGitHubDispatchRerun(ctx, planID, nonce, 77, 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkFleetGitHubDispatchRerunSubmitting(ctx, planID, nonce, 77, 2); err != nil {
		t.Fatalf("next failed generation did not get its own fence: %v", err)
	}
	if _, err := db.FinishFleetGitHubDispatchRerun(ctx, planID, nonce, 77, 2, 4); err == nil {
		t.Fatal("jumped rerun attempt was accepted")
	}

	resetPlanID := uuid.NewString()
	if err := db.InsertCompletedOperation(ctx, &model.Operation{ID: resetPlanID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Payload: map[string]interface{}{"action": "scale"}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	reset := item
	reset.PlanID = resetPlanID
	reset.DispatchNonceSHA256 = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if _, err := db.CreateFleetGitHubDispatch(ctx, reset); err != nil {
		t.Fatal(err)
	}
	if err := db.DeletePreparedFleetGitHubDispatch(ctx, resetPlanID, reset.DispatchNonceSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetFleetGitHubDispatch(ctx, resetPlanID); err == nil {
		t.Fatal("confirmed pre-submit reset left a binding behind")
	}

	retryPlanID := uuid.NewString()
	if err := db.InsertCompletedOperation(ctx, &model.Operation{ID: retryPlanID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Payload: map[string]interface{}{"action": "scale"}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	retry := item
	retry.PlanID = retryPlanID
	retry.DispatchNonceSHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, err := db.CreateFleetGitHubDispatch(ctx, retry); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindFleetGitHubDispatchApproval(ctx, retryPlanID, retry.DispatchNonceSHA256, approval); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkFleetGitHubDispatchSubmitting(ctx, retryPlanID, retry.DispatchNonceSHA256); err != nil {
		t.Fatal(err)
	}
	prepared, err := db.ResetFleetGitHubDispatchPreSubmit(ctx, retryPlanID, retry.DispatchNonceSHA256)
	if err != nil || prepared.DispatchState != "prepared" || prepared.ApprovalEnvelopeSHA256 != approval || prepared.RunID != 0 {
		t.Fatalf("pre-submit retry reset = %+v, %v", prepared, err)
	}
}
