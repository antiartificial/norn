package handler

import (
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
	op.AttachReceipt()
	writeJSON(w, op)
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
