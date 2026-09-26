package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/hub"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

func (h *Handler) InvokeFunction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Process string `json:"process"`
		Body    string `json:"body"`
		Method  string `json:"method"`
		Path    string `json:"path"`
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

	// Find the function process
	procName := req.Process
	if procName == "" {
		// Default to first function process
		for name, proc := range spec.Processes {
			if proc.Function != nil {
				procName = name
				break
			}
		}
	}
	if procName == "" {
		writeError(w, http.StatusBadRequest, "no function process specified or found")
		return
	}

	proc, ok := spec.Processes[procName]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("process %s not found", procName))
		return
	}
	if proc.Function == nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("process %s is not a function", procName))
		return
	}

	// Resolve image tag from last deployment
	deps, err := h.db.ListDeployments(r.Context(), id, 1)
	if err != nil || len(deps) == 0 {
		writeError(w, http.StatusBadRequest, "no previous deployment found")
		return
	}
	imageTag := deps[0].ImageTag

	// Resolve secrets + function env
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

	// Inject request context as env vars
	if req.Body != "" {
		env["NORN_REQUEST_BODY"] = req.Body
	}
	if req.Method != "" {
		env["NORN_REQUEST_METHOD"] = req.Method
	}
	if req.Path != "" {
		env["NORN_REQUEST_PATH"] = req.Path
	}

	// Create unique job ID
	execID := uuid.New().String()
	jobID := fmt.Sprintf("%s-%s-%s", id, procName, execID)

	// Record execution
	fe := &store.FuncExecution{
		ID:        execID,
		App:       id,
		Process:   procName,
		Status:    "running",
		StartedAt: time.Now(),
	}
	if err := h.db.InsertFuncExecution(r.Context(), fe); err != nil {
		writeError(w, http.StatusServiceUnavailable, "cannot record function execution")
		return
	}

	// Named databases reach the invocation only through its own private,
	// create-only copy of the app's promoted delivery revision, revalidated
	// against the running targets. The copy is never updated; cleanup
	// deletes only exactly that material.
	var owned map[string]string
	revision := int64(0)
	if spec.NamedDatabases() {
		fail := func(status int, message string) {
			h.db.UpdateFuncExecution(r.Context(), execID, "failed", 1, 0)
			writeError(w, status, message)
		}
		if conflicts := spec.DatabaseEnvConflicts(env); len(conflicts) > 0 {
			fail(http.StatusConflict, fmt.Sprintf("%s delivered by the database binding must not also come from secrets or the request", strings.Join(conflicts, ", ")))
			return
		}
		if nomad.HasRuntimeDatabases(spec) {
			if h.pipeline == nil || h.pipeline.DatabaseTargets == nil {
				fail(http.StatusConflict, "named databases require a database profile")
				return
			}
			if h.findSpec(jobID) != nil {
				fail(http.StatusConflict, "function job ID collides with an app name")
				return
			}
			material, err := h.pipeline.RunningDeliveryRevision(r.Context(), spec, "global", id)
			if err != nil {
				fail(http.StatusConflict, err.Error())
				return
			}
			if owned, err = h.nomad.CopyDatabaseVariable("global", jobID, material); err != nil {
				fail(http.StatusConflict, err.Error())
				return
			}
			revision = material.Revision
		}
	}

	// Build and submit batch job
	batchJob := nomad.TranslateBatchAt(spec, procName, proc, imageTag, env, jobID, revision)
	_, err = h.nomad.SubmitJob(batchJob)
	if err != nil {
		if owned != nil {
			_ = h.nomad.DeleteDatabaseVariable("global", jobID, owned)
		}
		h.db.UpdateFuncExecution(r.Context(), execID, "failed", 1, 0)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Async wait for completion
	// Once Nomad accepted the job, the completion watcher must outlive the
	// HTTP request that created it. Its own deadline is bounded below.
	go func() {
		timeout := 30 * time.Second
		if proc.Function != nil && proc.Function.Timeout != "" {
			if d, err := time.ParseDuration(proc.Function.Timeout); err == nil {
				timeout = d
			}
		}

		start := time.Now()
		watchCtx, cancel := context.WithTimeout(context.Background(), timeout+time.Minute)
		defer cancel()
		status, exitCode, waitErr := h.nomad.WaitBatchComplete(watchCtx, jobID, timeout)
		durationMs := time.Since(start).Milliseconds()
		if waitErr != nil && status == "" {
			status = "unknown"
		}

		updateCtx, updateCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer updateCancel()
		h.db.UpdateFuncExecution(updateCtx, execID, status, exitCode, durationMs)
		if owned != nil && (status == "complete" || status == "failed") {
			// The one-shot job was purged; its copy of the connection goes.
			// Any other outcome leaves the copy, which only this unique job
			// ID can read, rather than racing a still-pending allocation.
			_ = h.nomad.DeleteDatabaseVariable("global", jobID, owned)
		}
		h.ws.Broadcast(hub.Event{
			Type:  "function.completed",
			AppID: id,
			Payload: map[string]string{
				"execId":   execID,
				"process":  procName,
				"status":   status,
				"exitCode": fmt.Sprintf("%d", exitCode),
			},
		})
	}()

	writeJSON(w, map[string]string{
		"id":      execID,
		"jobId":   jobID,
		"status":  "running",
		"process": procName,
	})
}

func (h *Handler) FunctionHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	execs, err := h.db.ListFuncExecutions(r.Context(), id, 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if execs == nil {
		execs = []store.FuncExecution{}
	}
	writeJSON(w, execs)
}
