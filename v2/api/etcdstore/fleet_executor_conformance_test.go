package etcdstore

// Package etcdstore (not etcdstore_test), like fleet_target_conformance_test.go,
// so the harness can seed ledger operations and age attempts through the
// store's private keys. It mirrors store/fleet_executor_conformance_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/internal/integrationtest"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

var etcdExecutorRunSeq atomic.Int64

type etcdExecutorHarness struct {
	t       *testing.T
	s       *V3OperationStore
	targets map[string]string // cluster -> target ID
}

func newEtcdExecutorHarness(t *testing.T) *etcdExecutorHarness {
	t.Helper()
	// A fresh prefix per call: integrationtest.Etcd names a unique one.
	return &etcdExecutorHarness{t: t, s: fleetTargetConformanceStore(t), targets: map[string]string{}}
}

func (h *etcdExecutorHarness) ctx() context.Context { return context.Background() }

func (h *etcdExecutorHarness) RegisterTarget(cluster string) string {
	h.t.Helper()
	identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "exec-" + cluster, StateBackend: "s3://exec-" + cluster + "/state"}
	target, err := h.s.RegisterFleetTarget(h.ctx(), identity, []string{"cluster:" + cluster}, uuid.NewString())
	if err != nil {
		h.t.Fatal(err)
	}
	h.targets[cluster] = target.TargetID
	return target.TargetID
}

// putOp writes a succeeded ledger operation and its kind-index entry, keyed by
// the operation's StartedAt (the acceptance time a recorder would use).
func (h *etcdExecutorHarness) putOp(op model.Operation) {
	h.t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if op.StartedAt.IsZero() {
		op.StartedAt = now
	}
	op.UpdatedAt, op.Status, op.FinishedAt, op.MaxAttempts = op.StartedAt, model.OperationSucceeded, &op.StartedAt, 1
	if op.Metadata == nil {
		op.Metadata = map[string]interface{}{}
	}
	record, err := json.Marshal(v3Record{Operation: op})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.s.kv.Txn(h.ctx()).Then(
		clientv3.OpPut(h.s.opKey(op.ID), string(record)),
		clientv3.OpPut(h.s.operationKindIndexKey(op.Kind, op.StartedAt, op.ID), op.ID),
	).Commit(); err != nil {
		h.t.Fatal(err)
	}
}

func etcdRandomHex(n int) string {
	return strings.Repeat(strings.ReplaceAll(uuid.NewString(), "-", ""), 3)[:n]
}

func (h *etcdExecutorHarness) SeedConvergePlan(spec fleettest.ExecutorPlanSpec) fleettest.ExecutorPlanRef {
	h.t.Helper()
	if spec.Generation == 0 {
		spec.Generation = 1
	}
	if spec.StartedAt.IsZero() {
		spec.StartedAt = time.Now().UTC().Add(-10 * time.Minute)
	}
	spec.StartedAt = spec.StartedAt.Truncate(time.Microsecond)
	if spec.PathClass == "" {
		spec.PathClass = executor.PathClassConfig
	}
	if spec.Resource == "" {
		spec.Resource = spec.Cluster
	}
	ref := fleettest.ExecutorPlanRef{ID: uuid.NewString(), Cluster: spec.Cluster, CommitSHA: etcdRandomHex(40), PlanSHA256: etcdRandomHex(64), Generation: spec.Generation}
	payload := map[string]interface{}{"id": ref.ID, "cluster": spec.Cluster, "action": executor.ActionConverge, "digest": "sha256:" + ref.PlanSHA256}
	if spec.NotConverge {
		payload["action"] = "scale"
	} else {
		payload["converge"] = executor.ConvergePlan{
			Resource: spec.Resource, Repository: "acme/norn-fleet", CommitSHA: ref.CommitSHA, PathClass: spec.PathClass,
			RequiresApproval: spec.RequiresApproval, Generation: spec.Generation,
		}
	}
	h.putOp(model.Operation{ID: ref.ID, Kind: "fleet.capacity-plan", Ref: spec.Resource, Payload: payload, StartedAt: spec.StartedAt})
	return ref
}

func (h *etcdExecutorHarness) SeedApprove(plan fleettest.ExecutorPlanRef, commitSHA string, resume bool, sourceAttemptID string) {
	h.t.Helper()
	payload := map[string]interface{}{"planId": plan.ID, "commitSha": commitSHA}
	if resume {
		payload["resume"] = true
		payload["sourceAttemptId"] = sourceAttemptID
	}
	h.putOp(model.Operation{ID: uuid.NewString(), Kind: executor.KindConvergeApprove, Ref: plan.ID, Payload: payload})
}

func etcdToMap(t *testing.T, value interface{}) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if !decodeFleetLedgerValue(value, &out) {
		t.Fatalf("cannot encode %T", value)
	}
	return out
}

func (h *etcdExecutorHarness) SeedStopAttestation(plan fleettest.ExecutorPlanRef, attesterDeviceID string, request executor.StopAttestationRequest) {
	h.t.Helper()
	h.putOp(model.Operation{
		ID: uuid.NewString(), Kind: executor.KindStopAttestation, Ref: plan.ID,
		Payload:  etcdToMap(h.t, executor.StopAttestationPayload{PlanID: plan.ID, StopAttestationRequest: request}),
		Metadata: map[string]interface{}{store.FleetExecutorAttesterMetadataKey: executor.PrincipalSubject(attesterDeviceID)},
	})
}

func (h *etcdExecutorHarness) SeedProviderEvidence(plan fleettest.ExecutorPlanRef, sourceAttemptID string, evidence executor.ProviderEvidence) {
	h.t.Helper()
	h.putOp(model.Operation{
		ID: uuid.NewString(), Kind: store.KindFleetExecutorProviderEvidence, Ref: plan.ID,
		Payload:  etcdToMap(h.t, store.FleetExecutorProviderEvidencePayload{PlanID: plan.ID, SourceAttemptID: sourceAttemptID, ProviderEvidence: evidence}),
		Metadata: map[string]interface{}{executor.ProviderEvidenceDigestMetadataKey: evidence.Digest()},
	})
}

func (h *etcdExecutorHarness) SeedResource(cluster string, desired controller.DesiredRevision) string {
	h.t.Helper()
	name := "resource-" + cluster
	if _, err := h.s.EnsureFleetResource(h.ctx(), name, h.targets[cluster]); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.s.setDesiredFleetResource(h.ctx(), name, nil, desired); err != nil {
		h.t.Fatal(err)
	}
	return name
}

func (h *etcdExecutorHarness) refusal(err error) fleettest.ExecResp {
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

func (h *etcdExecutorHarness) start(plan fleettest.ExecutorPlanRef, deviceID string, request executor.AttemptStartRequest) fleettest.ExecResp {
	result, err := h.s.CreateFleetExecutorAttempt(h.ctx(), store.FleetExecutorStart{
		PlanID: plan.ID, Request: request, PrincipalSubject: executor.PrincipalSubject(deviceID),
	})
	if err != nil {
		return h.refusal(err)
	}
	return fleettest.ExecResp{Attempt: result.Attempt, Replayed: result.Replayed}
}

func (h *etcdExecutorHarness) Start(plan fleettest.ExecutorPlanRef, deviceID string, request executor.AttemptStartRequest) fleettest.ExecResp {
	resp := h.start(plan, deviceID, request)
	if resp.Failed {
		h.t.Errorf("unexpected start failure")
	}
	return resp
}

func (h *etcdExecutorHarness) StartInsertFails(plan fleettest.ExecutorPlanRef, deviceID string, request executor.AttemptStartRequest) fleettest.ExecResp {
	// The one Txn carries the fence write and the attempt put. A value above
	// etcd's request limit makes that Txn fail at Commit, so nothing of it (the
	// fence write included) is applied.
	request.Executor.LoopRelease = strings.Repeat("x", 3<<20)
	return h.start(plan, deviceID, request)
}

func (h *etcdExecutorHarness) Heartbeat(attempt *fleet.RunnerAttempt, sequence int64) fleettest.ExecResp {
	h.t.Helper()
	current, err := h.s.GetFleetRunnerAttempt(h.ctx(), attempt.PlanID, attempt.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	updated, err := h.s.UpdateFleetRunnerAttempt(h.ctx(), attempt.PlanID, attempt.ID, current.Revision, "heartbeat", sequence, "ok")
	var fenceErr *lifecycle.FenceError
	switch {
	case errors.As(err, &fenceErr):
		return fleettest.ExecResp{Code: fenceErr.Code}
	case errors.Is(err, ErrNotFound):
		return fleettest.ExecResp{Code: "fleet_runner_attempt_revision_conflict"}
	case err != nil:
		h.t.Fatal(err)
	}
	return fleettest.ExecResp{Attempt: updated}
}

var etcdExecutorPhaseOrder = []string{"prechange_verified", "provider_applying", "infrastructure_applied", "inventory_generated", "nodes_configured", "nodes_enrolled", "readiness_verified", "complete"}

func (h *etcdExecutorHarness) CompleteAttempt(attempt *fleet.RunnerAttempt) fleettest.ExecResp {
	h.t.Helper()
	for {
		current, err := h.s.GetFleetRunnerAttempt(h.ctx(), attempt.PlanID, attempt.ID)
		if err != nil {
			h.t.Fatal(err)
		}
		if current.Status == "succeeded" {
			return fleettest.ExecResp{Attempt: current}
		}
		next := ""
		for index, phase := range etcdExecutorPhaseOrder {
			if phase == current.CurrentPhase {
				next = etcdExecutorPhaseOrder[index+1]
			}
		}
		if next == "" {
			h.t.Fatalf("no phase after %q", current.CurrentPhase)
		}
		if resp := h.checkpoint(current); resp.Code != "" {
			return resp
		}
		if _, err := h.s.UpdateFleetRunnerAttempt(h.ctx(), current.PlanID, current.ID, current.Revision, "advance", next); err != nil {
			var fenceErr *lifecycle.FenceError
			if errors.As(err, &fenceErr) {
				return fleettest.ExecResp{Code: fenceErr.Code}
			}
			h.t.Fatal(err)
		}
	}
}

// checkpoint records the succeeded reconciliation checkpoint for the attempt's
// current phase through the signed acceptance path.
func (h *etcdExecutorHarness) checkpoint(attempt *fleet.RunnerAttempt) fleettest.ExecResp {
	h.t.Helper()
	authority, _ := h.s.Authority(h.ctx())
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := fleet.ReconciliationRequest{
		SchemaVersion: fleet.ReconciliationSchemaVersion, Phase: attempt.CurrentPhase, Status: "succeeded",
		CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256, AttemptID: attempt.ID,
		EvidenceDigest: "sha256:" + strings.Repeat("d", 64),
	}
	payload := map[string]interface{}{
		"schemaVersion": request.SchemaVersion, "phase": request.Phase, "status": request.Status,
		"commitSha": request.CommitSHA, "planSha256": request.PlanSHA256, "attemptId": request.AttemptID, "evidenceDigest": request.EvidenceDigest,
	}
	acceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "runner"}, Kind: "fleet.reconciliation", Resource: attempt.PlanID, Key: uuid.NewString()},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "fleet.reconciliation", Ref: attempt.PlanID, Status: model.OperationSucceeded, Source: "fleet-runner", Risk: "append-only infrastructure reconciliation evidence", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: payload, Metadata: map[string]interface{}{}},
		Audit:     store.AcceptanceAuditContext{Source: "test"},
		Semantics: map[string]interface{}{"action": "fleet.reconciliation", "planId": attempt.PlanID, "request": request},
		FleetReconciliation: &store.FleetReconciliationAdmission{PlanID: attempt.PlanID, AttemptID: attempt.ID, RequireActiveAttempt: true,
			RunnerAttemptID: attempt.RunnerAttemptID, WorkflowURL: attempt.WorkflowURL},
	}
	var err error
	if acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.s.Accept(h.ctx(), acceptance); err != nil {
		var admission *store.FleetReconciliationAdmissionError
		if errors.As(err, &admission) {
			return fleettest.ExecResp{Code: admission.Code}
		}
		h.t.Fatal(err)
	}
	return fleettest.ExecResp{}
}

func (h *etcdExecutorHarness) Get(attempt *fleet.RunnerAttempt) *fleet.RunnerAttempt {
	h.t.Helper()
	current, err := h.s.GetFleetRunnerAttempt(h.ctx(), attempt.PlanID, attempt.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return current
}

func (h *etcdExecutorHarness) ExpireAttempt(attempt *fleet.RunnerAttempt) {
	h.t.Helper()
	key := h.s.fleetRunnerAttemptKey(attempt.PlanID, attempt.ID)
	response, err := h.s.kv.Get(h.ctx(), key)
	if err != nil || len(response.Kvs) != 1 {
		h.t.Fatalf("attempt %s: %v", attempt.ID, err)
	}
	var item fleet.RunnerAttempt
	if err := decodeV3Record(response.Kvs[0].Value, &item); err != nil {
		h.t.Fatal(err)
	}
	past := time.Now().UTC().Add(-2 * time.Hour)
	item.HeartbeatAt, item.HeartbeatExpiresAt = past, past
	encoded, _ := json.Marshal(item)
	if _, err := h.s.kv.Put(h.ctx(), key, string(encoded)); err != nil {
		h.t.Fatal(err)
	}
}

func (h *etcdExecutorHarness) GitHubStart(cluster string) fleettest.GitHubStartResult {
	h.t.Helper()
	planID, digest, sourceDigest := fleetFenceRaceSeedPlan(h.t, h.s, cluster)
	result := fleettest.GitHubStartResult{PlanID: planID}
	acceptance := fleetFenceRaceDispatchAcceptance(h.s.authority, planID, digest, sourceDigest)
	var err error
	if acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance); err != nil {
		h.t.Fatal(err)
	}
	commit, planSHA := strings.Repeat("c", 40), strings.Repeat("b", 64)
	_, prepared, err := h.s.AcceptFleetGitHubDispatch(h.ctx(), acceptance, FleetGitHubDispatchPreparation{PlanID: planID, PlanRunID: 1, PlanSHA256: planSHA, ApprovedHeadSHA: commit, FleetEnvironment: "staging/nyc3"})
	var fenceErr *lifecycle.FenceError
	if errors.As(err, &fenceErr) {
		result.Code = fenceErr.Code
		return result
	}
	if err != nil {
		h.t.Fatal(err)
	}
	result.NonceSHA256 = prepared.DispatchNonceSHA256
	runID := 800_000_000 + etcdExecutorRunSeq.Add(1)
	workflow := fmt.Sprintf("https://github.com/acme/norn-fleet/actions/runs/%d", runID)
	if err := h.s.FinishFleetGitHubDispatch(h.ctx(), planID, prepared.DispatchNonceSHA256, runID, workflow); err != nil {
		h.t.Fatal(err)
	}
	runner := fmt.Sprintf("github-actions:acme/norn-fleet:%d:1", runID)
	attemptID := uuid.NewString()
	admission := &store.FleetRunnerAttemptAdmission{PlanID: planID, AttemptID: attemptID, RunnerAttemptID: runner, CommitSHA: commit, PlanSHA256: planSHA, WorkflowURL: workflow,
		DispatchNonceSHA256: prepared.DispatchNonceSHA256, SourceDispatchRunID: fmt.Sprint(runID), HeartbeatTimeoutSeconds: 120, WorkloadIntent: "apply", WorkloadRunID: fmt.Sprint(runID), WorkloadSHA: commit}
	now := time.Now().UTC().Truncate(time.Microsecond)
	payload := map[string]interface{}{"attemptId": attemptID, "planId": planID, "runnerAttemptId": runner, "commitSha": commit, "planSha256": planSHA, "workflowUrl": workflow,
		"dispatchNonceSha256": prepared.DispatchNonceSHA256, "sourceDispatchRunId": fmt.Sprint(runID), "resume": false, "heartbeatTimeoutSeconds": 120}
	attemptAcceptance := store.OperationAcceptance{
		Identity:           store.OperationRequestIdentity{Authority: h.s.authority, Actor: store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "runner:" + fmt.Sprint(runID)}, Kind: "fleet.runner-attempt", Resource: planID, Key: uuid.NewString()},
		Operation:          model.Operation{ID: uuid.NewString(), Kind: "fleet.runner-attempt", Ref: planID, Status: model.OperationSucceeded, Source: "fleet-runner", Risk: "bounded protected runner lease", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: payload, Metadata: map[string]interface{}{}},
		Audit:              store.AcceptanceAuditContext{Source: "test"},
		Semantics:          map[string]interface{}{"action": "fleet.runner-attempt", "planId": planID, "runnerAttemptId": runner, "workload": map[string]interface{}{"intent": "apply", "runId": fmt.Sprint(runID), "sha": commit}},
		FleetRunnerAttempt: admission,
	}
	if attemptAcceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(attemptAcceptance); err != nil {
		h.t.Fatal(err)
	}
	accepted, err := h.s.Accept(h.ctx(), attemptAcceptance)
	if errors.As(err, &fenceErr) {
		result.Code = fenceErr.Code
		return result
	}
	var admissionErr *store.FleetRunnerAttemptAdmissionError
	if errors.As(err, &admissionErr) {
		result.Code = admissionErr.Code
		return result
	}
	if err != nil {
		h.t.Fatal(err)
	}
	result.Attempt = accepted.FleetRunnerAttempt
	return result
}

func (h *etcdExecutorHarness) Epoch() int64 {
	epoch, err := h.s.FleetAuthorityEpoch(h.ctx())
	if err != nil {
		h.t.Fatal(err)
	}
	return epoch
}

func (h *etcdExecutorHarness) AdvanceEpoch(expected int64) error {
	_, err := h.s.AdvanceFleetAuthorityEpoch(h.ctx(), expected, "executor conformance")
	return err
}

func (h *etcdExecutorHarness) FenceFacts(targetID string) lifecycle.FenceFacts {
	h.t.Helper()
	fence, _, err := h.s.GetFleetTargetFence(h.ctx(), targetID)
	if err != nil {
		h.t.Fatal(err)
	}
	return fence
}

func (h *etcdExecutorHarness) Occupancy(targetID string) (lifecycle.Occupancy, string) {
	h.t.Helper()
	occupancy, reason, err := h.s.FleetTargetOutcome(h.ctx(), targetID, time.Now().UTC())
	if err != nil {
		h.t.Fatal(err)
	}
	return occupancy, reason
}

func (h *etcdExecutorHarness) ReconcileResource(name string) *controller.Resource {
	h.t.Helper()
	resource, err := h.s.ReconcileFleetResource(h.ctx(), name, controller.DeriveStatus, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return resource
}

func TestFleetExecutorConformanceEtcd(t *testing.T) {
	fleettest.RunFleetExecutorConformance(t, func(t *testing.T) fleettest.ExecutorHarness {
		return newEtcdExecutorHarness(t)
	})
}

// TestFleetExecutorProviderEvidenceOnlyFromPinnedKindEtcd mirrors the PG test:
// evidence-shaped data on any other kind, or a forged digest, is never stop
// proof.
func TestFleetExecutorProviderEvidenceOnlyFromPinnedKindEtcd(t *testing.T) {
	h := newEtcdExecutorHarness(t)
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
	forged := etcdToMap(t, store.FleetExecutorProviderEvidencePayload{PlanID: plan.ID, SourceAttemptID: source.Attempt.ID, ProviderEvidence: evidence})
	forged["commitSha"] = plan.CommitSHA
	for _, kind := range []string{executor.KindConvergeApprove, "fleet.reconciliation", "fleet.target.abandon-plan", "fleet.target.fence-release"} {
		h.putOp(model.Operation{ID: uuid.NewString(), Kind: kind, Ref: plan.ID, Payload: forged, Metadata: map[string]interface{}{executor.ProviderEvidenceDigestMetadataKey: evidence.Digest()}})
	}
	rebuilt := fleettest.NewExecutorHost()
	resume := fleettest.ExecutorStartRequest(plan, rebuilt, true)
	if resp := h.Start(plan, rebuilt.DeviceID, resume); resp.Code != lifecycle.CodeFleetTargetRecoveryRequiresStoppedSource {
		t.Fatalf("evidence outside the pinned kind must not admit a recovery: %+v", resp)
	}
	h.putOp(model.Operation{ID: uuid.NewString(), Kind: store.KindFleetExecutorProviderEvidence, Ref: plan.ID, Payload: forged,
		Metadata: map[string]interface{}{executor.ProviderEvidenceDigestMetadataKey: "sha256:" + strings.Repeat("0", 64)}})
	if resp := h.Start(plan, rebuilt.DeviceID, resume); resp.Code != lifecycle.CodeFleetTargetRecoveryRequiresStoppedSource {
		t.Fatalf("a digest that does not match the evidence must not admit a recovery: %+v", resp)
	}
	h.SeedProviderEvidence(plan, source.Attempt.ID, evidence)
	if resp := h.Start(plan, rebuilt.DeviceID, resume); resp.Attempt == nil || resp.Code != "" {
		t.Fatalf("the pinned kind admits one recovery: %+v", resp)
	}
}

// TestFleetExecutorNewestPlanIgnoresIndexTimeEtcd: the kind index is keyed by
// the acceptance instant, which need not equal the plan's StartedAt. Newest
// follows StartedAt (PG's order), whatever order the index holds.
func TestFleetExecutorNewestPlanIgnoresIndexTimeEtcd(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	h.RegisterTarget("index-time")
	older := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "index-time", StartedAt: time.Now().UTC().Add(-20 * time.Minute)})
	newer := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "index-time", StartedAt: time.Now().UTC().Add(-15 * time.Minute)})
	// Re-index the older plan as the most recently accepted one.
	if _, err := h.s.kv.Put(h.ctx(), h.s.operationKindIndexKey("fleet.capacity-plan", time.Now().UTC(), older.ID), older.ID); err != nil {
		t.Fatal(err)
	}
	host := fleettest.NewExecutorHost()
	if resp := h.start(older, host.DeviceID, fleettest.ExecutorStartRequest(older, host, false)); resp.Code != executor.CodeFleetExecutorPlanNotNewest {
		t.Fatalf("the plan with the older StartedAt must be refused: %+v", resp)
	}
	if resp := h.Start(newer, host.DeviceID, fleettest.ExecutorStartRequest(newer, host, false)); resp.Attempt == nil {
		t.Fatalf("the plan with the newest StartedAt starts: %+v", resp)
	}
}

// TestFleetExecutorIndependentClientRaceEtcd races several independent stores
// (one etcd client each, standing in for API processes) on one registry.
func TestFleetExecutorIndependentClientRaceEtcd(t *testing.T) {
	client, prefix := integrationtest.Etcd(t)
	authority := uuid.NewString()
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-fleet-executor-race-signing-key")
	if err != nil {
		t.Fatal(err)
	}
	newStore := func(c *clientv3.Client) *V3OperationStore {
		adapter, err := NewV3OperationStore(c, prefix, authority, signer)
		if err != nil {
			t.Fatal(err)
		}
		return adapter
	}
	const racers = 6
	stores := make([]*V3OperationStore, racers)
	for i := range stores {
		stores[i] = newStore(integrationtest.EtcdClient(t))
	}
	seed := &etcdExecutorHarness{t: t, s: newStore(client), targets: map[string]string{}}
	target := seed.RegisterTarget("race")

	// Different plans, one target: exactly one fence winner.
	type outcome struct {
		attempt *fleet.RunnerAttempt
		err     error
	}
	results := make([]outcome, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		plan := seed.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "race", Resource: fmt.Sprintf("race-%d", i)})
		host := fleettest.NewExecutorHost()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := stores[i].CreateFleetExecutorAttempt(context.Background(), store.FleetExecutorStart{
				PlanID: plan.ID, Request: fleettest.ExecutorStartRequest(plan, host, false), PrincipalSubject: executor.PrincipalSubject(host.DeviceID),
			})
			if err == nil {
				results[i].attempt = result.Attempt
			}
			results[i].err = err
		}(i)
	}
	wg.Wait()
	var winner *fleet.RunnerAttempt
	for _, r := range results {
		var fenceErr *lifecycle.FenceError
		switch {
		case r.err == nil && winner == nil:
			winner = r.attempt
		case r.err == nil:
			t.Fatal("two independent clients won one fence")
		case errors.As(r.err, &fenceErr) && fenceErr.Code == lifecycle.CodeFleetTargetExecutionOccupied:
		default:
			t.Fatalf("loser must be refused as occupied, got %v", r.err)
		}
	}
	if winner == nil {
		t.Fatal("nobody won the fence")
	}
	fence, _, err := seed.s.GetFleetTargetFence(context.Background(), target)
	if err != nil || !fence.Held || fence.HolderPlanID != winner.PlanID || fence.Generation != 1 {
		t.Fatalf("fence must be held once by the winner: %+v err=%v", fence, err)
	}

	// One resume approve, several clients racing a recovery: exactly one wins.
	seed.RegisterTarget("race-resume")
	plan := seed.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "race-resume"})
	host := fleettest.NewExecutorHost()
	source := seed.Start(plan, host.DeviceID, fleettest.ExecutorStartRequest(plan, host, false)).Attempt
	if source == nil {
		t.Fatal("source start failed")
	}
	seed.ExpireAttempt(source)
	seed.SeedApprove(plan, plan.CommitSHA, true, source.ID)
	recoveries := make([]outcome, racers)
	for i := 0; i < racers; i++ {
		racer := host.NextInvocation()
		seed.SeedStopAttestation(plan, racer.DeviceID, fleettest.ExecutorAttestation(source, racer))
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := stores[i].CreateFleetExecutorAttempt(context.Background(), store.FleetExecutorStart{
				PlanID: plan.ID, Request: fleettest.ExecutorStartRequest(plan, racer, true), PrincipalSubject: executor.PrincipalSubject(racer.DeviceID),
			})
			if err == nil {
				recoveries[i].attempt = result.Attempt
			}
			recoveries[i].err = err
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, r := range recoveries {
		var execErr *executor.Error
		switch {
		case r.err == nil:
			wins++
		case errors.As(r.err, &execErr) && execErr.Code == executor.CodeFleetConvergeApprovalConsumed:
		default:
			t.Fatalf("racing recovery must win or be refused as consumed, got %v", r.err)
		}
	}
	if wins != 1 {
		t.Fatalf("one resume approve authorizes exactly one recovery, got %d", wins)
	}
}

func TestFleetResourceExecutionModeEtcd(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	ctx := context.Background()
	target := h.RegisterTarget("mode")
	name := h.SeedResource("mode", controller.DesiredRevision{Generation: 1, PlanID: "operator", CommitSHA: strings.Repeat("d", 40), Verification: controller.VerificationOperatorDeclared, AcceptedBy: "operator", AcceptedAt: time.Now().UTC()})
	identity := func(key string) store.OperationRequestIdentity {
		return store.OperationRequestIdentity{Authority: h.s.authority, Actor: store.OperationActor{Issuer: "test", Subject: "admin"}, Kind: executor.KindResourceExecutionMode, Resource: name, Key: key}
	}
	audit := store.AcceptanceAuditContext{Source: "test"}
	set := func(key, mode string, revision int64) (store.AcceptedOperation, error) {
		return h.s.SetFleetResourceExecutionMode(ctx, identity(key), audit, executor.ExecutionModeRequest{Mode: mode, ExpectedRevision: revision})
	}
	revision := func() int64 {
		resource, err := h.s.GetFleetResource(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return resource.Revision
	}
	if watched, err := h.s.ListWatchedResources(ctx); err != nil || len(watched) != 0 {
		t.Fatalf("a github-mode resource is not watched: %+v %v", watched, err)
	}

	// A stale revision is refused and records nothing.
	if _, err := set("stale", executor.ModeController, revision()+5); !errors.Is(err, ErrFleetResourceRevisionConflict) {
		t.Fatalf("stale expectedRevision: %v", err)
	}
	// An unstarted converge plan of the resource, and a started one.
	unstarted := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "mode", Resource: name, StartedAt: time.Now().UTC().Add(-30 * time.Minute)})
	_ = unstarted
	first, err := set("k1", executor.ModeController, revision())
	if err != nil || first.Replayed {
		t.Fatalf("switch to controller: %+v %v", first, err)
	}
	changed := revision()
	resource, _ := h.s.GetFleetResource(ctx, name)
	if resource.ExecutionMode != executor.ModeController || changed != 1 {
		t.Fatalf("controller mode and revision bump expected: %+v", resource)
	}
	if first.Operation.Metadata["previousMode"] != executor.ModeGitHub {
		t.Fatalf("metadata %+v", first.Operation.Metadata)
	}
	if watched, err := h.s.ListWatchedResources(ctx); err != nil || len(watched) != 1 || watched[0].Name != name || watched[0].Cluster != "mode" || watched[0].Repository != "" {
		t.Fatalf("controller resource is watched: %+v %v", watched, err)
	}

	// Replay applies nothing; the same key with another body is refused.
	replay, err := set("k1", executor.ModeController, 99)
	var conflict *store.AcceptanceConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("same key with another expectedRevision must be a conflict: %+v %v", replay, err)
	}
	replay, err = set("k1", executor.ModeController, 0)
	if err != nil || !replay.Replayed || replay.Operation.ID != first.Operation.ID || revision() != changed {
		t.Fatalf("replay must return the stored op and change nothing: %+v %v", replay, err)
	}
	if _, err := set("k1", executor.ModeGitHub, 0); !errors.As(err, &conflict) {
		t.Fatalf("key reuse with another mode: %v", err)
	}

	// Setting the same mode records an op but does not bump the revision.
	same, err := set("k2", executor.ModeController, changed)
	if err != nil || revision() != changed || same.Operation.ID == first.Operation.ID {
		t.Fatalf("same mode: %+v %v rev=%d", same, err, revision())
	}

	// A held fence refuses and leaves no ledger row.
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "mode", Resource: name})
	host := fleettest.NewExecutorHost()
	started := h.Start(plan, host.DeviceID, fleettest.ExecutorStartRequest(plan, host, false))
	if started.Attempt == nil {
		t.Fatalf("start: %+v", started)
	}
	before, _ := h.s.ListOperationsByKind(ctx, executor.KindResourceExecutionMode, 100)
	_, err = set("held", executor.ModeGitHub, changed)
	var execErr *executor.Error
	if !errors.As(err, &execErr) || execErr.Code != executor.CodeFleetExecutionModeFenceHeld {
		t.Fatalf("held fence: %v", err)
	}
	after, _ := h.s.ListOperationsByKind(ctx, executor.KindResourceExecutionMode, 100)
	if len(after) != len(before) || h.FenceFacts(target).HolderPlanID != plan.ID {
		t.Fatalf("a refused change must leave nothing: %d then %d", len(before), len(after))
	}
	if done := h.CompleteAttempt(started.Attempt); done.Code != "" {
		t.Fatalf("complete: %+v", done)
	}

	// Switching to github abandons the converge plans that have no attempt and
	// keeps the one that ran. Newer unstarted plan:
	newer := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "mode", Resource: name, StartedAt: time.Now().UTC().Add(-time.Minute)})
	if _, err := set("gh", executor.ModeGitHub, changed); err != nil {
		t.Fatalf("switch to github: %v", err)
	}
	for plan, want := range map[string]bool{unstarted.ID: true, newer.ID: true, plan.ID: false} {
		got, err := h.s.checkFleetTargetPlanAbandoned(ctx, plan)
		if err != nil || got != want {
			t.Fatalf("plan %s abandoned=%v want %v (%v)", plan, got, want, err)
		}
	}
	abandoned, _ := h.s.kv.Get(ctx, h.s.fleetTargetAbandonedKey(newer.ID))
	var record v3FleetTargetAbandoned
	if err := decodeV3Record(abandoned.Kvs[0].Value, &record); err != nil || record.NonceSHA256 != executor.HolderNonceSHA256(newer.ID) {
		t.Fatalf("abandon must use the executor holder nonce: %+v %v", record, err)
	}
	other := fleettest.NewExecutorHost()
	if resp := h.start(newer, other.DeviceID, fleettest.ExecutorStartRequest(newer, other, false)); resp.Code != lifecycle.CodeFleetTargetHolderAbandoned {
		t.Fatalf("an abandoned plan can never start: %+v", resp)
	}
	if watched, _ := h.s.ListWatchedResources(ctx); len(watched) != 0 {
		t.Fatalf("a github resource is not watched: %+v", watched)
	}
}
