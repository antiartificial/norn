package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// TestDispatchPreSubmitFailureLeavesSubmitting pins the pre-existing defect
// recorded in plan.md §1.6 (H4/M6): in DispatchFleetGitHubApply,
// MarkFleetGitHubDispatchSubmitting (fleet_github.go:857) fences the row to
// 'submitting' immediately before the one permitted GitHub POST. When that
// POST then fails with githubapp.ErrDispatchPreSubmit, the handler calls
// DeletePreparedFleetGitHubDispatch (fleet_github.go:867-869), but that
// delete matches only dispatch_state='prepared' AND run_id=0
// (store/fleet_github_dispatches.go:128), so it is a no-op against a
// 'submitting' row. The row is left stuck in 'submitting' with no bound run
// and no reset path: ResetFleetGitHubDispatchPreSubmit is reserved for the
// distinct external-Mac execute lane, not this ordinary dispatch path. This
// test replays the handler's store-call sequence (it cannot drive the handler,
// whose *githubapp.Client has no test seam), so it pins the store semantics
// only; a handler fix must update it and add a handler-level check.
func TestDispatchPreSubmitFailureLeavesSubmitting(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	finished := now
	planID := uuid.NewString()
	plan := &model.Operation{ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Payload: map[string]interface{}{"action": "scale"}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished}
	if err := db.InsertCompletedOperation(ctx, plan); err != nil {
		t.Fatal(err)
	}
	nonceDigest := sha256.Sum256([]byte("presubmit-defect-nonce"))
	nonceHash := hex.EncodeToString(nonceDigest[:])
	if _, err := db.CreateFleetGitHubDispatch(ctx, store.FleetGitHubDispatch{
		PlanID: planID, PlanRunID: 91, PlanSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ApprovedHeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", FleetEnvironment: "production/nyc3",
		DispatchNonceSHA256: nonceHash, DispatchState: "prepared",
	}); err != nil {
		t.Fatal(err)
	}

	// Immediately before the single permitted POST (fleet_github.go:857).
	submitting, err := db.MarkFleetGitHubDispatchSubmitting(ctx, planID, nonceHash)
	if err != nil || submitting.DispatchState != "submitting" {
		t.Fatalf("fence to submitting: %+v, %v", submitting, err)
	}

	// The handler's ErrDispatchPreSubmit branch (fleet_github.go:867-869).
	if err := db.DeletePreparedFleetGitHubDispatch(ctx, planID, nonceHash); err != nil {
		t.Fatalf("pre-submit cleanup returned an error instead of a silent no-op: %v", err)
	}

	stuck, err := db.GetFleetGitHubDispatch(ctx, planID)
	if err != nil || stuck.DispatchState != "submitting" || stuck.RunID != 0 {
		t.Fatalf("defect no longer reproduces; update this pin alongside any intentional fix: %+v, %v", stuck, err)
	}
}

// TestDispatchAmbiguousOrdinaryLaneOccupiesTarget drives the real ordinary-
// lane HTTP dispatch route for a registered target whose GitHub dispatch
// outcome is ambiguous: the first POST keeps the route's unchanged legacy
// 502 fleet_github_dispatch_failed, a retry reports 409
// fleet_github_dispatch_ambiguous, the dispatch row stays submitting, and the
// target stays occupied (Uncertain/DispatchSubmissionUnresolved), so a second
// plan on the same cluster is refused until break-glass abandon resolves it.
func TestDispatchAmbiguousOrdinaryLaneOccupiesTarget(t *testing.T) {
	// newPGFenceHarness wires the real dispatch route and a production/nyc3
	// fleet root.
	p := newPGFenceHarness(t)
	reg := p.RegisterTarget(lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "occupied", StateBackend: "s3://occupied/state"}, []string{"cluster:occupied-cluster"})
	if reg.HTTPStatus != http.StatusCreated {
		t.Fatalf("register: %+v", reg)
	}
	post := func(plan fleettest.PlanRef) *httptest.ResponseRecorder {
		body, _ := json.Marshal(fleetGitHubDispatchRequest{})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+plan.ID+"/github/dispatch", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "token-occupies", DeviceID: "device-occupies", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		return rec
	}
	planA := p.SeedPlanOnCluster("scale", "occupied-cluster", "")
	p.github.configure(planA.ID, "", fleettest.GitHubOutcomeAmbiguous)
	if rec := post(planA); rec.Code != http.StatusBadGateway || decodeProblemCode(rec) != "fleet_github_dispatch_failed" {
		t.Fatalf("ambiguous dispatch must keep the legacy 502 = %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(planA); rec.Code != http.StatusConflict || decodeProblemCode(rec) != "fleet_github_dispatch_ambiguous" {
		t.Fatalf("ambiguous dispatch retry = %d %s", rec.Code, rec.Body.String())
	}
	stuck, err := p.db.GetFleetGitHubDispatch(context.Background(), planA.ID)
	if err != nil || stuck.DispatchState != "submitting" || stuck.RunID != 0 {
		t.Fatalf("ambiguous dispatch must stay submitting: %+v, %v", stuck, err)
	}
	if fence := p.FenceFacts(reg.TargetID); !fence.Held || fence.HolderPlanID != planA.ID {
		t.Fatalf("ambiguous dispatch must occupy the target: %+v", fence)
	}
	if occ, reason, err := p.Occupancy(reg.TargetID); err != nil || occ != lifecycle.OccupancyUncertain || reason != lifecycle.ReasonDispatchSubmissionUnresolved {
		t.Fatalf("occupancy = %s/%s, %v", occ, reason, err)
	}
	planB := p.SeedPlanOnCluster("scale", "occupied-cluster", "")
	p.github.configure(planB.ID, "", fleettest.GitHubOutcomeSubmitted)
	if rec := post(planB); rec.Code != http.StatusConflict || decodeProblemCode(rec) != lifecycle.CodeFleetTargetExecutionOccupied {
		t.Fatalf("second plan = %d %s", rec.Code, rec.Body.String())
	}
	// m11: the pre-check ran before the reservation, so the refused request
	// left no queued apply-dispatch reservation (only the legacy, replaceable
	// prepared binding).
	if row, err := p.db.GetFleetGitHubDispatch(context.Background(), planB.ID); err == nil && row.DispatchState != "prepared" {
		t.Fatalf("a refused plan advanced its dispatch: %+v", row)
	}
	var reservations int
	if err := p.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE kind='fleet.github.apply-dispatch' AND ref=$1`, planB.ID).Scan(&reservations); err != nil || reservations != 0 {
		t.Fatalf("a refused plan left %d reservations (err %v)", reservations, err)
	}
}

// TestReconcileAbandonedPlanIsConflictNotServerError drives the real
// github/reconcile route for an ordinary-lane dispatch left ambiguous and then
// break-glass abandoned: the recovered run can no longer be bound, and that is
// 409 fleet_target_holder_abandoned (consistent with the dispatch, execute and
// rerun routes), not a 500 fleet_github_receipt_failed.
func TestReconcileAbandonedPlanIsConflictNotServerError(t *testing.T) {
	p := newPGFenceHarness(t)
	reg := p.RegisterTarget(lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "reconcile-abandoned", StateBackend: "s3://reconcile-abandoned/state"}, []string{"cluster:reconcile-abandoned"})
	if reg.HTTPStatus != http.StatusCreated {
		t.Fatalf("register: %+v", reg)
	}
	post := func(path string, body interface{}) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "token-reconcile-abandoned", DeviceID: "device-reconcile-abandoned", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		return rec
	}
	plan := p.SeedPlanOnCluster("scale", "reconcile-abandoned", "")
	p.github.configure(plan.ID, "", fleettest.GitHubOutcomeAmbiguous)
	if rec := post("/api/v1/fleet/plans/"+plan.ID+"/github/dispatch", fleetGitHubDispatchRequest{}); rec.Code != http.StatusBadGateway {
		t.Fatalf("ambiguous dispatch = %d %s", rec.Code, rec.Body.String())
	}
	p.AgeHolder(plan, lifecycle.AbandonMinimumAge+time.Hour)
	if resp := p.AbandonPlan(plan); resp.HTTPStatus != http.StatusOK && resp.HTTPStatus != http.StatusCreated {
		t.Fatalf("abandon: %+v", resp)
	}
	// The workflow run now exists, so reconcile can recover it; binding it is
	// what abandonment refuses.
	p.github.configure(plan.ID, "", fleettest.GitHubOutcomeSubmitted)
	rec := post("/api/v1/fleet/plans/"+plan.ID+"/github/reconcile", fleetGitHubReconcileRequest{Kind: "apply-dispatch"})
	if rec.Code != http.StatusConflict || decodeProblemCode(rec) != lifecycle.CodeFleetTargetHolderAbandoned {
		t.Fatalf("reconcile of an abandoned plan = %d %s", rec.Code, rec.Body.String())
	}
}

func TestReconcileAbandonedCanceledRunTerminalizesReservationWithoutRebinding(t *testing.T) {
	p := newPGFenceHarness(t)
	reg := p.RegisterTarget(lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "cancelled-before-execution", StateBackend: "s3://cancelled-before-execution/state"}, []string{"cluster:cancelled-before-execution"})
	if reg.HTTPStatus != http.StatusCreated {
		t.Fatalf("register: %+v", reg)
	}
	post := func(path string, body interface{}) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "token-cancelled", DeviceID: "device-cancelled", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		return rec
	}
	plan := p.SeedPlanOnCluster("scale", "cancelled-before-execution", "")
	p.github.configure(plan.ID, "", fleettest.GitHubOutcomeAmbiguous)
	if rec := post("/api/v1/fleet/plans/"+plan.ID+"/github/dispatch", fleetGitHubDispatchRequest{}); rec.Code != http.StatusBadGateway {
		t.Fatalf("ambiguous dispatch = %d %s", rec.Code, rec.Body.String())
	}
	before, err := p.db.GetFleetGitHubDispatch(context.Background(), plan.ID)
	if err != nil || before.DispatchState != "submitting" || before.RunID != 0 || before.RunAttempt != 0 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	p.github.configure(plan.ID, "", fleettest.GitHubOutcomeSubmitted)
	runID := p.github.cancelBeforeProviderExecution(plan.ID)
	if runID == 0 {
		t.Fatal("cancelled run was not materialized")
	}
	notAbandoned := post("/api/v1/fleet/plans/"+plan.ID+"/github/reconcile", fleetGitHubReconcileRequest{Kind: "apply-dispatch", ExactRunID: runID, ExactRunAttempt: 1})
	if notAbandoned.Code != http.StatusConflict || decodeProblemCode(notAbandoned) != "fleet_github_reconcile_ambiguous" {
		t.Fatalf("non-abandoned exact reconcile = %d %s", notAbandoned.Code, notAbandoned.Body.String())
	}
	unchanged, err := p.db.GetFleetGitHubDispatch(context.Background(), plan.ID)
	if err != nil || unchanged.DispatchState != "submitting" || unchanged.RunID != 0 || unchanged.RunAttempt != 0 || unchanged.WorkflowURL != "" {
		t.Fatalf("non-abandoned dispatch changed: %+v err=%v", unchanged, err)
	}
	var queuedStatus model.OperationStatus
	if err := p.db.Pool.QueryRow(context.Background(), `SELECT status FROM operations WHERE kind='fleet.github.apply-dispatch' AND ref=$1`, plan.ID).Scan(&queuedStatus); err != nil || queuedStatus != model.OperationQueued {
		t.Fatalf("non-abandoned reservation status=%s err=%v", queuedStatus, err)
	}
	p.AgeHolder(plan, lifecycle.AbandonMinimumAge+time.Hour)
	if resp := p.AbandonPlan(plan); resp.HTTPStatus != http.StatusOK && resp.HTTPStatus != http.StatusCreated {
		t.Fatalf("abandon: %+v", resp)
	}
	rec := post("/api/v1/fleet/plans/"+plan.ID+"/github/reconcile", fleetGitHubReconcileRequest{Kind: "apply-dispatch", ExactRunID: runID, ExactRunAttempt: 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("exact reconcile = %d %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Operation model.Operation `json:"operation"`
		Outcome   string          `json:"outcome"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Outcome != "verified-cancelled-before-execution" || response.Operation.Status != model.OperationCanceled || response.Operation.Receipt == nil {
		t.Fatalf("response=%+v", response)
	}
	after, err := p.db.GetFleetGitHubDispatch(context.Background(), plan.ID)
	if err != nil || after.DispatchState != "submitting" || after.RunID != 0 || after.RunAttempt != 0 || after.WorkflowURL != "" {
		t.Fatalf("after=%+v err=%v", after, err)
	}
}

func TestReconcileCanceledRunRejectsBoundReservation(t *testing.T) {
	p := newPGFenceHarness(t)
	reg := p.RegisterTarget(lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "cancelled-bound", StateBackend: "s3://cancelled-bound/state"}, []string{"cluster:cancelled-bound"})
	if reg.HTTPStatus != http.StatusCreated {
		t.Fatalf("register: %+v", reg)
	}
	post := func(path string, body interface{}) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "token-cancelled-bound", DeviceID: "device-cancelled-bound", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		return rec
	}
	boundPlan := p.SeedPlanOnCluster("scale", "cancelled-bound", "")
	p.github.configure(boundPlan.ID, "", fleettest.GitHubOutcomeAmbiguous)
	if rec := post("/api/v1/fleet/plans/"+boundPlan.ID+"/github/dispatch", fleetGitHubDispatchRequest{}); rec.Code != http.StatusBadGateway {
		t.Fatalf("bound-plan ambiguous dispatch = %d %s", rec.Code, rec.Body.String())
	}
	p.github.configure(boundPlan.ID, "", fleettest.GitHubOutcomeSubmitted)
	boundRunID := p.github.cancelBeforeProviderExecution(boundPlan.ID)
	bound, err := p.db.GetFleetGitHubDispatch(context.Background(), boundPlan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.FinishFleetGitHubDispatch(context.Background(), boundPlan.ID, bound.DispatchNonceSHA256, boundRunID, 1, fmt.Sprintf("https://github.com/%s/actions/runs/%d", pgConformanceRepository, boundRunID)); err != nil {
		t.Fatal(err)
	}
	boundRec := post("/api/v1/fleet/plans/"+boundPlan.ID+"/github/reconcile", fleetGitHubReconcileRequest{Kind: "apply-dispatch", ExactRunID: boundRunID, ExactRunAttempt: 1})
	if boundRec.Code != http.StatusConflict || decodeProblemCode(boundRec) != "fleet_github_reconcile_ambiguous" {
		t.Fatalf("bound exact reconcile = %d %s", boundRec.Code, boundRec.Body.String())
	}
}
