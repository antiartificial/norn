package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

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
				status.AllocationSummary = summarizeAllocations(status.Allocations)

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
			status.AllocationSummary = summarizeAllocations(status.Allocations)
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
	h.emitAppActivity(r, id, "app.restarted", "App restarted", id+" was restarted", nil)
	writeJSON(w, map[string]string{"status": "restarted"})
}

func (h *Handler) ScaleApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.pipeline == nil || !h.pipeline.ScaleAvailable() {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "durable_scale_unavailable", "durable Nomad scale execution is unavailable")
		return
	}

	var req struct {
		Group  string `json:"group"`
		Region string `json:"region"`
		Count  int    `json:"count"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Group == "" || req.Count < 0 {
		writeError(w, http.StatusBadRequest, "group and count required")
		return
	}
	// Validate the stable app/group target before accepting a mutable durable
	// request, so a 202 always names a task group that exists in declared app
	// intent rather than a request guaranteed to fail in the worker.
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "app_discovery_failed", "failed to discover app intent")
		return
	}
	var spec *model.InfraSpec
	for _, candidate := range specs {
		if candidate.App == id {
			spec = candidate
			break
		}
	}
	if spec == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_process_not_found", "app or process group was not found")
		return
	}
	process, found := spec.Processes[req.Group]
	if !found {
		WriteControlProblem(w, r, http.StatusNotFound, "app_process_not_found", "app or process group was not found")
		return
	}
	if process.Schedule != "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "scheduled_process_not_scalable", "scheduled process groups use periodic jobs and cannot be scaled")
		return
	}
	regions := spec.ResolvedRegions()
	if req.Region == "" && len(regions) == 1 {
		req.Region = regions[0].Name
	}
	validRegion := false
	nomadRegion := ""
	for _, region := range regions {
		if region.Name == req.Region && spec.ProcessRunsInRegion(process, region.Name) {
			validRegion = true
			nomadRegion = region.NomadRegion
			break
		}
	}
	if !validRegion {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_scale_region", "region is required for this process and must be declared for the app")
		return
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"app": id, "group": req.Group, "region": req.Region, "nomadRegion": nomadRegion, "count": req.Count})
	if !ok {
		return
	}
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.scale", App: id, SagaID: uuid.NewString(), Ref: req.Region + "/" + req.Group, Status: model.OperationQueued, Risk: "Nomad task-group scale", Source: "app-control-api", Message: fmt.Sprintf("queued scale for %s/%s process %q to %d", id, req.Region, req.Group, req.Count), StartedAt: now, MaxAttempts: 1, Payload: map[string]interface{}{"app": id, "group": req.Group, "region": req.Region, "nomadRegion": nomadRegion, "count": req.Count}}
	accepted, err := h.pipeline.QueueOperation(r.Context(), op, enqueue)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	accepted.Operation.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	if accepted.Replayed {
		writeJSON(w, accepted.Operation)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, accepted.Operation)
}
