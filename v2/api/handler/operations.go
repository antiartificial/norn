package handler

import (
	"net/http"
	"strconv"

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
	writeJSON(w, map[string]interface{}{
		"operations": ops,
		"count":      len(ops),
	})
}

func (h *Handler) ActiveOperations(w http.ResponseWriter, r *http.Request) {
	ops, err := h.db.ListOperations(r.Context(), store.OperationFilter{Active: true, Limit: 100})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ops == nil {
		ops = []model.Operation{}
	}
	writeJSON(w, map[string]interface{}{
		"operations": ops,
		"count":      len(ops),
	})
}
