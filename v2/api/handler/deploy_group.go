package handler

import (
	"fmt"
	"net/http"
	"sort"

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
	if h.productionRequiresSignedPromotion() {
		WriteControlProblem(w, r, http.StatusConflict, "signed_promotion_required", "production deploy groups cannot bypass per-app signed staging promotion")
		return
	}
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
	members := make([]string, 0, len(group.Apps))
	for _, app := range group.Apps {
		members = append(members, app.App)
	}
	sort.Strings(members)
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"group": name, "members": members, "ref": req.Ref})
	if !ok {
		return
	}
	queued, err := h.pipeline.RunGroup(r.Context(), group, req.Ref, h.cfg.AppsDir, enqueue)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"group":       name,
		"operationId": queued.OperationID,
		"replayed":    queued.Replayed,
		"deploys":     queued.Deploys,
		"ref":         req.Ref,
	})
}
