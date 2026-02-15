package handler

import (
	"fmt"
	"net/http"
	"os"
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
	jobID := fmt.Sprintf("%s-%s-%d", id, procName, time.Now().UnixMilli())

	// Record execution
	fe := &store.FuncExecution{
		ID:        execID,
		App:       id,
		Process:   procName,
		Status:    "running",
		StartedAt: time.Now(),
	}
	h.db.InsertFuncExecution(r.Context(), fe)

	// Build and submit batch job
	batchJob := nomad.TranslateBatch(spec, procName, proc, imageTag, env, jobID)
	_, err = h.nomad.SubmitJob(batchJob)
	if err != nil {
		h.db.UpdateFuncExecution(r.Context(), execID, "failed", 1, 0)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Async wait for completion
	go func() {
		timeout := 30 * time.Second
		if proc.Function != nil && proc.Function.Timeout != "" {
			if d, err := time.ParseDuration(proc.Function.Timeout); err == nil {
				timeout = d
			}
		}

		start := time.Now()
		status, exitCode, _ := h.nomad.WaitBatchComplete(r.Context(), jobID, timeout)
		durationMs := time.Since(start).Milliseconds()

		h.db.UpdateFuncExecution(r.Context(), execID, status, exitCode, durationMs)
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
