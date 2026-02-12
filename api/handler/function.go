package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) FuncInvoke(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")
	if h.funcExecutor == nil {
		http.Error(w, "function executor not initialized", http.StatusServiceUnavailable)
		return
	}

	spec, err := h.loadSpec(appID)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	if !spec.IsFunction() {
		http.Error(w, "app is not a function", http.StatusBadRequest)
		return
	}

	body, _ := io.ReadAll(r.Body)

	exec, err := h.funcExecutor.Invoke(spec, r.Method, r.URL.Path, string(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, exec)
}

func (h *Handler) FuncHistory(w http.ResponseWriter, r *http.Request) {
	app := chi.URLParam(r, "id")
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	execs, err := h.db.ListFuncExecutions(r.Context(), app, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"executions": execs,
	})
}
