package handler

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

// periodicJobFor translates a scheduled process for resubmission. For an app
// with delivered named databases the job references the periodic job's
// promoted delivery revision, revalidated against the running targets; there
// is no mutable fallback and no target substitution.
func (h *Handler) periodicJobFor(r *http.Request, spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string) (*nomadapi.Job, error) {
	region := spec.ResolvedRegions()[0]
	revision := int64(0)
	if spec.NamedDatabases() && nomad.HasRuntimeDatabases(spec) {
		if h.pipeline == nil || h.pipeline.DatabaseTargets == nil {
			return nil, fmt.Errorf("named databases require a database profile")
		}
		material, err := h.pipeline.RunningDeliveryRevision(r.Context(), spec, region.NomadRegion, spec.App+"-"+procName)
		if err != nil {
			return nil, err
		}
		revision = material.Revision
	}
	return nomad.TranslatePeriodicForRegionAt(spec, procName, proc, imageTag, env, region, revision), nil
}

type cronHistoryEntry struct {
	Process  string          `json:"process"`
	Schedule string          `json:"schedule"`
	Paused   bool            `json:"paused"`
	Runs     []nomad.CronRun `json:"runs"`
}

func (h *Handler) CronHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}

	var entries []cronHistoryEntry
	for procName, proc := range spec.Processes {
		if proc.Schedule == "" {
			continue
		}

		entry := cronHistoryEntry{
			Process:  procName,
			Schedule: proc.Schedule,
		}

		// Check DB state
		state, err := h.db.GetCronState(r.Context(), id, procName)
		if err == nil {
			entry.Paused = state.Paused
			if state.Schedule != "" {
				entry.Schedule = state.Schedule
			}
		}

		// Get recent runs from Nomad
		if h.nomad != nil {
			jobID := fmt.Sprintf("%s-%s", id, procName)
			runs, err := h.nomad.PeriodicChildren(jobID)
			if err == nil {
				entry.Runs = runs
			}
		}

		entries = append(entries, entry)
	}

	if entries == nil {
		entries = []cronHistoryEntry{}
	}
	writeJSON(w, entries)
}

func (h *Handler) CronTrigger(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Process string `json:"process"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.nomad == nil {
		writeError(w, http.StatusServiceUnavailable, "nomad not connected")
		return
	}

	jobID := fmt.Sprintf("%s-%s", id, req.Process)
	evalID, err := h.nomad.PeriodicForce(jobID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{
		"status": "triggered",
		"evalId": evalID,
	})
	h.emitBeacon(r.Context(), model.BeaconEvent{
		App:       id,
		Type:      "job.triggered",
		Severity:  model.BeaconInfo,
		Title:     fmt.Sprintf("%s %s job triggered", id, req.Process),
		Body:      fmt.Sprintf("Cron process %s was triggered manually.", req.Process),
		DedupeKey: fmt.Sprintf("%s:%s:cron", id, req.Process),
		Metadata: map[string]interface{}{
			"process":        req.Process,
			"evalId":         evalID,
			"correlationKey": fmt.Sprintf("%s:%s:cron", id, req.Process),
		},
	})
}

func (h *Handler) CronPause(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Process string `json:"process"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	spec := h.findSpec(id)
	if spec == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_process_not_found", "app or scheduled process was not found")
		return
	}
	proc, ok := spec.Processes[req.Process]
	if !ok || proc.Schedule == "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "scheduled_process_required", "cron pause requires a declared scheduled process")
		return
	}
	if h.pipeline == nil || !h.pipeline.CronPauseAvailable() || h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_pause_execution_unavailable", "durable cron pause execution is unavailable")
		return
	}
	// The stored custom schedule is the effective schedule that resume must use.
	state, stateErr := h.db.GetCronState(r.Context(), id, req.Process)
	schedule, effectiveErr := cronPauseEffectiveSchedule(proc.Schedule, state, stateErr)
	if effectiveErr != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_state_unavailable", "durable cron state is unavailable")
		return
	}
	if h.nomad == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_pause_nomad_unavailable", "Nomad periodic job state is unavailable")
		return
	}
	jobID := id + "-" + req.Process
	periodic, err := h.nomad.PeriodicJobSchedule(jobID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_pause_nomad_unavailable", "Nomad periodic job state is unavailable")
		return
	}
	timezone := model.ResolveProcessTimezone(spec, proc)
	if periodic.Paused || periodic.Schedule != schedule || periodic.TimeZone != timezone || periodic.Version == 0 || periodic.ModifyIndex == 0 {
		WriteControlProblem(w, r, http.StatusConflict, "cron_pause_intent_stale", "Nomad periodic job no longer matches the requested cron pause intent")
		return
	}
	// Keep uint64 revisions as decimal strings: generic JSON map decoding uses
	// float64 and would silently lose a Nomad index above 2^53.
	payload := map[string]interface{}{"app": id, "process": req.Process, "schedule": schedule, "timezone": timezone, "jobId": jobID, "version": fmt.Sprint(periodic.Version), "modifyIndex": fmt.Sprint(periodic.ModifyIndex), "action": "pause"}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), payload)
	if !ok {
		return
	}
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.cron-pause", App: id, SagaID: uuid.NewString(), Ref: req.Process, Status: model.OperationQueued, Risk: "stop Nomad periodic job", Source: "app-control-api", Message: fmt.Sprintf("queued cron pause for %s process %q", id, req.Process), StartedAt: now, NextAttemptAt: now, MaxAttempts: 1, Payload: payload}
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

// cronPauseEffectiveSchedule treats a missing override as the declared
// schedule. Any other read failure makes the signed intent indeterminate, so
// callers must reject rather than queue with a guessed schedule.
func cronPauseEffectiveSchedule(declared string, state *store.CronState, err error) (string, error) {
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if state != nil && state.Schedule != "" {
		return state.Schedule, nil
	}
	return declared, nil
}

func (h *Handler) CronResume(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Process string `json:"process"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.nomad == nil {
		writeError(w, http.StatusServiceUnavailable, "nomad not connected")
		return
	}

	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}

	proc, ok := spec.Processes[req.Process]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("process %s not found", req.Process))
		return
	}

	// Check for custom schedule
	state, err := h.db.GetCronState(r.Context(), id, req.Process)
	if err == nil && state.Schedule != "" {
		proc.Schedule = state.Schedule
	}

	// Resolve image tag from last deployment
	deps, err := h.db.ListDeployments(r.Context(), id, 1)
	if err != nil || len(deps) == 0 {
		writeError(w, http.StatusBadRequest, "no previous deployment found")
		return
	}
	imageTag := deps[0].ImageTag

	// Resolve secrets
	env := make(map[string]string)
	if h.secrets != nil {
		secretEnv, err := h.secrets.EnvMap(id)
		if err != nil && !os.IsNotExist(err) {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("resolve secrets: %v", err))
			return
		}
		for k, v := range secretEnv {
			env[k] = v
		}
	}

	// Re-submit periodic job; its templates read the delivery variable the
	// latest deploy wrote, which no secret may shadow.
	if conflicts := spec.DatabaseEnvConflicts(env); len(conflicts) > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf("%s delivered by the database binding must not also come from app secrets", strings.Join(conflicts, ", ")))
		return
	}
	periodicJob, err := h.periodicJobFor(r, spec, req.Process, proc, imageTag, env)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	_, err = h.nomad.SubmitJob(periodicJob)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.db.UpsertCronState(r.Context(), id, req.Process, false, proc.Schedule)

	writeJSON(w, map[string]string{"status": "resumed"})
	h.emitBeacon(r.Context(), model.BeaconEvent{
		App:       id,
		Type:      "job.resumed",
		Severity:  model.BeaconInfo,
		Title:     fmt.Sprintf("%s %s job resumed", id, req.Process),
		Body:      fmt.Sprintf("Cron process %s was resumed.", req.Process),
		DedupeKey: fmt.Sprintf("%s:%s:cron", id, req.Process),
		Metadata: map[string]interface{}{
			"process":        req.Process,
			"schedule":       proc.Schedule,
			"imageTag":       imageTag,
			"correlationKey": fmt.Sprintf("%s:%s:cron", id, req.Process),
		},
	})
}

func (h *Handler) CronUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Process  string `json:"process"`
		Schedule string `json:"schedule"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.nomad == nil {
		writeError(w, http.StatusServiceUnavailable, "nomad not connected")
		return
	}

	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}

	proc, ok := spec.Processes[req.Process]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("process %s not found", req.Process))
		return
	}

	// Use the new schedule
	proc.Schedule = req.Schedule

	// Resolve image tag from last deployment
	deps, err := h.db.ListDeployments(r.Context(), id, 1)
	if err != nil || len(deps) == 0 {
		writeError(w, http.StatusBadRequest, "no previous deployment found")
		return
	}
	imageTag := deps[0].ImageTag

	// Resolve secrets
	env := make(map[string]string)
	if h.secrets != nil {
		secretEnv, err := h.secrets.EnvMap(id)
		if err != nil && !os.IsNotExist(err) {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("resolve secrets: %v", err))
			return
		}
		for k, v := range secretEnv {
			env[k] = v
		}
	}

	// Re-submit periodic job with new schedule (same delivery rule as resume)
	if conflicts := spec.DatabaseEnvConflicts(env); len(conflicts) > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf("%s delivered by the database binding must not also come from app secrets", strings.Join(conflicts, ", ")))
		return
	}
	periodicJob, err := h.periodicJobFor(r, spec, req.Process, proc, imageTag, env)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	_, err = h.nomad.SubmitJob(periodicJob)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.db.UpsertCronState(r.Context(), id, req.Process, false, req.Schedule)

	writeJSON(w, map[string]string{
		"status":   "updated",
		"schedule": req.Schedule,
	})
	h.emitBeacon(r.Context(), model.BeaconEvent{
		App:       id,
		Type:      "job.schedule_updated",
		Severity:  model.BeaconInfo,
		Title:     fmt.Sprintf("%s %s schedule updated", id, req.Process),
		Body:      fmt.Sprintf("Cron process %s schedule changed.", req.Process),
		DedupeKey: fmt.Sprintf("%s:%s:cron", id, req.Process),
		Metadata: map[string]interface{}{
			"process":        req.Process,
			"schedule":       req.Schedule,
			"imageTag":       imageTag,
			"correlationKey": fmt.Sprintf("%s:%s:cron", id, req.Process),
		},
	})
}
