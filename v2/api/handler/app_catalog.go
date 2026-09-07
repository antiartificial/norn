package handler

import "net/http"

// rejectAppCatalogMutation makes a root-owned reviewed catalog an explicit API
// state. It is deliberately limited to endpoints that redefine files below
// NORN_APPS_DIR; deploy and recovery operations still consume that catalog.
func (h *Handler) rejectAppCatalogMutation(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.cfg == nil || !h.cfg.IsAppCatalogReadOnly() {
		return false
	}
	WriteControlProblem(w, r, http.StatusConflict, "app_catalog_read_only", "the reviewed app catalog is read-only; update it through its approved source")
	return true
}
