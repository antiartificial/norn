package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
)

// pgExecutionModeHarness runs the shared execution-mode cases against the PG
// operation store's atomic SetFleetResourceExecutionMode.
type pgExecutionModeHarness struct {
	*pgExecutorHarness
	ops *PGOperationStore
}

func newPGExecutionModeHarness(t *testing.T) *pgExecutionModeHarness {
	t.Helper()
	db := fleetTargetConformanceDB(t)
	signer, err := NewHMACAcceptanceSigner("pg-execution-mode-conformance-signing-key-0000")
	if err != nil {
		t.Fatal(err)
	}
	ops, err := NewPGOperationStore(db, signer, AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	return &pgExecutionModeHarness{pgExecutorHarness: &pgExecutorHarness{t: t, db: db, targets: map[string]string{}}, ops: ops}
}

func (h *pgExecutionModeHarness) SetMode(resource, key string, request executor.ExecutionModeRequest) fleettest.ModeResp {
	authority, err := h.ops.Authority(h.ctx())
	if err != nil {
		h.t.Fatal(err)
	}
	identity := OperationRequestIdentity{Authority: authority, Actor: OperationActor{Issuer: "test", Subject: "admin"}, Kind: executor.KindResourceExecutionMode, Resource: resource, Key: key}
	accepted, err := h.ops.SetFleetResourceExecutionMode(h.ctx(), identity, AcceptanceAuditContext{Source: "test"}, request)
	var refusal *executor.Error
	var conflict *AcceptanceConflictError
	switch {
	case err == nil:
		previous, _ := accepted.Operation.Metadata[FleetResourceExecutionModePreviousModeKey].(string)
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

func (h *pgExecutionModeHarness) ResourceMode(resource string) (string, int64) {
	h.t.Helper()
	got, err := h.db.GetFleetResource(h.ctx(), resource)
	if err != nil {
		h.t.Fatal(err)
	}
	return got.ExecutionMode, got.Revision
}

func (h *pgExecutionModeHarness) PlanAbandoned(planID string) bool {
	h.t.Helper()
	var abandoned bool
	if err := h.db.Pool.QueryRow(h.ctx(), `SELECT EXISTS(SELECT 1 FROM fleet_target_abandoned_plans WHERE plan_id=$1)`, planID).Scan(&abandoned); err != nil {
		h.t.Fatal(err)
	}
	return abandoned
}

func (h *pgExecutionModeHarness) ModeOperations(resource string) int {
	h.t.Helper()
	var count int
	if err := h.db.Pool.QueryRow(h.ctx(), `SELECT count(*) FROM operations WHERE kind=$1 AND ref=$2`, executor.KindResourceExecutionMode, resource).Scan(&count); err != nil {
		h.t.Fatal(err)
	}
	return count
}

func (h *pgExecutionModeHarness) Watched() []controller.WatchedResource {
	h.t.Helper()
	watched, err := h.db.ListWatchedResources(h.ctx())
	if err != nil {
		h.t.Fatal(err)
	}
	return watched
}

func TestFleetExecutionModeConformancePostgres(t *testing.T) {
	fleettest.RunFleetExecutionModeConformance(t, func(t *testing.T) fleettest.ExecutionModeHarness {
		return newPGExecutionModeHarness(t)
	})
}

// TestFleetResourceDocumentOmitsUnsetExecutionMode: a document without a mode
// stays byte-identical, so existing documents and digests do not change.
func TestFleetResourceDocumentOmitsUnsetExecutionMode(t *testing.T) {
	document, err := json.Marshal(fleetResourceDocumentOf(controller.Resource{SchemaVersion: controller.SchemaVersion}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(document), "executionMode") {
		t.Fatalf("an unset mode must be absent from the document: %s", document)
	}
	set, err := json.Marshal(fleetResourceDocumentOf(controller.Resource{SchemaVersion: controller.SchemaVersion, ExecutionMode: executor.ModeController}))
	if err != nil || !strings.Contains(string(set), `"executionMode":"controller"`) {
		t.Fatalf("a set mode round-trips through the document: %s %v", set, err)
	}
}

// TestFleetAbandonConvergePlanWithoutAttemptPostgres: the admin abandon of a
// converge plan that never had an attempt used to fail with
// fleet_target_release_evidence_mismatch (no dispatch row, so no holder
// nonce). It now derives executor.HolderNonceSHA256 and records the abandon;
// a supplied providerEvidence is still refused for a non-executor holder.
func TestFleetAbandonConvergePlanWithoutAttemptPostgres(t *testing.T) {
	h := newPGExecutionModeHarness(t)
	h.RegisterTarget("unstarted")
	plan := h.SeedConvergePlan(fleettest.ExecutorPlanSpec{Cluster: "unstarted"})
	authority, err := h.ops.Authority(h.ctx())
	if err != nil {
		t.Fatal(err)
	}
	actor := OperationActor{Issuer: "test", Subject: "admin"}
	admission := FleetTargetMutationAdmission{Kind: FleetTargetMutationAbandonPlan, PlanID: plan.ID, Release: &FleetTargetReleaseAdmission{Mode: "abandon"}}
	withEvidence := admission
	withEvidence.Release = &FleetTargetReleaseAdmission{Mode: "abandon", ProviderEvidence: &executor.ProviderEvidence{Provider: "digitalocean", DropletID: "123", Status: executor.ProviderStatusOff, ObservedAt: time.Now().UTC()}}
	refused, err := NewFleetTargetMutationAcceptance(authority, actor, "abandon-evidence", AcceptanceAuditContext{Source: "test"}, withEvidence, nil)
	if err != nil {
		t.Fatal(err)
	}
	var validation *AcceptanceValidationError
	if _, err := h.ops.Accept(h.ctx(), refused); !errors.As(err, &validation) {
		t.Fatalf("providerEvidence applies only to an executor-lane holder: %v", err)
	}
	acceptance, err := NewFleetTargetMutationAcceptance(authority, actor, "abandon-unstarted", AcceptanceAuditContext{Source: "test"}, admission, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.ops.Accept(h.ctx(), acceptance); err != nil {
		t.Fatalf("abandoning an unstarted converge plan: %v", err)
	}
	var nonce string
	if err := h.db.Pool.QueryRow(h.ctx(), `SELECT nonce_sha256 FROM fleet_target_abandoned_plans WHERE plan_id=$1`, plan.ID).Scan(&nonce); err != nil || nonce != executor.HolderNonceSHA256(plan.ID) {
		t.Fatalf("the abandon record carries the derived executor nonce: %q %v", nonce, err)
	}
	host := fleettest.NewExecutorHost()
	resp, _ := h.start(plan, host.DeviceID, fleettest.ExecutorStartRequest(plan, host, false))
	if resp.Code != lifecycle.CodeFleetTargetHolderAbandoned {
		t.Fatalf("an abandoned plan can never start: %+v", resp)
	}
}
