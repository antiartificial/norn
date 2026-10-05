package etcdstore

import (
	"errors"
	"testing"

	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/store"
)

// etcdExecutionModeHarness runs the shared execution-mode cases against
// V3OperationStore.SetFleetResourceExecutionMode.
type etcdExecutionModeHarness struct {
	*etcdExecutorHarness
}

func (h *etcdExecutionModeHarness) SetMode(resource, key string, request executor.ExecutionModeRequest) fleettest.ModeResp {
	identity := store.OperationRequestIdentity{Authority: h.s.authority, Actor: store.OperationActor{Issuer: "test", Subject: "admin"}, Kind: executor.KindResourceExecutionMode, Resource: resource, Key: key}
	accepted, err := h.s.SetFleetResourceExecutionMode(h.ctx(), identity, store.AcceptanceAuditContext{Source: "test"}, request)
	var refusal *executor.Error
	var conflict *store.AcceptanceConflictError
	switch {
	case err == nil:
		previous, _ := accepted.Operation.Metadata["previousMode"].(string)
		return fleettest.ModeResp{OpID: accepted.Operation.ID, Replayed: accepted.Replayed, PreviousMode: previous}
	case errors.As(err, &refusal):
		return fleettest.ModeResp{Code: refusal.Code}
	case errors.As(err, &conflict):
		return fleettest.ModeResp{Code: fleettest.ModeCodeKeyReused}
	case errors.Is(err, ErrFleetResourceRevisionConflict):
		return fleettest.ModeResp{Code: fleettest.ModeCodeRevisionConflict}
	case errors.Is(err, ErrFleetResourceNotFound):
		return fleettest.ModeResp{Code: fleettest.ModeCodeNotFound}
	}
	h.t.Logf("unexpected set-mode failure: %v", err)
	return fleettest.ModeResp{Failed: true}
}

func (h *etcdExecutionModeHarness) ResourceMode(resource string) (string, int64) {
	h.t.Helper()
	got, err := h.s.GetFleetResource(h.ctx(), resource)
	if err != nil {
		h.t.Fatal(err)
	}
	return got.ExecutionMode, got.Revision
}

func (h *etcdExecutionModeHarness) PlanAbandoned(planID string) bool {
	h.t.Helper()
	abandoned, err := h.s.checkFleetTargetPlanAbandoned(h.ctx(), planID)
	if err != nil {
		h.t.Fatal(err)
	}
	return abandoned
}

func (h *etcdExecutionModeHarness) ModeOperations(resource string) int {
	h.t.Helper()
	ops, err := h.s.ListOperationsByKind(h.ctx(), executor.KindResourceExecutionMode, 100)
	if err != nil {
		h.t.Fatal(err)
	}
	count := 0
	for _, op := range ops {
		if op.Ref == resource {
			count++
		}
	}
	return count
}

func (h *etcdExecutionModeHarness) Watched() []controller.WatchedResource {
	h.t.Helper()
	watched, err := h.s.ListWatchedResources(h.ctx())
	if err != nil {
		h.t.Fatal(err)
	}
	return watched
}

func TestFleetExecutionModeConformanceEtcd(t *testing.T) {
	fleettest.RunFleetExecutionModeConformance(t, func(t *testing.T) fleettest.ExecutionModeHarness {
		return &etcdExecutionModeHarness{etcdExecutorHarness: newEtcdExecutorHarness(t)}
	})
}

// TestFleetAbandonConvergePlanWithoutAttemptEtcd mirrors the PG test: the
// admin abandon of a converge plan with no attempt derives the executor holder
// nonce instead of failing with fleet_target_release_evidence_mismatch.
func TestFleetAbandonConvergePlanWithoutAttemptEtcd(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	h.RegisterTarget("unstarted")
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "unstarted"})
	actor := store.OperationActor{Issuer: "test", Subject: "admin"}
	admission := store.FleetTargetMutationAdmission{Kind: store.FleetTargetMutationAbandonPlan, PlanID: plan.ID, Release: &store.FleetTargetReleaseAdmission{Mode: "abandon"}}
	acceptance, err := store.NewFleetTargetMutationAcceptance(h.s.authority, actor, "abandon-unstarted", store.AcceptanceAuditContext{Source: "test"}, admission, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Accept(h.ctx(), acceptance); err != nil {
		t.Fatalf("abandoning an unstarted converge plan: %v", err)
	}
	response, err := h.s.kv.Get(h.ctx(), h.s.fleetTargetAbandonedKey(plan.ID))
	if err != nil || len(response.Kvs) != 1 {
		t.Fatalf("abandon record: %v", err)
	}
	var record v3FleetTargetAbandoned
	if err := decodeV3Record(response.Kvs[0].Value, &record); err != nil || record.NonceSHA256 != executor.HolderNonceSHA256(plan.ID) {
		t.Fatalf("the abandon record carries the derived executor nonce: %+v %v", record, err)
	}
	host := fleettest.NewExecutorHost()
	if resp := h.start(plan, host.DeviceID, fleettest.ExecutorStartRequest(plan, host, false)); resp.Code != lifecycle.CodeFleetTargetHolderAbandoned {
		t.Fatalf("an abandoned plan can never start: %+v", resp)
	}
}
