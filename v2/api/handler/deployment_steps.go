package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
)

func (h *Handler) ListDeploymentSteps(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	steps, err := h.db.ListDeploymentSteps(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if steps == nil {
		steps = []model.DeploymentStep{}
	}
	writeJSON(w, map[string]interface{}{
		"steps": steps,
		"count": len(steps),
	})
}
