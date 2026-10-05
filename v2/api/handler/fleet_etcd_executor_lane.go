package handler

// The etcd executor lane for attempt reads, heartbeat, advance, cancel and
// reconciliation checkpoints (plan-5-7 E6). The GitHub lane's workload() needs
// a GitHub Actions token. The executor principal and the attempt-bound
// credential (fleet:attempt) are instead checked against the immutable
// sibling record /v3/fleet-executor-attempts/<plan>/<attempt> (principal
// subject), after the converge plan verified under the converge-aware
// verifier.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// executorLane returns the request's principal when it is the executor
// principal or the attempt-bound credential.
func (h *EtcdFleetRunnerHandler) executorLane(r *http.Request) (AccessPrincipal, bool) {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok || !(IsFleetExecutorPrincipal(principal) || isFleetAttemptCredential(principal)) {
		return AccessPrincipal{}, false
	}
	return principal, true
}

// requireExecutorDevice is the defence-in-depth platform check for the
// executor principal: its live device must be a fleet-executor device. The
// attempt-bound credential has no device claim; its attempt binding replaces
// this check.
func (h *EtcdFleetRunnerHandler) requireExecutorDevice(w http.ResponseWriter, r *http.Request, principal AccessPrincipal) bool {
	if h.devices == nil {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "the executor lane requires a "+FleetExecutorPlatform+" device")
		return false
	}
	if device, err := h.devices.ActiveAccessDevice(r.Context(), principal.DeviceID); err != nil || device == nil || !isFleetExecutorPlatform(device.Platform) {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "the executor lane requires a "+FleetExecutorPlatform+" device")
		return false
	}
	return true
}

// executorPlan loads the path plan as a converge plan that verifies under the
// converge-aware verifier. (plan() verifies a capacity plan, which would drop
// the converge object.)
func (h *EtcdFleetRunnerHandler) executorPlan(w http.ResponseWriter, r *http.Request) (*model.Operation, bool) {
	if h == nil || h.operations == nil || h.control == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_runner_attempt_store_unavailable", "durable Fleet runner storage is unavailable")
		return nil, false
	}
	id := chi.URLParam(r, "planID")
	if _, err := uuid.Parse(id); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "plan ID must be a UUID")
		return nil, false
	}
	plan, err := h.operations.GetOperation(r.Context(), id)
	if errors.Is(err, etcdstore.ErrNotFound) || (err == nil && plan.Kind != "fleet.capacity-plan") {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_plan_not_found", "fleet capacity plan not found")
		return nil, false
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_plan_read_failed", "fleet capacity plan could not be read")
		return nil, false
	}
	if _, decoded := decodeConvergePlan(plan); !decoded {
		WriteControlProblem(w, r, http.StatusConflict, executor.CodeFleetPlanNotConverge, "the plan is not a converge plan")
		return nil, false
	}
	if !h.control.fleetConvergePlanVerified(plan) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_plan_invalid", "converge plan failed integrity verification")
		return nil, false
	}
	return plan, true
}

// ownExecutorAttempt resolves attemptID under plan and checks the lane's
// ownership against the executor-attempt metadata. The executor principal must
// be the attempt's principal subject on a live fleet-executor device. The
// attempt-bound credential (when allowed) must be bound to exactly this
// attempt, and with live also needs a queued or running attempt.
func (h *EtcdFleetRunnerHandler) ownExecutorAttempt(w http.ResponseWriter, r *http.Request, principal AccessPrincipal, plan *model.Operation, attemptID string, allowCredential, live bool) (*fleet.RunnerAttempt, bool) {
	credential := isFleetAttemptCredential(principal)
	if credential && !allowCredential {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "the attempt-bound credential cannot call this route")
		return nil, false
	}
	if !credential && !h.requireExecutorDevice(w, r, principal) {
		return nil, false
	}
	if _, err := uuid.Parse(attemptID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt_id", "runner attempt ID must be a UUID")
		return nil, false
	}
	item, err := h.operations.GetFleetRunnerAttempt(r.Context(), plan.ID, attemptID)
	if errors.Is(err, etcdstore.ErrNotFound) {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_runner_attempt_not_found", "runner attempt not found")
		return nil, false
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_runner_attempt_read_failed", "runner attempt could not be read")
		return nil, false
	}
	meta, err := h.operations.GetFleetExecutorAttemptMeta(r.Context(), plan.ID, item.ID)
	if errors.Is(err, etcdstore.ErrNotFound) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_identity_mismatch", "the attempt is not an executor-lane attempt")
		return nil, false
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_runner_attempt_read_failed", "runner attempt metadata could not be read")
		return nil, false
	}
	droplet, invocation, planID, idOK := executor.ParseRunnerAttemptID(item.RunnerAttemptID)
	if !idOK || planID != plan.ID || item.WorkflowURL != executor.WorkflowURL(droplet, invocation) || meta.Executor.DropletID != droplet || meta.Executor.InvocationID != invocation {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_identity_mismatch", "the executor attempt's identifiers are inconsistent")
		return nil, false
	}
	if credential {
		if !FleetAttemptCredentialMayActOn(principal, item.ID) {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_binding_conflict", "the attempt-bound credential may act only on its own attempt")
			return nil, false
		}
		if live && item.Status != string(model.FleetRunnerAttemptQueued) && item.Status != string(model.FleetRunnerAttemptRunning) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_stale", "the bound attempt is no longer live")
			return nil, false
		}
		return item, true
	}
	if meta.PrincipalSubject != executor.PrincipalSubject(principal.DeviceID) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_binding_conflict", "only the executor that started this attempt may use it")
		return nil, false
	}
	return item, true
}

func (h *EtcdFleetRunnerHandler) executorEnqueue(r *http.Request, principal AccessPrincipal, attemptID, key string, semantics map[string]interface{}) (pipeline.EnqueueRequest, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 200 {
		return pipeline.EnqueueRequest{}, errors.New("a non-empty Idempotency-Key of at most 200 characters is required")
	}
	authority, err := h.operations.Authority(r.Context())
	if err != nil {
		return pipeline.EnqueueRequest{}, err
	}
	actor := store.OperationActor{Issuer: authority + "/device", Subject: principal.DeviceID}
	if isFleetAttemptCredential(principal) {
		actor = store.OperationActor{Issuer: authority + "/fleet-attempt", Subject: attemptID}
	}
	return pipeline.EnqueueRequest{Authority: authority, Actor: actor, Key: key, Audit: store.AcceptanceAuditContext{CredentialID: principal.TokenID, DeviceID: principal.DeviceID, Source: "etcd-fleet-executor", Scopes: append([]string(nil), principal.Scopes...)}, Semantics: semantics}, nil
}

func (h *EtcdFleetRunnerHandler) reconcileExecutor(w http.ResponseWriter, r *http.Request, principal AccessPrincipal) {
	plan, ok := h.executorPlan(w, r)
	if !ok {
		return
	}
	var request fleet.ReconciliationRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_reconciliation", err.Error())
		return
	}
	request.Message = strings.TrimSpace(request.Message)
	if err := validateFleetReconciliationRequest(request); err != nil || request.AttemptID == "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_reconciliation", "reconciliation requires a valid attempt-bound checkpoint")
		return
	}
	item, ok := h.ownExecutorAttempt(w, r, principal, plan, request.AttemptID, true, true)
	if !ok {
		return
	}
	if item.CommitSHA != request.CommitSHA || item.PlanSHA256 != request.PlanSHA256 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_binding_conflict", "checkpoint does not match the attempt's reviewed input")
		return
	}
	semantics := map[string]interface{}{"action": "fleet.reconciliation", "planId": plan.ID, "request": request}
	enqueue, err := h.executorEnqueue(r, principal, item.ID, r.Header.Get("Idempotency-Key"), semantics)
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", err.Error())
		return
	}
	enqueue.FleetReconciliation = &store.FleetReconciliationAdmission{PlanID: plan.ID, AttemptID: item.ID, RequireActiveAttempt: true, RunnerAttemptID: item.RunnerAttemptID, WorkflowURL: item.WorkflowURL}
	h.finishReconcile(w, r, plan, request, enqueue)
}

func (h *EtcdFleetRunnerHandler) listReconciliationsExecutor(w http.ResponseWriter, r *http.Request, principal AccessPrincipal) {
	plan, ok := h.executorPlan(w, r)
	if !ok {
		return
	}
	attemptID := strings.TrimSpace(r.URL.Query().Get("attemptId"))
	if attemptID == "" {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_binding_conflict", "a Fleet runner must name its durable attempt to read reconciliation evidence")
		return
	}
	item, ok := h.ownExecutorAttempt(w, r, principal, plan, attemptID, true, false)
	if !ok {
		return
	}
	items, err := h.operations.ListFleetReconciliations(r.Context(), plan.ID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_reconciliation_read_failed", "reconciliation checkpoints could not be read")
		return
	}
	owned := make([]model.Operation, 0, len(items))
	for _, op := range items {
		if bound, _ := op.Payload["attemptId"].(string); bound == item.ID {
			op.AttachReceipt()
			owned = append(owned, op)
		}
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{"schemaVersion": fleet.ReconciliationSchemaVersion, "planId": plan.ID, "reconciliations": owned, "count": len(owned)})
}
