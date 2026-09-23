package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

func (h *Handler) ListFleetRunnerAttempts(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireFleetReadScope(w, r); !ok {
		return
	}
	plan, ok := h.fleetAttemptPlan(w, r)
	if !ok {
		return
	}
	items, err := h.db.ListFleetRunnerAttempts(r.Context(), plan.ID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_read_failed", "failed to read runner attempts")
		return
	}
	if principal, exists := AccessPrincipalFromRequest(r); exists && principal.CI != nil {
		owned := make([]fleet.RunnerAttempt, 0, 1)
		for _, item := range items {
			if item.RunnerAttemptID == canonicalRunnerAttemptID(principal.CI) && item.WorkflowURL == canonicalWorkflowRunURL(principal.CI.Repository, principal.CI.RunID) {
				owned = append(owned, item)
			}
		}
		items = owned
	}
	writeJSON(w, map[string]interface{}{"schemaVersion": fleet.RunnerAttemptSchemaVersion, "planId": plan.ID, "attempts": items, "count": len(items), "serverTime": time.Now().UTC()})
}

func (h *Handler) GetFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireFleetReadScope(w, r); !ok {
		return
	}
	item, ok := h.fleetAttempt(w, r)
	if !ok {
		return
	}
	writeJSON(w, item)
}

func (h *Handler) CreateFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireFleetRunnerPrincipal(w, r)
	if !ok {
		return
	}
	// Recovery authorization is established by the nonce-bearing create body;
	// it cannot first perform a GET tied to the new recovery workflow identity.
	plan, ok := h.fleetAttemptPlanForCreate(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerAttemptCreateRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt", err.Error())
		return
	}
	if err := validateRunnerAttemptCreate(request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt", err.Error())
		return
	}
	if request.WorkflowURL != canonicalWorkflowRunURL(principal.CI.Repository, principal.CI.RunID) || request.RunnerAttemptID != canonicalRunnerAttemptID(principal.CI) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_identity_mismatch", "fleet workload token does not bind this commit and workflow run")
		return
	}
	typedPlan, planErr := typedCapacityPlan(plan)
	if planErr != nil || !h.verifyCapacityPlan(typedPlan) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_plan_invalid", "fleet capacity plan failed portable integrity verification")
		return
	}
	if h.pipeline == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "durable signed operation acceptance is unavailable")
		return
	}
	admission := &store.FleetRunnerAttemptAdmission{
		PlanID: plan.ID, AttemptID: uuid.NewString(), RunnerAttemptID: request.RunnerAttemptID,
		CommitSHA: request.CommitSHA, PlanSHA256: request.PlanSHA256, WorkflowURL: request.WorkflowURL,
		DispatchNonceSHA256: hashFleetDispatchNonce(request.DispatchNonce), SourceDispatchRunID: request.SourceDispatchRunID,
		Resume: request.Resume, HeartbeatTimeoutSeconds: request.HeartbeatTimeoutSeconds,
		WorkloadIntent: principal.CI.Intent, WorkloadRunID: principal.CI.RunID, WorkloadSHA: principal.CI.SHA,
	}
	semantics := map[string]interface{}{
		"action": "fleet.runner-attempt", "planId": plan.ID,
		"runnerAttemptId": request.RunnerAttemptID, "commitSha": request.CommitSHA, "planSha256": request.PlanSHA256,
		"workflowUrl": request.WorkflowURL, "dispatchNonceSha256": admission.DispatchNonceSHA256,
		"sourceDispatchRunId": request.SourceDispatchRunID, "resume": request.Resume,
		"heartbeatTimeoutSeconds": request.HeartbeatTimeoutSeconds,
		"workload":                map[string]interface{}{"intent": principal.CI.Intent, "runId": principal.CI.RunID, "sha": principal.CI.SHA},
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), semantics)
	if !ok {
		return
	}
	enqueue.FleetRunnerAttempt = admission
	if replayed, found, resolveErr := h.resolveFleetRunnerAttemptReplay(r.Context(), enqueue, plan.ID); resolveErr != nil {
		writeOperationAcceptanceError(w, r, resolveErr)
		return
	} else if found {
		writeJSON(w, replayed)
		return
	}
	existing, err := h.db.ListFleetRunnerAttempts(r.Context(), plan.ID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_read_failed", "failed to read existing runner attempts")
		return
	}
	if len(existing) > 0 {
		admission.ExpectedPredecessorID = existing[0].ID
	}
	now := time.Now().UTC()
	finished := now
	payload := map[string]interface{}{
		"attemptId": admission.AttemptID, "planId": plan.ID, "runnerAttemptId": request.RunnerAttemptID,
		"commitSha": request.CommitSHA, "planSha256": request.PlanSHA256, "workflowUrl": request.WorkflowURL,
		"dispatchNonceSha256": admission.DispatchNonceSHA256, "sourceDispatchRunId": request.SourceDispatchRunID,
		"resume": request.Resume, "heartbeatTimeoutSeconds": request.HeartbeatTimeoutSeconds,
	}
	op := model.Operation{ID: uuid.NewString(), Kind: "fleet.runner-attempt", Ref: plan.ID, Status: model.OperationSucceeded,
		Risk: "bounded protected runner lease; external execution termination remains independently proven", Source: "fleet-runner",
		Message: "protected fleet runner attempt accepted", Payload: payload, Metadata: map[string]interface{}{},
		StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1}
	accepted, err := h.pipeline.QueueOperation(r.Context(), op, enqueue)
	if err != nil {
		if replayed, found, _ := h.resolveFleetRunnerAttemptReplay(r.Context(), enqueue, plan.ID); found {
			writeJSON(w, replayed)
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

func (h *Handler) resolveFleetRunnerAttemptReplay(ctx context.Context, request pipeline.EnqueueRequest, planID string) (*fleet.RunnerAttempt, bool, error) {
	accepted, err := h.pipeline.ResolveEnqueue(ctx, request, "fleet.runner-attempt", planID)
	if errors.Is(err, store.ErrAcceptanceNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !acceptedRequestMatches(accepted, request) || accepted.Operation.Kind != "fleet.runner-attempt" || accepted.Operation.Ref != planID || accepted.FleetRunnerAttempt == nil {
		return nil, false, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "fleet.runner-attempt", Resource: planID}}
	}
	return accepted.FleetRunnerAttempt, true, nil
}

func (h *Handler) HeartbeatFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireFleetRunnerPrincipal(w, r); !ok {
		return
	}
	item, ok := h.fleetAttempt(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerAttemptHeartbeatRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt", err.Error())
		return
	}
	if request.SchemaVersion != fleet.RunnerAttemptSchemaVersion || request.Phase != item.CurrentPhase || request.Sequence != item.HeartbeatSequence+1 || request.Revision != item.Revision || len(request.Message) > 500 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_stale", "heartbeat must use the current phase, next sequence, and revision")
		return
	}
	updated, err := h.db.UpdateFleetRunnerAttempt(r.Context(), item.PlanID, item.ID, item.Revision, "heartbeat", request.Sequence, strings.TrimSpace(request.Message))
	h.writeRunnerAttemptUpdate(w, r, updated, err)
}

func (h *Handler) AdvanceFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireFleetRunnerPrincipal(w, r); !ok {
		return
	}
	item, ok := h.fleetAttempt(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerAttemptAdvanceRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt", err.Error())
		return
	}
	if request.SchemaVersion != fleet.RunnerAttemptSchemaVersion || request.ExpectedPhase != item.CurrentPhase || request.Revision != item.Revision {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_stale", "advance must use the current phase and revision")
		return
	}
	if !h.hasRunnerEvidence(r.Context(), item) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_evidence_required", "advance requires successful matching reconciliation evidence for the current phase")
		return
	}
	next := nextFleetReconciliationPhase(item.CurrentPhase)
	if next == "" {
		next = "complete"
	}
	updated, err := h.db.UpdateFleetRunnerAttempt(r.Context(), item.PlanID, item.ID, item.Revision, "advance", next)
	h.writeRunnerAttemptUpdate(w, r, updated, err)
}

func (h *Handler) CancelFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireFleetRunnerPrincipal(w, r); !ok {
		return
	}
	item, ok := h.fleetAttempt(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerAttemptCancelRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt", err.Error())
		return
	}
	if request.SchemaVersion != fleet.RunnerAttemptSchemaVersion || request.Revision != item.Revision || len(request.Reason) > 1000 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_stale", "cancel must use the current revision")
		return
	}
	updated, err := h.db.UpdateFleetRunnerAttempt(r.Context(), item.PlanID, item.ID, item.Revision, "cancel", strings.TrimSpace(request.Reason))
	h.writeRunnerAttemptUpdate(w, r, updated, err)
}

func (h *Handler) fleetAttemptPlan(w http.ResponseWriter, r *http.Request) (*model.Operation, bool) {
	plan, ok := h.fleetAttemptPlanForCreate(w, r)
	if !ok {
		return nil, false
	}
	if !h.fleetPlanReadable(r, plan) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_plan_read_forbidden", "workload token may read only its own approved fleet plan")
		return nil, false
	}
	return plan, true
}

func (h *Handler) fleetAttemptPlanForCreate(w http.ResponseWriter, r *http.Request) (*model.Operation, bool) {
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_runner_attempt_store_unavailable", "durable runner attempt storage is unavailable")
		return nil, false
	}
	planID := chi.URLParam(r, "planID")
	if _, err := uuid.Parse(planID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "plan ID must be a UUID")
		return nil, false
	}
	plan, err := h.db.GetOperation(r.Context(), planID)
	if err == pgx.ErrNoRows || (err == nil && plan.Kind != "fleet.capacity-plan") {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_plan_not_found", "fleet capacity plan not found")
		return nil, false
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_plan_read_failed", "failed to read fleet capacity plan")
		return nil, false
	}
	return plan, true
}

// fleetPlanReadable keeps fleet:operate deliberately resource-bound. A
// workload run may inspect the plan Norn dispatched to that exact GitHub run,
// while interactive api:read/api:write operators retain their normal view.
func (h *Handler) fleetPlanReadable(r *http.Request, plan *model.Operation) bool {
	principal, present := AccessPrincipalFromRequest(r)
	if !present || principal.Legacy || principal.Allows(ScopeAdmin) || principal.Allows(ScopeAPIRead) || principal.Allows(ScopeAPIWrite) {
		return true
	}
	if plan == nil || !principal.Allows(ScopeFleetOperate) || principal.CI == nil {
		return false
	}
	if principal.CI.Intent == "apply" {
		binding, err := h.db.GetFleetGitHubDispatch(r.Context(), plan.ID)
		return err == nil && binding.RunID > 0 && principal.CI.RunID == fmt.Sprintf("%d", binding.RunID) && principal.CI.SHA == binding.ApprovedHeadSHA
	}
	if principal.CI.Intent != "recover" {
		return false
	}
	// Once a recovery has atomically created its retry attempt from the original
	// source-dispatch nonce, later GET/mutation calls are limited to that exact
	// recovery runner identity.
	attempts, err := h.db.ListFleetRunnerAttempts(r.Context(), plan.ID)
	if err != nil {
		return false
	}
	for _, attempt := range attempts {
		if attempt.RunnerAttemptID == canonicalRunnerAttemptID(principal.CI) && attempt.WorkflowURL == canonicalWorkflowRunURL(principal.CI.Repository, principal.CI.RunID) {
			return true
		}
	}
	return false
}
func (h *Handler) fleetAttempt(w http.ResponseWriter, r *http.Request) (*fleet.RunnerAttempt, bool) {
	plan, ok := h.fleetAttemptPlan(w, r)
	if !ok {
		return nil, false
	}
	id := chi.URLParam(r, "attemptID")
	if _, err := uuid.Parse(id); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt_id", "runner attempt ID must be a UUID")
		return nil, false
	}
	item, err := h.db.GetFleetRunnerAttempt(r.Context(), plan.ID, id)
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_runner_attempt_not_found", "fleet runner attempt not found")
		return nil, false
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_read_failed", "failed to read runner attempt")
		return nil, false
	}
	if principal, exists := AccessPrincipalFromRequest(r); exists && principal.CI != nil && (item.RunnerAttemptID != canonicalRunnerAttemptID(principal.CI) || item.WorkflowURL != canonicalWorkflowRunURL(principal.CI.Repository, principal.CI.RunID)) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_identity_mismatch", "workload token may mutate only its own runner attempt")
		return nil, false
	}
	return item, true
}
func (h *Handler) writeRunnerAttemptUpdate(w http.ResponseWriter, r *http.Request, item *fleet.RunnerAttempt, err error) {
	if err == nil {
		writeJSON(w, item)
		return
	}
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_stale", "runner attempt was changed, finished, or expired")
		return
	}
	WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_store_failed", "failed to update runner attempt")
}
func requireFleetReadScope(w http.ResponseWriter, r *http.Request) (AccessPrincipal, bool) {
	if principal, exists := AccessPrincipalFromRequest(r); exists && (principal.Allows(ScopeAPIRead) || principal.Allows(ScopeFleetOperate)) {
		return principal, true
	}
	return requireControlScope(w, r, ScopeAPIRead)
}
func requireFleetRunnerPrincipal(w http.ResponseWriter, r *http.Request) (AccessPrincipal, bool) {
	principal, ok := requireControlScope(w, r, ScopeFleetOperate)
	if !ok {
		return AccessPrincipal{}, false
	}
	if principal.CI == nil {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_oidc_required", "runner attempt lifecycle requires a verified GitHub Actions workload identity")
		return AccessPrincipal{}, false
	}
	return principal, true
}
func validateRunnerAttemptCreate(request fleet.RunnerAttemptCreateRequest) error {
	if request.SchemaVersion != fleet.RunnerAttemptSchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", fleet.RunnerAttemptSchemaVersion)
	}
	if !validRunnerAttemptID(request.RunnerAttemptID) || !fleetCommitSHARe.MatchString(request.CommitSHA) || !fleetPlanSHARe.MatchString(request.PlanSHA256) || !fleetPlanSHARe.MatchString(request.DispatchNonce) || !validGitHubRunID(request.SourceDispatchRunID) || !safeFleetWorkflowURL(request.WorkflowURL) || request.HeartbeatTimeoutSeconds < 30 || request.HeartbeatTimeoutSeconds > 900 {
		return fmt.Errorf("runner attempt fields are invalid")
	}
	return nil
}
func validRunnerAttemptID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 200
}
func safeFleetWorkflowURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}
func workflowRunMatches(value, runID string) bool {
	return runID != "" && strings.HasSuffix(strings.TrimRight(value, "/"), "/actions/runs/"+runID)
}
func canonicalWorkflowRunURL(repository, runID string) string {
	return "https://github.com/" + repository + "/actions/runs/" + runID
}
func canonicalRunnerAttemptID(ci *CIIdentity) string {
	if ci == nil {
		return ""
	}
	return "github-actions:" + ci.Repository + ":" + ci.RunID + ":" + ci.RunAttempt
}
func nextFleetReconciliationPhase(current string) string {
	for i, phase := range fleetReconciliationPhases {
		if phase == current && i+1 < len(fleetReconciliationPhases) {
			return fleetReconciliationPhases[i+1]
		}
	}
	return ""
}
func (h *Handler) hasRunnerEvidence(ctx context.Context, item *fleet.RunnerAttempt) bool {
	ops, err := h.db.ListOperations(ctx, store.OperationFilter{Kind: "fleet.reconciliation", Ref: item.PlanID, Status: string(model.OperationSucceeded), Limit: 100})
	if err != nil {
		return false
	}
	for _, op := range ops {
		if op.Payload["phase"] == item.CurrentPhase && op.Payload["attemptId"] == item.ID && op.Payload["commitSha"] == item.CommitSHA && op.Payload["planSha256"] == item.PlanSHA256 {
			return true
		}
	}
	return false
}
func validGitHubRunID(value string) bool {
	return len(value) <= 20 && strings.TrimSpace(value) != "" && strings.TrimLeft(value, "0123456789") == "" && value[0] != '0'
}
func hashFleetDispatchNonce(nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return hex.EncodeToString(sum[:])
}
