package function

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"norn/api/hub"
	"norn/api/model"
	"norn/api/runtime"
	"norn/api/store"
)

type Executor struct {
	Runner runtime.Runner
	DB     *store.DB
	WS     *hub.Hub
	Images map[string]string // app -> latest image tag
}

func NewExecutor(runner runtime.Runner, db *store.DB, ws *hub.Hub) *Executor {
	return &Executor{
		Runner: runner,
		DB:     db,
		WS:     ws,
		Images: make(map[string]string),
	}
}

func (e *Executor) SetImage(app, imageTag string) {
	e.Images[app] = imageTag
}

func (e *Executor) Invoke(spec *model.InfraSpec, method, path, body string) (*model.FuncExecution, error) {
	imageTag := e.Images[spec.App]
	if imageTag == "" {
		imageTag = spec.App + ":latest"
	}

	timeout := 30 * time.Second
	if spec.Function != nil && spec.Function.Timeout > 0 {
		timeout = time.Duration(spec.Function.Timeout) * time.Second
	}

	memory := "256m"
	if spec.Function != nil && spec.Function.Memory != "" {
		memory = spec.Function.Memory
	}

	env := make(map[string]string)
	for k, v := range spec.Env {
		env[k] = v
	}
	env["NORN_REQUEST_BODY"] = body
	env["NORN_REQUEST_METHOD"] = method
	env["NORN_REQUEST_PATH"] = path

	exec := &model.FuncExecution{
		App:       spec.App,
		ImageTag:  imageTag,
		Status:    model.FuncRunning,
		StartedAt: time.Now(),
	}

	id, err := e.DB.InsertFuncExecution(context.Background(), exec)
	if err != nil {
		return nil, fmt.Errorf("insert execution: %w", err)
	}
	exec.ID = id

	e.WS.Broadcast(hub.Event{Type: "func.started", AppID: spec.App, Payload: map[string]interface{}{
		"executionId": id,
		"imageTag":    imageTag,
	}})

	var command []string
	if spec.Command != "" {
		command = []string{"sh", "-c", spec.Command}
	}

	result, runErr := e.Runner.Run(context.Background(), runtime.RunOpts{
		Image:   imageTag,
		Command: command,
		Env:     env,
		Timeout: timeout,
		Memory:  memory,
		Network: "bridge",
	})

	var status model.FuncExecStatus
	var exitCode int
	var output string
	var durationMs int64

	if result != nil {
		exitCode = result.ExitCode
		output = result.Output
		durationMs = result.Duration.Milliseconds()
	}

	if runErr != nil {
		if strings.Contains(runErr.Error(), "timed out") {
			status = model.FuncTimedOut
		} else {
			status = model.FuncFailed
			output = fmt.Sprintf("%s\n%s", output, runErr.Error())
		}
	} else if exitCode != 0 {
		status = model.FuncFailed
	} else {
		status = model.FuncSucceeded
	}

	e.DB.UpdateFuncExecution(context.Background(), id, status, exitCode, output, durationMs)

	eventType := "func.completed"
	if status == model.FuncFailed || status == model.FuncTimedOut {
		eventType = "func.failed"
	}
	e.WS.Broadcast(hub.Event{Type: eventType, AppID: spec.App, Payload: map[string]interface{}{
		"executionId": id,
		"status":      string(status),
		"exitCode":    exitCode,
		"durationMs":  durationMs,
	}})

	exec.Status = status
	exec.ExitCode = exitCode
	exec.Output = output
	exec.DurationMs = durationMs
	log.Printf("function: %s invoked, status=%s exit=%d duration=%dms", spec.App, status, exitCode, durationMs)

	return exec, nil
}
