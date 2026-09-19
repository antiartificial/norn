package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
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
	if !h.hasMatchingFleetDispatch(r.Context(), plan.ID, request, principal.CI) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_dispatch_mismatch", "runner attempt is not bound to Norn's approved protected apply dispatch")
		return
	}
	existing, err := h.db.ListFleetRunnerAttempts(r.Context(), plan.ID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_read_failed", "failed to read existing runner attempts")
		return
	}
	for _, item := range existing {
		if item.RunnerAttemptID == request.RunnerAttemptID {
			if item.CommitSHA != request.CommitSHA || item.PlanSHA256 != request.PlanSHA256 || item.WorkflowURL != request.WorkflowURL {
				WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_binding_mismatch", "runner attempt ID is already bound to different reviewed input")
				return
			}
			writeJSON(w, item)
			return
		}
	}
	if len(existing) > 0 && existing[0].CommitSHA != request.CommitSHA {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_binding_mismatch", "plan already has a runner attempt bound to a different commit")
		return
	}
	if len(existing) > 0 && existing[0].PlanSHA256 != request.PlanSHA256 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_binding_mismatch", "plan already has a runner attempt bound to a different plan digest")
		return
	}
	retryOf := ""
	phase := fleetRunnerAttemptInitialPhase(plan)
	if len(existing) > 0 && existing[0].Status == "succeeded" {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_complete", "fleet plan already has a successful runner attempt")
		return
	}
	if len(existing) > 0 && (existing[0].Status == "queued" || existing[0].Status == "running") {
		if !request.Resume {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_active", "another runner attempt is still live")
			return
		}
		if _, err := h.db.UpdateFleetRunnerAttempt(r.Context(), plan.ID, existing[0].ID, existing[0].Revision, "cancel", "superseded by verified protected recovery runner"); err != nil {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_stale", "the live runner attempt changed before recovery could supersede it")
			return
		}
		retryOf = existing[0].ID
		phase = fleetRunnerAttemptResumePhase(plan, existing[0])
	} else if len(existing) > 0 && (existing[0].Status == "failed" || existing[0].Status == "canceled" || existing[0].Status == "abandoned") {
		retryOf = existing[0].ID
		phase = fleetRunnerAttemptResumePhase(plan, existing[0])
	}
	item, err := h.db.CreateFleetRunnerAttempt(r.Context(), fleet.RunnerAttempt{ID: uuid.NewString(), PlanID: plan.ID, RunnerAttemptID: request.RunnerAttemptID, CommitSHA: request.CommitSHA, PlanSHA256: request.PlanSHA256, WorkflowURL: request.WorkflowURL, RetryOf: retryOf, CurrentPhase: phase, HeartbeatTimeoutSeconds: request.HeartbeatTimeoutSeconds})
	if err != nil {
		if pgErr, isConflict := err.(*pgconn.PgError); isConflict && pgErr.Code == "23505" {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_active", "another runner attempt is still live")
			return
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_store_failed", "failed to create runner attempt")
		return
	}
	w.Header().Set("Location", "/api/v1/fleet/plans/"+plan.ID+"/attempts/"+item.ID)
	writeJSONStatus(w, http.StatusCreated, item)
}

func fleetRunnerAttemptInitialPhase(plan *model.Operation) string {
	if fleetPlanRequiresDrain(plan) {
		return "prechange_verified"
	}
	return "provider_applying"
}

func fleetRunnerAttemptResumePhase(plan *model.Operation, previous fleet.RunnerAttempt) string {
	// Destructive recovery must never reuse a pre-change proof from another
	// lease. A crash during provider apply can leave partially changed
	// infrastructure, so the new protected runner starts by creating its own
	// prechange_verified receipt before it is allowed to continue apply.
	if fleetPlanRequiresDrain(plan) {
		return "prechange_verified"
	}
	return previous.CurrentPhase
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
	return "github:" + ci.Repository + ":" + ci.RunID + ":" + ci.RunAttempt
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
func (h *Handler) hasMatchingFleetDispatch(ctx context.Context, planID string, request fleet.RunnerAttemptCreateRequest, ci *CIIdentity) bool {
	binding, err := h.db.GetFleetGitHubDispatch(ctx, planID)
	if err != nil || binding.PlanSHA256 != request.PlanSHA256 || binding.ApprovedHeadSHA != request.CommitSHA || binding.DispatchNonceSHA256 != hashFleetDispatchNonce(request.DispatchNonce) || binding.RunID <= 0 || request.SourceDispatchRunID != fmt.Sprintf("%d", binding.RunID) {
		return false
	}
	// An apply token is minted in the dispatched run and must match its exact
	// source commit. A recovery token is minted in a new run; it instead proves
	// continuity through the original dispatch run ID and one-time nonce.
	if ci.Intent == "apply" {
		return ci.RunID == request.SourceDispatchRunID && ci.SHA == binding.ApprovedHeadSHA
	}
	return ci.Intent == "recover"
}

func hashFleetDispatchNonce(nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return hex.EncodeToString(sum[:])
}
