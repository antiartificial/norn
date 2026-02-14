package handler

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/saga"
)

func (h *Handler) GetSagaEvents(w http.ResponseWriter, r *http.Request) {
	sagaID := chi.URLParam(r, "sagaId")
	events, err := h.sagaStore.ListBySaga(r.Context(), sagaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []saga.Event{}
	}
	writeJSON(w, events)
}

func (h *Handler) ListRecentSaga(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	app := r.URL.Query().Get("app")

	if app != "" {
		events, err := h.sagaStore.ListByApp(r.Context(), app, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if events == nil {
			events = []saga.Event{}
		}
		writeJSON(w, events)
		return
	}

	events, err := h.sagaStore.ListRecent(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []saga.Event{}
	}
	writeJSON(w, events)
}
