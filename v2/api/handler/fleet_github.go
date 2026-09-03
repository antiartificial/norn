package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet"
	"norn/v2/api/githubapp"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func (h *Handler) FleetGitHubStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	if h.fleetGitHubConfigError != nil {
		writeJSON(w, githubapp.Status{SchemaVersion: "norn.fleet-github-status/v1", Configured: true, Message: "GitHub App configuration is invalid"})
		return
	}
	if h.fleetGitHub == nil {
		writeJSON(w, githubapp.Status{SchemaVersion: "norn.fleet-github-status/v1", Configured: false, Message: "GitHub App is not configured"})
		return
	}
	writeJSON(w, h.fleetGitHub.Status(r.Context()))
}

func (h *Handler) CreateFleetGitHubPullRequest(w http.ResponseWriter, r *http.Request) {
	principal, plan, ok := h.requireFleetGitHubPlan(w, r)
	if !ok {
		return
	}
	typed, err := typedCapacityPlan(plan)
	if err != nil || !h.verifyCapacityPlan(typed) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_plan_invalid", "stored fleet capacity plan is invalid")
		return
	}
	if existing := h.existingFleetGitHubOperation(r, plan.ID, "fleet.github.pull-request"); existing != nil {
		existing.AttachReceipt()
		writeJSON(w, existing)
		return
	}
	result, err := h.fleetGitHub.CreatePullRequest(r.Context(), typed.ID, typed.Digest, typed.Pool, typed.Action, typed.Proposed, typed.SourceDigest)
	if errors.Is(err, githubapp.ErrStalePlan) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_plan_stale", "GitHub main changed after this Norn capacity plan; create a fresh plan")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_pull_request_failed", "GitHub could not create or recover the fleet pull request")
		return
	}
	op, err := h.recordFleetGitHubOperation(r, principal, plan.ID, "fleet.github.pull-request", "fleet pull request opened", map[string]interface{}{
		"planId": plan.ID, "pullRequestNumber": result.Number, "url": result.URL, "branch": result.Branch, "state": result.State,
	})
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "pull request exists but its durable Norn receipt could not be stored; retry safely")
		return
	}
	preventSensitiveResponseCaching(w)
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusCreated, op)
}

type fleetGitHubDispatchRequest struct {
	AllowDestructive bool `json:"allowDestructive"`
}

func (h *Handler) DispatchFleetGitHubApply(w http.ResponseWriter, r *http.Request) {
	principal, plan, ok := h.requireFleetGitHubPlan(w, r)
	if !ok {
		return
	}
	var request fleetGitHubDispatchRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_github_dispatch", err.Error())
		return
	}
	typed, err := typedCapacityPlan(plan)
	if err != nil || !h.verifyCapacityPlan(typed) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_plan_invalid", "stored fleet capacity plan is invalid")
		return
	}
	if existing := h.existingFleetGitHubOperation(r, plan.ID, "fleet.github.apply-dispatch"); existing != nil {
		existing.AttachReceipt()
		writeJSON(w, existing)
		return
	}
	destructive := typed.Action == "replace" || (typed.Action == "scale" && typed.Proposed.Desired < typed.Current.Desired)
	if destructive && !request.AllowDestructive {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_destructive_ack_required", "replacement and contraction plans require explicit allowDestructive acknowledgement")
		return
	}
	result, err := h.fleetGitHub.DispatchApprovedPlan(r.Context(), plan.ID, request.AllowDestructive)
	if errors.Is(err, githubapp.ErrNotReady) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_plan_not_ready", "merge the fleet pull request and wait for its protected main-branch plan workflow to succeed")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_dispatch_failed", "GitHub could not dispatch or recover the protected apply workflow")
		return
	}
	op, err := h.recordFleetGitHubOperation(r, principal, plan.ID, "fleet.github.apply-dispatch", "protected fleet apply dispatched", map[string]interface{}{
		"planId": plan.ID, "runId": result.RunID, "url": result.URL, "planRunId": result.PlanRunID,
		"planSha256": result.PlanSHA, "allowDestructive": request.AllowDestructive, "existing": result.Existing,
	})
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "workflow dispatch exists but its durable Norn receipt could not be stored; retry safely")
		return
	}
	preventSensitiveResponseCaching(w)
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusCreated, op)
}

func (h *Handler) requireFleetGitHubPlan(w http.ResponseWriter, r *http.Request) (AccessPrincipal, *model.Operation, bool) {
	principal, ok := requireControlScope(w, r, ScopeAPIWrite)
	if !ok {
		return AccessPrincipal{}, nil, false
	}
	if h.fleetGitHubConfigError != nil || h.fleetGitHub == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_not_configured", "configure a repository-scoped GitHub App on the Norn server")
		return AccessPrincipal{}, nil, false
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_plan_store_unavailable", "durable operation storage is unavailable")
		return AccessPrincipal{}, nil, false
	}
	planID := chi.URLParam(r, "planID")
	if _, err := uuid.Parse(planID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "plan ID must be a UUID")
		return AccessPrincipal{}, nil, false
	}
	plan, err := h.db.GetOperation(r.Context(), planID)
	if err == pgx.ErrNoRows || (err == nil && plan.Kind != "fleet.capacity-plan") {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_plan_not_found", "fleet capacity plan not found")
		return AccessPrincipal{}, nil, false
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_plan_read_failed", "failed to read fleet capacity plan")
		return AccessPrincipal{}, nil, false
	}
	return principal, plan, true
}

func typedCapacityPlan(op *model.Operation) (*fleet.CapacityPlan, error) {
	encoded, err := json.Marshal(op.Payload)
	if err != nil {
		return nil, err
	}
	var plan fleet.CapacityPlan
	if err := json.Unmarshal(encoded, &plan); err != nil {
		return nil, err
	}
	if plan.ID != op.ID || plan.Pool == "" || plan.Digest == "" || plan.SourceDigest == "" {
		return nil, fmt.Errorf("incomplete capacity plan")
	}
	return &plan, nil
}

func (h *Handler) verifyCapacityPlan(plan *fleet.CapacityPlan) bool {
	if plan == nil {
		return false
	}
	canonical, err := canonicalCapacityPlan(plan)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(canonical)
	if plan.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		return false
	}
	keys := []string{}
	if h.cfg != nil {
		keys = append(keys, h.cfg.AuditSigningKey)
		keys = append(keys, h.cfg.AuditPreviousSigningKeys...)
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		mac := hmac.New(sha256.New, []byte(key))
		_, _ = mac.Write([]byte(plan.Digest))
		want := "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
		if hmac.Equal([]byte(plan.Signature), []byte(want)) {
			return true
		}
	}
	return (h.cfg == nil || !h.cfg.Production()) && plan.Signature == ""
}

func (h *Handler) existingFleetGitHubOperation(r *http.Request, planID, kind string) *model.Operation {
	ops, err := h.db.ListOperations(r.Context(), store.OperationFilter{Kind: kind, Ref: planID, Limit: 1})
	if err != nil || len(ops) == 0 || ops[0].Status != model.OperationSucceeded {
		return nil
	}
	return &ops[0]
}

func (h *Handler) recordFleetGitHubOperation(r *http.Request, principal AccessPrincipal, planID, kind, message string, payload map[string]interface{}) (*model.Operation, error) {
	now := time.Now().UTC()
	finished := now
	op := &model.Operation{
		ID: uuid.NewString(), Kind: kind, Ref: planID, Status: model.OperationSucceeded,
		Risk: "GitOps mutation only; provider credentials remain in protected GitHub environments", Source: "control-api",
		Message: message, Payload: payload, Metadata: map[string]interface{}{"principal": strings.TrimSpace(principal.Subject), "principalTokenId": strings.TrimSpace(principal.TokenID), "environment": principal.Environment, "requestCI": principal.CI},
		StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1,
	}
	if err := h.db.InsertCompletedOperation(r.Context(), op); err != nil {
		return nil, err
	}
	op.AttachReceipt()
	return op, nil
}
