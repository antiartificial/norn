package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
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
	if h.pipeline == nil || !h.pipeline.CronTriggerAvailable() {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_trigger_execution_unavailable", "durable cron trigger execution is unavailable")
		return
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"app": id, "process": req.Process, "action": "trigger"})
	if !ok {
		return
	}
	previous, err := h.pipeline.ResolveEnqueue(r.Context(), enqueue, "app.cron-trigger", id)
	if err == nil {
		if previous.Operation.Ref != req.Process || previous.Operation.Payload["process"] != req.Process {
			writeOperationAcceptanceError(w, r, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "app.cron-trigger", Resource: id}})
			return
		}
		previous.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+previous.Operation.ID)
		writeJSON(w, previous.Operation)
		return
	}
	if !errors.Is(err, store.ErrAcceptanceNotFound) {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	spec := h.findSpec(id)
	if spec == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_process_not_found", "app or scheduled process was not found")
		return
	}
	proc, exists := spec.Processes[req.Process]
	if !exists || proc.Schedule == "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "scheduled_process_required", "cron trigger requires a declared scheduled process")
		return
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_trigger_spec_unavailable", "scheduled process spec cannot be fingerprinted")
		return
	}
	state, stateErr := h.db.GetCronState(r.Context(), id, req.Process)
	schedule, err := cronPauseEffectiveSchedule(proc.Schedule, state, stateErr)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_state_unavailable", "durable cron state is unavailable")
		return
	}
	jobID := id + "-" + req.Process
	periodic, err := h.nomad.PeriodicJobSchedule(jobID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_trigger_nomad_unavailable", "Nomad periodic job state is unavailable")
		return
	}
	timezone := model.ResolveProcessTimezone(spec, proc)
	if periodic.Paused || periodic.Schedule != schedule || periodic.TimeZone != timezone || periodic.ModifyIndex == 0 {
		WriteControlProblem(w, r, http.StatusConflict, "cron_trigger_intent_stale", "Nomad periodic job no longer matches the requested trigger intent")
		return
	}
	payload := map[string]interface{}{"app": id, "process": req.Process, "jobId": jobID, "schedule": schedule, "timezone": timezone, "specDigest": digest, "version": fmt.Sprint(periodic.Version), "modifyIndex": fmt.Sprint(periodic.ModifyIndex), "action": "trigger"}
	enqueue.Semantics = payload
	enqueue.Admission.OneActiveMutablePerApp = true
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.cron-trigger", App: id, SagaID: uuid.NewString(), Ref: req.Process, Status: model.OperationQueued, Risk: "trigger Nomad periodic job", Source: "app-control-api", Message: fmt.Sprintf("queued cron trigger for %s process %q", id, req.Process), StartedAt: now, NextAttemptAt: now, MaxAttempts: 3, Payload: payload}
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

func (h *Handler) QueueCronTriggerReconciliation(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIWrite); !ok {
		return
	}
	var req struct {
		EffectID string `json:"effectId"`
		EvalID   string `json:"evalId"`
		Confirm  bool   `json:"confirm"`
	}
	if err := decodeControlJSON(w, r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_cron_reconciliation", err.Error())
		return
	}
	if !req.Confirm || req.EffectID == "" || req.EvalID == "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "confirmation_required", "cron trigger reconciliation requires confirm=true, effectId, and evalId")
		return
	}
	if h.pipeline == nil || !h.pipeline.CronTriggerAvailable() {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_reconciliation_unavailable", "durable cron reconciliation is unavailable")
		return
	}
	app, sourceID := chi.URLParam(r, "id"), chi.URLParam(r, "operationID")
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{
		"action": "cron-trigger-reconciliation", "app": app, "sourceOperationId": sourceID, "effectId": req.EffectID, "evalId": req.EvalID, "confirm": true,
	})
	if !ok {
		return
	}
	if previous, err := h.pipeline.ResolveEnqueue(r.Context(), enqueue, "app.cron-trigger-reconcile", app); err == nil {
		if previous.Operation.Ref != sourceID || previous.Operation.Payload["effectId"] != req.EffectID || previous.Operation.Payload["evalId"] != req.EvalID {
			WriteControlProblem(w, r, http.StatusConflict, "idempotency_conflict", "idempotency key belongs to another cron trigger correction")
			return
		}
		previous.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+previous.Operation.ID)
		writeJSON(w, previous.Operation)
		return
	} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	accepted, err := h.pipeline.QueueCronTriggerReconciliation(r.Context(), app, sourceID, req.EffectID, req.EvalID, enqueue)
	if errors.Is(err, store.ErrCronTriggerReconciliationUnavailable) {
		WriteControlProblem(w, r, http.StatusConflict, "cron_reconciliation_unavailable", "source cron trigger effect cannot be reconciled")
		return
	}
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

func (h *Handler) CronPause(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Process string `json:"process"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.pipeline == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_pause_execution_unavailable", "durable cron pause execution is unavailable")
		return
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"app": id, "process": req.Process, "action": "pause"})
	if !ok {
		return
	}
	// Resolve the original signed request before reading mutable Nomad state.
	// A retry after a successful pause must return its receipt even though the
	// periodic parent is now stopped and its ModifyIndex has changed.
	if previous, replayed, err := h.resolveCronPauseReplay(r.Context(), enqueue, id, req.Process); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	} else if replayed {
		previous.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+previous.Operation.ID)
		writeJSON(w, previous.Operation)
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
	enqueue.Semantics = payload
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.cron-pause", App: id, SagaID: uuid.NewString(), Ref: req.Process, Status: model.OperationQueued, Risk: "stop Nomad periodic job", Source: "app-control-api", Message: fmt.Sprintf("queued cron pause for %s process %q", id, req.Process), StartedAt: now, NextAttemptAt: now, MaxAttempts: 3, Payload: payload}
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

func (h *Handler) resolveCronPauseReplay(ctx context.Context, request pipeline.EnqueueRequest, app, process string) (store.AcceptedOperation, bool, error) {
	previous, err := h.pipeline.ResolveEnqueue(ctx, request, "app.cron-pause", app)
	if errors.Is(err, store.ErrAcceptanceNotFound) {
		return store.AcceptedOperation{}, false, nil
	}
	if err != nil {
		return store.AcceptedOperation{}, false, err
	}
	if previous.Operation.Kind != "app.cron-pause" || previous.Operation.App != app || previous.Operation.Ref != process || previous.Operation.Payload["process"] != process {
		return store.AcceptedOperation{}, false, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "app.cron-pause", Resource: app}}
	}
	return previous, true, nil
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
	if h.pipeline == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_resume_execution_unavailable", "durable cron resume execution is unavailable")
		return
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"app": id, "process": req.Process, "action": "resume"})
	if !ok {
		return
	}
	if previous, replayed, err := h.resolveCronResumeReplay(r.Context(), enqueue, id, req.Process); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	} else if replayed {
		previous.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+previous.Operation.ID)
		writeJSON(w, previous.Operation)
		return
	}
	spec := h.findSpec(id)
	if spec == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_process_not_found", "app or scheduled process was not found")
		return
	}
	proc, ok := spec.Processes[req.Process]
	if !ok || proc.Schedule == "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "scheduled_process_required", "cron resume requires a declared scheduled process")
		return
	}
	specDigest, err := model.InfraSpecDigest(spec)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_resume_spec_unavailable", "scheduled process spec cannot be fingerprinted")
		return
	}
	if !h.pipeline.CronResumeAvailable() || h.db == nil || h.nomad == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_resume_execution_unavailable", "durable cron resume execution is unavailable")
		return
	}
	state, stateErr := h.db.GetCronState(r.Context(), id, req.Process)
	schedule, effectiveErr := cronPauseEffectiveSchedule(proc.Schedule, state, stateErr)
	if effectiveErr != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_state_unavailable", "durable cron state is unavailable")
		return
	}
	if state == nil || !state.Paused {
		WriteControlProblem(w, r, http.StatusConflict, "cron_resume_intent_stale", "durable cron state is not paused")
		return
	}
	jobID := id + "-" + req.Process
	periodic, err := h.nomad.PeriodicJobSchedule(jobID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_resume_nomad_unavailable", "Nomad periodic job state is unavailable")
		return
	}
	timezone := model.ResolveProcessTimezone(spec, proc)
	if !periodic.Paused || periodic.Schedule != schedule || periodic.TimeZone != timezone || periodic.Version == 0 || periodic.ModifyIndex == 0 {
		WriteControlProblem(w, r, http.StatusConflict, "cron_resume_intent_stale", "Nomad periodic job no longer matches the requested cron resume intent")
		return
	}
	deps, err := h.db.ListDeployments(r.Context(), id, 1)
	if err != nil || len(deps) == 0 || strings.TrimSpace(deps[0].ImageTag) == "" {
		WriteControlProblem(w, r, http.StatusConflict, "cron_resume_image_unavailable", "no previous deployment image was found")
		return
	}
	deliveryRevision := int64(0)
	if spec.NamedDatabases() && nomad.HasRuntimeDatabases(spec) {
		if h.pipeline.DatabaseTargets == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_resume_database_unavailable", "named database delivery is unavailable")
			return
		}
		regions := spec.ResolvedRegions()
		if len(regions) == 0 {
			WriteControlProblem(w, r, http.StatusConflict, "cron_resume_region_unavailable", "scheduled process has no region")
			return
		}
		material, err := h.pipeline.RunningDeliveryRevision(r.Context(), spec, regions[0].NomadRegion, jobID)
		if err != nil {
			WriteControlProblem(w, r, http.StatusConflict, "cron_resume_database_unavailable", "running database delivery is unavailable")
			return
		}
		deliveryRevision = material.Revision
	}
	// The image reference is public deployment identity. Secret values and
	// database delivery material are reconstructed only in the claimed worker.
	payload := map[string]interface{}{"app": id, "process": req.Process, "schedule": schedule, "timezone": timezone, "jobId": jobID, "imageTag": deps[0].ImageTag, "specDigest": specDigest, "deliveryRevision": fmt.Sprint(deliveryRevision), "version": fmt.Sprint(periodic.Version), "modifyIndex": fmt.Sprint(periodic.ModifyIndex), "action": "resume"}
	enqueue.Semantics = payload
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.cron-resume", App: id, SagaID: uuid.NewString(), Ref: req.Process, Status: model.OperationQueued, Risk: "resume Nomad periodic job", Source: "app-control-api", Message: fmt.Sprintf("queued cron resume for %s process %q", id, req.Process), StartedAt: now, NextAttemptAt: now, MaxAttempts: 3, Payload: payload}
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

func (h *Handler) resolveCronResumeReplay(ctx context.Context, request pipeline.EnqueueRequest, app, process string) (store.AcceptedOperation, bool, error) {
	previous, err := h.pipeline.ResolveEnqueue(ctx, request, "app.cron-resume", app)
	if errors.Is(err, store.ErrAcceptanceNotFound) {
		return store.AcceptedOperation{}, false, nil
	}
	if err != nil {
		return store.AcceptedOperation{}, false, err
	}
	if previous.Operation.Kind != "app.cron-resume" || previous.Operation.App != app || previous.Operation.Ref != process || previous.Operation.Payload["process"] != process {
		return store.AcceptedOperation{}, false, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "app.cron-resume", Resource: app}}
	}
	return previous, true, nil
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

	if h.pipeline == nil || !h.pipeline.CronScheduleAvailable() || h.db == nil || h.nomad == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_schedule_execution_unavailable", "durable cron schedule execution is unavailable")
		return
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"app": id, "process": req.Process, "schedule": req.Schedule, "action": "schedule"})
	if !ok {
		return
	}
	if previous, replayed, err := h.resolveCronScheduleReplay(r.Context(), enqueue, id, req.Process); err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	} else if replayed {
		previous.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+previous.Operation.ID)
		writeJSON(w, previous.Operation)
		return
	}
	spec := h.findSpec(id)
	if spec == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_process_not_found", "app or scheduled process was not found")
		return
	}
	proc, ok := spec.Processes[req.Process]
	if !ok || strings.TrimSpace(proc.Schedule) == "" || strings.TrimSpace(req.Schedule) == "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "scheduled_process_required", "schedule updates require a declared process and non-empty schedule")
		return
	}
	specDigest, err := model.InfraSpecDigest(spec)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_schedule_spec_unavailable", "scheduled process spec cannot be fingerprinted")
		return
	}
	state, stateErr := h.db.GetCronState(r.Context(), id, req.Process)
	previousSchedule, err := cronPauseEffectiveSchedule(proc.Schedule, state, stateErr)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_state_unavailable", "durable cron state is unavailable")
		return
	}
	paused := state != nil && state.Paused
	jobID := id + "-" + req.Process
	periodic, err := h.nomad.PeriodicJobSchedule(jobID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_schedule_nomad_unavailable", "Nomad periodic job state is unavailable")
		return
	}
	timezone := model.ResolveProcessTimezone(spec, proc)
	if periodic.Schedule != previousSchedule || periodic.TimeZone != timezone || periodic.Paused != paused || periodic.Version == 0 || periodic.ModifyIndex == 0 {
		WriteControlProblem(w, r, http.StatusConflict, "cron_schedule_intent_stale", "Nomad periodic job no longer matches the requested schedule update intent")
		return
	}
	deps, err := h.db.ListDeployments(r.Context(), id, 1)
	if err != nil || len(deps) == 0 || strings.TrimSpace(deps[0].ImageTag) == "" {
		WriteControlProblem(w, r, http.StatusConflict, "cron_schedule_image_unavailable", "no previous deployment image was found")
		return
	}
	deliveryRevision := int64(0)
	if spec.NamedDatabases() && nomad.HasRuntimeDatabases(spec) {
		if h.pipeline.DatabaseTargets == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "cron_schedule_database_unavailable", "named database delivery is unavailable")
			return
		}
		regions := spec.ResolvedRegions()
		if len(regions) == 0 {
			WriteControlProblem(w, r, http.StatusConflict, "cron_schedule_region_unavailable", "scheduled process has no region")
			return
		}
		material, e := h.pipeline.RunningDeliveryRevision(r.Context(), spec, regions[0].NomadRegion, jobID)
		if e != nil {
			WriteControlProblem(w, r, http.StatusConflict, "cron_schedule_database_unavailable", "running database delivery is unavailable")
			return
		}
		deliveryRevision = material.Revision
	}
	payload := map[string]interface{}{"app": id, "process": req.Process, "previousSchedule": previousSchedule, "schedule": req.Schedule, "timezone": timezone, "jobId": jobID, "imageTag": deps[0].ImageTag, "specDigest": specDigest, "deliveryRevision": fmt.Sprint(deliveryRevision), "version": fmt.Sprint(periodic.Version), "modifyIndex": fmt.Sprint(periodic.ModifyIndex), "paused": paused, "action": "schedule"}
	enqueue.Semantics = payload
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.cron-schedule", App: id, SagaID: uuid.NewString(), Ref: req.Process, Status: model.OperationQueued, Risk: "replace Nomad periodic schedule", Source: "app-control-api", Message: fmt.Sprintf("queued cron schedule update for %s process %q", id, req.Process), StartedAt: now, NextAttemptAt: now, MaxAttempts: 3, Payload: payload}
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

func (h *Handler) resolveCronScheduleReplay(ctx context.Context, request pipeline.EnqueueRequest, app, process string) (store.AcceptedOperation, bool, error) {
	previous, err := h.pipeline.ResolveEnqueue(ctx, request, "app.cron-schedule", app)
	if errors.Is(err, store.ErrAcceptanceNotFound) {
		return store.AcceptedOperation{}, false, nil
	}
	if err != nil {
		return store.AcceptedOperation{}, false, err
	}
	if previous.Operation.Kind != "app.cron-schedule" || previous.Operation.App != app || previous.Operation.Ref != process || previous.Operation.Payload["process"] != process {
		return store.AcceptedOperation{}, false, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "app.cron-schedule", Resource: app}}
	}
	return previous, true, nil
}
