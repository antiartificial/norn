package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

const maintenanceOutputLimit = 64 * 1024

var maintenanceRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,199}$`)

type MaintenanceExecutor interface {
	Execute(context.Context, *model.Operation) (map[string]interface{}, error)
}

type CommandMaintenanceExecutor struct {
	Repo           string
	PlatformScript string
	HostScript     string
}

func (e *CommandMaintenanceExecutor) Execute(ctx context.Context, op *model.Operation) (map[string]interface{}, error) {
	program, args, extraEnv, err := e.command(op)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	output, runErr := cmd.CombinedOutput()
	truncated := false
	if len(output) > maintenanceOutputLimit {
		output = output[len(output)-maintenanceOutputLimit:]
		truncated = true
	}
	metadata := map[string]interface{}{
		"output": strings.TrimSpace(string(output)), "outputTruncated": truncated,
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			metadata["exitCode"] = exitErr.ExitCode()
		}
		return metadata, fmt.Errorf("%s: %w", op.Kind, runErr)
	}
	metadata["exitCode"] = 0
	return metadata, nil
}

func (e *CommandMaintenanceExecutor) command(op *model.Operation) (string, []string, []string, error) {
	repo := strings.TrimSpace(e.Repo)
	platformScript, err := resolveMaintenanceScript(e.PlatformScript, repo, "platform-upgrade")
	if err != nil && strings.HasPrefix(op.Kind, "platform.") {
		return "", nil, nil, err
	}
	hostScript, hostErr := resolveMaintenanceScript(e.HostScript, repo, "host-runtime")
	if hostErr != nil && strings.HasPrefix(op.Kind, "host.") {
		return "", nil, nil, hostErr
	}
	env := []string{"NORN_DRAIN_EXCLUDE_OPERATION_ID=" + op.ID}
	if repo != "" {
		env = append(env, "NORN_PLATFORM_REPO="+repo, "NORN_HOST_REPO="+repo)
	}
	switch op.Kind {
	case "platform.preflight":
		ref := payloadString(op.Payload, "ref", "HEAD")
		if !maintenanceRefPattern.MatchString(ref) {
			return "", nil, nil, fmt.Errorf("invalid platform ref")
		}
		return platformScript, []string{"preflight", ref}, env, nil
	case "platform.upgrade":
		ref := payloadString(op.Payload, "ref", "HEAD")
		mode := payloadString(op.Payload, "mode", "restart")
		drainMode := payloadString(op.Payload, "drainMode", "fail")
		if !maintenanceRefPattern.MatchString(ref) {
			return "", nil, nil, fmt.Errorf("invalid platform ref")
		}
		if mode != "restart" && mode != "proxy" {
			return "", nil, nil, fmt.Errorf("invalid platform upgrade mode")
		}
		if drainMode != "fail" && drainMode != "wait" && drainMode != "force" {
			return "", nil, nil, fmt.Errorf("invalid platform drain mode")
		}
		env = append(env,
			"NORN_PLATFORM_UPGRADE_MODE="+mode,
			"NORN_DRAIN_MODE="+drainMode,
		)
		return platformScript, []string{"upgrade", ref}, env, nil
	case "platform.smoke":
		return platformScript, []string{"smoke"}, env, nil
	case "platform.rollback":
		sha := payloadString(op.Payload, "sha", "")
		if !maintenanceRefPattern.MatchString(sha) {
			return "", nil, nil, fmt.Errorf("invalid platform release sha")
		}
		return platformScript, []string{"rollback", sha}, env, nil
	case "host.assure":
		args := []string{"assure"}
		if repo != "" {
			args = append(args, "--repo", repo)
		}
		return hostScript, args, env, nil
	default:
		return "", nil, nil, fmt.Errorf("unsupported maintenance operation %s", op.Kind)
	}
}

func resolveMaintenanceScript(explicit, repo, name string) (string, error) {
	candidates := []string{}
	if strings.TrimSpace(explicit) != "" {
		candidates = append(candidates, explicit)
	}
	if repo != "" {
		candidates = append(candidates, filepath.Join(repo, "v2", "scripts", name))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "v2", "scripts", name), filepath.Join(cwd, "scripts", name))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s script not found", name)
}

func payloadString(payload map[string]interface{}, key, fallback string) string {
	if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

type MaintenanceWorker struct {
	db       *store.DB
	executor MaintenanceExecutor
	id       string
	kinds    []string
	lease    time.Duration
	poll     time.Duration
}

func NewMaintenanceWorker(db *store.DB, executor MaintenanceExecutor) *MaintenanceWorker {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	return &MaintenanceWorker{
		db: db, executor: executor, id: fmt.Sprintf("host-agent:%s:%d", host, os.Getpid()),
		kinds: []string{"platform.preflight", "platform.upgrade", "platform.rollback", "platform.smoke", "host.assure"},
		lease: 5 * time.Minute, poll: 2 * time.Second,
	}
}

func (w *MaintenanceWorker) Run(ctx context.Context) {
	if err := w.db.RecoverMaintenanceOperations(ctx); err != nil {
		log.Printf("maintenance recovery: %v", err)
	}
	log.Printf("maintenance worker %s started", w.id)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := w.runOnce(ctx); err != nil {
				log.Printf("maintenance worker: %v", err)
			}
			timer.Reset(w.poll)
		}
	}
}

func (w *MaintenanceWorker) runOnce(ctx context.Context) error {
	for {
		op, err := w.db.ClaimNextOperation(ctx, w.id, w.lease, w.kinds)
		if err != nil || op == nil {
			return err
		}
		w.handle(ctx, op)
	}
}

func (w *MaintenanceWorker) handle(ctx context.Context, op *model.Operation) {
	w.recordEvent(ctx, op, "maintenance.started", "running", "maintenance operation started")
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if err := w.db.RenewOperationLease(heartbeatCtx, op.ID, w.id, time.Now().Add(w.lease)); err != nil {
					log.Printf("maintenance lease %s: %v", op.ID, err)
				}
			}
		}
	}()
	metadata, err := w.executor.Execute(ctx, op)
	stopHeartbeat()
	<-heartbeatDone
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if err != nil {
		_ = w.db.FinishOperation(ctx, op.ID, model.OperationFailed, err.Error(), metadata)
		w.recordEvent(ctx, op, "maintenance.failed", "failed", err.Error())
		return
	}
	_ = w.db.FinishOperation(ctx, op.ID, model.OperationSucceeded, op.Kind+" complete", metadata)
	w.recordEvent(ctx, op, "maintenance.completed", "succeeded", op.Kind+" complete")
}

func (w *MaintenanceWorker) recordEvent(ctx context.Context, op *model.Operation, eventType, status, message string) {
	event := hub.Event{Timestamp: time.Now().UTC(), Type: eventType, Payload: map[string]interface{}{
		"operationId": op.ID, "kind": op.Kind, "status": status, "message": message,
	}}
	if err := w.db.AppendHubEvent(ctx, &event); err != nil {
		log.Printf("maintenance event %s: %v", op.ID, err)
	}
}
