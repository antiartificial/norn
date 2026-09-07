package handler

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"norn/v2/api/config"
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
	for index := range attempts {
		h.attachFleetRunnerTiming(&attempts[index])
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
	classification, err := fleetTimingClassificationFromRequest(request)
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_runner_timing_classification", err.Error())
		return
	}
	request.RunnerAttemptID = strings.TrimSpace(request.RunnerAttemptID)
	request.WorkflowURL = strings.TrimSpace(request.WorkflowURL)
	binding, err := h.db.GetFleetGitHubDispatch(r.Context(), plan.ID)
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_dispatch_required", "a protected Fleet dispatch binding is required to start a runner")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_dispatch_read_failed", "failed to read the protected Fleet dispatch binding")
		return
	}
	if !request.Resume && binding.RunID == 0 {
		binding, err = h.awaitFleetGitHubDispatchRun(r.Context(), plan.ID, request.SourceDispatchRunID)
		if err != nil {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_dispatch_pending", "the protected apply dispatch has not durably recorded its exact run; retry shortly")
			return
		}
	}
	if err := validateFleetRunnerDispatchBinding(h.cfg, principal, plan.ID, request, *binding); err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_dispatch_binding_mismatch", err.Error())
		return
	}
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
		if classification.OperationClass == "" {
			// A timeout/retry of the same protected workflow may omit optional
			// timing fields; preserve its already-bound classification rather
			// than silently downgrading the attempt to an unknown estimate.
			classification = fleetTimingClassificationForAttempt(attempt)
		}
		if !fleetRunnerAttemptMatchesStartRequest(attempt, request, classification) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_binding_conflict", "runnerAttemptId is already bound to different reviewed input")
			return
		}
		h.attachFleetRunnerTiming(attempt)
		writeJSON(w, attempt)
		return
	}
	if request.Resume {
		if len(existing) == 0 {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_retry_required", "a durable source runner attempt is required before recovery")
			return
		}
		previous := &existing[0]
		if previous.SourceDispatchRunID != request.SourceDispatchRunID || previous.CommitSHA != request.CommitSHA || previous.PlanSHA256 != request.PlanSHA256 {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_binding_conflict", "recovery does not match the durable source runner attempt")
			return
		}
		previousClassification := fleetTimingClassificationForAttempt(previous)
		if classification.OperationClass == "" {
			classification = previousClassification
		} else if !fleetTimingClassificationEqual(previousClassification, classification) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_binding_conflict", "recovery timing classification differs from the durable source attempt")
			return
		}
		now := time.Now().UTC()
		recovery := &model.FleetRunnerAttempt{
			ID: uuid.NewString(), PlanID: previous.PlanID, RunnerAttemptID: request.RunnerAttemptID,
			Status: model.FleetRunnerAttemptRunning, CurrentPhase: previous.CurrentPhase,
			CommitSHA: previous.CommitSHA, PlanSHA256: previous.PlanSHA256, WorkflowURL: request.WorkflowURL,
			SourceDispatchRunID: previous.SourceDispatchRunID, Recovery: true,
			PrincipalSubject: principalIdentity(principal), RetryOf: previous.ID,
			HeartbeatTimeoutSeconds: previous.HeartbeatTimeoutSeconds, Revision: 1,
			StartedAt: now, HeartbeatAt: now, UpdatedAt: now,
			Metadata: fleetRunnerTimingMetadata(classification),
		}
		recovery.Metadata["recoveryReason"] = "verified GitHub Actions recovery workflow"
		if err := h.db.RecoverFleetRunnerAttempt(r.Context(), previous, recovery); errors.Is(err, store.ErrFleetRunnerAttemptConflict) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_resume_conflict", "the durable source runner attempt changed before recovery could create its retry lineage")
			return
		} else if pgErr, duplicate := err.(*pgconn.PgError); duplicate && pgErr.Code == "23505" {
			if concurrent, lookupErr := h.db.ListFleetRunnerAttempts(r.Context(), previous.PlanID, 100); lookupErr == nil {
				for index := range concurrent {
					candidate := &concurrent[index]
					if fleetRunnerRecoveryCandidateMatchesStartRequest(candidate, previous.ID, request, classification) {
						h.attachFleetRunnerTiming(candidate)
						writeJSON(w, candidate)
						return
					}
				}
			}
			WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_attempt_exists", "a runner attempt with this recovery identity already exists")
			return
		} else if err != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_store_failed", "failed to durably create recovery retry attempt")
			return
		}
		w.Header().Set("Location", "/api/v1/fleet/plans/"+previous.PlanID+"/attempts/"+recovery.ID)
		h.attachFleetRunnerTiming(recovery)
		writeJSONStatus(w, http.StatusCreated, recovery)
		return
	}
	if len(existing) > 0 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_retry_required", "this plan already has runner history; retry the latest failed, canceled, or abandoned attempt")
		return
	}
	// A first protected attempt cannot inherit completion from compatibility-era
	// reconciliation records, which have no bound durable attempt identity.
	currentPhase, complete := firstIncompleteFleetRunnerPhase(plan, nil, "")
	if complete {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_runner_plan_already_complete", "all required reconciliation phases already have successful evidence")
		return
	}
	now := time.Now().UTC()
	attempt := &model.FleetRunnerAttempt{
		ID: uuid.NewString(), PlanID: plan.ID, RunnerAttemptID: strings.TrimSpace(request.RunnerAttemptID),
		Status: model.FleetRunnerAttemptRunning, CurrentPhase: currentPhase,
		CommitSHA: request.CommitSHA, PlanSHA256: request.PlanSHA256, WorkflowURL: strings.TrimSpace(request.WorkflowURL),
		SourceDispatchRunID: request.SourceDispatchRunID, Recovery: request.Resume,
		PrincipalSubject: principalIdentity(principal), HeartbeatTimeoutSeconds: normalizedFleetHeartbeatTimeout(request.HeartbeatTimeoutSeconds),
		Revision: 1, StartedAt: now, PhaseStartedAt: now, HeartbeatAt: now, UpdatedAt: now,
		Metadata: fleetRunnerTimingMetadata(classification),
	}
	if err := h.db.CreateFleetRunnerAttempt(r.Context(), attempt); err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
			if concurrent, lookupErr := h.db.ListFleetRunnerAttempts(r.Context(), plan.ID, 100); lookupErr == nil {
				for index := range concurrent {
					candidate := &concurrent[index]
					if fleetRunnerAttemptMatchesStartRequest(candidate, request, classification) {
						h.attachFleetRunnerTiming(candidate)
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
	h.attachFleetRunnerTiming(attempt)
	writeJSONStatus(w, http.StatusCreated, attempt)
}

func (h *Handler) GetFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	principal, authenticated := AccessPrincipalFromRequest(r)
	if !authenticated {
		WriteControlProblem(w, r, http.StatusUnauthorized, "authenticated_principal_required", "an explicitly authenticated control principal is required")
		return
	}
	preventSensitiveResponseCaching(w)
	attempt, ok := h.requireFleetAttempt(w, r)
	if !ok {
		return
	}
	if !fleetRunnerCanReadAttempt(principal, attempt) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_binding_conflict", "only the bound GitHub Actions runner may read this attempt")
		return
	}
	h.attachFleetRunnerTiming(attempt)
	writeJSON(w, attempt)
}

func (h *Handler) HeartbeatFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireFleetOperateScope(w, r)
	if !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	attempt, ok := h.requireFleetAttempt(w, r)
	if !ok {
		return
	}
	if !fleetRunnerPrincipalOwnsAttempt(principal, attempt) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_binding_conflict", "only the bound GitHub Actions runner may heartbeat this attempt")
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
		h.attachFleetRunnerTiming(attempt)
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
	h.attachFleetRunnerTiming(updated)
	writeJSON(w, updated)
}

func (h *Handler) AdvanceFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireFleetOperateScope(w, r)
	if !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	attempt, ok := h.requireFleetAttempt(w, r)
	if !ok {
		return
	}
	if !fleetRunnerPrincipalOwnsAttempt(principal, attempt) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_binding_conflict", "only the bound GitHub Actions runner may advance this attempt")
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
		h.attachFleetRunnerTiming(attempt)
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
	h.attachFleetRunnerTiming(updated)
	writeJSON(w, updated)
}

func (h *Handler) CancelFleetRunnerAttempt(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireFleetOperateScope(w, r)
	if !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	attempt, ok := h.requireFleetAttempt(w, r)
	if !ok {
		return
	}
	// This endpoint is runner self-cancel only. A human/operator cancellation
	// requires a separate audited administrative operation rather than the
	// protected runner mutation path.
	if !fleetRunnerPrincipalOwnsAttempt(principal, attempt) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_runner_attempt_binding_conflict", "only the bound GitHub Actions runner may cancel this attempt")
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
		h.attachFleetRunnerTiming(attempt)
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
	h.attachFleetRunnerTiming(updated)
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
	if !fleetDispatchNonceRe.MatchString(request.DispatchNonce) || request.SourceDispatchRunID <= 0 {
		return fmt.Errorf("dispatchNonce and a positive sourceDispatchRunId are required")
	}
	if request.HeartbeatTimeoutSeconds != 0 && (request.HeartbeatTimeoutSeconds < minFleetHeartbeatTimeout || request.HeartbeatTimeoutSeconds > maxFleetHeartbeatTimeout) {
		return fmt.Errorf("heartbeatTimeoutSeconds must be between %d and %d", minFleetHeartbeatTimeout, maxFleetHeartbeatTimeout)
	}
	if !validOptionalFleetWorkflowURL(request.WorkflowURL) || strings.TrimSpace(request.WorkflowURL) == "" {
		return fmt.Errorf("workflowUrl must be an absolute credential-free HTTPS URL")
	}
	return nil
}

func fleetTimingClassificationFromRequest(request fleet.RunnerAttemptStartRequest) (model.FleetTimingClassification, error) {
	classification := model.FleetTimingClassification{OperationClass: strings.TrimSpace(request.OperationClass), CreatedNodeCount: request.CreatedNodeCount}
	if classification.OperationClass == "" && classification.CreatedNodeCount == 0 {
		return classification, nil
	}
	switch classification.OperationClass {
	case model.FleetTimingColdStart:
		if classification.CreatedNodeCount == 5 {
			return classification, nil
		}
	case model.FleetTimingUnclassified:
		if classification.CreatedNodeCount >= 0 && classification.CreatedNodeCount <= 1000 {
			return classification, nil
		}
	}
	return model.FleetTimingClassification{}, fmt.Errorf("operationClass must be cold_start with createdNodeCount exactly 5, unknown with createdNodeCount 0 through 1000, or both fields must be omitted")
}

func fleetRunnerTimingMetadata(classification model.FleetTimingClassification) map[string]interface{} {
	return map[string]interface{}{"timingClassification": map[string]interface{}{
		"operationClass": classification.OperationClass, "createdNodeCount": classification.CreatedNodeCount,
	}}
}

func fleetTimingClassificationForAttempt(attempt *model.FleetRunnerAttempt) model.FleetTimingClassification {
	if attempt == nil || attempt.Metadata == nil {
		return model.FleetTimingClassification{}
	}
	raw, _ := attempt.Metadata["timingClassification"].(map[string]interface{})
	classification, _ := raw["operationClass"].(string)
	return model.FleetTimingClassification{OperationClass: classification, CreatedNodeCount: int(numberAsFloat64(raw["createdNodeCount"]))}
}

func fleetTimingClassificationEqual(left, right model.FleetTimingClassification) bool {
	return left.OperationClass == right.OperationClass && left.CreatedNodeCount == right.CreatedNodeCount
}

// fleetRunnerAttemptMatchesStartRequest is the one replay predicate for both
// ordinary idempotency and a concurrent insert's unique-violation recovery.
// Every item here is an immutable value durably bound to the protected runner
// request; candidate lookup must not weaken that binding.
func fleetRunnerAttemptMatchesStartRequest(attempt *model.FleetRunnerAttempt, request fleet.RunnerAttemptStartRequest, classification model.FleetTimingClassification) bool {
	if attempt == nil {
		return false
	}
	return attempt.RunnerAttemptID == strings.TrimSpace(request.RunnerAttemptID) &&
		attempt.CommitSHA == request.CommitSHA &&
		attempt.PlanSHA256 == request.PlanSHA256 &&
		attempt.SourceDispatchRunID == request.SourceDispatchRunID &&
		attempt.Recovery == request.Resume &&
		attempt.WorkflowURL == strings.TrimSpace(request.WorkflowURL) &&
		attempt.HeartbeatTimeoutSeconds == normalizedFleetHeartbeatTimeout(request.HeartbeatTimeoutSeconds) &&
		fleetTimingClassificationEqual(fleetTimingClassificationForAttempt(attempt), classification)
}

func fleetRunnerRecoveryCandidateMatchesStartRequest(candidate *model.FleetRunnerAttempt, retryOf string, request fleet.RunnerAttemptStartRequest, classification model.FleetTimingClassification) bool {
	return fleetRunnerAttemptMatchesStartRequest(candidate, request, classification) && candidate.RetryOf == retryOf
}

// attachFleetRunnerTiming is presentation-only. The object is recomputed for
// every response and is deliberately not included in revision or transition
// checks; heartbeats remain liveness-only evidence.
func (h *Handler) attachFleetRunnerTiming(attempt *model.FleetRunnerAttempt) {
	if attempt == nil {
		return
	}
	attempt.Timing = model.NewFleetRunnerTiming(attempt, fleetTimingClassificationForAttempt(attempt), time.Now().UTC())
}

var fleetDispatchNonceRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func fleetDispatchNonceHash(nonce string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(nonce)))
	return fmt.Sprintf("%x", sum[:])
}

func validateFleetRunnerDispatchBinding(cfg *config.Config, principal AccessPrincipal, planID string, request fleet.RunnerAttemptStartRequest, binding store.FleetGitHubDispatch) error {
	ci := principal.CI
	if cfg == nil || ci == nil || ci.Provider != "github-actions" || !principal.Allows(ScopeFleetOperate) {
		return fmt.Errorf("fleet runner start requires a bound GitHub Actions fleet:operate token")
	}
	if strings.TrimSpace(binding.PlanID) == "" || binding.PlanID != planID {
		return fmt.Errorf("sourceDispatchRunId does not match the protected dispatch")
	}
	if len(binding.DispatchNonceSHA256) != 64 || subtle.ConstantTimeCompare([]byte(fleetDispatchNonceHash(request.DispatchNonce)), []byte(binding.DispatchNonceSHA256)) != 1 {
		return fmt.Errorf("dispatchNonce does not match the protected dispatch")
	}
	if request.PlanSHA256 != binding.PlanSHA256 || request.CommitSHA != binding.ApprovedHeadSHA || (!request.Resume && ci.SHA != binding.ApprovedHeadSHA) {
		return fmt.Errorf("runner commit or plan does not match the protected dispatch")
	}
	if ci.Repository != fleetRepositoryFromBinding(cfg.GitHubActionsFleetAllowedRepository) || ci.Environment != fleetControlEnvironment(binding.FleetEnvironment) || !ci.RefProtected || ci.RunID == "" || ci.RunAttempt == "" {
		return fmt.Errorf("runner GitHub identity does not match the protected Fleet environment")
	}
	if request.WorkflowURL != canonicalFleetWorkflowURL(ci) || request.RunnerAttemptID != canonicalFleetRunnerAttemptID(ci) {
		return fmt.Errorf("runner workflow identity is not canonical")
	}
	currentRun, err := strconv.ParseInt(ci.RunID, 10, 64)
	if err != nil || currentRun <= 0 {
		return fmt.Errorf("runner GitHub run identity is invalid")
	}
	if request.Resume {
		if ci.Intent != "recover" || binding.RunID <= 0 || request.SourceDispatchRunID != binding.RunID || currentRun == binding.RunID {
			return fmt.Errorf("recovery must resume a bound source dispatch from a distinct recover workflow run")
		}
		return nil
	}
	if ci.Intent != "apply" || currentRun != request.SourceDispatchRunID || binding.RunID != request.SourceDispatchRunID {
		return fmt.Errorf("apply runner does not match the protected source dispatch")
	}
	return nil
}

func (h *Handler) awaitFleetGitHubDispatchRun(ctx context.Context, planID string, runID int64) (*store.FleetGitHubDispatch, error) {
	for attempt := 0; attempt < 3; attempt++ {
		binding, err := h.db.GetFleetGitHubDispatch(ctx, planID)
		if err != nil {
			return nil, err
		}
		if binding.RunID == runID {
			return binding, nil
		}
		if binding.RunID != 0 {
			return nil, fmt.Errorf("protected dispatch is bound to a different run")
		}
		if attempt < 2 {
			time.Sleep(25 * time.Millisecond)
		}
	}
	return nil, fmt.Errorf("protected dispatch run is pending")
}

func fleetRepositoryFromBinding(value string) string {
	repository, _, _ := strings.Cut(strings.TrimSpace(value), "@")
	return repository
}

func fleetControlEnvironment(value string) string {
	environment, _, _ := strings.Cut(strings.TrimSpace(value), "/")
	return environment
}

func canonicalFleetWorkflowURL(ci *CIIdentity) string {
	if ci == nil {
		return ""
	}
	return "https://github.com/" + ci.Repository + "/actions/runs/" + ci.RunID
}

func canonicalFleetRunnerAttemptID(ci *CIIdentity) string {
	if ci == nil {
		return ""
	}
	return "github-actions:" + ci.Repository + ":" + ci.RunID + ":" + ci.RunAttempt
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

func fleetRunnerPrincipalOwnsAttempt(principal AccessPrincipal, attempt *model.FleetRunnerAttempt) bool {
	if attempt == nil || principal.CI == nil || principal.CI.Provider != "github-actions" || !principalHasExactScope(principal, ScopeFleetOperate) {
		return false
	}
	identity := principalIdentity(principal)
	if identity == "" || attempt.PrincipalSubject != identity {
		return false
	}
	return attempt.RunnerAttemptID == canonicalFleetRunnerAttemptID(principal.CI) && attempt.WorkflowURL == canonicalFleetWorkflowURL(principal.CI)
}

func fleetRunnerCanReadAttempt(principal AccessPrincipal, attempt *model.FleetRunnerAttempt) bool {
	return principal.Allows(ScopeAPIRead) || fleetRunnerPrincipalOwnsAttempt(principal, attempt)
}

func principalHasExactScope(principal AccessPrincipal, scope string) bool {
	for _, candidate := range principal.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
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

// firstIncompleteFleetRunnerPhase makes workflow restarts resumable only from
// successful evidence bound to the exact durable attempt. Compatibility-era
// records without attemptId never advance protected runner state.
func firstIncompleteFleetRunnerPhase(plan *model.Operation, checkpoints []model.Operation, attemptID string) (string, bool) {
	succeeded := make(map[string]bool, len(checkpoints))
	for _, checkpoint := range checkpoints {
		boundID, _ := checkpoint.Payload["attemptId"].(string)
		if attemptID == "" || boundID != attemptID {
			continue
		}
		if _, err := uuid.Parse(boundID); err != nil || checkpoint.Status != model.OperationSucceeded {
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
