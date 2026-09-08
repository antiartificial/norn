package handler

import (
	"crypto/hmac"
	"crypto/rand"
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

	"norn/v2/api/config"
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
	destructive := typed.Action == "replace" || (typed.Action == "scale" && typed.Proposed.Desired < typed.Current.Desired)
	if destructive && !request.AllowDestructive {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_destructive_ack_required", "replacement and contraction plans require explicit allowDestructive acknowledgement")
		return
	}
	fleetEnvironment, ok := h.requireMatchingFleetEnvironment(w, r)
	if !ok {
		return
	}
	// A completed operation is replay-safe only within the same current lane.
	// In particular, disposable/fleet/nyc3 is shared by pilot runs and cannot
	// be used to replay a receipt that was authorized by an older authority.
	if existing := h.existingFleetGitHubOperation(r, plan.ID, "fleet.github.apply-dispatch"); existing != nil {
		binding, bindingErr := h.db.GetFleetGitHubDispatch(r.Context(), plan.ID)
		if bindingErr != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not read the protected dispatch binding for the completed operation")
			return
		}
		if !fleetGitHubDispatchMatchesCurrentLane(*binding, fleetEnvironment, configuredPilotRunID(h.cfg), request.AllowDestructive) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "the completed dispatch receipt belongs to a different current Fleet lane")
			return
		}
		existing.AttachReceipt()
		writeJSON(w, existing)
		return
	}
	// Serialize durable binding creation and external dispatch per plan. This
	// closes the find-then-dispatch race while retaining a recoverable binding.
	release, locked, lockErr := h.db.AcquireAppOperationLock(r.Context(), "fleet-github-dispatch:"+plan.ID)
	if lockErr != nil || !locked {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_in_progress", "another protected dispatch is resolving this fleet plan")
		return
	}
	defer release()
	binding, bindingErr := h.db.GetFleetGitHubDispatch(r.Context(), plan.ID)
	if bindingErr == nil && binding.DispatchState == "prepared" && binding.RunID == 0 {
		// A prior call failed before its single permitted POST. The raw nonce was
		// intentionally never retained, so discard this clear pre-submit state
		// and mint a new proof for a safe retry.
		if err := h.db.DeletePreparedFleetGitHubDispatch(r.Context(), plan.ID, binding.DispatchNonceSHA256); err != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not reset the pre-submit dispatch binding")
			return
		}
		binding, bindingErr = nil, pgx.ErrNoRows
	}
	var nonce string
	if bindingErr == pgx.ErrNoRows {
		approved, resolveErr := h.fleetGitHub.ResolveApprovedPlan(r.Context(), plan.ID, fleetEnvironment)
		if errors.Is(resolveErr, githubapp.ErrNotReady) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_plan_not_ready", "merge the fleet pull request and wait for its protected main-branch plan workflow to succeed")
			return
		}
		if resolveErr != nil {
			WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_dispatch_failed", "GitHub could not resolve the protected fleet plan")
			return
		}
		var nonceHash string
		var nonceErr error
		nonce, nonceHash, nonceErr = newFleetDispatchNonce()
		if nonceErr != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not create the protected dispatch binding")
			return
		}
		binding, bindingErr = h.db.CreateFleetGitHubDispatch(r.Context(), store.FleetGitHubDispatch{
			PlanID: plan.ID, PlanRunID: approved.PlanRunID, PlanSHA256: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA,
			PilotRunID: approved.PilotRunID, FleetEnvironment: fleetEnvironment, AllowDestructive: request.AllowDestructive, DispatchNonceSHA256: nonceHash, DispatchState: "prepared",
		})
		if bindingErr != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not persist the protected dispatch binding")
			return
		}
	} else if bindingErr != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not read the protected dispatch binding")
		return
	}
	if !fleetGitHubDispatchMatchesCurrentLane(*binding, fleetEnvironment, configuredPilotRunID(h.cfg), request.AllowDestructive) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "this plan already has a different protected dispatch binding")
		return
	}
	if binding.RunID > 0 {
		result := &githubapp.Dispatch{RunID: binding.RunID, URL: binding.WorkflowURL, PlanRunID: binding.PlanRunID, PlanSHA: binding.PlanSHA256, ApprovedHeadSHA: binding.ApprovedHeadSHA, PilotRunID: binding.PilotRunID, Existing: true}
		h.recordCompletedFleetGitHubDispatch(w, r, principal, plan.ID, fleetEnvironment, request.AllowDestructive, result)
		return
	}
	approved := &githubapp.Dispatch{PlanRunID: binding.PlanRunID, PlanSHA: binding.PlanSHA256, ApprovedHeadSHA: binding.ApprovedHeadSHA, PilotRunID: binding.PilotRunID}
	if binding.DispatchState == "submitting" {
		// The process may have died after the single permitted POST. Recover by
		// matching only the durable nonce hash against exact GitHub run identity;
		// never mint a new nonce or send another POST from this fenced state.
		result, recoverErr := h.fleetGitHub.RecoverBoundPlan(r.Context(), plan.ID, fleetEnvironment, approved, binding.DispatchNonceSHA256)
		if recoverErr != nil {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_ambiguous", "the protected dispatch outcome is ambiguous; inspect the bound GitHub run before retrying")
			return
		}
		if _, finishErr := h.db.FinishFleetGitHubDispatch(r.Context(), plan.ID, binding.DispatchNonceSHA256, result.RunID, result.URL); finishErr != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "workflow dispatch exists but its protected binding could not be completed; retry safely")
			return
		}
		h.recordCompletedFleetGitHubDispatch(w, r, principal, plan.ID, fleetEnvironment, request.AllowDestructive, result)
		return
	}
	if nonce == "" || binding.DispatchState != "prepared" {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_ambiguous", "the protected dispatch outcome is ambiguous; inspect the bound GitHub run before retrying")
		return
	}
	if _, err := h.db.MarkFleetGitHubDispatchSubmitting(r.Context(), plan.ID, binding.DispatchNonceSHA256); err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_in_progress", "the protected dispatch submission fence could not be acquired")
		return
	}
	result, err := h.fleetGitHub.DispatchBoundPlan(r.Context(), plan.ID, fleetEnvironment, request.AllowDestructive, approved, nonce)
	if errors.Is(err, githubapp.ErrNotReady) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_plan_not_ready", "merge the fleet pull request and wait for its protected main-branch plan workflow to succeed")
		return
	}
	if err != nil {
		if errors.Is(err, githubapp.ErrDispatchPreSubmit) {
			_ = h.db.DeletePreparedFleetGitHubDispatch(r.Context(), plan.ID, binding.DispatchNonceSHA256)
		}
		WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_dispatch_failed", "GitHub could not dispatch or recover the protected apply workflow")
		return
	}
	if _, err := h.db.FinishFleetGitHubDispatch(r.Context(), plan.ID, binding.DispatchNonceSHA256, result.RunID, result.URL); err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "workflow dispatch exists but its protected binding could not be completed; retry safely")
		return
	}
	h.recordCompletedFleetGitHubDispatch(w, r, principal, plan.ID, fleetEnvironment, request.AllowDestructive, result)
}

func (h *Handler) recordCompletedFleetGitHubDispatch(w http.ResponseWriter, r *http.Request, principal AccessPrincipal, planID, fleetEnvironment string, allowDestructive bool, result *githubapp.Dispatch) {
	op, err := h.recordFleetGitHubOperation(r, principal, planID, "fleet.github.apply-dispatch", "protected fleet apply dispatched", map[string]interface{}{
		"planId": planID, "runId": result.RunID, "url": result.URL, "planRunId": result.PlanRunID,
		"planSha256": result.PlanSHA, "approvedHeadSha": result.ApprovedHeadSHA, "pilotRunId": result.PilotRunID, "fleetEnvironment": fleetEnvironment, "allowDestructive": allowDestructive, "existing": result.Existing,
	})
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "workflow dispatch exists but its durable Norn receipt could not be stored; retry safely")
		return
	}
	preventSensitiveResponseCaching(w)
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusCreated, op)
}

func newFleetDispatchNonce() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	nonce := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(nonce))
	return nonce, hex.EncodeToString(sum[:]), nil
}

func (h *Handler) requireMatchingFleetEnvironment(w http.ResponseWriter, r *http.Request) (string, bool) {
	inventory, err := h.loadFleetInventory()
	if err != nil || inventory.Document == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_config_read_failed", "configured fleet root is unavailable for protected GitHub operations")
		return "", false
	}
	fleetEnvironment, err := configuredFleetEnvironment(inventory.Document, h.cfg)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_config_invalid", "configured fleet root does not map to an allowed protected workflow environment")
		return "", false
	}
	controlEnvironment := "development"
	if h.cfg != nil {
		controlEnvironment = h.cfg.EnvironmentID()
	}
	if !fleetEnvironmentMatchesControlPlane(controlEnvironment, fleetEnvironment, h.cfg) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_environment_mismatch", "configured fleet root does not match this Norn control-plane environment")
		return "", false
	}
	return fleetEnvironment, true
}

func fleetEnvironmentMatchesControlPlane(controlEnvironment, fleetEnvironment string, cfg *config.Config) bool {
	if cfg != nil {
		if runBoundRoot, allowlisted := githubapp.RunBoundFleetRoot(cfg.FleetGitHubConfigPath); allowlisted {
			return controlEnvironment == "staging" && cfg.FleetGitHubPilotRunID != "" && fleetEnvironment == runBoundRoot
		}
	}
	if controlEnvironment == "development" {
		return true
	}
	return strings.HasPrefix(fleetEnvironment, controlEnvironment+"/")
}

func configuredPilotRunID(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.FleetGitHubPilotRunID
}

func fleetGitHubDispatchMatchesCurrentLane(binding store.FleetGitHubDispatch, fleetEnvironment, pilotRunID string, allowDestructive bool) bool {
	return binding.FleetEnvironment == fleetEnvironment && binding.PilotRunID == pilotRunID && binding.AllowDestructive == allowDestructive
}

func configuredFleetEnvironment(document *fleet.Document, cfg *config.Config) (string, error) {
	if document == nil || (document.Metadata.Environment != "staging" && document.Metadata.Environment != "production") || document.Cluster.Region != "nyc3" {
		return "", fmt.Errorf("fleet metadata environment and cluster region are not supported")
	}
	if cfg != nil && cfg.FleetGitHubPilotRunID != "" {
		runBoundRoot, allowlisted := githubapp.RunBoundFleetRoot(cfg.FleetGitHubConfigPath)
		if cfg.EnvironmentID() != "staging" || !allowlisted || document.Metadata.Environment != "staging" {
			return "", fmt.Errorf("disposable Fleet root does not match the run-bound staging GitHub configuration")
		}
		return runBoundRoot, nil
	}
	return document.Metadata.Environment + "/" + document.Cluster.Region, nil
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
