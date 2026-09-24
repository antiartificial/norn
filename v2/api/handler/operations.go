package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func (h *Handler) ListOperations(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	active := r.URL.Query().Get("active") == "true" || r.URL.Query().Get("active") == "1"
	ops, err := h.db.ListOperations(r.Context(), store.OperationFilter{
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
		ops[i].AttachReceipt()
	}
	writeJSON(w, map[string]interface{}{
		"operations": ops,
		"count":      len(ops),
	})
}

func (h *Handler) ActiveOperations(w http.ResponseWriter, r *http.Request) {
	ops, err := h.db.ListOperations(r.Context(), store.OperationFilter{Active: true, ExcludeID: r.URL.Query().Get("excludeId"), Limit: 100})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ops == nil {
		ops = []model.Operation{}
	}
	for i := range ops {
		ops[i].AttachReceipt()
	}
	writeJSON(w, map[string]interface{}{
		"operations": ops,
		"count":      len(ops),
	})
}

func (h *Handler) GetOperation(w http.ResponseWriter, r *http.Request) {
	op, err := h.db.GetOperation(r.Context(), chi.URLParam(r, "id"))
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusNotFound, "operation_not_found", "operation not found")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_lookup_failed", "failed to load operation")
		return
	}
	if !releaseOperationReadable(r, op) {
		WriteControlProblem(w, r, http.StatusForbidden, "operation_read_forbidden", "workload token may read only its own compatible release operation")
		return
	}
	op.AttachReceipt()
	writeJSON(w, op)
}

func releaseOperationReadable(r *http.Request, op *model.Operation) bool {
	principal, present := AccessPrincipalFromRequest(r)
	if !present || principal.Legacy || principal.Allows(ScopeAdmin) || principal.Allows(ScopeAPIRead) || principal.Allows(ScopeAPIWrite) {
		return true
	}
	if op == nil || principal.App == "" || principal.App != op.App || principal.Environment == "" {
		return false
	}
	environment, _ := op.Metadata["environment"].(string)
	if environment != principal.Environment {
		return false
	}
	if !releaseOperationKindAllowed(principal, op) {
		return false
	}
	var requested CIIdentity
	if raw, ok := op.Metadata["requestCI"]; ok {
		encoded, _ := json.Marshal(raw)
		_ = json.Unmarshal(encoded, &requested)
	}
	if requested.Provider == "" && principal.Allows(ScopeReleaseStage) {
		if raw, ok := op.Metadata["candidate"]; ok {
			encoded, _ := json.Marshal(raw)
			var candidate model.ReleaseCandidate
			_ = json.Unmarshal(encoded, &candidate)
			requested = CIIdentity{Provider: candidate.Provider, Repository: candidate.Repository, RepositoryID: candidate.RepositoryID, RepositoryOwnerID: candidate.OwnerID, RunID: candidate.RunID, RunAttempt: candidate.RunAttempt, WorkflowRef: candidate.WorkflowRef, WorkflowSHA: candidate.WorkflowSHA, JobWorkflowRef: candidate.SignerWorkflowRef, JobWorkflowSHA: candidate.SignerWorkflowSHA, Ref: candidate.Ref, SHA: candidate.Attestation.MaterialSHA}
		}
	}
	return ciIdentityMatches(principal.CI, &requested)
}

func releaseOperationKindAllowed(principal AccessPrincipal, op *model.Operation) bool {
	if principal.Allows(ScopeReleaseStage) {
		return (op.Kind == "app.preflight" || op.Kind == "app.deploy") && op.Metadata["promotionQualification"] == nil && op.Metadata["releaseRollback"] == nil
	}
	if principal.Allows(ScopeReleaseQualify) {
		return op.Kind == "release.qualification"
	}
	if principal.Allows(ScopeReleasePromote) {
		return op.Kind == "app.deploy" && op.Metadata["promotionQualification"] != nil
	}
	return principal.Allows(ScopeReleaseRollback) && op.Metadata["releaseRollback"] == true
}

func ciIdentityMatches(actual, expected *CIIdentity) bool {
	return actual != nil && expected != nil && expected.Provider != "" && actual.Provider == expected.Provider && actual.Repository == expected.Repository && actual.RepositoryID == expected.RepositoryID && actual.RepositoryOwnerID == expected.RepositoryOwnerID && actual.RunID == expected.RunID && actual.RunAttempt == expected.RunAttempt && actual.WorkflowRef == expected.WorkflowRef && actual.WorkflowSHA == expected.WorkflowSHA && actual.JobWorkflowRef == expected.JobWorkflowRef && actual.JobWorkflowSHA == expected.JobWorkflowSHA && actual.Ref == expected.Ref && actual.SHA == expected.SHA
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
	if existing.Kind == "fleet.github.pull-request" || existing.Kind == "fleet.github.apply-dispatch" {
		WriteControlProblem(w, r, http.StatusConflict, "fleet_github_reconciliation_required", "Fleet GitHub reservations require remote reconciliation before cancellation")
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
