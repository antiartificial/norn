package handler

import (
	"fmt"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/nomad"
)

type cronHistoryEntry struct {
	Process  string         `json:"process"`
	Schedule string         `json:"schedule"`
	Paused   bool           `json:"paused"`
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

	if h.nomad == nil {
		writeError(w, http.StatusServiceUnavailable, "nomad not connected")
		return
	}

	jobID := fmt.Sprintf("%s-%s", id, req.Process)
	if err := h.nomad.StopJob(jobID, false); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Persist the schedule so we can resume later
	spec := h.findSpec(id)
	schedule := ""
	if spec != nil {
		if proc, ok := spec.Processes[req.Process]; ok {
			schedule = proc.Schedule
		}
	}
	// Check if there's already a custom schedule in DB
	state, err := h.db.GetCronState(r.Context(), id, req.Process)
	if err == nil && state.Schedule != "" {
		schedule = state.Schedule
	}

	h.db.UpsertCronState(r.Context(), id, req.Process, true, schedule)

	writeJSON(w, map[string]string{"status": "paused"})
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

	// Re-submit periodic job
	periodicJob := nomad.TranslatePeriodic(spec, req.Process, proc, imageTag, env)
	_, err = h.nomad.SubmitJob(periodicJob)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.db.UpsertCronState(r.Context(), id, req.Process, false, proc.Schedule)

	writeJSON(w, map[string]string{"status": "resumed"})
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

	// Re-submit periodic job with new schedule
	periodicJob := nomad.TranslatePeriodic(spec, req.Process, proc, imageTag, env)
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
}
