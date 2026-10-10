package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

var executorRunSeq atomic.Int64

// pgExecutorHarness implements fleettest.ExecutorHarness against a real PG
// control schema, calling the store directly.
type pgExecutorHarness struct {
	t       *testing.T
	db      *DB
	targets map[string]string // cluster -> target ID
}

func (h *pgExecutorHarness) ctx() context.Context { return context.Background() }

func (h *pgExecutorHarness) RegisterTarget(cluster string) string {
	h.t.Helper()
	identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "exec-" + cluster, StateBackend: "s3://exec-" + cluster + "/state"}
	target, err := h.db.RegisterFleetTarget(h.ctx(), identity, []string{"cluster:" + cluster}, "op-exec-"+cluster)
	if err != nil {
		h.t.Fatal(err)
	}
	h.targets[cluster] = target.TargetID
	return target.TargetID
}

func (h *pgExecutorHarness) insertOp(op *model.Operation) {
	h.t.Helper()
	now := time.Now().UTC()
	if op.StartedAt.IsZero() {
		op.StartedAt = now
	}
	op.UpdatedAt, op.Status = now, model.OperationSucceeded
	if err := h.db.InsertCompletedOperation(h.ctx(), op); err != nil {
		h.t.Fatal(err)
	}
}

func randomHex(n int) string {
	return strings.Repeat(strings.ReplaceAll(uuid.NewString(), "-", ""), 3)[:n]
}

func (h *pgExecutorHarness) SeedConvergePlan(spec fleettest.ExecutorPlanSpec) fleettest.ExecutorPlanRef {
	h.t.Helper()
	if spec.Generation == 0 {
		spec.Generation = 1
	}
	if spec.StartedAt.IsZero() {
		spec.StartedAt = time.Now().UTC().Add(-10 * time.Minute)
	}
	if spec.PathClass == "" {
		spec.PathClass = executor.PathClassConfig
	}
	if spec.Resource == "" {
		spec.Resource = spec.Cluster
	}
	ref := fleettest.ExecutorPlanRef{
		ID: uuid.NewString(), Cluster: spec.Cluster, CommitSHA: randomHex(40), PlanSHA256: randomHex(64), Generation: spec.Generation,
	}
	payload := map[string]interface{}{
		"id": ref.ID, "cluster": spec.Cluster, "action": executor.ActionConverge, "digest": "sha256:" + ref.PlanSHA256,
	}
	if spec.NotConverge {
		payload["action"] = "scale"
	} else {
		payload["converge"] = executor.ConvergePlan{
			Resource: spec.Resource, Repository: "acme/norn-fleet", CommitSHA: ref.CommitSHA, PathClass: spec.PathClass,
			RequiresApproval: spec.RequiresApproval, Generation: spec.Generation,
		}
	}
	// Ref = resource, as the PG recorder (handler/fleet_converge.go) records it.
	h.insertOp(&model.Operation{ID: ref.ID, Kind: "fleet.capacity-plan", Ref: spec.Resource, Payload: payload, StartedAt: spec.StartedAt})
	return ref
}

func (h *pgExecutorHarness) SeedApprove(plan fleettest.ExecutorPlanRef, commitSHA string, resume bool, sourceAttemptID string) {
	h.t.Helper()
	payload := map[string]interface{}{"planId": plan.ID, "commitSha": commitSHA}
	if resume {
		payload["resume"] = true
		payload["sourceAttemptId"] = sourceAttemptID
	}
	h.insertOp(&model.Operation{ID: uuid.NewString(), Kind: executor.KindConvergeApprove, Ref: plan.ID, Payload: payload})
}

func (h *pgExecutorHarness) SeedRefusal(plan fleettest.ExecutorPlanRef, code, commitSHA string) {
	h.t.Helper()
	h.insertOp(&model.Operation{
		ID: uuid.NewString(), Kind: executor.KindConvergeRefusal, Ref: plan.ID,
		Payload: toMap(h.t, executor.ConvergeRefusalPayload{PlanID: plan.ID, ConvergeRefusalRequest: executor.ConvergeRefusalRequest{Code: code, CommitSHA: commitSHA, PlanSHA256: plan.PlanSHA256}}),
	})
}

func (h *pgExecutorHarness) SeedStopAttestation(plan fleettest.ExecutorPlanRef, attesterDeviceID string, request executor.StopAttestationRequest) {
	h.t.Helper()
	payload := toMap(h.t, executor.StopAttestationPayload{PlanID: plan.ID, StopAttestationRequest: request})
	h.insertOp(&model.Operation{
		ID: uuid.NewString(), Kind: executor.KindStopAttestation, Ref: plan.ID, Payload: payload,
		Metadata: map[string]interface{}{FleetExecutorAttesterMetadataKey: executor.PrincipalSubject(attesterDeviceID)},
	})
}

func (h *pgExecutorHarness) SeedProviderEvidence(plan fleettest.ExecutorPlanRef, sourceAttemptID string, evidence executor.ProviderEvidence) {
	h.t.Helper()
	h.insertOp(&model.Operation{
		ID: uuid.NewString(), Kind: KindFleetExecutorProviderEvidence, Ref: plan.ID,
		Payload:  toMap(h.t, FleetExecutorProviderEvidencePayload{PlanID: plan.ID, SourceAttemptID: sourceAttemptID, ProviderEvidence: evidence}),
		Metadata: map[string]interface{}{executor.ProviderEvidenceDigestMetadataKey: evidence.Digest()},
	})
}

func toMap(t *testing.T, value interface{}) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if !decodeLedgerValue(value, &out) {
		t.Fatalf("cannot encode %T", value)
	}
	return out
}

func (h *pgExecutorHarness) SeedResource(cluster string, desired controller.DesiredRevision) string {
	h.t.Helper()
	name := "resource-" + cluster
	if _, err := h.db.EnsureFleetResource(h.ctx(), name, h.targets[cluster]); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.db.setDesiredFleetResource(h.ctx(), name, nil, desired); err != nil {
		h.t.Fatal(err)
	}
	return name
}

func (h *pgExecutorHarness) refusal(err error) fleettest.ExecResp {
	var fenceErr *lifecycle.FenceError
	var execErr *executor.Error
	switch {
	case errors.As(err, &fenceErr):
		return fleettest.ExecResp{Code: fenceErr.Code}
	case errors.As(err, &execErr):
		return fleettest.ExecResp{Code: execErr.Code}
	}
	return fleettest.ExecResp{Failed: true}
}

func (h *pgExecutorHarness) start(plan fleettest.ExecutorPlanRef, deviceID string, request executor.AttemptStartRequest) (fleettest.ExecResp, error) {
	result, err := h.db.CreateFleetExecutorAttempt(h.ctx(), FleetExecutorStart{
		PlanID: plan.ID, Request: request, PrincipalSubject: executor.PrincipalSubject(deviceID),
	})
	if err != nil {
		return h.refusal(err), err
	}
	attempt := lifecycle.FromLegacy(result.Attempt)
	return fleettest.ExecResp{Attempt: &attempt, Replayed: result.Replayed}, nil
}

func (h *pgExecutorHarness) Start(plan fleettest.ExecutorPlanRef, deviceID string, request executor.AttemptStartRequest) fleettest.ExecResp {
	resp, _ := h.start(plan, deviceID, request)
	if resp.Failed {
		h.t.Errorf("unexpected start failure")
	}
	return resp
}

func (h *pgExecutorHarness) StartInsertFails(plan fleettest.ExecutorPlanRef, deviceID string, request executor.AttemptStartRequest) fleettest.ExecResp {
	// A NUL byte passes shape validation but JSONB refuses it, so the insert
	// fails after the fence was written in the same transaction.
	request.Executor.LoopRelease = "bad\x00release"
	resp, _ := h.start(plan, deviceID, request)
	return resp
}

func (h *pgExecutorHarness) Heartbeat(attempt *fleet.RunnerAttempt, sequence int64) fleettest.ExecResp {
	h.t.Helper()
	current, err := h.db.GetFleetRunnerAttempt(h.ctx(), attempt.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	updated, err := h.db.HeartbeatFleetRunnerAttempt(h.ctx(), attempt.ID, current.CurrentPhase, sequence, current.Revision, "ok")
	if errors.Is(err, ErrFleetRunnerAttemptConflict) {
		if superseded, _ := h.db.FleetTargetEpochSupersededForPlan(h.ctx(), attempt.PlanID); superseded {
			return fleettest.ExecResp{Code: lifecycle.CodeFleetTargetAuthoritySuperseded}
		}
		return fleettest.ExecResp{Code: "fleet_runner_attempt_revision_conflict"}
	}
	if err != nil {
		h.t.Fatal(err)
	}
	neutral := lifecycle.FromLegacy(updated)
	return fleettest.ExecResp{Attempt: &neutral}
}

func (h *pgExecutorHarness) CompleteAttempt(attempt *fleet.RunnerAttempt) fleettest.ExecResp {
	h.t.Helper()
	for {
		current, err := h.db.GetFleetRunnerAttempt(h.ctx(), attempt.ID)
		if err != nil {
			h.t.Fatal(err)
		}
		if current.Status == model.FleetRunnerAttemptSucceeded {
			neutral := lifecycle.FromLegacy(current)
			return fleettest.ExecResp{Attempt: &neutral}
		}
		now := time.Now().UTC()
		checkpoint := &model.Operation{
			ID: uuid.NewString(), Kind: "fleet.reconciliation", Ref: current.PlanID, Status: model.OperationSucceeded,
			Payload: map[string]interface{}{
				"phase": current.CurrentPhase, "attemptId": current.ID, "commitSha": current.CommitSHA, "planSha256": current.PlanSHA256, "status": "succeeded",
			},
			StartedAt: now, UpdatedAt: now, FinishedAt: &now,
		}
		if err := h.db.InsertFleetReconciliation(h.ctx(), checkpoint, current.ID); err != nil {
			h.t.Fatal(err)
		}
		next, terminal, err := lifecycle.NextPhase(lifecycle.PostgresLegacy, current.CurrentPhase, false)
		if err != nil {
			h.t.Fatal(err)
		}
		if _, err := h.db.AdvanceFleetRunnerAttempt(h.ctx(), current.ID, current.CurrentPhase, next, current.Revision, terminal); err != nil {
			var fenceErr *lifecycle.FenceError
			if errors.As(err, &fenceErr) {
				return fleettest.ExecResp{Code: fenceErr.Code}
			}
			h.t.Fatal(err)
		}
	}
}

func (h *pgExecutorHarness) Get(attempt *fleet.RunnerAttempt) *fleet.RunnerAttempt {
	h.t.Helper()
	current, err := h.db.GetFleetRunnerAttempt(h.ctx(), attempt.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	neutral := lifecycle.FromLegacy(current)
	return &neutral
}

func (h *pgExecutorHarness) ExpireAttempt(attempt *fleet.RunnerAttempt) {
	h.t.Helper()
	if _, err := h.db.Pool.Exec(h.ctx(), `UPDATE fleet_runner_attempts SET heartbeat_at=now()-interval '2 hours', heartbeat_expires_at=now()-interval '2 hours' WHERE id=$1`, attempt.ID); err != nil {
		h.t.Fatal(err)
	}
}

func (h *pgExecutorHarness) GitHubStart(cluster string) fleettest.GitHubStartResult {
	h.t.Helper()
	now := time.Now().UTC()
	planID := uuid.NewString()
	h.insertOp(&model.Operation{ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Payload: map[string]interface{}{"cluster": cluster, "action": "scale"}})
	nonceHash := randomHex(64)
	commit, planSHA := randomHex(40), randomHex(64)
	environment := "staging/nyc3"
	result := fleettest.GitHubStartResult{PlanID: planID, NonceSHA256: nonceHash}
	if _, err := h.db.CreateFleetGitHubDispatch(h.ctx(), FleetGitHubDispatch{
		PlanID: planID, PlanRunID: 1, PlanSHA256: planSHA, ApprovedHeadSHA: commit, FleetEnvironment: environment,
		DispatchNonceSHA256: nonceHash, DispatchState: "prepared",
	}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.db.MarkFleetGitHubDispatchSubmittingFenced(h.ctx(), planID, nonceHash, cluster, environment, now.Add(10*time.Minute)); err != nil {
		var fenceErr *lifecycle.FenceError
		if errors.As(err, &fenceErr) {
			result.Code = fenceErr.Code
			return result
		}
		h.t.Fatal(err)
	}
	runID := 800_000_000 + executorRunSeq.Add(1)
	if _, err := h.db.FinishFleetGitHubDispatch(h.ctx(), planID, nonceHash, runID, 1, fmt.Sprintf("https://github.com/acme/norn-fleet/actions/runs/%d", runID)); err != nil {
		h.t.Fatal(err)
	}
	attempt := &model.FleetRunnerAttempt{
		ID: uuid.NewString(), PlanID: planID, RunnerAttemptID: fmt.Sprintf("github-actions:acme/norn-fleet:%d:1", runID),
		Status: model.FleetRunnerAttemptRunning, CurrentPhase: lifecycle.Phases(lifecycle.PostgresLegacy, false)[0],
		CommitSHA: commit, PlanSHA256: planSHA, WorkflowURL: fmt.Sprintf("https://github.com/acme/norn-fleet/actions/runs/%d", runID),
		SourceDispatchRunID: runID, PrincipalSubject: "runner", HeartbeatTimeoutSeconds: 120, Revision: 1,
		StartedAt: now, PhaseStartedAt: now, HeartbeatAt: now, UpdatedAt: now,
	}
	if err := h.db.CreateFleetRunnerAttempt(h.ctx(), attempt); err != nil {
		var fenceErr *lifecycle.FenceError
		if errors.As(err, &fenceErr) {
			result.Code = fenceErr.Code
			return result
		}
		h.t.Fatal(err)
	}
	neutral := lifecycle.FromLegacy(attempt)
	result.Attempt = &neutral
	return result
}

func (h *pgExecutorHarness) Epoch() int64 {
	var epoch int64
	if err := h.db.Pool.QueryRow(h.ctx(), `SELECT epoch FROM fleet_authority_epoch WHERE singleton`).Scan(&epoch); err != nil {
		h.t.Fatal(err)
	}
	return epoch
}

func (h *pgExecutorHarness) AdvanceEpoch(expected int64) error {
	_, err := h.db.AdvanceFleetAuthorityEpoch(h.ctx(), expected, "executor conformance")
	return err
}

func (h *pgExecutorHarness) FenceFacts(targetID string) lifecycle.FenceFacts {
	h.t.Helper()
	tx, err := h.db.Pool.Begin(h.ctx())
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(h.ctx()) }()
	fence, err := GetFleetTargetFence(h.ctx(), tx, targetID, false)
	if err != nil {
		h.t.Fatal(err)
	}
	return fence
}

func (h *pgExecutorHarness) Occupancy(targetID string) (lifecycle.Occupancy, string) {
	h.t.Helper()
	tx, err := h.db.Pool.Begin(h.ctx())
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(h.ctx()) }()
	fence, err := GetFleetTargetFence(h.ctx(), tx, targetID, false)
	if err != nil {
		h.t.Fatal(err)
	}
	epoch, err := FleetAuthorityEpoch(h.ctx(), tx)
	if err != nil {
		h.t.Fatal(err)
	}
	var holder lifecycle.HolderFacts
	if fence.Held {
		if holder, _, err = readFleetTargetHolderFactsTx(h.ctx(), tx, fence.HolderPlanID); err != nil {
			h.t.Fatal(err)
		}
	}
	return lifecycle.Outcome(fence, holder, epoch, time.Now().UTC())
}

func (h *pgExecutorHarness) ReconcileResource(name string) *controller.Resource {
	h.t.Helper()
	resource, err := h.db.ReconcileFleetResource(h.ctx(), name, controller.DeriveStatus, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return resource
}

func TestFleetExecutorConformancePostgres(t *testing.T) {
	fleettest.RunFleetExecutorConformance(t, func(t *testing.T) fleettest.ExecutorHarness {
		return &pgExecutorHarness{t: t, db: fleetTargetConformanceDB(t), targets: map[string]string{}}
	})
}

// TestFleetExecutorProviderEvidenceOnlyFromPinnedKind: a providerEvidence key
// and digest metadata on any ledger kind other than
// KindFleetExecutorProviderEvidence (for example a converge approve, a
// checkpoint or an abandon) is never stop proof for a recovery.
func TestFleetExecutorProviderEvidenceOnlyFromPinnedKind(t *testing.T) {
	h := &pgExecutorHarness{t: t, db: fleetTargetConformanceDB(t), targets: map[string]string{}}
	h.RegisterTarget("forged")
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "forged"})
	old := fleettest.NewExecutorHost()
	source := h.Start(plan, old.DeviceID, fleettest.ExecutorStartRequest(plan, old, false))
	if source.Attempt == nil {
		t.Fatalf("start: %+v", source)
	}
	h.ExpireAttempt(source.Attempt)
	h.SeedApprove(plan, plan.CommitSHA, true, source.Attempt.ID)
	evidence := executor.ProviderEvidence{Provider: "digitalocean", DropletID: old.DropletID, Status: executor.ProviderStatusOff, ObservedAt: time.Now().UTC()}
	forged := toMap(t, FleetExecutorProviderEvidencePayload{PlanID: plan.ID, SourceAttemptID: source.Attempt.ID, ProviderEvidence: evidence})
	forged["commitSha"] = plan.CommitSHA
	for _, kind := range []string{executor.KindConvergeApprove, "fleet.reconciliation", "fleet.target.abandon-plan", "fleet.target.fence-release"} {
		h.insertOp(&model.Operation{ID: uuid.NewString(), Kind: kind, Ref: plan.ID, Payload: forged,
			Metadata: map[string]interface{}{executor.ProviderEvidenceDigestMetadataKey: evidence.Digest()}})
	}
	rebuilt := fleettest.NewExecutorHost()
	resume := fleettest.ExecutorStartRequest(plan, rebuilt, true)
	if resp := h.Start(plan, rebuilt.DeviceID, resume); resp.Code != lifecycle.CodeFleetTargetRecoveryRequiresStoppedSource {
		t.Fatalf("evidence outside the pinned kind must not admit a recovery: %+v", resp)
	}
	// The pinned kind with a caller-forged digest is refused too.
	h.insertOp(&model.Operation{ID: uuid.NewString(), Kind: KindFleetExecutorProviderEvidence, Ref: plan.ID, Payload: forged,
		Metadata: map[string]interface{}{executor.ProviderEvidenceDigestMetadataKey: "sha256:" + strings.Repeat("0", 64)}})
	if resp := h.Start(plan, rebuilt.DeviceID, resume); resp.Code != lifecycle.CodeFleetTargetRecoveryRequiresStoppedSource {
		t.Fatalf("a digest that does not match the evidence must not admit a recovery: %+v", resp)
	}
	h.SeedProviderEvidence(plan, source.Attempt.ID, evidence)
	if resp := h.Start(plan, rebuilt.DeviceID, resume); resp.Attempt == nil || resp.Code != "" {
		t.Fatalf("the pinned kind admits one recovery: %+v", resp)
	}
}
