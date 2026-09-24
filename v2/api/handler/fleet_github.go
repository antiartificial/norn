package handler

import (
	"context"
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
	release, locked, lockErr := h.db.AcquireAppOperationLock(r.Context(), "fleet-github-pull-request:"+plan.ID)
	if lockErr != nil || !locked {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_pull_request_in_progress", "another protected pull request is resolving this fleet plan")
		return
	}
	defer release()
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
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_plan_stale", "GitHub main changed after this Norn capacity plan; create a fresh plan")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_pull_request_failed", "GitHub could not create or recover the fleet pull request")
		return
	}
	op, err := h.db.FinishReservedFleetGitHubOperation(r.Context(), reservation.ID, plan.ID, "fleet.github.pull-request", "fleet pull request opened", map[string]interface{}{
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
	if existing, found, err := h.existingFleetGitHubOperation(r, plan.ID, "fleet.github.apply-dispatch"); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	} else if found {
		existing.AttachReceipt()
		writeJSON(w, existing)
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
	// This lock serializes durable binding creation and external dispatch for a
	// plan. It prevents two API requests from racing past a find-then-dispatch
	// check with the same approved infrastructure intent.
	release, locked, lockErr := h.db.AcquireAppOperationLock(r.Context(), "fleet-github-dispatch:"+plan.ID)
	if lockErr != nil || !locked {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_in_progress", "another protected dispatch is resolving this fleet plan")
		return
	}
	defer release()
	if existing, found, err := h.existingFleetGitHubOperation(r, plan.ID, "fleet.github.apply-dispatch"); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	} else if found {
		existing.AttachReceipt()
		writeJSON(w, existing)
		return
	}
	reservation, err := h.reserveFleetGitHubOperation(r, principal, plan.ID, "fleet.github.apply-dispatch", map[string]interface{}{
		"planId": plan.ID, "planDigest": typed.Digest, "sourceDigest": typed.SourceDigest,
		"pool": typed.Pool, "action": typed.Action, "allowDestructive": request.AllowDestructive,
	})
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	binding, bindingErr := h.db.GetFleetGitHubDispatch(r.Context(), plan.ID)
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
		nonce, nonceHash, nonceErr := newFleetDispatchNonce()
		if nonceErr != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not create the protected dispatch binding")
			return
		}
		binding, bindingErr = h.db.CreateFleetGitHubDispatch(r.Context(), store.FleetGitHubDispatch{
			PlanID: plan.ID, PlanRunID: approved.PlanRunID, PlanSHA256: approved.PlanSHA, ApprovedHeadSHA: approved.ApprovedHeadSHA,
			FleetEnvironment: fleetEnvironment, AllowDestructive: request.AllowDestructive, DispatchNonce: nonce, DispatchNonceSHA256: nonceHash,
		})
		if bindingErr != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not persist the protected dispatch binding")
			return
		}
	} else if bindingErr != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "could not read the protected dispatch binding")
		return
	}
	if binding.FleetEnvironment != fleetEnvironment || binding.AllowDestructive != request.AllowDestructive {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_dispatch_binding_mismatch", "this plan already has a different protected dispatch binding")
		return
	}
	approved := &githubapp.Dispatch{PlanRunID: binding.PlanRunID, PlanSHA: binding.PlanSHA256, ApprovedHeadSHA: binding.ApprovedHeadSHA}
	result, err := h.fleetGitHub.DispatchBoundPlan(r.Context(), plan.ID, fleetEnvironment, request.AllowDestructive, approved, binding.DispatchNonce)
	if errors.Is(err, githubapp.ErrNotReady) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_plan_not_ready", "merge the fleet pull request and wait for its protected main-branch plan workflow to succeed")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadGateway, "fleet_github_dispatch_failed", "GitHub could not dispatch or recover the protected apply workflow")
		return
	}
	if _, err := h.db.FinishFleetGitHubDispatch(r.Context(), plan.ID, binding.DispatchNonceSHA256, result.RunID, result.URL); err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "fleet_github_receipt_failed", "workflow dispatch exists but its protected binding could not be completed; retry safely")
		return
	}
	op, err := h.db.FinishReservedFleetGitHubOperation(r.Context(), reservation.ID, plan.ID, "fleet.github.apply-dispatch", "protected fleet apply dispatched", map[string]interface{}{
		"planId": plan.ID, "runId": result.RunID, "url": result.URL, "planRunId": result.PlanRunID,
		"planSha256": result.PlanSHA, "approvedHeadSha": result.ApprovedHeadSHA, "fleetEnvironment": fleetEnvironment, "allowDestructive": request.AllowDestructive, "existing": result.Existing,
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
	fleetEnvironment, err := configuredFleetEnvironment(inventory.Document)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_config_invalid", "configured fleet root does not map to an allowed protected workflow environment")
		return "", false
	}
	controlEnvironment := "development"
	if h.cfg != nil {
		controlEnvironment = h.cfg.EnvironmentID()
	}
	if !fleetEnvironmentMatchesControlPlane(controlEnvironment, fleetEnvironment) {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_environment_mismatch", "configured fleet root does not match this Norn control-plane environment")
		return "", false
	}
	return fleetEnvironment, true
}

func fleetEnvironmentMatchesControlPlane(controlEnvironment, fleetEnvironment string) bool {
	if controlEnvironment == "development" {
		return true
	}
	return strings.HasPrefix(fleetEnvironment, controlEnvironment+"/")
}

func configuredFleetEnvironment(document *fleet.Document) (string, error) {
	if document == nil || (document.Metadata.Environment != "staging" && document.Metadata.Environment != "production") || document.Cluster.Region != "nyc3" {
		return "", fmt.Errorf("fleet metadata environment and cluster region are not supported")
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
	switch accepted.Operation.Status {
	case model.OperationSucceeded:
		return &accepted.Operation, true, nil
	case model.OperationQueued:
		// A process may have crashed after admission. Re-entering the action is
		// safe because the GitHub clients recover by deterministic branch or
		// dispatch nonce, and completion rejects a different result.
		return &accepted.Operation, false, nil
	default:
		return nil, false, &store.AcceptanceConflictError{Identity: identity}
	}
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
