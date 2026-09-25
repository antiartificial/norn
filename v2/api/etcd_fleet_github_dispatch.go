package main

// The etcd normal-router bridge keeps GitHub dispatch recovery server-side.
// Its only durable external fact is the runner dispatch binding; the opaque
// nonce remains in the etcd preparation aggregate.

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
	"norn/v2/api/model"
)

type etcdFleetGitHubDispatcher interface {
	ResolveApprovedPlan(context.Context, string, string) (*githubapp.Dispatch, error)
	DispatchBoundPlan(context.Context, string, string, bool, *githubapp.Dispatch, string) (*githubapp.Dispatch, error)
}

type etcdFleetGitHubDispatchRequest struct {
	AllowDestructive bool `json:"allowDestructive"`
}

type etcdFleetGitHubDispatchResponse struct {
	PlanID           string `json:"planId"`
	PlanRunID        int64  `json:"planRunId"`
	PlanSHA256       string `json:"planSha256"`
	ApprovedHeadSHA  string `json:"approvedHeadSha"`
	RunID            int64  `json:"runId"`
	WorkflowURL      string `json:"workflowUrl"`
	AllowDestructive bool   `json:"allowDestructive"`
}

// etcdFleetGitHubDispatch is registered by the normal etcd runtime only when
// its GitHub App client is valid. It never returns a preparation or nonce.
func etcdFleetGitHubDispatch(cfg *config.Config, operations *etcdstore.V3OperationStore, github etcdFleetGitHubDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg == nil || operations == nil || github == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_not_configured", "configure a repository-scoped GitHub App on the Norn server")
			return
		}
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.CI != nil || !principal.Allows(handler.ScopeAPIWrite) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "Fleet GitHub dispatch requires a non-CI managed api:write principal")
			return
		}
		planID := chi.URLParam(r, "planID")
		if _, err := uuid.Parse(planID); err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "plan ID must be a UUID")
			return
		}
		var request etcdFleetGitHubDispatchRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_github_dispatch", "invalid request body")
			return
		}
		var trailing interface{}
		if err := decoder.Decode(&trailing); err != io.EOF {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_github_dispatch", "request body must contain one JSON value")
			return
		}
		plan, err := operations.GetOperation(r.Context(), planID)
		if err != nil || plan == nil {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "fleet_plan_not_found", "fleet capacity plan not found")
			return
		}
		if !etcdFleetGitHubPlanValid(cfg, plan) {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_plan_invalid", "fleet capacity plan is not a verified immutable plan")
			return
		}
		environment, err := etcdFleetGitHubEnvironment(cfg)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_not_configured", "configured Fleet GitHub root is invalid")
			return
		}
		if bound, bindErr := operations.GetFleetRunnerDispatchBinding(r.Context(), planID); bindErr == nil {
			prepared, prepErr := operations.GetFleetGitHubDispatchPreparation(r.Context(), planID)
			if prepErr != nil {
				handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_dispatch_preparation_unavailable", "protected dispatch preparation is unavailable")
				return
			}
			if prepared.FleetEnvironment != environment || prepared.AllowDestructive != request.AllowDestructive {
				handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "this plan already has a different protected dispatch binding")
				return
			}
			writeEtcdSourceJSON(w, http.StatusOK, etcdFleetGitHubDispatchResponse{PlanID: bound.PlanID, PlanRunID: prepared.PlanRunID, PlanSHA256: bound.PlanSHA256, ApprovedHeadSHA: bound.ApprovedHeadSHA, RunID: bound.RunID, WorkflowURL: bound.WorkflowURL, AllowDestructive: prepared.AllowDestructive})
			return
		} else if !errors.Is(bindErr, etcdstore.ErrNotFound) {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_dispatch_unavailable", "protected dispatch binding is unavailable")
			return
		}
		prepared, prepErr := operations.GetFleetGitHubDispatchPreparation(r.Context(), planID)
		if errors.Is(prepErr, etcdstore.ErrNotFound) {
			approved, resolveErr := github.ResolveApprovedPlan(r.Context(), planID, environment)
			if resolveErr != nil || !validEtcdFleetGitHubApproved(approved) {
				handler.WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_plan_unproven", "GitHub could not prove the approved immutable fleet plan")
				return
			}
			prepared, _, prepErr = operations.PrepareFleetGitHubDispatch(r.Context(), etcdstore.FleetGitHubDispatchPreparation{PlanID: planID, PlanRunID: approved.PlanRunID, PlanSHA256: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA, FleetEnvironment: environment, AllowDestructive: request.AllowDestructive})
		}
		if prepErr != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_preparation_unavailable", "protected dispatch preparation is unavailable")
			return
		}
		if prepared.FleetEnvironment != environment || prepared.AllowDestructive != request.AllowDestructive {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "this plan already has a different protected dispatch binding")
			return
		}
		approved := &githubapp.Dispatch{PlanRunID: prepared.PlanRunID, PlanSHA: prepared.PlanSHA256, ApprovedHeadSHA: prepared.ApprovedHeadSHA}
		result, dispatchErr := github.DispatchBoundPlan(r.Context(), planID, prepared.FleetEnvironment, prepared.AllowDestructive, approved, prepared.DispatchNonce)
		if dispatchErr != nil || !validEtcdFleetGitHubResult(cfg, result, approved) {
			handler.WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_dispatch_unproven", "GitHub dispatch outcome is ambiguous or does not prove the protected workflow identity")
			return
		}
		if err := operations.FinishFleetGitHubDispatch(r.Context(), planID, prepared.DispatchNonceSHA256, result.RunID, result.URL); err != nil {
			handler.WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "verified workflow run could not be bound to its durable dispatch")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeEtcdSourceJSON(w, http.StatusCreated, etcdFleetGitHubDispatchResponse{PlanID: planID, PlanRunID: prepared.PlanRunID, PlanSHA256: prepared.PlanSHA256, ApprovedHeadSHA: prepared.ApprovedHeadSHA, RunID: result.RunID, WorkflowURL: result.URL, AllowDestructive: prepared.AllowDestructive})
	}
}

func validEtcdFleetGitHubApproved(value *githubapp.Dispatch) bool {
	return value != nil && value.PlanRunID > 0 && fleetLowerHexMain(value.PlanSHA, 64) && fleetLowerHexMain(value.ApprovedHeadSHA, 40)
}

func validEtcdFleetGitHubResult(cfg *config.Config, value, approved *githubapp.Dispatch) bool {
	if value == nil || approved == nil || value.RunID <= 0 || value.PlanRunID != approved.PlanRunID || value.PlanSHA != approved.PlanSHA || value.ApprovedHeadSHA != approved.ApprovedHeadSHA {
		return false
	}
	return strings.TrimSpace(value.URL) == fmt.Sprintf("https://github.com/%s/actions/runs/%d", strings.TrimSpace(cfg.FleetGitHubRepository), value.RunID)
}

func etcdFleetGitHubPlanValid(cfg *config.Config, op *model.Operation) bool {
	if cfg == nil || op == nil || op.Kind != "fleet.capacity-plan" || op.Status != model.OperationSucceeded {
		return false
	}
	encoded, err := json.Marshal(op.Payload)
	if err != nil {
		return false
	}
	var plan fleet.CapacityPlan
	if json.Unmarshal(encoded, &plan) != nil || plan.ID != op.ID || plan.Digest == "" || plan.Signature == "" {
		return false
	}
	for _, key := range append([]string{cfg.AuditSigningKey}, cfg.AuditPreviousSigningKeys...) {
		if key == "" {
			continue
		}
		expected := plan
		if refreshEtcdCapacityPlanSignature(&expected, key) == nil && hmac.Equal([]byte(plan.Digest), []byte(expected.Digest)) && hmac.Equal([]byte(plan.Signature), []byte(expected.Signature)) {
			return true
		}
	}
	return false
}

func etcdFleetGitHubEnvironment(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("Fleet GitHub configuration is unavailable")
	}
	parts := strings.Split(strings.Trim(strings.TrimSpace(cfg.FleetGitHubConfigPath), "/"), "/")
	if len(parts) != 4 || parts[0] != "environments" || parts[3] != "cluster.yaml" || (parts[1] != "staging" && parts[1] != "production") || parts[2] != "nyc3" {
		return "", fmt.Errorf("Fleet GitHub configuration root is invalid")
	}
	return parts[1] + "/" + parts[2], nil
}

func fleetLowerHexMain(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
