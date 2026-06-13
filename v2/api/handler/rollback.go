package handler

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
)

func (h *Handler) Rollback(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var spec *model.InfraSpec
	for _, s := range specs {
		if s.App == id {
			spec = s
			break
		}
	}
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}

	deployments, err := h.db.ListDeployments(ctx, id, 1)
	if err != nil || len(deployments) == 0 {
		writeError(w, http.StatusNotFound, "no current deployment found")
		return
	}
	current := deployments[0]

	prev, err := h.db.LastSuccessfulDeployment(ctx, id, current.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no previous successful deployment to roll back to")
		return
	}

	sagaID := h.pipeline.Rollback(spec, current, prev)
	writeJSON(w, map[string]string{
		"sagaId":   sagaID,
		"status":   "queued",
		"imageTag": prev.ImageTag,
	})
}
