package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/store"
)

type mysqlRestoreInspector interface {
	InspectMySQLRestore(context.Context, string) (store.MySQLRestoreInspection, error)
}

// GetMySQLRestoreInspection exposes only verified, redacted identity evidence
// for a restore already inside the private execution lane. It grants no
// restore, retry, completion, or acknowledgement capability.
func (h *Handler) GetMySQLRestoreInspection(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	inspector, ok := h.operationStore.(mysqlRestoreInspector)
	if !ok {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "mysql_restore_inspection_unavailable", "signed restore inspection is unavailable")
		return
	}
	inspection, err := inspector.InspectMySQLRestore(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrMySQLRestoreInspectionNotFound) {
		WriteControlProblem(w, r, http.StatusNotFound, "mysql_restore_inspection_not_found", "an ambiguous MySQL restore was not found")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "mysql_restore_inspection_unverified", "signed MySQL restore evidence could not be verified")
		return
	}
	writeJSON(w, inspection)
}
