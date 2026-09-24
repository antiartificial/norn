package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

var maintenanceRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,199}$`)

type platformRequest struct {
	Ref       string `json:"ref"`
	Mode      string `json:"mode,omitempty"`
	DrainMode string `json:"drainMode,omitempty"`
}

func (h *Handler) QueuePlatformPreflight(w http.ResponseWriter, r *http.Request) {
	var req platformRequest
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	if !maintenanceRefPattern.MatchString(req.Ref) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_platform_ref", "invalid platform ref")
		return
	}
	h.queueMaintenanceOperation(w, r, "platform.preflight", req.Ref, "read-only candidate build", map[string]interface{}{"ref": req.Ref})
}

func (h *Handler) QueuePlatformUpgrade(w http.ResponseWriter, r *http.Request) {
	var req platformRequest
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	if !maintenanceRefPattern.MatchString(req.Ref) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_platform_ref", "invalid platform ref")
		return
	}
	if req.Mode == "" {
		req.Mode = "restart"
	}
	if req.Mode != "restart" && req.Mode != "proxy" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_upgrade_mode", "mode must be restart or proxy")
		return
	}
	if req.DrainMode == "" {
		req.DrainMode = "fail"
	}
	if req.DrainMode != "fail" && req.DrainMode != "wait" && req.DrainMode != "force" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_drain_mode", "drainMode must be fail, wait, or force")
		return
	}
	h.queueMaintenanceOperation(w, r, "platform.upgrade", req.Ref, "control-plane replacement", map[string]interface{}{
		"ref": req.Ref, "mode": req.Mode, "drainMode": req.DrainMode,
	})
}

func (h *Handler) QueuePlatformSmoke(w http.ResponseWriter, r *http.Request) {
	h.queueMaintenanceOperation(w, r, "platform.smoke", "", "read-only platform assurance", map[string]interface{}{})
}

func (h *Handler) QueuePlatformRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SHA string `json:"sha"`
	}
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !maintenanceRefPattern.MatchString(req.SHA) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_release_sha", "invalid release sha")
		return
	}
	h.queueMaintenanceOperation(w, r, "platform.rollback", req.SHA, "control-plane rollback", map[string]interface{}{"sha": req.SHA})
}

func (h *Handler) QueueHostAssurance(w http.ResponseWriter, r *http.Request) {
	h.queueMaintenanceOperation(w, r, "host.assure", "", "bounded host repair and endpoint probes", map[string]interface{}{})
}

func (h *Handler) queueMaintenanceOperation(w http.ResponseWriter, r *http.Request, kind, ref, risk string, payload map[string]interface{}) {
	requiredScope := ScopePlatformOperate
	if kind == "host.assure" {
		requiredScope = ScopeHostOperate
	}
	if _, ok := requireControlScope(w, r, requiredScope); !ok {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "a non-empty Idempotency-Key of at most 200 characters is required")
		return
	}
	requestContext, ok := operationAcceptanceRequestContextFromRequest(r)
	if !ok || requestContext.ReceiptID == "" || h.operationStore == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "durable signed operation acceptance is unavailable")
		return
	}
	if requestContext.ActorErr != nil || requestContext.Actor.Issuer == "" || requestContext.Actor.Subject == "" {
		WriteControlProblem(w, r, http.StatusConflict, "operation_actor_ambiguous", "the authenticated credential does not establish a stable operation actor")
		return
	}
	resource := "control-plane"
	if kind == "host.assure" {
		resource = "host"
	}
	authority, err := h.operationStore.Authority(r.Context())
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "control authority is unavailable")
		return
	}
	now := time.Now().UTC()
	op := model.Operation{
		ID: uuid.NewString(), Kind: kind, Ref: ref, Status: model.OperationQueued,
		Risk: risk, Source: "control-api", Message: "queued " + kind,
		Payload: payload, Metadata: map[string]interface{}{}, StartedAt: now, MaxAttempts: 1,
	}
	acceptance := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{
			Authority: authority,
			Actor: store.OperationActor{
				Issuer: requestContext.Actor.Issuer, Subject: requestContext.Actor.Subject,
			},
			Kind: kind, Resource: resource, Key: idempotencyKey,
		},
		Operation: op,
		Audit: store.AcceptanceAuditContext{
			RequestReceiptID: requestContext.ReceiptID,
			RequestID:        requestContext.RequestID,
			CredentialID:     requestContext.Actor.CredentialID,
			DeviceID:         requestContext.Actor.DeviceID,
			Source:           requestContext.Actor.Source,
			Scopes:           append([]string{}, requestContext.Actor.Scopes...),
		},
		Semantics: payload,
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_acceptance_failed", "failed to fingerprint operation request")
		return
	}
	accepted, err := h.operationStore.Accept(r.Context(), acceptance)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	if accepted.Replayed {
		writeJSON(w, accepted.Operation)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, accepted.Operation)
}

func writeOperationAcceptanceError(w http.ResponseWriter, r *http.Request, err error) {
	var evidenceReserve *store.EvidenceReserveExhaustedError
	if errors.As(err, &evidenceReserve) {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "evidence_reserve_exhausted", "protected operation receipt capacity is exhausted; retry after archived evidence is available")
		return
	}
	var runnerAdmission *store.FleetRunnerAttemptAdmissionError
	if errors.As(err, &runnerAdmission) {
		status := http.StatusConflict
		if runnerAdmission.Code == "fleet_runner_attempt_identity_mismatch" || runnerAdmission.Code == "fleet_runner_attempt_dispatch_mismatch" {
			status = http.StatusForbidden
		}
		WriteControlProblem(w, r, status, runnerAdmission.Code, runnerAdmission.Reason)
		return
	}
	var fleetAdmission *store.FleetReconciliationAdmissionError
	if errors.As(err, &fleetAdmission) {
		status := http.StatusConflict
		if fleetAdmission.Code == "fleet_reconciliation_identity_mismatch" {
			status = http.StatusForbidden
		}
		WriteControlProblem(w, r, status, fleetAdmission.Code, fleetAdmission.Reason)
		return
	}
	// Database target refusals come from the same resolver execution uses;
	// their reasons name catalog resources and never carry credentials.
	var targetErr *pipeline.DatabaseTargetError
	var resolverErr *database.ResolverError
	if errors.As(err, &targetErr) || errors.As(err, &resolverErr) {
		WriteControlProblem(w, r, http.StatusConflict, "database_target_rejected", err.Error())
		return
	}
	switch {
	case errors.Is(err, store.ErrAcceptanceExpired):
		WriteControlProblem(w, r, http.StatusGone, "idempotency_window_expired", "Idempotency-Key replay window expired; use a new key for a new operation")
	case errors.Is(err, store.ErrAcceptanceConflict):
		WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different operation request")
	case errors.Is(err, store.ErrLegacyReplayAmbiguous):
		WriteControlProblem(w, r, http.StatusConflict, "legacy_idempotency_ambiguous", "a legacy operation with this key cannot be safely attributed; reconcile it before retrying")
	case errors.Is(err, store.ErrAcceptanceAdmission):
		WriteControlProblem(w, r, http.StatusConflict, "operation_admission_rejected", "another conflicting operation is already active")
	case errors.Is(err, store.ErrAcceptanceIndeterminate):
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_indeterminate", "operation acceptance is indeterminate; retry with the same Idempotency-Key")
	case errors.Is(err, store.ErrAcceptanceAuthority), errors.Is(err, store.ErrAcceptanceSignature):
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "durable signed operation acceptance is unavailable")
	default:
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_acceptance_failed", "failed to accept operation")
	}
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, target interface{}) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	if err := decodeControlJSON(w, r, target); err != nil && err != io.EOF {
		return fmt.Errorf("invalid request body")
	}
	return nil
}
