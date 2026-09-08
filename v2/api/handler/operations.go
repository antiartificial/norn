package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func (h *Handler) ListOperations(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	if h == nil || h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_store_unavailable", "durable operation storage is unavailable")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	active := r.URL.Query().Get("active") == "true" || r.URL.Query().Get("active") == "1"
	ops, err := h.db.ListOperationSummaries(r.Context(), store.OperationFilter{
		App:    r.URL.Query().Get("app"),
		Kind:   r.URL.Query().Get("kind"),
		Status: r.URL.Query().Get("status"),
		Active: active,
		Limit:  limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ops == nil {
		ops = []model.Operation{}
	}
	for i := range ops {
		ops[i] = operationSummary(ops[i])
	}
	writeJSON(w, map[string]interface{}{
		"operations": ops,
		"count":      len(ops),
	})
}

func (h *Handler) ActiveOperations(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	if h == nil || h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_store_unavailable", "durable operation storage is unavailable")
		return
	}
	ops, err := h.db.ListOperationSummaries(r.Context(), store.OperationFilter{Active: true, ExcludeID: r.URL.Query().Get("excludeId"), Limit: 100})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ops == nil {
		ops = []model.Operation{}
	}
	for i := range ops {
		ops[i] = operationSummary(ops[i])
	}
	writeJSON(w, map[string]interface{}{
		"operations": ops,
		"count":      len(ops),
	})
}

func operationSummary(operation model.Operation) model.Operation {
	operation.AttachReceipt()
	if strings.HasPrefix(operation.Kind, "fleet.github.") {
		// Fleet delivery uses these two non-secret display fields to reconnect
		// a plan to its protected GitHub review/apply URL.
		operation.Payload = map[string]interface{}{"planId": operationStringValue(operation.Payload, "planId"), "url": operationStringValue(operation.Payload, "url")}
	} else {
		operation.Payload = nil
	}
	operation.Metadata = nil
	return operation
}

func operationStringValue(values map[string]interface{}, key string) string {
	value, _ := values[key].(string)
	return value
}

func (h *Handler) GetOperation(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	op, err := h.db.GetOperation(r.Context(), chi.URLParam(r, "id"))
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusNotFound, "operation_not_found", "operation not found")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_lookup_failed", "failed to load operation")
		return
	}
	if !authorizeOperationRead(w, r, op) {
		return
	}
	if principal, exists := AccessPrincipalFromRequest(r); exists && principal.CI != nil && !principal.Legacy && !principal.Allows(ScopeAdmin) && !principal.Allows(ScopeAPIRead) {
		writeJSON(w, operationPollView(op))
		return
	}
	op.AttachReceipt()
	writeJSON(w, op)
}

func operationPollView(op *model.Operation) map[string]interface{} {
	if op == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{
		"id": op.ID, "kind": op.Kind, "app": op.App, "ref": op.Ref,
		"status": op.Status, "message": op.Message, "lastError": op.LastError,
		"attempts": op.Attempts, "maxAttempts": op.MaxAttempts,
		"startedAt": op.StartedAt, "updatedAt": op.UpdatedAt, "finishedAt": op.FinishedAt,
	}
}

// authorizeOperationRead keeps workflow polling narrow without granting a
// release or Fleet token general api:read access. The token must be the exact
// app/lane/CI identity that created the operation; ordinary API readers and
// administrators retain their existing compatibility.
func authorizeOperationRead(w http.ResponseWriter, r *http.Request, op *model.Operation) bool {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok {
		// In compatibility development mode the global middleware has already
		// admitted trusted loopback/access-grant traffic without synthesizing a
		// principal. Explicit-auth and non-local traffic cannot reach this branch.
		return true
	}
	if principal.Legacy || principal.Allows(ScopeAdmin) || principal.Allows(ScopeAPIRead) {
		return true
	}
	if op == nil || principal.CI == nil || !operationIdentityMatchesPrincipal(op, principal) {
		WriteControlProblem(w, r, http.StatusForbidden, "operation_read_forbidden", "token is not bound to this operation")
		return false
	}
	environment, _ := op.Metadata["environment"].(string)
	if environment == "" || environment != principal.Environment || environment != principal.CI.Environment {
		WriteControlProblem(w, r, http.StatusForbidden, "operation_read_forbidden", "token is not bound to this operation environment")
		return false
	}
	if strings.HasPrefix(op.Kind, "fleet.") {
		if principal.Allows(ScopeFleetOperate) {
			return true
		}
	} else if op.App == principal.App {
		// The external-admission token may poll only its own independently
		// admitted direct-workload receipt. It is not a general staging deploy
		// token and cannot read ordinary app.deploy operations.
		if _, external := op.Metadata["externalFleetReceipt"]; external && op.Kind == "app.deploy" && principal.Allows(ScopeFleetExternalAdmission) {
			return true
		}
		requiredScope := operationReleaseReadScope(op, environment)
		if requiredScope != "" && principal.Allows(requiredScope) {
			return true
		}
	}
	WriteControlProblem(w, r, http.StatusForbidden, "operation_read_forbidden", "token scope is not valid for this operation")
	return false
}

func operationReleaseReadScope(op *model.Operation, environment string) string {
	if op == nil {
		return ""
	}
	switch op.Kind {
	case "release.attestation":
		return ScopeReleaseAttest
	case "release.qualification":
		return ScopeReleaseQualify
	case "app.preflight":
		if environment == "staging" {
			return ScopeReleaseStage
		}
	case "app.deploy":
		if environment == "staging" {
			return ScopeReleaseStage
		}
		if environment == "production" {
			return ScopeReleasePromote
		}
	case "app.rollback":
		releaseRollback, _ := op.Metadata["releaseRollback"].(bool)
		if environment == "production" && releaseRollback {
			return ScopeReleaseRollback
		}
	}
	return ""
}

func operationIdentityMatchesPrincipal(op *model.Operation, principal AccessPrincipal) bool {
	if op == nil || op.Metadata == nil {
		return false
	}
	// A ten-minute workflow token may be refreshed while the operation runs;
	// bind to the stable verified CI identity and subject, not the token JTI.
	if subject, _ := op.Metadata["principal"].(string); subject == "" || subject != principal.Subject {
		return false
	}
	rawCI, ok := op.Metadata["requestCI"]
	if !ok || rawCI == nil {
		return false
	}
	encoded, err := json.Marshal(rawCI)
	if err != nil {
		return false
	}
	var operationCI CIIdentity
	if json.Unmarshal(encoded, &operationCI) != nil {
		return false
	}
	return operationCI == *principal.CI
}

func (h *Handler) CancelOperation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, lookupErr := h.db.GetOperation(r.Context(), id)
	if lookupErr == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusNotFound, "operation_not_found", "operation not found")
		return
	}
	if lookupErr != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_lookup_failed", "failed to load operation")
		return
	}
	requestedBy := "control-client"
	required := ScopeAPIWrite
	if len(existing.Kind) >= 9 && existing.Kind[:9] == "platform." {
		required = ScopePlatformOperate
	}
	if len(existing.Kind) >= 5 && existing.Kind[:5] == "host." {
		required = ScopeHostOperate
	}
	principal, ok := requireControlScope(w, r, required)
	if !ok {
		return
	}
	if principal.Subject != "" {
		requestedBy = principal.Subject
	}
	op, canceled, err := h.db.CancelQueuedOperation(r.Context(), id, requestedBy)
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusNotFound, "operation_not_found", "operation not found")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_cancel_failed", "failed to cancel operation")
		return
	}
	if !canceled {
		code := "operation_not_cancelable"
		detail := "only queued operations can be canceled; running work requires kind-specific cooperative cancellation"
		if op.Status.Terminal() {
			code, detail = "operation_already_finished", "the operation has already reached a terminal state"
		}
		WriteControlProblem(w, r, http.StatusConflict, code, detail)
		return
	}
	op.AttachReceipt()
	writeJSON(w, op)
}
