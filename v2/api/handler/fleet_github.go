package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/config"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
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
	if _, ok := h.requireMatchingFleetEnvironment(w, r); !ok {
		return
	}
	if existing, found, err := h.existingFleetGitHubOperation(r, plan.ID, "fleet.github.pull-request"); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	} else if found {
		existing.AttachReceipt()
		writeJSON(w, existing)
		return
	}
	// The lock covers the signed receipt as well as the external call. GitHub
	// can recover a duplicate PR request, but it cannot create the one signed
	// Norn receipt that represents that protected plan mutation.
	appLock, locked, lockErr := h.db.AcquireAppOperationLock(r.Context(), "fleet-github-pull-request:"+plan.ID)
	if lockErr != nil || !locked {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_pull_request_in_progress", "another protected pull request is resolving this fleet plan")
		return
	}
	defer appLock.Release()
	r = r.WithContext(appLock.Context())
	if existing, found, err := h.existingFleetGitHubOperation(r, plan.ID, "fleet.github.pull-request"); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	} else if found {
		existing.AttachReceipt()
		writeJSON(w, existing)
		return
	}
	reservation, err := h.reserveFleetGitHubOperation(r, principal, plan.ID, "fleet.github.pull-request", map[string]interface{}{
		"planId": plan.ID, "planDigest": typed.Digest, "sourceDigest": typed.SourceDigest,
		"pool": typed.Pool, "action": typed.Action, "proposed": typed.Proposed,
	})
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	result, err := h.fleetGitHub.CreatePullRequest(r.Context(), typed.ID, typed.Digest, typed.Pool, typed.Action, typed.Proposed, typed.SourceDigest)
	if errors.Is(err, githubapp.ErrStalePlan) {
		if _, finishErr := h.finishFleetGitHubReservation(r.Context(), reservation.ID, plan.ID, "fleet.github.pull-request", model.OperationFailed, "fleet pull request was not created because the plan is stale", map[string]interface{}{"planId": plan.ID, "outcome": "stale-plan"}); finishErr != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "stale plan outcome could not be durably recorded; retry safely")
			return
		}
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_plan_stale", "GitHub main changed after this Norn capacity plan; create a fresh plan")
		return
	}
	if errors.Is(err, githubapp.ErrPermanentNoWrite) {
		if _, finishErr := h.finishFleetGitHubReservation(r.Context(), reservation.ID, plan.ID, "fleet.github.pull-request", model.OperationFailed, "fleet pull request was refused before GitHub mutation", map[string]interface{}{"planId": plan.ID, "outcome": "permanent-no-write"}); finishErr != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "GitHub refusal could not be durably recorded; retry safely")
			return
		}
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_pull_request_refused", "GitHub rejected this plan before creating a pull request; create a fresh plan")
		return
	}
	if errors.Is(err, githubapp.ErrPermanentAfterMutation) {
		if _, finishErr := h.finishFleetGitHubReservation(r.Context(), reservation.ID, plan.ID, "fleet.github.pull-request", model.OperationFailed, "fleet pull request was refused after a possible branch mutation", map[string]interface{}{"planId": plan.ID, "outcome": "permanent-after-mutation"}); finishErr != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "GitHub terminal outcome could not be durably recorded; retry safely")
			return
		}
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_pull_request_refused", "GitHub rejected this plan after a possible protected branch mutation; create a fresh plan")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_pull_request_failed", "GitHub could not create or recover the fleet pull request")
		return
	}
	op, err := h.finishFleetGitHubReservation(r.Context(), reservation.ID, plan.ID, "fleet.github.pull-request", model.OperationSucceeded, "fleet pull request opened", map[string]interface{}{
		"planId": plan.ID, "pullRequestNumber": result.Number, "url": result.URL, "branch": result.Branch, "state": result.State,
	})
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "pull request exists but its durable Norn receipt could not be stored; retry safely")
		return
	}
	preventSensitiveResponseCaching(w)
	op.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusCreated, op)
}

type fleetGitHubReconcileRequest struct {
	Kind string `json:"kind"`
}

func (h *Handler) ReconcileFleetGitHubReservation(w http.ResponseWriter, r *http.Request) {
	_, plan, ok := h.requireFleetGitHubPlan(w, r)
	if !ok {
		return
	}
	var request fleetGitHubReconcileRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_github_reconcile", err.Error())
		return
	}
	kind := "fleet.github." + request.Kind
	if request.Kind != "pull-request" && request.Kind != "apply-dispatch" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_github_reconcile", "kind must be pull-request or apply-dispatch")
		return
	}
	lockName := "fleet-github-pull-request:" + plan.ID
	if request.Kind == "apply-dispatch" {
		lockName = "fleet-github-dispatch:" + plan.ID
	}
	appLock, locked, lockErr := h.db.AcquireAppOperationLock(r.Context(), lockName)
	if lockErr != nil || !locked {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_reconcile_in_progress", "another request is resolving this Fleet GitHub reservation")
		return
	}
	defer appLock.Release()
	r = r.WithContext(appLock.Context())
	op, found, err := h.resolveFleetGitHubOperation(r, plan.ID, kind)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	if !found {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_github_reservation_not_found", "fleet GitHub reservation not found")
		return
	}
	if op.Status.Terminal() {
		op.AttachReceipt()
		outcome := "already-terminal"
		if completion, ok := op.Metadata["fleetGitHubCompletion"].(map[string]interface{}); ok {
			if result, ok := completion["result"].(map[string]interface{}); ok {
				if recorded, ok := result["outcome"].(string); ok && recorded != "" {
					outcome = recorded
				}
			}
		}
		writeJSON(w, map[string]interface{}{"operation": op, "outcome": outcome})
		return
	}
	var observed *githubapp.Reconciliation
	if kind == "fleet.github.pull-request" {
		typed, planErr := typedCapacityPlan(plan)
		if planErr != nil || !h.verifyCapacityPlan(typed) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_plan_invalid", "stored fleet capacity plan is invalid")
			return
		}
		observed, err = h.fleetGitHub.ReconcilePullRequest(r.Context(), typed.ID, typed.Digest, typed.Pool, typed.Action, typed.Proposed, typed.SourceDigest)
	} else {
		binding, bindErr := h.db.GetFleetGitHubDispatch(r.Context(), plan.ID)
		if bindErr != nil {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_reconcile_ambiguous", "dispatch binding is unavailable; the reservation remains queued")
			return
		}
		fleetEnvironment, environmentOK := h.requireMatchingFleetEnvironment(w, r)
		if !environmentOK {
			return
		}
		if !fleetGitHubDispatchMatchesCurrentLane(*binding, fleetEnvironment, configuredPilotRunID(h.cfg), binding.AllowDestructive) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "the dispatch reservation belongs to a different current Fleet lane")
			return
		}
		if binding.DispatchState == "prepared" && binding.RunID == 0 {
			observed = &githubapp.Reconciliation{Outcome: "verified-no-write"}
		} else {
			approved := &githubapp.Dispatch{PlanRunID: binding.PlanRunID, PlanSHA: binding.PlanSHA256, ApprovedHeadSHA: binding.ApprovedHeadSHA, PilotRunID: binding.PilotRunID}
			var recovered *githubapp.Dispatch
			recovered, err = h.fleetGitHub.RecoverBoundPlan(r.Context(), plan.ID, binding.FleetEnvironment, approved, binding.DispatchNonceSHA256)
			if err == nil {
				observed = &githubapp.Reconciliation{Outcome: "recovered", Dispatch: recovered}
			} else {
				// One exact cancelled pilot run is bound to immutable workflow, job,
				// plan, artifact, and log evidence. The target apply was skipped and
				// cleanup failed during console evaluation before its plan/apply; keep
				// the response narrow and do not claim independent provider state.
				pilot, pilotErr := h.fleetGitHub.ReconcileObservedPilotApply(r.Context(), plan.ID, binding.FleetEnvironment, binding.AllowDestructive, approved, binding.DispatchNonceSHA256)
				if pilotErr == nil && binding.DispatchState == "submitting" && binding.RunID == 0 && binding.RunAttempt == 0 && binding.WorkflowURL == "" && binding.ApprovalEnvelopeSHA256 == "" {
					preventSensitiveResponseCaching(w)
					writeJSON(w, map[string]interface{}{"operation": op, "outcome": pilot.Outcome, "reconciliation": pilot})
					return
				}
				// A mismatched workflow HEAD is never recovered as a dispatch. The
				// only exception is an exact nonce-bound disposable Fleet run whose
				// provider barrier failure and skipped post-barrier steps prove no
				// provider/state effect ran; that outcome cancels the reservation.
				observed, err = h.fleetGitHub.ReconcileFailedPlanVerification(r.Context(), plan.ID, binding.FleetEnvironment, binding.AllowDestructive, approved, binding.DispatchNonceSHA256)
			}
		}
	}
	if err != nil {
		var recoveryFailure *githubapp.DispatchRecoveryFailure
		if errors.As(err, &recoveryFailure) {
			log.Printf("fleet GitHub reservation recovery failed stage=%s", recoveryFailure.Stage)
		} else {
			log.Printf("fleet GitHub reservation recovery failed stage=unknown")
		}
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_reconcile_ambiguous", "GitHub could not authoritatively reconcile the reservation; it remains queued")
		return
	}
	if observed.Outcome == "ambiguous" {
		preventSensitiveResponseCaching(w)
		writeJSON(w, map[string]interface{}{"operation": op, "outcome": observed.Outcome})
		return
	}
	result := map[string]interface{}{"planId": plan.ID, "outcome": observed.Outcome, "verifiedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	status, message := model.OperationCanceled, "fleet GitHub reservation canceled after verified no external write"
	if observed.PullRequest != nil {
		status, message = model.OperationSucceeded, "fleet pull request recovered by operator reconciliation"
		result["pullRequestNumber"], result["url"], result["branch"], result["state"] = observed.PullRequest.Number, observed.PullRequest.URL, observed.PullRequest.Branch, observed.PullRequest.State
	}
	if observed.Dispatch != nil {
		binding, bindErr := h.db.GetFleetGitHubDispatch(r.Context(), plan.ID)
		if bindErr != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "verified workflow run could not be bound to its durable dispatch")
			return
		}
		if _, bindErr = h.db.FinishFleetGitHubDispatch(r.Context(), plan.ID, binding.DispatchNonceSHA256, observed.Dispatch.RunID, observed.Dispatch.RunAttempt, observed.Dispatch.URL); bindErr != nil {
			if abandoned, checkErr := h.db.IsFleetPlanAbandonedForPlan(r.Context(), plan.ID); checkErr == nil && abandoned {
				WriteControlProblem(w, r, http.StatusConflict, lifecycle.CodeFleetTargetHolderAbandoned, "fleet target holder plan is permanently abandoned")
				return
			}
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "verified workflow run could not be bound to its durable dispatch")
			return
		}
		status, message = model.OperationSucceeded, "protected fleet apply recovered by operator reconciliation"
		result["runId"], result["url"], result["planRunId"], result["planSha256"], result["approvedHeadSha"] = observed.Dispatch.RunID, observed.Dispatch.URL, observed.Dispatch.PlanRunID, observed.Dispatch.PlanSHA, observed.Dispatch.ApprovedHeadSHA
	}
	finished, err := h.finishFleetGitHubReservation(r.Context(), op.ID, plan.ID, kind, status, message, result)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "verified outcome could not be durably signed")
		return
	}
	finished.AttachReceipt()
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]interface{}{"operation": finished, "outcome": observed.Outcome})
}

type fleetGitHubDispatchRequest struct {
	AllowDestructive bool `json:"allowDestructive"`
}

// An external-Mac approval is produced off-host and binds only this digest and
// the server-issued nonce hash. The raw nonce is accepted transiently by
// execute and is never written to an operation, audit event, or database.
type fleetGitHubExecuteRequest struct {
	AllowDestructive       bool   `json:"allowDestructive"`
	DispatchNonce          string `json:"dispatchNonce"`
	ApprovalEnvelopeSHA256 string `json:"approvalEnvelopeSHA256"`
}

type fleetGitHubPreparationResponse struct {
	SchemaVersion          string `json:"schemaVersion"`
	PlanID                 string `json:"planId"`
	PlanRunID              int64  `json:"planRunId"`
	PlanSHA256             string `json:"planSha256"`
	ApprovedHeadSHA        string `json:"approvedHeadSha"`
	PilotRunID             string `json:"pilotRunId"`
	FleetEnvironment       string `json:"fleetEnvironment"`
	AllowDestructive       bool   `json:"allowDestructive"`
	DispatchNonceSHA256    string `json:"dispatchNonceSHA256"`
	DispatchNonce          string `json:"dispatchNonce,omitempty"`
	ApprovalEnvelopeSHA256 string `json:"approvalEnvelopeSHA256,omitempty"`
	DispatchState          string `json:"dispatchState"`
}

type fleetGitHubPrepareResetRequest struct {
	AllowDestructive bool `json:"allowDestructive"`
	ConfirmLostNonce bool `json:"confirmLostNonce"`
}

// A rerun has no raw nonce: Norn never persists it. The immutable approval
// digest is the caller's proof that this is the same owner-authorized lane.
type fleetGitHubRerunRequest struct {
	AllowDestructive       bool   `json:"allowDestructive"`
	ApprovalEnvelopeSHA256 string `json:"approvalEnvelopeSHA256"`
}

func externalMacFleetDispatch(cfg *config.Config) bool {
	if cfg == nil || cfg.FleetGitHubPilotRunID == "" {
		return false
	}
	root, ok := githubapp.RunBoundFleetRoot(cfg.FleetGitHubConfigPath)
	return ok && root == "disposable/external-mac/nyc3"
}

func validFleetSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func preparationResponse(binding *store.FleetGitHubDispatch, raw string) fleetGitHubPreparationResponse {
	return fleetGitHubPreparationResponse{SchemaVersion: "norn.fleet-github-dispatch-preparation/v1", PlanID: binding.PlanID, PlanRunID: binding.PlanRunID, PlanSHA256: binding.PlanSHA256, ApprovedHeadSHA: binding.ApprovedHeadSHA, PilotRunID: binding.PilotRunID, FleetEnvironment: binding.FleetEnvironment, AllowDestructive: binding.AllowDestructive, DispatchNonceSHA256: binding.DispatchNonceSHA256, DispatchNonce: raw, ApprovalEnvelopeSHA256: binding.ApprovalEnvelopeSHA256, DispatchState: binding.DispatchState}
}

func (h *Handler) PrepareFleetGitHubApply(w http.ResponseWriter, r *http.Request) {
	_, plan, ok := h.requireFleetGitHubPlan(w, r)
	if !ok {
		return
	}
	if !externalMacFleetDispatch(h.cfg) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_prepare_not_required", "two-phase preparation is reserved for the external-Mac Fleet lane")
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
	if !h.refuseAbandonedFleetPlan(w, r, plan.ID) {
		return
	}
	release, locked, lockErr := h.db.AcquireAppOperationLock(r.Context(), "fleet-github-dispatch:"+plan.ID)
	if lockErr != nil || !locked {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_in_progress", "another protected dispatch is resolving this fleet plan")
		return
	}
	defer release.Release()
	r = r.WithContext(release.Context())
	binding, bindErr := h.db.GetFleetGitHubDispatch(r.Context(), plan.ID)
	if bindErr == nil {
		if !fleetGitHubDispatchMatchesCurrentLane(*binding, fleetEnvironment, configuredPilotRunID(h.cfg), request.AllowDestructive) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "this plan already has a different protected dispatch binding")
			return
		}
		// The raw nonce is deliberately returned once only. An idempotent prepare
		// retry receives its durable metadata/hash, never a recoverable secret.
		preventSensitiveResponseCaching(w)
		writeJSON(w, preparationResponse(binding, ""))
		return
	}
	if bindErr != pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not read the protected dispatch binding")
		return
	}
	approved, resolveErr := h.fleetGitHub.ResolveApprovedPlan(r.Context(), plan.ID, fleetEnvironment)
	if errors.Is(resolveErr, githubapp.ErrNotReady) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_plan_not_ready", "merge the fleet pull request and wait for its protected main-branch plan workflow to succeed")
		return
	}
	if resolveErr != nil {
		WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_dispatch_failed", "GitHub could not resolve the protected fleet plan")
		return
	}
	nonce, nonceHash, nonceErr := newFleetDispatchNonce()
	if nonceErr != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not create the protected dispatch binding")
		return
	}
	binding, bindErr = h.db.CreateFleetGitHubDispatch(r.Context(), store.FleetGitHubDispatch{PlanID: plan.ID, PlanRunID: approved.PlanRunID, PlanSHA256: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA, PilotRunID: approved.PilotRunID, FleetEnvironment: fleetEnvironment, AllowDestructive: request.AllowDestructive, DispatchNonceSHA256: nonceHash, DispatchState: "prepared"})
	if bindErr != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not persist the protected dispatch binding")
		return
	}
	preventSensitiveResponseCaching(w)
	writeJSONStatus(w, http.StatusCreated, preparationResponse(binding, nonce))
}

// ResetFleetGitHubPreparation deliberately handles only a proven pre-submit
// row. It is the fail-closed escape hatch for a lost no-store prepare response:
// submitting/dispatched bindings and any approval-bound preparation remain
// immutable and require investigation rather than nonce reissue.
func (h *Handler) ResetFleetGitHubPreparation(w http.ResponseWriter, r *http.Request) {
	_, plan, ok := h.requireFleetGitHubPlan(w, r)
	if !ok {
		return
	}
	if !externalMacFleetDispatch(h.cfg) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_prepare_not_required", "two-phase preparation is reserved for the external-Mac Fleet lane")
		return
	}
	var request fleetGitHubPrepareResetRequest
	if err := decodeControlJSON(w, r, &request); err != nil || !request.ConfirmLostNonce {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_github_dispatch", "confirmLostNonce is required to reset a lost pre-submit nonce")
		return
	}
	fleetEnvironment, ok := h.requireMatchingFleetEnvironment(w, r)
	if !ok {
		return
	}
	if !h.refuseAbandonedFleetPlan(w, r, plan.ID) {
		return
	}
	release, locked, lockErr := h.db.AcquireAppOperationLock(r.Context(), "fleet-github-dispatch:"+plan.ID)
	if lockErr != nil || !locked {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_in_progress", "another protected dispatch is resolving this fleet plan")
		return
	}
	defer release.Release()
	r = r.WithContext(release.Context())
	binding, err := h.db.GetFleetGitHubDispatch(r.Context(), plan.ID)
	if err == pgx.ErrNoRows {
		writeJSONStatus(w, http.StatusNoContent, nil)
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not read the protected dispatch binding")
		return
	}
	if !fleetGitHubDispatchMatchesCurrentLane(*binding, fleetEnvironment, configuredPilotRunID(h.cfg), request.AllowDestructive) || binding.DispatchState != "prepared" || binding.RunID != 0 || binding.ApprovalEnvelopeSHA256 != "" {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_ambiguous", "only an unapproved pre-submit preparation can be reset")
		return
	}
	if err := h.db.DeletePreparedFleetGitHubDispatch(r.Context(), plan.ID, binding.DispatchNonceSHA256); err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not reset the pre-submit preparation")
		return
	}
	writeJSONStatus(w, http.StatusNoContent, nil)
}

func (h *Handler) ExecuteFleetGitHubApply(w http.ResponseWriter, r *http.Request) {
	principal, plan, ok := h.requireFleetGitHubPlan(w, r)
	if !ok {
		return
	}
	if !externalMacFleetDispatch(h.cfg) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_prepare_not_required", "two-phase execution is reserved for the external-Mac Fleet lane")
		return
	}
	var request fleetGitHubExecuteRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_github_dispatch", err.Error())
		return
	}
	if !validFleetSHA256(request.ApprovalEnvelopeSHA256) || !validFleetSHA256(request.DispatchNonce) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_github_dispatch", "dispatch nonce and approval envelope digest must be lowercase SHA-256 values")
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
	if !h.refuseAbandonedFleetPlan(w, r, plan.ID) {
		return
	}
	release, locked, lockErr := h.db.AcquireAppOperationLock(r.Context(), "fleet-github-dispatch:"+plan.ID)
	if lockErr != nil || !locked {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_in_progress", "another protected dispatch is resolving this fleet plan")
		return
	}
	defer release.Release()
	r = r.WithContext(release.Context())
	binding, bindErr := h.db.GetFleetGitHubDispatch(r.Context(), plan.ID)
	if bindErr == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_prepare_required", "prepare the external-Mac dispatch before presenting owner approval")
		return
	}
	if bindErr != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not read the protected dispatch binding")
		return
	}
	if !fleetGitHubDispatchMatchesCurrentLane(*binding, fleetEnvironment, configuredPilotRunID(h.cfg), request.AllowDestructive) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "this plan already has a different protected dispatch binding")
		return
	}
	nonceSum := sha256.Sum256([]byte(request.DispatchNonce))
	nonceHash := hex.EncodeToString(nonceSum[:])
	if subtle.ConstantTimeCompare([]byte(nonceHash), []byte(binding.DispatchNonceSHA256)) != 1 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "dispatch nonce does not match the prepared binding")
		return
	}
	if binding.ApprovalEnvelopeSHA256 == "" {
		binding, bindErr = h.db.BindFleetGitHubDispatchApproval(r.Context(), plan.ID, binding.DispatchNonceSHA256, request.ApprovalEnvelopeSHA256)
		if bindErr != nil {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "could not bind the prepared approval")
			return
		}
	} else if subtle.ConstantTimeCompare([]byte(binding.ApprovalEnvelopeSHA256), []byte(request.ApprovalEnvelopeSHA256)) != 1 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "approval envelope differs from the prepared execution")
		return
	}
	if binding.RunID > 0 {
		// Legacy / pre-migration dispatched rows carry no approval digest. They
		// are intentionally not valid external-Mac execute replays.
		if binding.ApprovalEnvelopeSHA256 == "" {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "dispatched binding lacks the required external-Mac approval digest")
			return
		}
		result := &githubapp.Dispatch{RunID: binding.RunID, URL: binding.WorkflowURL, PlanRunID: binding.PlanRunID, PlanSHA: binding.PlanSHA256, ApprovedHeadSHA: binding.ApprovedHeadSHA, PilotRunID: binding.PilotRunID, Existing: true}
		h.recordCompletedFleetGitHubDispatch(w, r, principal, plan.ID, fleetEnvironment, request.AllowDestructive, result)
		return
	}
	approved := &githubapp.Dispatch{PlanRunID: binding.PlanRunID, PlanSHA: binding.PlanSHA256, ApprovedHeadSHA: binding.ApprovedHeadSHA, PilotRunID: binding.PilotRunID}
	if binding.DispatchState == "submitting" {
		result, recoverErr := h.fleetGitHub.RecoverBoundPlan(r.Context(), plan.ID, fleetEnvironment, approved, binding.DispatchNonceSHA256)
		if recoverErr != nil {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_ambiguous", "the protected dispatch outcome is ambiguous; inspect the bound GitHub run before retrying")
			return
		}
		if _, err := h.db.FinishFleetGitHubDispatch(r.Context(), plan.ID, binding.DispatchNonceSHA256, result.RunID, result.RunAttempt, result.URL); err != nil {
			h.writeFleetGitHubFinishError(w, r, plan.ID, err)
			return
		}
		h.recordCompletedFleetGitHubDispatch(w, r, principal, plan.ID, fleetEnvironment, request.AllowDestructive, result)
		return
	}
	if binding.DispatchState != "prepared" {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_ambiguous", "the protected dispatch outcome is ambiguous")
		return
	}
	// m11: the non-locking occupancy pre-check runs after every replay
	// branch above and before the signed reservation is queued.
	if preflightErr := h.db.CheckFleetTargetAcquirePreflight(r.Context(), typed.Cluster, fleetEnvironment, plan.ID, binding.DispatchNonceSHA256, plan.StartedAt); preflightErr != nil {
		if !writeFleetFenceError(w, r, preflightErr) {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "failed to pre-check the protected fleet target fence")
		}
		return
	}
	if _, err := h.ensureFleetGitHubReservation(r, principal, plan.ID, "fleet.github.apply-dispatch", map[string]interface{}{
		"planId": plan.ID, "planDigest": typed.Digest, "sourceDigest": typed.SourceDigest,
		"pool": typed.Pool, "action": typed.Action, "allowDestructive": request.AllowDestructive,
	}); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	if _, err := h.db.MarkFleetGitHubDispatchSubmittingFenced(r.Context(), plan.ID, binding.DispatchNonceSHA256, typed.Cluster, fleetEnvironment, plan.StartedAt); err != nil {
		if !writeFleetFenceError(w, r, err) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_in_progress", "the protected dispatch submission fence could not be acquired")
		}
		return
	}
	result, dispatchErr := h.fleetGitHub.DispatchBoundExternalMacPlan(r.Context(), plan.ID, fleetEnvironment, request.AllowDestructive, approved, request.DispatchNonce, request.ApprovalEnvelopeSHA256)
	if dispatchErr != nil {
		if errors.Is(dispatchErr, githubapp.ErrDispatchPreSubmit) {
			if _, resetErr := h.db.ResetFleetGitHubDispatchPreSubmit(r.Context(), plan.ID, binding.DispatchNonceSHA256); resetErr != nil {
				WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "GitHub rejected the dispatch before submission but the protected retry state could not be restored")
				return
			}
		}
		WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_dispatch_failed", "GitHub could not dispatch or recover the protected apply workflow")
		return
	}
	if _, err := h.db.FinishFleetGitHubDispatch(r.Context(), plan.ID, binding.DispatchNonceSHA256, result.RunID, result.RunAttempt, result.URL); err != nil {
		h.writeFleetGitHubFinishError(w, r, plan.ID, err)
		return
	}
	h.recordCompletedFleetGitHubDispatch(w, r, principal, plan.ID, fleetEnvironment, request.AllowDestructive, result)
}

// RerunFleetGitHubApply permits a fenced full rerun of the exact GitHub
// workflow run. Each observed run_attempt gets at most one POST; a later
// failed/cancelled generation may be retried only before Norn has a durable
// runner attempt. It is intentionally external-Mac-only: an ordinary Fleet
// lane has no owner approval digest or same-run retry contract.
func (h *Handler) RerunFleetGitHubApply(w http.ResponseWriter, r *http.Request) {
	principal, plan, ok := h.requireFleetGitHubPlan(w, r)
	if !ok {
		return
	}
	if !externalMacFleetDispatch(h.cfg) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_rerun_not_allowed", "same-run retry is reserved for the external-Mac Fleet lane")
		return
	}
	var request fleetGitHubRerunRequest
	if err := decodeControlJSON(w, r, &request); err != nil || !validFleetSHA256(request.ApprovalEnvelopeSHA256) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_github_dispatch", "allowDestructive and a lowercase approval envelope digest are required")
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
	if !h.refuseAbandonedFleetPlan(w, r, plan.ID) {
		return
	}
	release, locked, lockErr := h.db.AcquireAppOperationLock(r.Context(), "fleet-github-dispatch:"+plan.ID)
	if lockErr != nil || !locked {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_in_progress", "another protected dispatch is resolving this fleet plan")
		return
	}
	defer release.Release()
	r = r.WithContext(release.Context())
	binding, err := h.db.GetFleetGitHubDispatch(r.Context(), plan.ID)
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_rerun_not_allowed", "no protected dispatch binding exists")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not read the protected dispatch binding")
		return
	}
	if !fleetGitHubDispatchMatchesCurrentLane(*binding, fleetEnvironment, configuredPilotRunID(h.cfg), request.AllowDestructive) || binding.RunID <= 0 || binding.RunAttempt <= 0 || binding.ApprovalEnvelopeSHA256 == "" || subtle.ConstantTimeCompare([]byte(binding.ApprovalEnvelopeSHA256), []byte(request.ApprovalEnvelopeSHA256)) != 1 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "same-run retry does not match the exact protected dispatch")
		return
	}
	approved := &githubapp.Dispatch{PlanRunID: binding.PlanRunID, PlanSHA: binding.PlanSHA256, ApprovedHeadSHA: binding.ApprovedHeadSHA, PilotRunID: binding.PilotRunID}
	if binding.DispatchState == "rerun_submitting" {
		result, recoverErr := h.fleetGitHub.RecoverBoundExternalMacRerun(r.Context(), plan.ID, fleetEnvironment, approved, binding.DispatchNonceSHA256, binding.RunID, binding.RunAttempt)
		if recoverErr != nil {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_ambiguous", "the protected same-run retry outcome is ambiguous; do not submit another rerun")
			return
		}
		binding, err = h.db.FinishFleetGitHubDispatchRerun(r.Context(), plan.ID, binding.DispatchNonceSHA256, binding.RunID, binding.RunAttempt, result.RunAttempt)
		if err != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "same-run retry exists but its durable receipt could not be completed")
			return
		}
		h.recordFleetGitHubRerun(w, r, principal, plan.ID, fleetEnvironment, request.AllowDestructive, binding)
		return
	}
	if binding.DispatchState != "dispatched" {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_rerun_not_allowed", "only a conclusively dispatched workflow can be retried")
		return
	}
	// No same-run retry may start once the workflow has registered a durable
	// Norn runner attempt. That record is the consumed-authority handoff point;
	// recovery, rather than replaying the apply workflow, owns the next step.
	attempts, attemptsErr := h.db.ListFleetRunnerAttempts(r.Context(), plan.ID, 1)
	if attemptsErr != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_runner_attempt_read_failed", "could not inspect durable runner history before retry")
		return
	}
	if len(attempts) != 0 {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_rerun_not_allowed", "a durable runner attempt exists; use protected recovery instead")
		return
	}
	if eligibilityErr := h.fleetGitHub.CheckBoundExternalMacRerunEligibility(r.Context(), plan.ID, fleetEnvironment, approved, binding.DispatchNonceSHA256, binding.ApprovalEnvelopeSHA256, binding.RunID, binding.RunAttempt); eligibilityErr != nil {
		if errors.Is(eligibilityErr, githubapp.ErrRerunIneligible) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_rerun_not_allowed", "the exact workflow generation is not conclusively failed, cancelled, or timed out")
			return
		}
		WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_dispatch_failed", "GitHub could not verify same-run retry eligibility")
		return
	}
	if _, err := h.ensureFleetGitHubReservation(r, principal, plan.ID, fmt.Sprintf("fleet.github.apply-rerun.%d", binding.RunAttempt+1), map[string]interface{}{
		"planId": plan.ID, "runId": binding.RunID, "priorRunAttempt": binding.RunAttempt,
		"fleetEnvironment": fleetEnvironment, "allowDestructive": request.AllowDestructive,
	}); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	if _, err := h.db.MarkFleetGitHubDispatchRerunSubmittingFenced(r.Context(), plan.ID, binding.DispatchNonceSHA256, binding.RunID, binding.RunAttempt, typed.Cluster, fleetEnvironment); err != nil {
		if !writeFleetFenceError(w, r, err) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_in_progress", "the one permitted same-run retry fence could not be acquired")
		}
		return
	}
	result, rerunErr := h.fleetGitHub.RerunBoundExternalMacPlan(r.Context(), plan.ID, fleetEnvironment, request.AllowDestructive, approved, binding.DispatchNonceSHA256, binding.ApprovalEnvelopeSHA256, binding.RunID, binding.RunAttempt)
	if rerunErr != nil {
		// The fence remains rerun_submitting. Even a failed HTTP response is not
		// proof that GitHub did not accept the rerun, so retry callers only inspect.
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_ambiguous", "the protected same-run retry outcome is ambiguous; do not submit another rerun")
		return
	}
	binding, err = h.db.FinishFleetGitHubDispatchRerun(r.Context(), plan.ID, binding.DispatchNonceSHA256, binding.RunID, binding.RunAttempt, result.RunAttempt)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "same-run retry exists but its durable receipt could not be completed")
		return
	}
	h.recordFleetGitHubRerun(w, r, principal, plan.ID, fleetEnvironment, request.AllowDestructive, binding)
}

func (h *Handler) recordFleetGitHubRerun(w http.ResponseWriter, r *http.Request, principal AccessPrincipal, planID, fleetEnvironment string, allowDestructive bool, binding *store.FleetGitHubDispatch) {
	kind := fmt.Sprintf("fleet.github.apply-rerun.%d", binding.RunAttempt)
	if existing, found, err := h.existingFleetGitHubOperation(r, planID, kind); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	} else if found {
		existing.AttachReceipt()
		writeJSON(w, existing)
		return
	}
	result := map[string]interface{}{
		"planId": planID, "runId": binding.RunID, "runAttempt": binding.RunAttempt, "url": binding.WorkflowURL,
		"planRunId": binding.PlanRunID, "planSha256": binding.PlanSHA256, "approvedHeadSha": binding.ApprovedHeadSHA,
		"pilotRunId": binding.PilotRunID, "fleetEnvironment": fleetEnvironment, "allowDestructive": allowDestructive,
	}
	reservation, err := h.ensureFleetGitHubReservation(r, principal, planID, kind, result)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	op := reservation
	if reservation.Status == model.OperationQueued {
		op, err = h.finishFleetGitHubReservation(r.Context(), reservation.ID, planID, kind, model.OperationSucceeded, "protected fleet apply rerun dispatched", result)
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "same-run retry exists but its durable Norn receipt could not be stored")
		return
	}
	preventSensitiveResponseCaching(w)
	op.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusCreated, op)
}

func (h *Handler) DispatchFleetGitHubApply(w http.ResponseWriter, r *http.Request) {
	principal, plan, ok := h.requireFleetGitHubPlan(w, r)
	if !ok {
		return
	}
	if externalMacFleetDispatch(h.cfg) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_prepare_required", "external-Mac dispatches require prepare then owner-approved execute")
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
	if !h.refuseAbandonedFleetPlan(w, r, plan.ID) {
		return
	}
	// A completed operation is replay-safe only within the same current lane.
	// In particular, disposable/fleet/nyc3 is shared by pilot runs and cannot
	// be used to replay a receipt that was authorized by an older authority.
	if existing, found, err := h.existingFleetGitHubOperation(r, plan.ID, "fleet.github.apply-dispatch"); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	} else if found {
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
	defer release.Release()
	r = r.WithContext(release.Context())
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
		if _, finishErr := h.db.FinishFleetGitHubDispatch(r.Context(), plan.ID, binding.DispatchNonceSHA256, result.RunID, result.RunAttempt, result.URL); finishErr != nil {
			h.writeFleetGitHubFinishError(w, r, plan.ID, finishErr)
			return
		}
		h.recordCompletedFleetGitHubDispatch(w, r, principal, plan.ID, fleetEnvironment, request.AllowDestructive, result)
		return
	}
	if nonce == "" || binding.DispatchState != "prepared" {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_ambiguous", "the protected dispatch outcome is ambiguous; inspect the bound GitHub run before retrying")
		return
	}
	// m11: a non-locking pre-check runs before the signed reservation is
	// queued, so a request the authoritative FOR UPDATE acquire below would
	// refuse anyway never leaves a queued reservation behind it.
	if preflightErr := h.db.CheckFleetTargetAcquirePreflight(r.Context(), typed.Cluster, fleetEnvironment, plan.ID, binding.DispatchNonceSHA256, plan.StartedAt); preflightErr != nil {
		if !writeFleetFenceError(w, r, preflightErr) {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "failed to pre-check the protected fleet target fence")
		}
		return
	}
	if _, err := h.ensureFleetGitHubReservation(r, principal, plan.ID, "fleet.github.apply-dispatch", map[string]interface{}{
		"planId": plan.ID, "planDigest": typed.Digest, "sourceDigest": typed.SourceDigest,
		"pool": typed.Pool, "action": typed.Action, "allowDestructive": request.AllowDestructive,
	}); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	if _, err := h.db.MarkFleetGitHubDispatchSubmittingFenced(r.Context(), plan.ID, binding.DispatchNonceSHA256, typed.Cluster, fleetEnvironment, plan.StartedAt); err != nil {
		if !writeFleetFenceError(w, r, err) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_in_progress", "the protected dispatch submission fence could not be acquired")
		}
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
	if _, err := h.db.FinishFleetGitHubDispatch(r.Context(), plan.ID, binding.DispatchNonceSHA256, result.RunID, result.RunAttempt, result.URL); err != nil {
		h.writeFleetGitHubFinishError(w, r, plan.ID, err)
		return
	}
	h.recordCompletedFleetGitHubDispatch(w, r, principal, plan.ID, fleetEnvironment, request.AllowDestructive, result)
}

func (h *Handler) recordCompletedFleetGitHubDispatch(w http.ResponseWriter, r *http.Request, principal AccessPrincipal, planID, fleetEnvironment string, allowDestructive bool, result *githubapp.Dispatch) {
	payload := map[string]interface{}{
		"planId": planID, "runId": result.RunID, "url": result.URL, "planRunId": result.PlanRunID,
		"planSha256": result.PlanSHA, "approvedHeadSha": result.ApprovedHeadSHA, "pilotRunId": result.PilotRunID, "fleetEnvironment": fleetEnvironment, "allowDestructive": allowDestructive, "existing": result.Existing,
	}
	reservation, err := h.ensureFleetGitHubReservation(r, principal, planID, "fleet.github.apply-dispatch", payload)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	op := reservation
	if reservation.Status == model.OperationQueued {
		op, err = h.finishFleetGitHubReservation(r.Context(), reservation.ID, planID, "fleet.github.apply-dispatch", model.OperationSucceeded, "protected fleet apply dispatched", payload)
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "workflow dispatch exists but its durable Norn receipt could not be stored; retry safely")
		return
	}
	preventSensitiveResponseCaching(w)
	op.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusCreated, op)
}

// refuseAbandonedFleetPlan reports false (after writing the response) when
// planID is break-glass abandoned (B1/H7) or its abandonment state cannot be
// read. It runs before every replay branch: abandonment refuses every
// dispatch and execute call, replays included (plan.md §2.2, matching the
// etcd route). A plan that was never abandoned is unaffected.
func (h *Handler) refuseAbandonedFleetPlan(w http.ResponseWriter, r *http.Request, planID string) bool {
	abandoned, err := h.db.IsFleetPlanAbandonedForPlan(r.Context(), planID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not read the fleet target abandonment state")
		return false
	}
	if abandoned {
		WriteControlProblem(w, r, http.StatusConflict, lifecycle.CodeFleetTargetHolderAbandoned, "fleet target holder plan is permanently abandoned")
		return false
	}
	return true
}

// writeFleetGitHubFinishError reports why FinishFleetGitHubDispatch
// refused: a break-glass-abandoned plan (B1/H7) is 409
// fleet_target_holder_abandoned; any other cause keeps the existing 500
// fleet_github_receipt_failed. FinishFleetGitHubDispatch's own predicate
// (store/fleet_github_dispatches.go) costs no lock, so this diagnostic read
// is the only way to tell the two apart without weakening that predicate.
func (h *Handler) writeFleetGitHubFinishError(w http.ResponseWriter, r *http.Request, planID string, err error) {
	if abandoned, checkErr := h.db.IsFleetPlanAbandonedForPlan(r.Context(), planID); checkErr == nil && abandoned {
		WriteControlProblem(w, r, http.StatusConflict, lifecycle.CodeFleetTargetHolderAbandoned, "fleet target holder plan is permanently abandoned")
		return
	}
	WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "workflow dispatch exists but its protected binding could not be completed; retry safely")
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
	fleetEnvironment, problem := h.matchingFleetEnvironment()
	if problem != nil {
		WriteControlProblem(w, r, problem.status, problem.code, problem.detail)
		return "", false
	}
	return fleetEnvironment, true
}

// matchingFleetEnvironment is requireMatchingFleetEnvironment without the
// response, for callers (Fleet resource routes) that fold the refusal into
// their own flow.
func (h *Handler) matchingFleetEnvironment() (string, *routeProblem) {
	inventory, err := h.loadFleetInventory()
	if err != nil || inventory.Document == nil {
		return "", &routeProblem{http.StatusServiceUnavailable, "fleet_config_read_failed", "configured fleet root is unavailable for protected GitHub operations"}
	}
	fleetEnvironment, err := configuredFleetEnvironment(inventory.Document, h.cfg)
	if err != nil {
		return "", &routeProblem{http.StatusConflict, "fleet_config_invalid", "configured fleet root does not map to an allowed protected workflow environment"}
	}
	controlEnvironment := "development"
	if h.cfg != nil {
		controlEnvironment = h.cfg.EnvironmentID()
	}
	if !fleetEnvironmentMatchesControlPlane(controlEnvironment, fleetEnvironment, h.cfg) {
		return "", &routeProblem{http.StatusConflict, "fleet_environment_mismatch", "configured fleet root does not match this Norn control-plane environment"}
	}
	return fleetEnvironment, nil
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
	if !h.fleetGitHubAcceptanceAvailable(w, r) {
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

// Fleet GitHub actions are plan-scoped protected mutations. Their acceptance
// identity is deliberately independent of the caller credential so a retry by
// a rotated token or another authorized operator recovers the one durable
// external-action receipt. The initiating credential remains signed audit
// evidence, but must not make a second receipt possible for the same plan.
func (h *Handler) fleetGitHubOperationIdentity(ctx context.Context, planID, kind string) (store.OperationRequestIdentity, error) {
	if h == nil || h.operationStore == nil {
		return store.OperationRequestIdentity{}, fmt.Errorf("signed operation acceptance is unavailable")
	}
	authority, err := h.operationStore.Authority(ctx)
	if err != nil {
		return store.OperationRequestIdentity{}, err
	}
	return store.OperationRequestIdentity{
		Authority: authority,
		Actor:     store.OperationActor{Issuer: authority + "/fleet-github", Subject: planID},
		Kind:      kind,
		Resource:  planID,
		Key:       "protected-plan-receipt/v1",
	}, nil
}

func (h *Handler) fleetGitHubAcceptanceAvailable(w http.ResponseWriter, r *http.Request) bool {
	requestContext, ok := operationAcceptanceRequestContextFromRequest(r)
	if !ok || requestContext.ReceiptID == "" || h.operationStore == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "durable signed operation acceptance is unavailable")
		return false
	}
	if requestContext.ActorErr != nil || requestContext.Actor.Issuer == "" || requestContext.Actor.Subject == "" {
		WriteControlProblem(w, r, http.StatusConflict, "operation_actor_ambiguous", "the authenticated credential does not establish a stable operation actor")
		return false
	}
	return true
}

func (h *Handler) fleetGitHubAcceptanceAudit(r *http.Request) (store.AcceptanceAuditContext, error) {
	requestContext, ok := operationAcceptanceRequestContextFromRequest(r)
	if !ok || requestContext.ReceiptID == "" || h.operationStore == nil {
		return store.AcceptanceAuditContext{}, fmt.Errorf("signed operation acceptance is unavailable")
	}
	if requestContext.ActorErr != nil || requestContext.Actor.Issuer == "" || requestContext.Actor.Subject == "" {
		return store.AcceptanceAuditContext{}, fmt.Errorf("stable operation actor is unavailable")
	}
	return store.AcceptanceAuditContext{
		RequestReceiptID: requestContext.ReceiptID,
		RequestID:        requestContext.RequestID,
		CredentialID:     requestContext.Actor.CredentialID,
		DeviceID:         requestContext.Actor.DeviceID,
		Source:           requestContext.Actor.Source,
		Scopes:           append([]string(nil), requestContext.Actor.Scopes...),
	}, nil
}

func (h *Handler) existingFleetGitHubOperation(r *http.Request, planID, kind string) (*model.Operation, bool, error) {
	operation, found, err := h.resolveFleetGitHubOperation(r, planID, kind)
	if err != nil || !found {
		return operation, found, err
	}
	switch operation.Status {
	case model.OperationSucceeded:
		return operation, true, nil
	case model.OperationQueued:
		return operation, false, nil
	default:
		identity, identityErr := h.fleetGitHubOperationIdentity(r.Context(), planID, kind)
		if identityErr != nil {
			return nil, false, identityErr
		}
		return nil, false, &store.AcceptanceConflictError{Identity: identity}
	}
}

func (h *Handler) resolveFleetGitHubOperation(r *http.Request, planID, kind string) (*model.Operation, bool, error) {
	identity, err := h.fleetGitHubOperationIdentity(r.Context(), planID, kind)
	if err != nil {
		return nil, false, err
	}
	resolver, ok := h.operationStore.(store.OperationIdentityResolver)
	if !ok {
		return nil, false, fmt.Errorf("signed operation acceptance cannot resolve fleet GitHub receipts")
	}
	accepted, err := resolver.ResolveIdentity(r.Context(), identity)
	if errors.Is(err, store.ErrAcceptanceNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if accepted.Operation.Kind != kind || accepted.Operation.Ref != planID {
		return nil, false, &store.AcceptanceConflictError{Identity: identity}
	}
	return &accepted.Operation, true, nil
}

func (h *Handler) ensureFleetGitHubReservation(r *http.Request, principal AccessPrincipal, planID, kind string, payload map[string]interface{}) (*model.Operation, error) {
	if existing, found, err := h.resolveFleetGitHubOperation(r, planID, kind); err != nil {
		return nil, err
	} else if found {
		if existing.Status != model.OperationQueued && existing.Status != model.OperationSucceeded {
			identity, identityErr := h.fleetGitHubOperationIdentity(r.Context(), planID, kind)
			if identityErr != nil {
				return nil, identityErr
			}
			return nil, &store.AcceptanceConflictError{Identity: identity}
		}
		return existing, nil
	}
	return h.reserveFleetGitHubOperation(r, principal, planID, kind, payload)
}

// reserveFleetGitHubOperation admits a signed, immutable intent and its
// archive capacity before any GitHub write. Its identity is plan-scoped, so a
// crash can be retried by another authorized caller without creating another
// external action or receipt.
func (h *Handler) reserveFleetGitHubOperation(r *http.Request, _ AccessPrincipal, planID, kind string, payload map[string]interface{}) (*model.Operation, error) {
	identity, err := h.fleetGitHubOperationIdentity(r.Context(), planID, kind)
	if err != nil {
		return nil, err
	}
	audit, err := h.fleetGitHubAcceptanceAudit(r)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	op := &model.Operation{
		ID: uuid.NewString(), Kind: kind, Ref: planID, Status: model.OperationQueued,
		Risk: "GitOps mutation only; provider credentials remain in protected GitHub environments", Source: "control-api",
		Message: "protected Fleet GitHub action reserved", Payload: map[string]interface{}{"fleetGitHub": payload},
		Metadata: map[string]interface{}{}, StartedAt: now, UpdatedAt: now, MaxAttempts: 1,
	}
	acceptance := store.OperationAcceptance{Identity: identity, Operation: *op, Audit: audit, Semantics: map[string]interface{}{
		"fleetGitHub": map[string]interface{}{"planId": planID, "kind": kind},
	}}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		return nil, err
	}
	accepted, err := h.operationStore.Accept(r.Context(), acceptance)
	if err != nil {
		return nil, err
	}
	accepted.Operation.AttachReceipt()
	return &accepted.Operation, nil
}

type fleetGitHubCompletionCanonical struct {
	Schema      string                 `json:"schema"`
	OperationID string                 `json:"operationId"`
	PlanID      string                 `json:"planId"`
	Kind        string                 `json:"kind"`
	Status      model.OperationStatus  `json:"status"`
	Result      map[string]interface{} `json:"result"`
}

// finishFleetGitHubReservation signs the recovered GitHub outcome separately
// from the pre-dispatch acceptance. The operation metadata carries only this
// signed completion record, which archive verification re-proves before it
// accepts the terminal bundle.
func (h *Handler) finishFleetGitHubReservation(ctx context.Context, operationID, planID, kind string, status model.OperationStatus, message string, result map[string]interface{}) (*model.Operation, error) {
	if h == nil || h.cfg == nil {
		return nil, fmt.Errorf("Fleet GitHub completion signer is unavailable")
	}
	canonical, err := json.Marshal(fleetGitHubCompletionCanonical{Schema: "norn.fleet-github-completion/v1", OperationID: operationID, PlanID: planID, Kind: kind, Status: status, Result: result})
	if err != nil {
		return nil, err
	}
	signer, err := store.NewHMACAcceptanceSigner(h.cfg.AuditSigningKey, h.cfg.AuditPreviousSigningKeys...)
	if err != nil {
		return nil, err
	}
	signature, err := signer.Sign(ctx, canonical)
	if err != nil {
		return nil, err
	}
	completion := map[string]interface{}{
		"schema": "norn.fleet-github-completion/v1", "canonicalBytes": base64.StdEncoding.EncodeToString(canonical),
		"signingAlgorithm": signature.Algorithm, "signingKeyId": signature.KeyID, "signature": signature.Value,
		"result": result,
	}
	return h.db.FinishReservedFleetGitHubOperation(ctx, operationID, planID, kind, status, message, completion)
}
