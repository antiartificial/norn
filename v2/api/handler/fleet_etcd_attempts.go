package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// EtcdFleetRunnerHandler shares the PG route's request and identity validators
// while admitting all mutations through the etcd signed operation aggregate.
// The normal Fleet router registers these routes only with a complete
// protected-dispatch bridge and externally fenced recovery contract.
type EtcdFleetRunnerHandler struct {
	operations *etcdstore.V3OperationStore
	control    *Handler
}

func NewEtcdFleetRunnerHandler(cfg *config.Config, operations *etcdstore.V3OperationStore) *EtcdFleetRunnerHandler {
	return &EtcdFleetRunnerHandler{operations: operations, control: &Handler{cfg: cfg, operationStore: operations, pipeline: &pipeline.Pipeline{OperationStore: operations}}}
}

func (h *EtcdFleetRunnerHandler) plan(w http.ResponseWriter, r *http.Request) (*model.Operation, bool) {
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
	typed, err := typedCapacityPlan(plan)
	if err != nil || !h.control.verifyCapacityPlan(typed) || plan.Status != model.OperationSucceeded {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_plan_invalid", "fleet capacity plan failed portable integrity verification")
		return nil, false
	}
	return plan, true
}

func (h *EtcdFleetRunnerHandler) workload(w http.ResponseWriter, r *http.Request) (AccessPrincipal, bool) {
	principal, ok := requireFleetRunnerPrincipal(w, r)
	if !ok || principal.Source != AccessPrincipalSourceManagedToken || principal.TokenID == "" {
		if ok {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_oidc_required", "a durable GitHub Actions workload token is required")
		}
		return AccessPrincipal{}, false
	}
	return principal, true
}

func (h *EtcdFleetRunnerHandler) enqueue(r *http.Request, principal AccessPrincipal, key string, semantics map[string]interface{}) (pipeline.EnqueueRequest, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 200 {
		return pipeline.EnqueueRequest{}, fmt.Errorf("a non-empty Idempotency-Key of at most 200 characters is required")
	}
	authority, err := h.operations.Authority(r.Context())
	if err != nil {
		return pipeline.EnqueueRequest{}, err
	}
	return pipeline.EnqueueRequest{Authority: authority, Actor: store.OperationActor{Issuer: "github-actions", Subject: canonicalRunnerAttemptID(principal.CI)}, Key: key, Audit: store.AcceptanceAuditContext{CredentialID: principal.TokenID, Source: "etcd-fleet-runner", Scopes: append([]string(nil), principal.Scopes...)}, Semantics: semantics}, nil
}

func (h *EtcdFleetRunnerHandler) Create(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.workload(w, r)
	if !ok {
		return
	}
	plan, ok := h.plan(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerAttemptCreateRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt", err.Error())
		return
	}
	if err := validateRunnerAttemptCreate(request); err != nil || request.WorkflowURL != canonicalWorkflowRunURL(principal.CI.Repository, principal.CI.RunID) || request.RunnerAttemptID != canonicalRunnerAttemptID(principal.CI) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_identity_mismatch", "runner request does not match the verified workflow identity")
		return
	}
	admission := &store.FleetRunnerAttemptAdmission{PlanID: plan.ID, AttemptID: uuid.NewString(), RunnerAttemptID: request.RunnerAttemptID, CommitSHA: request.CommitSHA, PlanSHA256: request.PlanSHA256, WorkflowURL: request.WorkflowURL, DispatchNonceSHA256: hashFleetDispatchNonce(request.DispatchNonce), SourceDispatchRunID: request.SourceDispatchRunID, Resume: request.Resume, HeartbeatTimeoutSeconds: request.HeartbeatTimeoutSeconds, WorkloadIntent: principal.CI.Intent, WorkloadRunID: principal.CI.RunID, WorkloadSHA: principal.CI.SHA}
	semantics := map[string]interface{}{"action": "fleet.runner-attempt", "planId": plan.ID, "runnerAttemptId": request.RunnerAttemptID, "commitSha": request.CommitSHA, "planSha256": request.PlanSHA256, "workflowUrl": request.WorkflowURL, "dispatchNonceSha256": admission.DispatchNonceSHA256, "sourceDispatchRunId": request.SourceDispatchRunID, "resume": request.Resume, "heartbeatTimeoutSeconds": request.HeartbeatTimeoutSeconds, "workload": map[string]interface{}{"intent": principal.CI.Intent, "runId": principal.CI.RunID, "sha": principal.CI.SHA}}
	enqueue, err := h.enqueue(r, principal, r.Header.Get("Idempotency-Key"), semantics)
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", err.Error())
		return
	}
	enqueue.FleetRunnerAttempt = admission
	if replay, err := h.control.pipeline.ResolveEnqueue(r.Context(), enqueue, "fleet.runner-attempt", plan.ID); err == nil {
		if !acceptedRequestMatches(replay, enqueue) || replay.FleetRunnerAttempt == nil {
			WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for another runner request")
			return
		}
		writeJSON(w, replay.FleetRunnerAttempt)
		return
	} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	attempts, err := h.operations.ListFleetRunnerAttempts(r.Context(), plan.ID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_runner_attempt_read_failed", "existing runner attempts could not be read")
		return
	}
	if len(attempts) > 0 {
		admission.ExpectedPredecessorID = attempts[0].ID
	}
	now := time.Now().UTC()
	finished := now
	op := model.Operation{ID: uuid.NewString(), Kind: "fleet.runner-attempt", Ref: plan.ID, Status: model.OperationSucceeded, Risk: "protected runner lease; external termination requires independent proof", Source: "fleet-runner", Message: "protected fleet runner attempt accepted", Payload: map[string]interface{}{"attemptId": admission.AttemptID, "planId": plan.ID, "runnerAttemptId": request.RunnerAttemptID, "commitSha": request.CommitSHA, "planSha256": request.PlanSHA256, "workflowUrl": request.WorkflowURL, "dispatchNonceSha256": admission.DispatchNonceSHA256, "sourceDispatchRunId": request.SourceDispatchRunID, "resume": request.Resume, "heartbeatTimeoutSeconds": request.HeartbeatTimeoutSeconds}, Metadata: map[string]interface{}{}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1}
	accepted, err := h.control.pipeline.QueueOperation(r.Context(), op, enqueue)
	if err != nil {
		if replay, resolveErr := h.control.pipeline.ResolveEnqueue(r.Context(), enqueue, "fleet.runner-attempt", plan.ID); resolveErr == nil && acceptedRequestMatches(replay, enqueue) && replay.FleetRunnerAttempt != nil {
			writeJSON(w, replay.FleetRunnerAttempt)
			return
		}
		writeOperationAcceptanceError(w, r, err)
		return
	}
	if accepted.FleetRunnerAttempt == nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_store_failed", "accepted runner attempt is unavailable")
		return
	}
	w.Header().Set("Location", "/api/v1/fleet/plans/"+plan.ID+"/attempts/"+accepted.FleetRunnerAttempt.ID)
	if accepted.Replayed {
		writeJSON(w, accepted.FleetRunnerAttempt)
		return
	}
	writeJSONStatus(w, http.StatusCreated, accepted.FleetRunnerAttempt)
}

func (h *EtcdFleetRunnerHandler) ownAttempt(w http.ResponseWriter, r *http.Request) (*fleet.RunnerAttempt, bool) {
	principal, ok := h.workload(w, r)
	if !ok {
		return nil, false
	}
	plan, ok := h.plan(w, r)
	if !ok {
		return nil, false
	}
	id := chi.URLParam(r, "attemptID")
	if _, err := uuid.Parse(id); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt_id", "runner attempt ID must be a UUID")
		return nil, false
	}
	item, err := h.operations.GetFleetRunnerAttempt(r.Context(), plan.ID, id)
	if errors.Is(err, etcdstore.ErrNotFound) {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_runner_attempt_not_found", "runner attempt not found")
		return nil, false
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_runner_attempt_read_failed", "runner attempt could not be read")
		return nil, false
	}
	if item.RunnerAttemptID != canonicalRunnerAttemptID(principal.CI) || item.WorkflowURL != canonicalWorkflowRunURL(principal.CI.Repository, principal.CI.RunID) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_identity_mismatch", "workload token may access only its own attempt")
		return nil, false
	}
	return item, true
}

func (h *EtcdFleetRunnerHandler) readablePlan(w http.ResponseWriter, r *http.Request) (*model.Operation, AccessPrincipal, bool) {
	principal, ok := h.workload(w, r)
	if !ok {
		return nil, AccessPrincipal{}, false
	}
	plan, ok := h.plan(w, r)
	if !ok {
		return nil, AccessPrincipal{}, false
	}
	if principal.CI.Intent == "apply" {
		binding, err := h.operations.GetFleetRunnerDispatch(r.Context(), plan.ID)
		if err == nil && strconv.FormatInt(binding.RunID, 10) == principal.CI.RunID && binding.ApprovedHeadSHA == principal.CI.SHA {
			return plan, principal, true
		}
	} else if principal.CI.Intent == "recover" {
		attempts, err := h.operations.ListFleetRunnerAttempts(r.Context(), plan.ID)
		if err == nil {
			for _, item := range attempts {
				if item.RunnerAttemptID == canonicalRunnerAttemptID(principal.CI) && item.WorkflowURL == canonicalWorkflowRunURL(principal.CI.Repository, principal.CI.RunID) {
					return plan, principal, true
				}
			}
		}
	}
	WriteControlProblem(w, r, http.StatusForbidden, "fleet_plan_read_forbidden", "workload token is not bound to this plan")
	return nil, AccessPrincipal{}, false
}

func (h *EtcdFleetRunnerHandler) List(w http.ResponseWriter, r *http.Request) {
	plan, principal, ok := h.readablePlan(w, r)
	if !ok {
		return
	}
	items, err := h.operations.ListFleetRunnerAttempts(r.Context(), plan.ID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_runner_attempt_read_failed", "runner attempts could not be read")
		return
	}
	owned := make([]fleet.RunnerAttempt, 0, 1)
	for _, item := range items {
		if item.RunnerAttemptID == canonicalRunnerAttemptID(principal.CI) && item.WorkflowURL == canonicalWorkflowRunURL(principal.CI.Repository, principal.CI.RunID) {
			owned = append(owned, item)
		}
	}
	writeJSON(w, map[string]interface{}{"schemaVersion": fleet.RunnerAttemptSchemaVersion, "planId": plan.ID, "attempts": owned, "count": len(owned), "serverTime": time.Now().UTC()})
}

func (h *EtcdFleetRunnerHandler) Get(w http.ResponseWriter, r *http.Request) {
	item, ok := h.ownAttempt(w, r)
	if ok {
		writeJSON(w, item)
	}
}

func (h *EtcdFleetRunnerHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	item, ok := h.ownAttempt(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerAttemptHeartbeatRequest
	if err := decodeControlJSON(w, r, &request); err != nil || request.SchemaVersion != fleet.RunnerAttemptSchemaVersion || request.Phase != item.CurrentPhase || request.Sequence != item.HeartbeatSequence+1 || request.Revision != item.Revision || len(request.Message) > 500 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_stale", "heartbeat must use the current phase, next sequence, and revision")
		return
	}
	updated, err := h.operations.UpdateFleetRunnerAttempt(r.Context(), item.PlanID, item.ID, item.Revision, "heartbeat", request.Sequence, strings.TrimSpace(request.Message))
	h.writeUpdate(w, r, updated, err)
}

func (h *EtcdFleetRunnerHandler) Advance(w http.ResponseWriter, r *http.Request) {
	item, ok := h.ownAttempt(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerAttemptAdvanceRequest
	if err := decodeControlJSON(w, r, &request); err != nil || request.SchemaVersion != fleet.RunnerAttemptSchemaVersion || request.ExpectedPhase != item.CurrentPhase || request.Revision != item.Revision {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_stale", "advance must use the current phase and revision")
		return
	}
	next := nextFleetReconciliationPhase(item.CurrentPhase)
	if next == "" {
		next = "complete"
	}
	updated, err := h.operations.UpdateFleetRunnerAttempt(r.Context(), item.PlanID, item.ID, item.Revision, "advance", next)
	h.writeUpdate(w, r, updated, err)
}

func (h *EtcdFleetRunnerHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	item, ok := h.ownAttempt(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerAttemptCancelRequest
	if err := decodeControlJSON(w, r, &request); err != nil || request.SchemaVersion != fleet.RunnerAttemptSchemaVersion || request.Revision != item.Revision || len(request.Reason) > 1000 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_stale", "cancel must use the current revision")
		return
	}
	updated, err := h.operations.UpdateFleetRunnerAttempt(r.Context(), item.PlanID, item.ID, item.Revision, "cancel", strings.TrimSpace(request.Reason))
	h.writeUpdate(w, r, updated, err)
}

func (h *EtcdFleetRunnerHandler) writeUpdate(w http.ResponseWriter, r *http.Request, item *fleet.RunnerAttempt, err error) {
	if err == nil {
		writeJSON(w, item)
		return
	}
	if errors.Is(err, etcdstore.ErrNotFound) || errors.Is(err, store.ErrFleetRunnerAttemptAdmission) || errors.Is(err, store.ErrFleetReconciliationAdmission) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_stale", "attempt changed, expired, or lacks matching evidence")
		return
	}
	WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_runner_attempt_store_failed", "runner attempt could not be updated")
}

func fleetReconciliationPayload(request fleet.ReconciliationRequest) map[string]interface{} {
	encoded, _ := json.Marshal(request)
	var payload map[string]interface{}
	_ = json.Unmarshal(encoded, &payload)
	return payload
}

func (h *EtcdFleetRunnerHandler) Reconcile(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.workload(w, r)
	if !ok {
		return
	}
	plan, ok := h.plan(w, r)
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
	item, err := h.operations.GetFleetRunnerAttempt(r.Context(), plan.ID, request.AttemptID)
	if err != nil || item.RunnerAttemptID != canonicalRunnerAttemptID(principal.CI) || item.WorkflowURL != canonicalWorkflowRunURL(principal.CI.Repository, principal.CI.RunID) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_identity_mismatch", "checkpoint must belong to this workflow attempt")
		return
	}
	semantics := map[string]interface{}{"action": "fleet.reconciliation", "planId": plan.ID, "request": request}
	enqueue, err := h.enqueue(r, principal, r.Header.Get("Idempotency-Key"), semantics)
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", err.Error())
		return
	}
	enqueue.FleetReconciliation = &store.FleetReconciliationAdmission{PlanID: plan.ID, AttemptID: request.AttemptID, RequireActiveAttempt: true, RunnerAttemptID: canonicalRunnerAttemptID(principal.CI), WorkflowURL: canonicalWorkflowRunURL(principal.CI.Repository, principal.CI.RunID)}
	if replay, err := h.control.pipeline.ResolveEnqueue(r.Context(), enqueue, "fleet.reconciliation", plan.ID); err == nil {
		if !acceptedRequestMatches(replay, enqueue) {
			WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for another checkpoint")
			return
		}
		replay.Operation.AttachReceipt()
		writeJSON(w, replay.Operation)
		return
	} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	now := time.Now().UTC()
	finished := now
	status := model.OperationSucceeded
	if request.Status == "failed" {
		status = model.OperationFailed
	}
	message := request.Message
	if message == "" {
		message = request.Phase + " " + request.Status
	}
	op := model.Operation{ID: uuid.NewString(), Kind: "fleet.reconciliation", Ref: plan.ID, Status: status, Risk: "append-only infrastructure reconciliation evidence", Source: "fleet-runner", Message: message, Payload: fleetReconciliationPayload(request), Metadata: map[string]interface{}{}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1}
	accepted, err := h.control.pipeline.QueueOperation(r.Context(), op, enqueue)
	if err != nil {
		if replay, resolveErr := h.control.pipeline.ResolveEnqueue(r.Context(), enqueue, "fleet.reconciliation", plan.ID); resolveErr == nil && acceptedRequestMatches(replay, enqueue) {
			replay.Operation.AttachReceipt()
			writeJSON(w, replay.Operation)
			return
		}
		writeOperationAcceptanceError(w, r, err)
		return
	}
	accepted.Operation.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	if accepted.Replayed {
		writeJSON(w, accepted.Operation)
		return
	}
	writeJSONStatus(w, http.StatusCreated, accepted.Operation)
}

func (h *EtcdFleetRunnerHandler) ListReconciliations(w http.ResponseWriter, r *http.Request) {
	plan, principal, ok := h.readablePlan(w, r)
	if !ok {
		return
	}
	items, err := h.operations.ListFleetReconciliations(r.Context(), plan.ID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_reconciliation_read_failed", "reconciliation checkpoints could not be read")
		return
	}
	owned := make([]model.Operation, 0, len(items))
	for _, item := range items {
		attemptID, _ := item.Payload["attemptId"].(string)
		if attemptID == "" {
			continue
		}
		attempt, err := h.operations.GetFleetRunnerAttempt(r.Context(), plan.ID, attemptID)
		if err == nil && attempt.RunnerAttemptID == canonicalRunnerAttemptID(principal.CI) && attempt.WorkflowURL == canonicalWorkflowRunURL(principal.CI.Repository, principal.CI.RunID) {
			item.AttachReceipt()
			owned = append(owned, item)
		}
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{"schemaVersion": fleet.ReconciliationSchemaVersion, "planId": plan.ID, "reconciliations": owned, "count": len(owned)})
}
