package handler

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/engine"
	"norn/v2/api/model"
)

type cronHistoryEntry struct {
	Process  string          `json:"process"`
	Schedule string          `json:"schedule"`
	Paused   bool            `json:"paused"`
	Runs     []engine.CronRun `json:"runs"`
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

		// Get recent runs from engine
		if h.engine != nil {
			jobID := fmt.Sprintf("%s-%s", id, procName)
			runs, err := h.engine.CronHistory(jobID)
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

	if h.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not available")
		return
	}

	jobID := fmt.Sprintf("%s-%s", id, req.Process)
	evalID, err := h.engine.CronForce(r.Context(), jobID)
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

	if h.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not available")
		return
	}

	jobID := fmt.Sprintf("%s-%s", id, req.Process)
	h.engine.UnregisterCron(jobID)

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
	h.emitBeacon(r.Context(), model.BeaconEvent{
		App:       id,
		Type:      "job.paused",
		Severity:  model.BeaconWarning,
		Title:     fmt.Sprintf("%s %s job paused", id, req.Process),
		Body:      fmt.Sprintf("Cron process %s was paused.", req.Process),
		DedupeKey: fmt.Sprintf("%s:%s:cron", id, req.Process),
		Metadata: map[string]interface{}{
			"process":        req.Process,
			"schedule":       schedule,
			"correlationKey": fmt.Sprintf("%s:%s:cron", id, req.Process),
		},
	})
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

	if h.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not available")
		return
	}

	jobID := fmt.Sprintf("%s-%s", id, req.Process)
	if err := h.engine.ResumeCron(jobID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	state, err := h.db.GetCronState(r.Context(), id, req.Process)
	if err == nil && state.Schedule != "" {
		h.engine.UpdateCronSchedule(jobID, state.Schedule)
	}

	h.db.UpsertCronState(r.Context(), id, req.Process, false, "")

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

	if h.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not available")
		return
	}

	jobID := fmt.Sprintf("%s-%s", id, req.Process)
	if err := h.engine.UpdateCronSchedule(jobID, req.Schedule); err != nil {
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
			"correlationKey": fmt.Sprintf("%s:%s:cron", id, req.Process),
		},
	})
}
