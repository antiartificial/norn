package handler

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

func enrichAllocations(allocs []*nomadapi.AllocationListStub, n *nomad.Client) []model.Allocation {
	nodeCache := make(map[string]*nomad.NodeInfo)
	var out []model.Allocation
	for _, a := range allocs {
		alloc := model.Allocation{
			ID:        a.ID[:8],
			TaskGroup: a.TaskGroup,
			Status:    a.ClientStatus,
			NodeID:    a.NodeID[:8],
		}
		if a.DeploymentStatus != nil {
			alloc.Healthy = a.DeploymentStatus.Healthy
		}
		if ni, ok := nodeCache[a.NodeID]; ok {
			alloc.NodeAddress = ni.Address
			alloc.NodeName = ni.Name
			alloc.NodeProvider = ni.Provider
			alloc.NodeRegion = ni.Region
		} else if ni, err := n.NodeInfo(a.NodeID); err == nil {
			nodeCache[a.NodeID] = ni
			alloc.NodeAddress = ni.Address
			alloc.NodeName = ni.Name
			alloc.NodeProvider = ni.Provider
			alloc.NodeRegion = ni.Region
		}
		out = append(out, alloc)
	}
	return out
}

func (h *Handler) ListApps(w http.ResponseWriter, r *http.Request) {
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var apps []model.AppStatus
	for _, spec := range specs {
		status := model.AppStatus{
			Spec:    spec,
			Healthy: false,
		}

		if h.nomad != nil {
			jobStatus, err := h.nomad.JobStatus(spec.App)
			if err == nil {
				status.NomadStatus = jobStatus
			}

			allocs, err := h.nomad.JobAllocations(spec.App)
			if err == nil {
				status.Allocations = enrichAllocations(allocs, h.nomad)

				for _, a := range allocs {
					if a.ClientStatus == "running" && a.DeploymentStatus != nil && a.DeploymentStatus.Healthy != nil && *a.DeploymentStatus.Healthy {
						status.Healthy = true
						break
					}
				}
			}
		}

		apps = append(apps, status)
	}

	writeJSON(w, apps)
}

func (h *Handler) GetApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
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

	status := model.AppStatus{
		Spec:    spec,
		Healthy: false,
	}

	if h.nomad != nil {
		jobStatus, err := h.nomad.JobStatus(spec.App)
		if err == nil {
			status.NomadStatus = jobStatus
		}

		allocs, err := h.nomad.JobAllocations(spec.App)
		if err == nil {
			status.Allocations = enrichAllocations(allocs, h.nomad)
			for _, a := range allocs {
				if a.ClientStatus == "running" && a.DeploymentStatus != nil && a.DeploymentStatus.Healthy != nil && *a.DeploymentStatus.Healthy {
					status.Healthy = true
					break
				}
			}
		}
	}

	if h.consul != nil {
		for procName := range spec.Processes {
			svcName := fmt.Sprintf("%s-%s", spec.App, procName)
			health, err := h.consul.ServiceHealthChecks(svcName)
			if err == nil {
				_ = health
			}
		}
	}

	writeJSON(w, status)
}

func (h *Handler) RestartApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.nomad == nil {
		writeError(w, http.StatusServiceUnavailable, "nomad not connected")
		return
	}
	if err := h.nomad.RestartJob(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "restarted"})
}

func (h *Handler) ScaleApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.nomad == nil {
		writeError(w, http.StatusServiceUnavailable, "nomad not connected")
		return
	}

	var req struct {
		Group string `json:"group"`
		Count int    `json:"count"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Group == "" || req.Count < 0 {
		writeError(w, http.StatusBadRequest, "group and count required")
		return
	}

	if err := h.nomad.ScaleJob(id, req.Group, req.Count); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "scaled"})
}
