package controlrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// TestFleetAuthorityEpochAdvanceAfterRestorePostgres is deliberately narrow.
// It calls the extracted restore step (advanceRestoredFleetAuthorityEpoch, the
// exact function RestorePassive runs after verification) directly on an
// isolated, freshly migrated schema seeded with fence, attempt and dispatch
// state, then checks the effect through the real PG store paths.
//
// End-to-end coverage (CreateBundle, RestorePassive into a separate database)
// is blocked by the base-branch Mini-extension classification drift: bundle
// and restore classification refuse the external_deployment_* and
// fleet_runner_checkpoint_refs tables and the extension columns. The same
// drift fails TestCreateVerifyRestorePassiveRoundTrip on the base. The fix is
// tracked on a separate branch; this test does not cover bundle or restore.
func TestFleetAuthorityEpochAdvanceAfterRestorePostgres(t *testing.T) {
	pool, schema := inspectionTestDatabase(t)
	ctx := context.Background()
	db := &store.DB{Pool: pool}
	signer, err := store.NewHMACAcceptanceSigner("fleet-epoch-restore-signing-key-000000000")
	if err != nil {
		t.Fatal(err)
	}
	operationStore, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	phases := lifecycle.Phases(lifecycle.PostgresLegacy, false)
	first, second := phases[0], phases[1]
	commit, planSHA := strings.Repeat("c", 40), strings.Repeat("b", 64)

	type seeded struct {
		targetID string
		planID   string
		plan     model.Operation
		nonce    string
		attempt  *model.FleetRunnerAttempt
	}
	// seed registers a target for a new cluster, acquires its fence through a
	// dispatch, and (when withAttempt) binds the first runner attempt.
	seed := func(name string, withAttempt bool) seeded {
		t.Helper()
		target, err := db.RegisterFleetTarget(ctx, lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: name, StateBackend: "s3://" + name + "/state"}, []string{"cluster:" + name}, "register-"+name)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		finished := now
		plan := model.Operation{
			ID: uuid.NewString(), Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Source: "control-api",
			StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1, Metadata: map[string]interface{}{},
			Payload: map[string]interface{}{"cluster": name, "pool": "app", "action": "scale", "digest": "sha256:" + strings.Repeat("a", 64),
				"current": map[string]interface{}{"desired": 2}, "proposed": map[string]interface{}{"desired": 3}},
		}
		plan.Payload["id"] = plan.ID
		if err := db.InsertCompletedOperation(ctx, &plan); err != nil {
			t.Fatal(err)
		}
		nonce, approval := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", ""), strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
		if _, err := db.CreateFleetGitHubDispatch(ctx, store.FleetGitHubDispatch{PlanID: plan.ID, PlanRunID: 1, PlanSHA256: planSHA, ApprovedHeadSHA: commit, FleetEnvironment: "staging", DispatchNonceSHA256: nonce}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.BindFleetGitHubDispatchApproval(ctx, plan.ID, nonce, approval); err != nil {
			t.Fatal(err)
		}
		if _, err := db.MarkFleetGitHubDispatchSubmittingFenced(ctx, plan.ID, nonce, name, "", time.Now().UTC()); err != nil {
			t.Fatalf("acquire %s: %v", name, err)
		}
		if _, err := db.FinishFleetGitHubDispatch(ctx, plan.ID, nonce, 500, 1, "https://example.invalid/run/500"); err != nil {
			t.Fatal(err)
		}
		item := seeded{targetID: target.TargetID, planID: plan.ID, plan: plan, nonce: nonce}
		if withAttempt {
			at := time.Now().UTC()
			attempt := &model.FleetRunnerAttempt{
				ID: uuid.NewString(), PlanID: plan.ID, RunnerAttemptID: "run-" + name, Status: model.FleetRunnerAttemptRunning, CurrentPhase: first,
				CommitSHA: commit, PlanSHA256: planSHA, WorkflowURL: "https://github.com/acme/fleet/actions/runs/" + name, SourceDispatchRunID: 500,
				HeartbeatTimeoutSeconds: 600, Revision: 1, StartedAt: at, PhaseStartedAt: at, HeartbeatAt: at, UpdatedAt: at,
			}
			if err := db.CreateFleetRunnerAttempt(ctx, attempt); err != nil {
				t.Fatalf("bind attempt %s: %v", name, err)
			}
			item.attempt = attempt
		}
		return item
	}
	checkpoint := func(s seeded, status, key string) error {
		t.Helper()
		authority, err := operationStore.Authority(ctx)
		if err != nil {
			t.Fatal(err)
		}
		request := fleet.ReconciliationRequest{
			SchemaVersion: fleet.ReconciliationSchemaVersion, Phase: first, Status: status, CommitSHA: commit, PlanSHA256: planSHA,
			EvidenceDigest: "sha256:" + strings.Repeat("d", 64), AttemptID: s.attempt.ID,
		}
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		payload := map[string]interface{}{}
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		opStatus := model.OperationSucceeded
		if status == "failed" {
			opStatus = model.OperationFailed
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		acceptance := store.OperationAcceptance{
			Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "owner:repo:run:1"}, Kind: "fleet.reconciliation", Resource: s.planID, Key: key},
			Operation: model.Operation{ID: uuid.NewString(), Kind: "fleet.reconciliation", Ref: s.planID, Status: opStatus, Source: "fleet-runner", Risk: "append-only infrastructure reconciliation evidence", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: payload, Metadata: map[string]interface{}{}},
			Audit:     store.AcceptanceAuditContext{Source: "integration-test"}, Semantics: map[string]interface{}{"action": "fleet.reconciliation", "planId": s.planID, "request": request},
			FleetReconciliation: &store.FleetReconciliationAdmission{PlanID: s.planID, AttemptID: s.attempt.ID, RequireActiveAttempt: true, RunnerAttemptID: s.attempt.RunnerAttemptID, WorkflowURL: s.attempt.WorkflowURL},
		}
		acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
		if err != nil {
			t.Fatal(err)
		}
		_, err = operationStore.Accept(ctx, acceptance)
		return err
	}
	fence := func(s seeded) lifecycle.FenceFacts {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		facts, err := store.GetFleetTargetFence(ctx, tx, s.targetID, false)
		if err != nil {
			t.Fatal(err)
		}
		return facts
	}
	counts := func() [4]int {
		t.Helper()
		var c [4]int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operations),(SELECT count(*) FROM fleet_runner_attempts),(SELECT count(*) FROM fleet_github_dispatches),(SELECT count(*) FROM fleet_target_fences)`).Scan(&c[0], &c[1], &c[2], &c[3]); err != nil {
			t.Fatal(err)
		}
		return c
	}

	heartbeat := seed("epoch-heartbeat", true)
	succeeded := seed("epoch-succeeded", true)
	failed := seed("epoch-failed", true)
	cancel := seed("epoch-cancel", true)
	rebind := seed("epoch-rebind", false)
	// The heartbeat attempt already holds its current phase's checkpoint, so
	// only the epoch can refuse its advance.
	if err := checkpoint(heartbeat, "succeeded", "pre-restore-checkpoint"); err != nil {
		t.Fatalf("pre-restore checkpoint: %v", err)
	}

	var before int64
	if err := pool.QueryRow(ctx, `SELECT epoch FROM fleet_authority_epoch WHERE singleton`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	fencesBefore := map[string]lifecycle.FenceFacts{}
	for _, s := range []seeded{heartbeat, succeeded, failed, cancel, rebind} {
		f := fence(s)
		if !f.Held || f.AuthorityEpoch != before {
			t.Fatalf("fixture fence %+v at epoch %d", f, before)
		}
		fencesBefore[s.planID] = f
	}
	countsBefore := counts()

	// The step under test.
	after, err := advanceRestoredFleetAuthorityEpoch(ctx, pool, schema, "bundle-epoch-test", before)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("epoch = %d, want %d", after, before+1)
	}
	// A repeated advance from the restored epoch is a CAS miss, not a second bump.
	if _, err := advanceRestoredFleetAuthorityEpoch(ctx, pool, schema, "bundle-epoch-retry", before); err == nil {
		t.Fatal("repeated restore advance from the restored epoch succeeded")
	}
	var reason string
	if err := pool.QueryRow(ctx, `SELECT reason FROM fleet_authority_epoch WHERE singleton`).Scan(&reason); err != nil || reason != "restore:bundle-epoch-test" {
		t.Fatalf("epoch reason = %q err=%v", reason, err)
	}

	// History is preserved: no row added or removed, no fence rewritten.
	if countsAfter := counts(); countsAfter != countsBefore {
		t.Fatalf("row counts changed: before=%v after=%v", countsBefore, countsAfter)
	}
	for _, s := range []seeded{heartbeat, succeeded, failed, cancel, rebind} {
		if got := fence(s); got != fencesBefore[s.planID] {
			t.Fatalf("fence rewritten: before=%+v after=%+v", fencesBefore[s.planID], got)
		}
	}

	// A held fence now derives Uncertain/AuthoritySuperseded.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := store.FleetAuthorityEpoch(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := store.FleetTargetHolderFacts(ctx, tx, heartbeat.planID)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if occupancy, why := lifecycle.Outcome(fence(heartbeat), holder, epoch, time.Now().UTC()); occupancy != lifecycle.OccupancyUncertain || why != lifecycle.ReasonAuthoritySuperseded {
		t.Fatalf("occupancy = %s/%s, want Uncertain/AuthoritySuperseded", occupancy, why)
	}

	// Old-epoch heartbeat and advance are refused through the real UPDATEs.
	if _, err := db.HeartbeatFleetRunnerAttempt(ctx, heartbeat.attempt.ID, first, 1, heartbeat.attempt.Revision, "after restore"); !errors.Is(err, store.ErrFleetRunnerAttemptConflict) {
		t.Fatalf("heartbeat under a superseded epoch err=%v", err)
	}
	if _, err := db.AdvanceFleetRunnerAttempt(ctx, heartbeat.attempt.ID, first, second, heartbeat.attempt.Revision, false); !errors.Is(err, store.ErrFleetRunnerAttemptConflict) {
		t.Fatalf("advance under a superseded epoch err=%v", err)
	}
	if superseded, err := db.FleetTargetEpochSupersededForPlan(ctx, heartbeat.planID); err != nil || !superseded {
		t.Fatalf("refusal is not attributed to the epoch: superseded=%v err=%v", superseded, err)
	}
	if got, err := db.GetFleetRunnerAttempt(ctx, heartbeat.attempt.ID); err != nil || got.Revision != heartbeat.attempt.Revision || got.HeartbeatSequence != 0 || got.CurrentPhase != first {
		t.Fatalf("refused writes changed the attempt: %+v err=%v", got, err)
	}

	// A succeeded checkpoint is refused with the superseded code.
	err = checkpoint(succeeded, "succeeded", "post-restore-succeeded")
	var admission *store.FleetReconciliationAdmissionError
	if !errors.As(err, &admission) || admission.Code != lifecycle.CodeFleetTargetAuthoritySuperseded {
		t.Fatalf("succeeded checkpoint under a superseded epoch err=%v, want %s", err, lifecycle.CodeFleetTargetAuthoritySuperseded)
	}

	// Q10: a failed checkpoint and cancel stay allowed.
	if err := checkpoint(failed, "failed", "post-restore-failed"); err != nil {
		t.Fatalf("failed checkpoint after the epoch advance: %v", err)
	}
	if canceled, err := db.CancelFleetRunnerAttempt(ctx, cancel.attempt.ID, cancel.attempt.Revision, "restore"); err != nil || canceled.Status != model.FleetRunnerAttemptCanceled {
		t.Fatalf("cancel after the epoch advance: %+v err=%v", canceled, err)
	}

	// M13: the first attempt, not yet bound when the epoch advanced, rebinds
	// under the new epoch with Generation+1.
	at := time.Now().UTC()
	firstAttempt := &model.FleetRunnerAttempt{
		ID: uuid.NewString(), PlanID: rebind.planID, RunnerAttemptID: "run-epoch-rebind", Status: model.FleetRunnerAttemptRunning, CurrentPhase: first,
		CommitSHA: commit, PlanSHA256: planSHA, WorkflowURL: "https://github.com/acme/fleet/actions/runs/epoch-rebind", SourceDispatchRunID: 500,
		HeartbeatTimeoutSeconds: 600, Revision: 1, StartedAt: at, PhaseStartedAt: at, HeartbeatAt: at, UpdatedAt: at,
	}
	if err := db.CreateFleetRunnerAttempt(ctx, firstAttempt); err != nil {
		t.Fatalf("first attempt rebind after the epoch advance: %v", err)
	}
	rebound := fence(rebind)
	prior := fencesBefore[rebind.planID]
	if rebound.AuthorityEpoch != after || rebound.Generation != prior.Generation+1 || rebound.HolderPlanID != rebind.planID {
		t.Fatalf("rebound fence = %+v, want epoch %d generation %d", rebound, after, prior.Generation+1)
	}
	if _, err := db.HeartbeatFleetRunnerAttempt(ctx, firstAttempt.ID, first, 1, firstAttempt.Revision, "rebound"); err != nil {
		t.Fatalf("heartbeat on the rebound attempt: %v", err)
	}
}
