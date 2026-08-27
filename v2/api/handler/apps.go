package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/connector"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

func (h *Handler) connectorStatus(ctx context.Context, spec *model.InfraSpec) (model.AppStatus, error) {
	status := model.AppStatus{Spec: spec}
	if h.workloads == nil {
		return status, nil
	}
	// The selected connector is configuration, not a per-application health
	// probe. Avoid invoking an external runtime command once for every app in a
	// list response merely to recover its stable name.
	status.WorkloadConnector = h.workloads.Name()
	if value, err := h.workloads.Status(ctx, spec.App); err == nil {
		status.NomadStatus = value // compatibility field retained for existing clients
	}
	for _, region := range spec.ResolvedRegions() {
		values, err := h.workloads.Poll(ctx, spec.App, region)
		if err != nil {
			continue
		}
		for _, value := range values {
			allocation := connector.ToModelAllocation(value)
			status.Allocations = append(status.Allocations, allocation)
			if allocation.Status == "running" && allocation.Healthy != nil && *allocation.Healthy {
				status.Healthy = true
			}
		}
	}
	status.AllocationSummary = summarizeAllocations(status.Allocations)
	return status, nil
}

func enrichAllocations(allocs []*nomadapi.AllocationListStub, n *nomad.Client) []model.Allocation {
	nodeCache := make(map[string]*nomad.NodeInfo)
	var out []model.Allocation
	for _, a := range allocs {
		alloc := model.Allocation{
			ID:        shortID(a.ID),
			TaskGroup: a.TaskGroup,
			Status:    a.ClientStatus,
			Lifecycle: allocationLifecycle(a.ClientStatus),
			NodeID:    shortID(a.NodeID),
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

func allocationLifecycle(status string) string {
	switch status {
	case "complete", "failed", "lost":
		return "retained"
	default:
		return "active"
	}
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func summarizeAllocations(allocations []model.Allocation) model.AllocationSummary {
	summary := model.AllocationSummary{
		Total:     len(allocations),
		ByProcess: make(map[string]model.ProcessAllocationCount),
		ByStatus:  make(map[string]int),
	}

	for _, alloc := range allocations {
		summary.ByStatus[alloc.Status]++
		if alloc.Status == "running" {
			summary.Running++
		}
		if alloc.Lifecycle == "retained" {
			summary.Retained++
		} else {
			summary.Active++
		}

		group := summary.ByProcess[alloc.TaskGroup]
		group.Total++
		if alloc.Status == "running" {
			group.Running++
		}
		if alloc.Lifecycle == "retained" {
			group.Retained++
		} else {
			group.Active++
		}
		summary.ByProcess[alloc.TaskGroup] = group
	}

	if len(summary.ByProcess) == 0 {
		summary.ByProcess = nil
	}
	if len(summary.ByStatus) == 0 {
		summary.ByStatus = nil
	}
	return summary
}

func (h *Handler) ListApps(w http.ResponseWriter, r *http.Request) {
	specs, err := model.DiscoverAllApps(h.cfg.AppsDir)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "app_discovery_failed", "failed to discover apps")
		return
	}

	var apps []model.AppStatus
	for _, spec := range specs {
		status, _ := h.connectorStatus(r.Context(), spec)

		apps = append(apps, status)
	}

	writeJSON(w, apps)
}

func (h *Handler) GetApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	specs, err := model.DiscoverAllApps(h.cfg.AppsDir)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "app_discovery_failed", "failed to discover apps")
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
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", fmt.Sprintf("app %s not found", id))
		return
	}

	status, _ := h.connectorStatus(r.Context(), spec)

	writeJSON(w, status)
}

func (h *Handler) RestartApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.workloads == nil {
		writeError(w, http.StatusServiceUnavailable, "workload connector not available")
		return
	}
	if err := h.workloads.Restart(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "restarted"})
}

func (h *Handler) ScaleApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.workloads == nil {
		writeError(w, http.StatusServiceUnavailable, "workload connector not available")
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

	if err := h.workloads.Scale(r.Context(), id, req.Group, req.Count); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "scaled"})
}
