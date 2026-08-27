package handler

import (
	"errors"
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

const (
	defaultFleetHeartbeatTimeout = 120
	minFleetHeartbeatTimeout     = 30
	maxFleetHeartbeatTimeout     = 900
)

func (h *Handler) ListFleetRunnerAttempts(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	plan, ok := h.requireFleetPlan(w, r, chi.URLParam(r, "planID"))
	if !ok {
		return
	}
	attempts, err := h.db.ListFleetRunnerAttempts(r.Context(), plan.ID, 50)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_read_failed", "failed to read fleet runner attempts")
		return
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{
		"schemaVersion": model.FleetRunnerAttemptSchemaVersion,
		"planId":        plan.ID, "attempts": attempts, "count": len(attempts), "serverTime": time.Now().UTC(),
	})
}

func (h *Handler) StartFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireFleetOperateScope(w, r)
	if !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	plan, ok := h.requireFleetPlan(w, r, chi.URLParam(r, "planID"))
	if !ok {
		return
	}
	var request fleet.RunnerAttemptStartRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt", err.Error())
		return
	}
	if err := validateFleetRunnerStart(request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt", err.Error())
		return
	}
	request.RunnerAttemptID = strings.TrimSpace(request.RunnerAttemptID)
	request.WorkflowURL = strings.TrimSpace(request.WorkflowURL)
	existing, err := h.db.ListFleetRunnerAttempts(r.Context(), plan.ID, 100)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_read_failed", "failed to inspect prior runner attempts")
		return
	}
	for index := range existing {
		attempt := &existing[index]
		if attempt.RunnerAttemptID != request.RunnerAttemptID {
			continue
		}
		if attempt.CommitSHA != request.CommitSHA || attempt.PlanSHA256 != request.PlanSHA256 ||
			attempt.WorkflowURL != strings.TrimSpace(request.WorkflowURL) || attempt.HeartbeatTimeoutSeconds != normalizedFleetHeartbeatTimeout(request.HeartbeatTimeoutSeconds) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_binding_conflict", "runnerAttemptId is already bound to different reviewed input")
			return
		}
		writeJSON(w, attempt)
		return
	}
	if len(existing) > 0 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_retry_required", "this plan already has runner history; retry the latest failed, canceled, or abandoned attempt")
		return
	}
	checkpoints, err := h.db.ListOperations(r.Context(), store.OperationFilter{Kind: "fleet.reconciliation", Ref: plan.ID, Limit: 100})
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_reconciliation_read_failed", "failed to inspect reconciliation binding")
		return
	}
	if err := validateFleetRunnerBinding(checkpoints, request.CommitSHA, request.PlanSHA256); err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_binding_conflict", err.Error())
		return
	}
	currentPhase, complete := firstIncompleteFleetRunnerPhase(plan, checkpoints)
	if complete {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_plan_already_complete", "all required reconciliation phases already have successful evidence")
		return
	}
	now := time.Now().UTC()
	attempt := &model.FleetRunnerAttempt{
		ID: uuid.NewString(), PlanID: plan.ID, RunnerAttemptID: strings.TrimSpace(request.RunnerAttemptID),
		Status: model.FleetRunnerAttemptRunning, CurrentPhase: currentPhase,
		CommitSHA: request.CommitSHA, PlanSHA256: request.PlanSHA256, WorkflowURL: strings.TrimSpace(request.WorkflowURL),
		PrincipalSubject: principalIdentity(principal), HeartbeatTimeoutSeconds: normalizedFleetHeartbeatTimeout(request.HeartbeatTimeoutSeconds),
		Revision: 1, StartedAt: now, HeartbeatAt: now, UpdatedAt: now,
	}
	if err := h.db.CreateFleetRunnerAttempt(r.Context(), attempt); err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
			if concurrent, lookupErr := h.db.ListFleetRunnerAttempts(r.Context(), plan.ID, 100); lookupErr == nil {
				for index := range concurrent {
					candidate := &concurrent[index]
					if candidate.RunnerAttemptID == request.RunnerAttemptID && candidate.CommitSHA == request.CommitSHA && candidate.PlanSHA256 == request.PlanSHA256 && candidate.WorkflowURL == strings.TrimSpace(request.WorkflowURL) && candidate.HeartbeatTimeoutSeconds == normalizedFleetHeartbeatTimeout(request.HeartbeatTimeoutSeconds) {
						writeJSON(w, candidate)
						return
					}
				}
			}
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_exists", "a runner attempt with this identity already exists")
			return
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_store_failed", "failed to durably start runner attempt")
		return
	}
	w.Header().Set("Location", "/api/v1/fleet/plans/"+plan.ID+"/attempts/"+attempt.ID)
	writeJSONStatus(w, http.StatusCreated, attempt)
}

func (h *Handler) GetFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	attempt, ok := h.requireFleetAttempt(w, r)
	if ok {
		writeJSON(w, attempt)
	}
}

func (h *Handler) HeartbeatFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireFleetOperateScope(w, r); !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	attempt, ok := h.requireFleetAttempt(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerHeartbeatRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_heartbeat", err.Error())
		return
	}
	if request.SchemaVersion != model.FleetRunnerAttemptSchemaVersion || request.Sequence <= 0 || request.Revision <= 0 || request.Phase != attempt.CurrentPhase || len(request.Message) > 500 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_heartbeat", "schemaVersion, current phase, positive sequence/revision, and a message of at most 500 characters are required")
		return
	}
	if request.Sequence == attempt.HeartbeatSequence {
		if request.Revision != attempt.Revision-1 {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_revision_conflict", "heartbeat replay does not match the revision that recorded it")
			return
		}
		writeJSON(w, attempt)
		return
	}
	if request.Sequence < attempt.HeartbeatSequence {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_heartbeat_out_of_order", "heartbeat sequence is older than the durable runner state")
		return
	}
	updated, err := h.db.HeartbeatFleetRunnerAttempt(r.Context(), attempt.ID, request.Phase, request.Sequence, request.Revision, strings.TrimSpace(request.Message))
	if errors.Is(err, store.ErrFleetRunnerAttemptConflict) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_revision_conflict", "runner attempt changed; refresh it before heartbeating")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_update_failed", "failed to record runner heartbeat")
		return
	}
	writeJSON(w, updated)
}

func (h *Handler) AdvanceFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireFleetOperateScope(w, r); !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	attempt, ok := h.requireFleetAttempt(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerAdvanceRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_advance", err.Error())
		return
	}
	if request.SchemaVersion != model.FleetRunnerAttemptSchemaVersion || request.Revision <= 0 || request.ExpectedPhase == "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_advance", "schemaVersion, expectedPhase, and positive revision are required")
		return
	}
	if attempt.Status == model.FleetRunnerAttemptSucceeded && request.ExpectedPhase == "complete" && request.Revision == attempt.Revision-1 {
		writeJSON(w, attempt)
		return
	}
	if attempt.CurrentPhase != request.ExpectedPhase {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_phase_conflict", "expectedPhase does not match the durable current phase")
		return
	}
	plan, err := h.db.GetOperation(r.Context(), attempt.PlanID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_plan_read_failed", "failed to read runner attempt plan")
		return
	}
	next, terminal, err := nextFleetRunnerPhase(attempt.CurrentPhase, fleetPlanRequiresDrain(plan))
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_phase_conflict", err.Error())
		return
	}
	updated, err := h.db.AdvanceFleetRunnerAttempt(r.Context(), attempt.ID, attempt.CurrentPhase, next, request.Revision, terminal)
	if errors.Is(err, store.ErrFleetRunnerAttemptConflict) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_advance_unproven", "record successful reconciliation evidence for the current attempt and phase, then refresh before advancing")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_update_failed", "failed to advance runner attempt")
		return
	}
	writeJSON(w, updated)
}

func (h *Handler) RetryFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireFleetOperateScope(w, r)
	if !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	previous, ok := h.requireFleetAttempt(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerRetryRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_retry", err.Error())
		return
	}
	if request.SchemaVersion != model.FleetRunnerAttemptSchemaVersion || request.Revision <= 0 || strings.TrimSpace(request.RunnerAttemptID) == "" || len(request.RunnerAttemptID) > 200 || strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 1000 || !validOptionalFleetWorkflowURL(request.WorkflowURL) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_retry", "schemaVersion, revision, a unique runnerAttemptId, a bounded reason, and an optional credential-free HTTPS workflowUrl are required")
		return
	}
	request.RunnerAttemptID = strings.TrimSpace(request.RunnerAttemptID)
	request.WorkflowURL = strings.TrimSpace(request.WorkflowURL)
	request.Reason = strings.TrimSpace(request.Reason)
	existing, err := h.db.ListFleetRunnerAttempts(r.Context(), previous.PlanID, 100)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_read_failed", "failed to inspect prior retries")
		return
	}
	for index := range existing {
		if existing[index].RunnerAttemptID == strings.TrimSpace(request.RunnerAttemptID) {
			workflowURL := strings.TrimSpace(request.WorkflowURL)
			if workflowURL == "" {
				workflowURL = previous.WorkflowURL
			}
			reason, _ := existing[index].Metadata["retryReason"].(string)
			if existing[index].RetryOf != previous.ID || existing[index].WorkflowURL != workflowURL || reason != strings.TrimSpace(request.Reason) || previous.Revision != request.Revision {
				WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_binding_conflict", "runnerAttemptId is already bound to a different retry request")
				return
			}
			writeJSON(w, &existing[index])
			return
		}
	}
	now := time.Now().UTC()
	workflowURL := strings.TrimSpace(request.WorkflowURL)
	if workflowURL == "" {
		workflowURL = previous.WorkflowURL
	}
	replacement := &model.FleetRunnerAttempt{
		ID: uuid.NewString(), PlanID: previous.PlanID, RunnerAttemptID: strings.TrimSpace(request.RunnerAttemptID),
		Status: model.FleetRunnerAttemptRunning, CurrentPhase: previous.CurrentPhase,
		CommitSHA: previous.CommitSHA, PlanSHA256: previous.PlanSHA256, WorkflowURL: workflowURL,
		PrincipalSubject: principalIdentity(principal), RetryOf: previous.ID,
		HeartbeatTimeoutSeconds: previous.HeartbeatTimeoutSeconds, Revision: 1,
		StartedAt: now, HeartbeatAt: now, UpdatedAt: now,
		Metadata: map[string]interface{}{"retryReason": strings.TrimSpace(request.Reason)},
	}
	previous.Revision = request.Revision
	if err := h.db.RetryFleetRunnerAttempt(r.Context(), previous, replacement); errors.Is(err, store.ErrFleetRunnerAttemptConflict) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_retry_not_allowed", "only the current revision of a failed, canceled, or abandoned attempt can be retried")
		return
	} else if pgErr, duplicate := err.(*pgconn.PgError); duplicate && pgErr.Code == "23505" {
		if concurrent, lookupErr := h.db.ListFleetRunnerAttempts(r.Context(), previous.PlanID, 100); lookupErr == nil {
			for index := range concurrent {
				candidate := &concurrent[index]
				reason, _ := candidate.Metadata["retryReason"].(string)
				if candidate.RunnerAttemptID == replacement.RunnerAttemptID && candidate.RetryOf == previous.ID && candidate.WorkflowURL == workflowURL && reason == strings.TrimSpace(request.Reason) {
					writeJSON(w, candidate)
					return
				}
			}
		}
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_exists", "a runner attempt with this identity already exists")
		return
	} else if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_store_failed", "failed to durably create retry attempt")
		return
	}
	w.Header().Set("Location", "/api/v1/fleet/plans/"+previous.PlanID+"/attempts/"+replacement.ID)
	writeJSONStatus(w, http.StatusCreated, replacement)
}

func (h *Handler) CancelFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireFleetOperateScope(w, r); !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	attempt, ok := h.requireFleetAttempt(w, r)
	if !ok {
		return
	}
	var request fleet.RunnerCancelRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_cancel", err.Error())
		return
	}
	if request.SchemaVersion != model.FleetRunnerAttemptSchemaVersion || request.Revision <= 0 || strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 1000 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_cancel", "schemaVersion, positive revision, and a bounded reason are required")
		return
	}
	if attempt.Status == model.FleetRunnerAttemptCanceled && request.Revision == attempt.Revision-1 && strings.TrimSpace(request.Reason) == attempt.LastError {
		writeJSON(w, attempt)
		return
	}
	updated, err := h.db.CancelFleetRunnerAttempt(r.Context(), attempt.ID, request.Revision, strings.TrimSpace(request.Reason))
	if errors.Is(err, store.ErrFleetRunnerAttemptConflict) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_cancel_not_allowed", "only the current revision of a running attempt can be canceled")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_update_failed", "failed to cancel runner attempt")
		return
	}
	writeJSON(w, updated)
}

func (h *Handler) requireFleetPlan(w http.ResponseWriter, r *http.Request, planID string) (*model.Operation, bool) {
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_runner_attempt_store_unavailable", "durable runner storage is unavailable")
		return nil, false
	}
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

func (h *Handler) requireFleetAttempt(w http.ResponseWriter, r *http.Request) (*model.FleetRunnerAttempt, bool) {
	plan, ok := h.requireFleetPlan(w, r, chi.URLParam(r, "planID"))
	if !ok {
		return nil, false
	}
	attemptID := chi.URLParam(r, "attemptID")
	if _, err := uuid.Parse(attemptID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_attempt_id", "attempt ID must be a UUID")
		return nil, false
	}
	attempt, err := h.db.GetFleetRunnerAttempt(r.Context(), attemptID)
	if err == pgx.ErrNoRows || (err == nil && attempt.PlanID != plan.ID) {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_runner_attempt_not_found", "fleet runner attempt not found for this plan")
		return nil, false
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_read_failed", "failed to read fleet runner attempt")
		return nil, false
	}
	return attempt, true
}

func validateFleetRunnerStart(request fleet.RunnerAttemptStartRequest) error {
	if request.SchemaVersion != model.FleetRunnerAttemptSchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", model.FleetRunnerAttemptSchemaVersion)
	}
	if strings.TrimSpace(request.RunnerAttemptID) == "" || len(request.RunnerAttemptID) > 200 {
		return fmt.Errorf("runnerAttemptId is required and must not exceed 200 characters")
	}
	if !fleetCommitSHARe.MatchString(request.CommitSHA) || !fleetPlanSHARe.MatchString(request.PlanSHA256) {
		return fmt.Errorf("commitSha and planSha256 must be lowercase Git/SHA-256 values")
	}
	if request.HeartbeatTimeoutSeconds != 0 && (request.HeartbeatTimeoutSeconds < minFleetHeartbeatTimeout || request.HeartbeatTimeoutSeconds > maxFleetHeartbeatTimeout) {
		return fmt.Errorf("heartbeatTimeoutSeconds must be between %d and %d", minFleetHeartbeatTimeout, maxFleetHeartbeatTimeout)
	}
	if !validOptionalFleetWorkflowURL(request.WorkflowURL) {
		return fmt.Errorf("workflowUrl must be an absolute credential-free HTTPS URL")
	}
	return nil
}

func validateFleetRunnerBinding(checkpoints []model.Operation, commitSHA, planSHA string) error {
	for _, checkpoint := range checkpoints {
		commit, _ := checkpoint.Payload["commitSha"].(string)
		plan, _ := checkpoint.Payload["planSha256"].(string)
		if commit != commitSHA || plan != planSHA {
			return fmt.Errorf("runner binding differs from existing reconciliation evidence")
		}
	}
	return nil
}

func validOptionalFleetWorkflowURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func normalizedFleetHeartbeatTimeout(value int) int {
	if value == 0 {
		return defaultFleetHeartbeatTimeout
	}
	return value
}

func principalIdentity(principal AccessPrincipal) string {
	if value := strings.TrimSpace(principal.Subject); value != "" {
		return value
	}
	return strings.TrimSpace(principal.DeviceID)
}

func nextFleetRunnerPhase(current string, requiresDrain bool) (string, bool, error) {
	phases := fleetRunnerPhases(requiresDrain)
	for index, phase := range phases {
		if phase != current {
			continue
		}
		if index == len(phases)-1 {
			return current, true, nil
		}
		return phases[index+1], false, nil
	}
	return "", false, fmt.Errorf("current phase is not supported")
}

func fleetRunnerPhases(requiresDrain bool) []string {
	if requiresDrain {
		return fleetReconciliationPhases
	}
	return []string{"infrastructure_applied", "inventory_generated", "nodes_configured", "nodes_enrolled", "readiness_verified", "complete"}
}

// firstIncompleteFleetRunnerPhase makes workflow restarts resumable. Durable,
// append-only checkpoints remain the authority; a new runner starts at the
// first phase for which the reviewed plan has no successful proof.
func firstIncompleteFleetRunnerPhase(plan *model.Operation, checkpoints []model.Operation) (string, bool) {
	succeeded := make(map[string]bool, len(checkpoints))
	for _, checkpoint := range checkpoints {
		if checkpoint.Status != model.OperationSucceeded {
			continue
		}
		phase, _ := checkpoint.Payload["phase"].(string)
		succeeded[phase] = true
	}
	for _, phase := range fleetRunnerPhases(fleetPlanRequiresDrain(plan)) {
		if !succeeded[phase] {
			return phase, false
		}
	}
	return "complete", true
}
