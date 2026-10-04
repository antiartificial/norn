package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

func fleetFenceTestPlan(t *testing.T, db *DB, ctx context.Context, cluster string) string {
	t.Helper()
	planID := uuid.NewString()
	now := time.Now().UTC()
	finished := now
	if err := db.InsertCompletedOperation(ctx, &model.Operation{
		ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded,
		Payload: map[string]interface{}{"cluster": cluster}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished,
	}); err != nil {
		t.Fatal(err)
	}
	return planID
}

// TestMarkFleetGitHubDispatchRerunSubmittingFencedRequiresCurrentEpochHolder
// is a direct (store-level) test of M5: the rerun fence must require the
// target fence to be held by the same plan and nonce at the current
// authority epoch, and must refuse an abandoned plan, before it ever POSTs
// a rerun.
func TestMarkFleetGitHubDispatchRerunSubmittingFencedRequiresCurrentEpochHolder(t *testing.T) {
	db := isolatedMigrationDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "rerun-fence", StateBackend: "s3://rerun-fence/state"}
	target, err := db.RegisterFleetTarget(ctx, identity, []string{"cluster:rerun-fence"}, "op-rerun-fence")
	if err != nil {
		t.Fatal(err)
	}
	planID := fleetFenceTestPlan(t, db, ctx, "rerun-fence")
	nonce, approval := "1111111111111111111111111111111111111111111111111111111111111111", "2222222222222222222222222222222222222222222222222222222222222222"
	if _, err := db.CreateFleetGitHubDispatch(ctx, FleetGitHubDispatch{
		PlanID: planID, PlanRunID: 1, PlanSHA256: "3333333333333333333333333333333333333333333333333333333333333333",
		ApprovedHeadSHA: "4444444444444444444444444444444444444444", FleetEnvironment: "staging", DispatchNonceSHA256: nonce,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindFleetGitHubDispatchApproval(ctx, planID, nonce, approval); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkFleetGitHubDispatchSubmittingFenced(ctx, planID, nonce, "rerun-fence", "", time.Now().UTC()); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := db.FinishFleetGitHubDispatch(ctx, planID, nonce, 500, 1, "https://example.invalid/run/500"); err != nil {
		t.Fatal(err)
	}
	acquired := mustGetFleetTargetFenceRow(t, db, target.TargetID)
	if !acquired.Held || acquired.HolderPlanID != planID || acquired.AuthorityEpoch != 1 {
		t.Fatalf("fixture did not acquire as expected: %+v", acquired)
	}

	// The epoch advances before the rerun. M5: DecideAcquire's same-holder
	// branch never silently adopts a newer epoch, so this must refuse.
	if _, err := db.AdvanceFleetAuthorityEpoch(ctx, 1, "rerun-fence-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkFleetGitHubDispatchRerunSubmittingFenced(ctx, planID, nonce, 500, 1, "rerun-fence", ""); err == nil {
		t.Fatal("rerun under a superseded epoch was accepted")
	} else if fe, ok := err.(*lifecycle.FenceError); !ok || fe.Code != lifecycle.CodeFleetTargetAuthoritySuperseded {
		t.Fatalf("rerun under a superseded epoch = %v, want %s", err, lifecycle.CodeFleetTargetAuthoritySuperseded)
	}
	afterRefusal := mustGetFleetTargetFenceRow(t, db, target.TargetID)
	if afterRefusal.Generation != acquired.Generation || afterRefusal.AuthorityEpoch != acquired.AuthorityEpoch {
		t.Fatalf("a refused rerun must not change the fence: before=%+v after=%+v", acquired, afterRefusal)
	}

	// Bind re-binds the fence to the new epoch (M13); the rerun then
	// succeeds at the current epoch without bumping generation (same holder).
	if err := bindFleetTargetFenceTxForTest(ctx, db, planID); err != nil {
		t.Fatal(err)
	}
	rebound := mustGetFleetTargetFenceRow(t, db, target.TargetID)
	if rebound.AuthorityEpoch != 2 || rebound.Generation != acquired.Generation+1 {
		t.Fatalf("bind did not re-bind to the new epoch: %+v", rebound)
	}
	if _, err := db.MarkFleetGitHubDispatchRerunSubmittingFenced(ctx, planID, nonce, 500, 1, "rerun-fence", ""); err != nil {
		t.Fatalf("rerun at the current epoch was refused: %v", err)
	}
	afterRerun := mustGetFleetTargetFenceRow(t, db, target.TargetID)
	if afterRerun.Generation != rebound.Generation {
		t.Fatalf("a same-holder rerun acquire must not bump generation: before=%+v after=%+v", rebound, afterRerun)
	}
	dispatch, err := db.GetFleetGitHubDispatch(ctx, planID)
	if err != nil || dispatch.DispatchState != "rerun_submitting" {
		t.Fatalf("rerun did not fence the dispatch row: %+v, %v", dispatch, err)
	}

	// An abandoned plan refuses the rerun fence permanently.
	if err := abandonFleetPlanForTest(ctx, db, planID, nonce, target.TargetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishFleetGitHubDispatchRerun(ctx, planID, nonce, 500, 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkFleetGitHubDispatchRerunSubmittingFenced(ctx, planID, nonce, 500, 2, "rerun-fence", ""); err == nil {
		t.Fatal("rerun of an abandoned plan was accepted")
	} else if fe, ok := err.(*lifecycle.FenceError); !ok || fe.Code != lifecycle.CodeFleetTargetHolderAbandoned {
		t.Fatalf("rerun of an abandoned plan = %v, want %s", err, lifecycle.CodeFleetTargetHolderAbandoned)
	}
}

// TestResetFleetGitHubDispatchPreSubmitDispatchNotSubmittedExemptsRevalidation
// is a direct (store-level) test of the dispatch_not_submitted exemption
// (M4/Q2): DecideAcquire refuses a new plan started before
// last_release_at+RevalidationSkew for every release reason EXCEPT
// dispatch_not_submitted, which ResetFleetGitHubDispatchPreSubmit produces.
// A plan that never POSTed was never executing, so the next plan on the
// same target may acquire immediately, with no skew to wait out.
func TestResetFleetGitHubDispatchPreSubmitDispatchNotSubmittedExemptsRevalidation(t *testing.T) {
	db := isolatedMigrationDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "presubmit-exempt", StateBackend: "s3://presubmit-exempt/state"}
	target, err := db.RegisterFleetTarget(ctx, identity, []string{"cluster:presubmit-exempt"}, "op-presubmit-exempt")
	if err != nil {
		t.Fatal(err)
	}
	planA := fleetFenceTestPlan(t, db, ctx, "presubmit-exempt")
	nonceA := "5555555555555555555555555555555555555555555555555555555555555555"
	if _, err := db.CreateFleetGitHubDispatch(ctx, FleetGitHubDispatch{
		PlanID: planA, PlanRunID: 1, PlanSHA256: "6666666666666666666666666666666666666666666666666666666666666666",
		ApprovedHeadSHA: "7777777777777777777777777777777777777777", FleetEnvironment: "staging", DispatchNonceSHA256: nonceA,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkFleetGitHubDispatchSubmittingFenced(ctx, planA, nonceA, "presubmit-exempt", "", time.Now().UTC()); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	reset, err := db.ResetFleetGitHubDispatchPreSubmit(ctx, planA, nonceA)
	if err != nil || reset.DispatchState != "prepared" {
		t.Fatalf("reset: %+v, %v", reset, err)
	}
	released := mustGetFleetTargetFenceRow(t, db, target.TargetID)
	if released.Held || released.LastRelease == nil || released.LastRelease.Reason != "dispatch_not_submitted" {
		t.Fatalf("reset did not release with reason dispatch_not_submitted: %+v", released)
	}

	planB := fleetFenceTestPlan(t, db, ctx, "presubmit-exempt")
	nonceB := "8888888888888888888888888888888888888888888888888888888888888888"
	if _, err := db.CreateFleetGitHubDispatch(ctx, FleetGitHubDispatch{
		PlanID: planB, PlanRunID: 1, PlanSHA256: "9999999999999999999999999999999999999999999999999999999999999999",
		ApprovedHeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FleetEnvironment: "staging", DispatchNonceSHA256: nonceB,
	}); err != nil {
		t.Fatal(err)
	}
	// planB "started" the instant the reset released the fence: with any
	// other release reason this would be refused as fleet_plan_revalidation_required.
	if _, err := db.MarkFleetGitHubDispatchSubmittingFenced(ctx, planB, nonceB, "presubmit-exempt", "", released.LastRelease.At); err != nil {
		t.Fatalf("acquire immediately after a dispatch_not_submitted release was refused: %v", err)
	}
}

func mustGetFleetTargetFenceRow(t *testing.T, db *DB, targetID string) lifecycle.FenceFacts {
	t.Helper()
	tx, err := db.Pool.BeginTx(context.Background(), pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	fence, err := GetFleetTargetFence(context.Background(), tx, targetID, false)
	if err != nil {
		t.Fatal(err)
	}
	return fence
}

func bindFleetTargetFenceTxForTest(ctx context.Context, db *DB, planID string) error {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := bindFleetTargetFenceTx(ctx, tx, planID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func abandonFleetPlanForTest(ctx context.Context, db *DB, planID, nonceSHA256, targetID string) error {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := AbandonFleetPlan(ctx, tx, planID, nonceSHA256, targetID, "op-abandon-test", time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// TestFleetGitHubDispatchPilotRunMigrationRoundTrip upgrades a real version-43
// dispatch row into the protected pilot schema without retaining its raw nonce.
func TestFleetGitHubDispatchPilotRunMigrationRoundTrip(t *testing.T) {
	db := isolatedMigrationDB(t)
	migrateControlThrough43(t, db)
	ctx := context.Background()
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
		allow_destructive, dispatch_nonce, dispatch_nonce_sha256, run_id, workflow_url
	) VALUES ($1,19,$2,$3,'staging',false,'legacy-raw-nonce',$4,0,'')`, legacyPlanID,
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
