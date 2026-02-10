package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) ListSnapshots(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")
	_ = appID
	// TODO: list pg_dump files from snapshot directory
	writeJSON(w, []interface{}{})
}

func (h *Handler) RestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")
	ts := chi.URLParam(r, "ts")
	_, _ = appID, ts
	// TODO: pg_restore from snapshot
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}
