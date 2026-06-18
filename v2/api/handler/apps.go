package handler

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/engine"
	"norn/v2/api/model"
)

func instancesToAllocations(instances []engine.Instance) []model.Allocation {
	var out []model.Allocation
	for _, inst := range instances {
		out = append(out, inst.ToAllocation())
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

		if h.engine != nil {
			jobStatus, err := h.engine.JobStatus(spec.App)
			if err == nil {
				status.NomadStatus = jobStatus
			}

			instances, err := h.engine.JobInstances(spec.App)
			if err == nil {
				status.Allocations = instancesToAllocations(instances)
				status.AllocationSummary = summarizeAllocations(status.Allocations)

				for _, inst := range instances {
					if inst.Healthy != nil && *inst.Healthy && inst.IsRunning() {
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

	if h.engine != nil {
		jobStatus, err := h.engine.JobStatus(spec.App)
		if err == nil {
			status.NomadStatus = jobStatus
		}

		instances, err := h.engine.JobInstances(spec.App)
		if err == nil {
			status.Allocations = instancesToAllocations(instances)
			status.AllocationSummary = summarizeAllocations(status.Allocations)
			for _, inst := range instances {
				if inst.Healthy != nil && *inst.Healthy && inst.IsRunning() {
					status.Healthy = true
					break
				}
			}
		}
	}

	writeJSON(w, status)
}

func (h *Handler) RestartApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not available")
		return
	}
	if err := h.engine.RestartJob(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "restarted"})
}

func (h *Handler) ScaleApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not available")
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

	if err := h.engine.ScaleJob(r.Context(), id, req.Group, req.Count); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "scaled"})
}
