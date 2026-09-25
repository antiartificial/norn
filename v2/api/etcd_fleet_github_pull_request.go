package main

// The normal etcd PR bridge follows the same durable sequence as the
// PostgreSQL bridge: signed reservation, deterministic GitHub call, then a
// separately signed completion. A lost HTTP response leaves the reservation
// queued and retries the exact branch identity.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type etcdFleetGitHubPullRequester interface {
	CreatePullRequest(context.Context, string, string, string, string, fleet.NodePool, string) (*githubapp.PullRequest, error)
	ReconcilePullRequest(context.Context, string, string, string, string, fleet.NodePool, string) (*githubapp.Reconciliation, error)
}

func etcdFleetGitHubPullRequest(cfg *config.Config, operations *etcdstore.V3OperationStore, github etcdFleetGitHubPullRequester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg == nil || operations == nil || github == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_not_configured", "configure a repository-scoped GitHub App on the Norn server")
			return
		}
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.CI != nil || !principal.Allows(handler.ScopeAPIWrite) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "Fleet GitHub pull request requires a non-CI managed api:write principal")
			return
		}
		planID := chi.URLParam(r, "planID")
		if _, err := uuid.Parse(planID); err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "plan ID must be a UUID")
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
		encoded, _ := json.Marshal(plan.Payload)
		var typed fleet.CapacityPlan
		if json.Unmarshal(encoded, &typed) != nil || typed.ID != plan.ID {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_plan_invalid", "fleet capacity plan is not a verified immutable plan")
			return
		}
		reservation, err := operations.GetFleetGitHubPullRequestReservation(r.Context(), planID)
		if errors.Is(err, etcdstore.ErrNotFound) {
			_, _, err = acceptEtcdFleetGitHubPullRequest(r.Context(), operations, principal, plan, typed)
			if err == nil {
				reservation, err = operations.GetFleetGitHubPullRequestReservation(r.Context(), planID)
			}
		}
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_github_pull_request_reservation_unavailable", "protected pull request reservation is unavailable")
			return
		}
		if err := operations.VerifyFleetGitHubPullRequestReservation(r.Context(), reservation); err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_receipt_failed", "signed pull request receipt is unavailable")
			return
		}
		op, err := operations.GetOperation(r.Context(), reservation.OperationID)
		if err != nil || op == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_receipt_failed", "protected pull request receipt is unavailable")
			return
		}
		if op.Status == model.OperationSucceeded {
			result := pullRequestFromEtcdOperation(op)
			if result == nil || operations.VerifyFleetGitHubPullRequestCompletion(r.Context(), reservation, result) != nil {
				handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_receipt_failed", "signed pull request completion is unavailable")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			op.AttachReceipt()
			writeEtcdSourceJSON(w, http.StatusOK, op)
			return
		}
		if op.Status != model.OperationQueued {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_github_pull_request_terminal", "fleet pull request reservation is terminal")
			return
		}
		observed, reconcileErr := github.ReconcilePullRequest(r.Context(), reservation.PlanID, reservation.PlanDigest, reservation.Pool, reservation.Action, reservation.Proposed, reservation.SourceDigest)
		if reconcileErr != nil || observed == nil || observed.Outcome == "ambiguous" {
			handler.WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_pull_request_unproven", "GitHub could not authoritatively reconcile the fleet pull request")
			return
		}
		if observed.PullRequest != nil {
			if observed.Outcome != "remote-success" || !validEtcdFleetGitHubPullRequest(cfg, reservation, observed.PullRequest) {
				handler.WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_pull_request_unproven", "GitHub reconciliation did not prove the fleet pull request")
				return
			}
			if err := operations.FinishFleetGitHubPullRequest(r.Context(), reservation, observed.PullRequest); err != nil {
				handler.WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "recovered pull request could not be durably recorded; retry safely")
				return
			}
			op, err = operations.GetOperation(r.Context(), reservation.OperationID)
			if err != nil {
				handler.WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "pull request completion could not be loaded")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			op.AttachReceipt()
			writeEtcdSourceJSON(w, http.StatusOK, op)
			return
		}
		if observed.Outcome != "verified-no-write" {
			handler.WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_pull_request_unproven", "GitHub reconciliation did not prove that no pull request was written")
			return
		}
		result, createErr := github.CreatePullRequest(r.Context(), reservation.PlanID, reservation.PlanDigest, reservation.Pool, reservation.Action, reservation.Proposed, reservation.SourceDigest)
		if createErr != nil || !validEtcdFleetGitHubPullRequest(cfg, reservation, result) {
			handler.WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_pull_request_unproven", "GitHub could not create or recover the fleet pull request")
			return
		}
		if err := operations.FinishFleetGitHubPullRequest(r.Context(), reservation, result); err != nil {
			handler.WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "pull request exists but its durable Norn receipt could not be stored; retry safely")
			return
		}
		op, err = operations.GetOperation(r.Context(), reservation.OperationID)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "pull request completion could not be loaded")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Location", "/api/v1/operations/"+op.ID)
		op.AttachReceipt()
		writeEtcdSourceJSON(w, http.StatusCreated, op)
	}
}

func validEtcdFleetGitHubPullRequest(cfg *config.Config, reservation etcdstore.FleetGitHubPullRequestReservation, result *githubapp.PullRequest) bool {
	return cfg != nil && result != nil && result.Number > 0 && strings.TrimSpace(result.Branch) == "norn/plan-"+reservation.PlanID && strings.TrimSpace(result.URL) == fmt.Sprintf("https://github.com/%s/pull/%d", strings.TrimSpace(cfg.FleetGitHubRepository), result.Number)
}

func acceptEtcdFleetGitHubPullRequest(ctx context.Context, operations *etcdstore.V3OperationStore, principal handler.AccessPrincipal, plan *model.Operation, typed fleet.CapacityPlan) (store.AcceptedOperation, etcdstore.FleetGitHubPullRequestReservation, error) {
	authority, err := operations.Authority(ctx)
	if err != nil {
		return store.AcceptedOperation{}, etcdstore.FleetGitHubPullRequestReservation{}, err
	}
	reservation := etcdstore.FleetGitHubPullRequestReservation{PlanID: plan.ID, PlanDigest: typed.Digest, SourceDigest: typed.SourceDigest, Pool: typed.Pool, Action: typed.Action, Proposed: typed.Proposed}
	payload := map[string]interface{}{"planId": plan.ID, "planDigest": typed.Digest, "sourceDigest": typed.SourceDigest, "pool": typed.Pool, "action": typed.Action, "proposed": typed.Proposed}
	now := time.Now().UTC().Truncate(time.Microsecond)
	op := model.Operation{ID: uuid.NewString(), Kind: "fleet.github.pull-request", Ref: plan.ID, Status: model.OperationQueued, Source: "etcd-normal-fleet", Risk: "GitOps mutation only; provider credentials remain in the repository-scoped GitHub App", Message: "Fleet GitHub pull request reserved", Payload: map[string]interface{}{"fleetGitHub": payload}, Metadata: map[string]interface{}{}, StartedAt: now, UpdatedAt: now, MaxAttempts: 1}
	acceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/fleet-github", Subject: plan.ID}, Kind: op.Kind, Resource: plan.ID, Key: "protected-plan-receipt/v1"}, Operation: op, Audit: store.AcceptanceAuditContext{CredentialID: principal.TokenID, DeviceID: principal.DeviceID, Source: "etcd-normal-fleet", Scopes: append([]string(nil), principal.Scopes...)}, Semantics: map[string]interface{}{"fleetGitHub": payload}}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		return store.AcceptedOperation{}, reservation, err
	}
	accepted, err := operations.AcceptFleetGitHubPullRequest(ctx, acceptance, reservation)
	return accepted, reservation, err
}

func pullRequestFromEtcdOperation(op *model.Operation) *githubapp.PullRequest {
	if op == nil {
		return nil
	}
	number := etcdFleetPullRequestNumber(op.Payload["pullRequestNumber"])
	url, _ := op.Payload["url"].(string)
	branch, _ := op.Payload["branch"].(string)
	state, _ := op.Payload["state"].(string)
	merged, _ := op.Payload["merged"].(bool)
	head, _ := op.Payload["headSha"].(string)
	if number <= 0 || strings.TrimSpace(url) == "" || strings.TrimSpace(branch) == "" {
		return nil
	}
	return &githubapp.PullRequest{Number: number, URL: url, Branch: branch, State: state, Merged: merged, HeadSHA: head}
}

func etcdFleetPullRequestNumber(value interface{}) int {
	switch number := value.(type) {
	case int:
		return number
	case int64:
		return int(number)
	case float64:
		return int(number)
	case json.Number:
		parsed, _ := number.Int64()
		return int(parsed)
	default:
		return 0
	}
}
