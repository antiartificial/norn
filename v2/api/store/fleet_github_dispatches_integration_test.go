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
	finishedDispatch, err := db.FinishFleetGitHubDispatch(ctx, item.PlanID, item.DispatchNonceSHA256, 77, "https://example.invalid/run/77")
	if err != nil || finishedDispatch.PilotRunID != item.PilotRunID || finishedDispatch.RunID != 77 {
		t.Fatalf("finish pilot run round trip = %+v, %v", finishedDispatch, err)
	}
	stored, err := db.GetFleetGitHubDispatch(ctx, item.PlanID)
	if err != nil || stored.PilotRunID != item.PilotRunID || stored.DispatchState != "dispatched" {
		t.Fatalf("get pilot run round trip = %+v, %v", stored, err)
	}
}
