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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type etcdFleetGitHubDispatcher interface {
	ResolveApprovedPlan(context.Context, string, string) (*githubapp.Dispatch, error)
	DispatchBoundPlan(context.Context, string, string, bool, *githubapp.Dispatch, string) (*githubapp.Dispatch, error)
}

type etcdFleetGitHubDispatchRequest struct {
	AllowDestructive bool `json:"allowDestructive"`
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
		planBytes, _ := json.Marshal(plan.Payload)
		var typedPlan fleet.CapacityPlan
		if err := json.Unmarshal(planBytes, &typedPlan); err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_plan_invalid", "fleet capacity plan is not a verified immutable plan")
			return
		}
		destructive := typedPlan.Action == "replace" || (typedPlan.Action == "scale" && typedPlan.Proposed.Desired < typedPlan.Current.Desired)
		if destructive && !request.AllowDestructive {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_github_destructive_ack_required", "replacement and contraction plans require explicit allowDestructive acknowledgement")
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
			if prepared.OperationID == "" || prepared.FleetEnvironment != environment || prepared.AllowDestructive != request.AllowDestructive {
				handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "this plan already has a different protected dispatch binding")
				return
			}
			if reservationErr := operations.VerifyFleetGitHubDispatchReservation(r.Context(), prepared); reservationErr != nil {
				handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_receipt_failed", "signed protected dispatch receipt is unavailable")
				return
			}
			if completionErr := operations.VerifyFleetGitHubDispatchCompletion(r.Context(), bound); completionErr != nil {
				handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_receipt_failed", "protected dispatch completion receipt is unavailable")
				return
			}
			writeEtcdFleetGitHubDispatchOperation(w, r, operations, prepared.OperationID, http.StatusOK)
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
			_, prepared, prepErr = acceptEtcdFleetGitHubDispatch(r.Context(), operations, principal, plan, typedPlan, approved, environment, request.AllowDestructive)
		}
		if prepErr != nil {
			if errors.Is(prepErr, store.ErrAcceptanceIndeterminate) {
				handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_indeterminate", "dispatch preparation outcome is indeterminate; retry the same plan request")
				return
			}
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_preparation_unavailable", "protected dispatch preparation is unavailable")
			return
		}
		if prepared.OperationID == "" || prepared.FleetEnvironment != environment || prepared.AllowDestructive != request.AllowDestructive {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "this plan already has a different protected dispatch binding")
			return
		}
		if err := operations.VerifyFleetGitHubDispatchReservation(r.Context(), prepared); err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_receipt_failed", "signed protected dispatch receipt is unavailable")
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
		writeEtcdFleetGitHubDispatchOperation(w, r, operations, prepared.OperationID, http.StatusCreated)
	}
}

func writeEtcdFleetGitHubDispatchOperation(w http.ResponseWriter, r *http.Request, operations *etcdstore.V3OperationStore, operationID string, status int) {
	op, err := operations.GetOperation(r.Context(), operationID)
	if err != nil || op == nil || op.Kind != fleetGitHubDispatchOperationKindMain || op.Status != model.OperationSucceeded {
		handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_receipt_failed", "protected dispatch operation receipt is unavailable")
		return
	}
	bound, err := operations.GetFleetRunnerDispatchBinding(r.Context(), op.Ref)
	if err != nil || operations.VerifyFleetGitHubDispatchCompletion(r.Context(), bound) != nil {
		handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_receipt_failed", "protected dispatch completion receipt is unavailable")
		return
	}
	op.AttachReceipt()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/api/v1/operations/"+operationID)
	writeEtcdSourceJSON(w, status, op)
}

const fleetGitHubDispatchOperationKindMain = "fleet.github.apply-dispatch"

// acceptEtcdFleetGitHubDispatch reserves a signed immutable operation before
// the first external GitHub write. Its plan-scoped identity makes retries
// recover the exact same receipt and private nonce.
func acceptEtcdFleetGitHubDispatch(ctx context.Context, operations *etcdstore.V3OperationStore, principal handler.AccessPrincipal, plan *model.Operation, typed fleet.CapacityPlan, approved *githubapp.Dispatch, environment string, allowDestructive bool) (store.AcceptedOperation, etcdstore.FleetGitHubDispatchPreparation, error) {
	authority, err := operations.Authority(ctx)
	if err != nil {
		return store.AcceptedOperation{}, etcdstore.FleetGitHubDispatchPreparation{}, err
	}
	payload := map[string]interface{}{"planId": plan.ID, "planDigest": typed.Digest, "sourceDigest": typed.SourceDigest, "planRunId": approved.PlanRunID, "planSha256": approved.PlanSHA, "approvedHeadSha": approved.ApprovedHeadSHA, "fleetEnvironment": environment, "allowDestructive": allowDestructive}
	now := time.Now().UTC().Truncate(time.Microsecond)
	op := model.Operation{ID: uuid.NewString(), Kind: "fleet.github.apply-dispatch", Ref: plan.ID, Status: model.OperationQueued, Source: "etcd-normal-fleet", Risk: "GitOps mutation only; provider credentials remain in protected GitHub environments", Message: "protected Fleet GitHub dispatch reserved", Payload: map[string]interface{}{"fleetGitHub": payload}, Metadata: map[string]interface{}{}, StartedAt: now, UpdatedAt: now, MaxAttempts: 1}
	acceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/fleet-github", Subject: plan.ID}, Kind: op.Kind, Resource: plan.ID, Key: "protected-plan-receipt/v1"}, Operation: op, Audit: store.AcceptanceAuditContext{CredentialID: principal.TokenID, DeviceID: principal.DeviceID, Source: "etcd-normal-fleet", Scopes: append([]string(nil), principal.Scopes...)}, Semantics: map[string]interface{}{"fleetGitHub": payload}}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		return store.AcceptedOperation{}, etcdstore.FleetGitHubDispatchPreparation{}, err
	}
	return operations.AcceptFleetGitHubDispatch(ctx, acceptance, etcdstore.FleetGitHubDispatchPreparation{PlanID: plan.ID, PlanRunID: approved.PlanRunID, PlanSHA256: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA, FleetEnvironment: environment, AllowDestructive: allowDestructive})
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
