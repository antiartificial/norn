package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/retention"
	"norn/v2/api/saga"
)

// GetAppSagaHistory returns a saga's complete history (hot events merged
// with verified archived bundles). Historical reads use the same scope and
// app binding as live reads: api:read, and an app-bound principal may read
// only its own app. A saga is served only under the app it belongs to.
// Pruned history that cannot be verified is an explicit error, never a
// partial answer.
func (h *Handler) GetAppSagaHistory(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireControlScope(w, r, ScopeAPIRead)
	if !ok {
		return
	}
	appID, sagaID := chi.URLParam(r, "id"), chi.URLParam(r, "sagaId")
	if principal.App != "" && principal.App != appID {
		WriteControlProblem(w, r, http.StatusForbidden, "app_history_forbidden", "this credential may read only its own app's history")
		return
	}
	if history, ok := h.sagaStore.(*retention.HistoryStore); ok {
		owner, err := history.SagaApp(r.Context(), sagaID)
		if err != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "history_lookup_failed", "saga ownership could not be read")
			return
		}
		if owner != appID {
			WriteControlProblem(w, r, http.StatusNotFound, "saga_not_found", "saga was not found for this app")
			return
		}
	}
	events, err := h.sagaStore.ListBySaga(r.Context(), sagaID)
	var unavailable *retention.ErrArchivedHistoryUnavailable
	if errors.As(err, &unavailable) {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "archived_history_unavailable", "archived history could not be verified; it is not served partially")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "history_lookup_failed", "saga history could not be read")
		return
	}
	for _, event := range events {
		if event.App != appID {
			WriteControlProblem(w, r, http.StatusNotFound, "saga_not_found", "saga was not found for this app")
			return
		}
	}
	if len(events) == 0 {
		WriteControlProblem(w, r, http.StatusNotFound, "saga_not_found", "saga was not found for this app")
		return
	}
	writeJSON(w, map[string]interface{}{"app": appID, "sagaId": sagaID, "events": events})
}

// GetEvidenceArchiveHealth reports archive state for operators: pending,
// verified and pruned bundles, the oldest pending age and the latest
// publication error (outages keep evidence pending and hot).
func (h *Handler) GetEvidenceArchiveHealth(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "evidence_archive_unavailable", "the control store is unavailable")
		return
	}
	history, ok := h.sagaStore.(*retention.HistoryStore)
	configured := ok && history.Archive != nil
	health, err := retention.ReadHealth(r.Context(), h.db)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "evidence_archive_unavailable", "archive state could not be read")
		return
	}
	reserve, err := h.db.EvidenceReserve(r.Context())
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "evidence_archive_unavailable", "evidence reserve could not be read")
		return
	}
	writeJSON(w, map[string]interface{}{"configured": configured, "health": health, "reserve": reserve})
}

var _ saga.Store = (*retention.HistoryStore)(nil)
