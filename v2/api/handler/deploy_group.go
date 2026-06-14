package handler

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
)

func (h *Handler) ListDeployGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := model.DiscoverDeployGroups(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("discover deploy groups: %v", err))
		return
	}
	if groups == nil {
		groups = []*model.DeployGroup{}
	}
	writeJSON(w, map[string]interface{}{
		"groups": groups,
	})
}

func (h *Handler) DeployGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	var req struct {
		Ref string `json:"ref"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}

	groups, err := model.DiscoverDeployGroups(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("discover deploy groups: %v", err))
		return
	}

	var group *model.DeployGroup
	for _, g := range groups {
		if g.Name == name {
			group = g
			break
		}
	}
	if group == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("deploy group %s not found", name))
		return
	}

	type deployResult struct {
		App    string `json:"app"`
		SagaID string `json:"sagaId,omitempty"`
		Error  string `json:"error,omitempty"`
	}

	var deploys []deployResult
	for _, app := range group.Apps {
		spec := h.findSpec(app.App)
		if spec == nil {
			deploys = append(deploys, deployResult{
				App:   app.App,
				Error: fmt.Sprintf("app %s not found", app.App),
			})
			continue
		}
		sagaID := h.pipeline.Run(spec, req.Ref)
		deploys = append(deploys, deployResult{
			App:    app.App,
			SagaID: sagaID,
		})
	}

	writeJSON(w, map[string]interface{}{
		"group":   name,
		"deploys": deploys,
		"ref":     req.Ref,
	})
}
