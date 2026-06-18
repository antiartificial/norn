package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"norn/v2/api/engine"
	"norn/v2/api/model"
)

type eventReconcileRequest struct {
	App    string `json:"app,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	DryRun bool   `json:"dryRun,omitempty"`
	By     string `json:"by,omitempty"`
}

type eventReconcileDecision struct {
	EventID  string   `json:"eventId"`
	App      string   `json:"app,omitempty"`
	Type     string   `json:"type"`
	Severity string   `json:"severity"`
	Title    string   `json:"title"`
	Action   string   `json:"action"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence,omitempty"`
}

type eventReconcileResponse struct {
	DryRun      bool                     `json:"dryRun"`
	Scanned     int                      `json:"scanned"`
	Reconciled  int                      `json:"reconciled"`
	NeedsReview int                      `json:"needsReview"`
	Decisions   []eventReconcileDecision `json:"decisions"`
}

func (h *Handler) ReconcileEvents(w http.ResponseWriter, r *http.Request) {
	var req eventReconcileRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.By == "" {
		req.By = "system"
	}

	events, err := h.db.ListOpenBeaconEvents(r.Context(), req.App, req.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := eventReconcileResponse{
		DryRun:    req.DryRun,
		Scanned:   len(events),
		Decisions: make([]eventReconcileDecision, 0, len(events)),
	}
	for _, event := range events {
		decision := h.reconcileEvent(r.Context(), event)
		if decision.Action == "acknowledge" {
			resp.Reconciled++
			if !req.DryRun {
				note := decision.Reason
				if len(decision.Evidence) > 0 {
					note += ": " + strings.Join(decision.Evidence, "; ")
				}
				if _, err := h.db.AcknowledgeBeaconEvent(r.Context(), event.ID, req.By, note); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
		} else {
			resp.NeedsReview++
		}
		resp.Decisions = append(resp.Decisions, decision)
	}
	writeJSON(w, resp)
}

func (h *Handler) reconcileEvent(ctx context.Context, event model.BeaconEvent) eventReconcileDecision {
	decision := eventReconcileDecision{
		EventID:  event.ID,
		App:      event.App,
		Type:     event.Type,
		Severity: string(event.Severity),
		Title:    event.Title,
		Action:   "needs_review",
		Reason:   "no deterministic reconciliation rule matched",
	}

	switch event.Type {
	case "deploy.failed":
		return h.reconcileDeployFailed(ctx, event, decision)
	case "service.health.critical", "service.health.warning":
		return h.reconcileServiceHealth(ctx, event, decision)
	case "cron.hung", "cron.failed", "cron.lost", "cron.missed_run":
		return h.reconcileCron(ctx, event, decision)
	case "instance.restarted":
		return h.reconcileTaskRestart(ctx, event, decision)
	default:
		return decision
	}
}

func (h *Handler) reconcileDeployFailed(ctx context.Context, event model.BeaconEvent, decision eventReconcileDecision) eventReconcileDecision {
	if event.App == "" {
		decision.Reason = "deploy event has no app"
		return decision
	}
	later, err := h.db.LaterBeaconEventExists(ctx, event.App, "deploy.succeeded", event.OccurredAt)
	if err != nil {
		decision.Reason = "failed to check for later deploy success"
		return decision
	}
	if !later {
		decision.Reason = "no later deploy.succeeded event exists"
		return decision
	}
	if ok, evidence := h.appRunningHealthy(event.App); ok {
		decision.Action = "acknowledge"
		decision.Reason = "later deploy succeeded and app is currently healthy"
		decision.Evidence = append([]string{"later deploy.succeeded exists"}, evidence...)
		return decision
	}
	decision.Reason = "later deploy succeeded, but current app health is not proven"
	return decision
}

func (h *Handler) reconcileServiceHealth(ctx context.Context, event model.BeaconEvent, decision eventReconcileDecision) eventReconcileDecision {
	later, err := h.db.LaterBeaconEventExists(ctx, event.App, "service.health.recovered", event.OccurredAt)
	if err == nil && later {
		decision.Action = "acknowledge"
		decision.Reason = "later service.health.recovered event exists"
		decision.Evidence = append(decision.Evidence, "later service.health.recovered exists")
		return decision
	}
	process := metadataString(event.Metadata, "process")
	if process == "" {
		process = strings.TrimPrefix(metadataString(event.Metadata, "service"), event.App+"-")
	}
	if ok, evidence := h.servicePassing(event.App, process); ok {
		decision.Action = "acknowledge"
		decision.Reason = "service is currently passing"
		decision.Evidence = evidence
		return decision
	}
	decision.Reason = "service recovery is not proven"
	return decision
}

func (h *Handler) reconcileCron(ctx context.Context, event model.BeaconEvent, decision eventReconcileDecision) eventReconcileDecision {
	if h.engine == nil {
		decision.Reason = "engine is not available"
		return decision
	}
	jobID := metadataString(event.Metadata, "jobId")
	process := metadataString(event.Metadata, "process")
	parentJobID := cronParentJobID(event.App, process, jobID)
	if parentJobID == "" {
		decision.Reason = "cron event lacks parent job evidence"
		return decision
	}
	info, err := h.engine.CronScheduleInfo(parentJobID)
	if err != nil || info == nil {
		decision.Reason = "cron schedule info is not available"
		decision.Evidence = append(decision.Evidence, fmt.Sprintf("parent=%s", parentJobID))
		return decision
	}
	if info.Status != "running" || info.Paused {
		decision.Reason = "cron job is not cleanly active"
		decision.Evidence = append(decision.Evidence,
			fmt.Sprintf("parent=%s", parentJobID),
			fmt.Sprintf("status=%s", info.Status),
			fmt.Sprintf("paused=%t", info.Paused),
		)
		return decision
	}
	// Check for any running cron instances
	runs, err := h.engine.CronHistory(parentJobID)
	if err == nil {
		for _, run := range runs {
			if run.Status == "running" {
				decision.Reason = "cron job has running instances"
				decision.Evidence = append(decision.Evidence,
					fmt.Sprintf("parent=%s", parentJobID),
					fmt.Sprintf("runningId=%s", run.ID),
				)
				return decision
			}
		}
	}
	decision.Action = "acknowledge"
	decision.Reason = "cron job is active with no running instances"
	decision.Evidence = append(decision.Evidence,
		fmt.Sprintf("parent=%s", parentJobID),
		fmt.Sprintf("schedule=%s", info.Schedule),
		fmt.Sprintf("status=%s", info.Status),
	)
	return decision
}

func (h *Handler) reconcileTaskRestart(ctx context.Context, event model.BeaconEvent, decision eventReconcileDecision) eventReconcileDecision {
	allocID := metadataString(event.Metadata, "allocId")
	if ok, evidence := h.appRunningHealthy(event.App); !ok {
		decision.Reason = "current app health is not proven"
		return decision
	} else {
		decision.Evidence = append(decision.Evidence, evidence...)
	}
	if allocID != "" && h.currentInstanceExists(event.App, allocID) {
		decision.Reason = "referenced instance is still active"
		decision.Evidence = append(decision.Evidence, fmt.Sprintf("instance=%s", allocID))
		return decision
	}
	decision.Action = "acknowledge"
	decision.Reason = "app is currently healthy and restarted instance is no longer active"
	if allocID != "" {
		decision.Evidence = append(decision.Evidence, fmt.Sprintf("instance absent=%s", allocID))
	}
	return decision
}

func (h *Handler) appRunningHealthy(app string) (bool, []string) {
	if h.engine == nil || app == "" {
		return false, nil
	}
	status, err := h.engine.JobStatus(app)
	if err != nil || status != "running" {
		return false, []string{fmt.Sprintf("jobStatus=%s", emptyIf(status, "unknown"))}
	}
	instances, err := h.engine.JobInstances(app)
	if err != nil {
		return false, []string{"instances unavailable"}
	}
	for _, inst := range instances {
		if !inst.IsRunning() {
			continue
		}
		if inst.Healthy != nil && *inst.Healthy {
			return true, []string{"jobStatus=running", fmt.Sprintf("healthyInstance=%s", engine.ShortID(inst.ContainerName))}
		}
	}
	return false, []string{"jobStatus=running", "healthyInstance=none"}
}

func (h *Handler) servicePassing(app, process string) (bool, []string) {
	if h.engine == nil || app == "" {
		return false, nil
	}
	if process == "" {
		return h.appRunningHealthy(app)
	}
	serviceName := fmt.Sprintf("%s-%s", app, process)
	health, err := h.engine.ServiceHealthChecks(serviceName)
	if err != nil || len(health) == 0 {
		return false, []string{fmt.Sprintf("service=%s unavailable", serviceName)}
	}
	for _, entry := range health {
		if entry.Status != "passing" {
			return false, []string{fmt.Sprintf("service=%s status=%s", serviceName, entry.Status)}
		}
	}
	return true, []string{fmt.Sprintf("service=%s passing", serviceName)}
}

func (h *Handler) currentInstanceExists(app, instanceID string) bool {
	if h.engine == nil || app == "" || instanceID == "" {
		return false
	}
	instances, err := h.engine.JobInstances(app)
	if err != nil {
		return false
	}
	for _, inst := range instances {
		if inst.IsTerminal() {
			continue
		}
		if strings.HasPrefix(inst.ContainerName, instanceID) || strings.HasPrefix(engine.ShortID(inst.ContainerName), instanceID) {
			return true
		}
	}
	return false
}

func metadataString(metadata map[string]interface{}, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func cronParentJobID(app, process, jobID string) string {
	if idx := strings.Index(jobID, "/periodic-"); idx > 0 {
		return jobID[:idx]
	}
	if jobID != "" {
		return jobID
	}
	if app != "" && process != "" {
		return fmt.Sprintf("%s-%s", app, process)
	}
	return ""
}

func emptyIf(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
