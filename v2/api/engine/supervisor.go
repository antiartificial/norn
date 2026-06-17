package engine

import (
	"context"
	"log"
	"time"

	"norn/v2/api/model"
)

const (
	supervisorInterval = 5 * time.Second
	restartMaxAttempts = 3
	restartWindow      = 5 * time.Minute
	restartDelay       = 15 * time.Second
)

func (e *Engine) runSupervisor(ctx context.Context) {
	ticker := time.NewTicker(supervisorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.checkInstances(ctx)
		}
	}
}

func (e *Engine) checkInstances(ctx context.Context) {
	entries, err := containerList(ctx)
	if err != nil {
		log.Printf("engine: supervisor: list: %v", err)
		return
	}

	alive := make(map[string]bool, len(entries))
	exitCodes := make(map[string]*int)
	for _, entry := range entries {
		if !IsNornContainer(entry.Name) {
			continue
		}
		alive[entry.Name] = entry.Status == "running" || entry.Status == "started"
		if entry.Status == "exited" || entry.Status == "stopped" {
			info, err := containerInspect(ctx, entry.Name)
			if err == nil && info.ExitCode != nil {
				exitCodes[entry.Name] = info.ExitCode
			}
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	for name, inst := range e.instances {
		if !inst.IsRunning() {
			continue
		}
		if inst.Kind == "cron" || inst.Kind == "batch" {
			continue
		}

		if alive[name] {
			continue
		}

		// Container is gone or exited
		exitCode := exitCodes[name]
		if exitCode != nil && *exitCode == 137 {
			inst.OOMKilled = true
		}
		if exitCode != nil {
			inst.ExitCode = exitCode
		}

		tracker := e.getRestartTracker(name)
		if e.shouldRestart(tracker) {
			tracker.attempts++
			tracker.lastRestart = time.Now()
			inst.Restarts++
			inst.LastEvent = "restarting"

			go e.restartContainer(ctx, inst, exitCode)
		} else {
			inst.Status = "failed"
			inst.LastEvent = "restart limit exceeded"
			e.emitInstanceEvent(ctx, inst, exitCode)
		}
	}
}

func (e *Engine) getRestartTracker(name string) *restartTracker {
	tracker, ok := e.restarts[name]
	if !ok {
		tracker = &restartTracker{windowStart: time.Now()}
		e.restarts[name] = tracker
	}
	if time.Since(tracker.windowStart) > restartWindow {
		tracker.attempts = 0
		tracker.windowStart = time.Now()
	}
	return tracker
}

func (e *Engine) shouldRestart(tracker *restartTracker) bool {
	return tracker.attempts < restartMaxAttempts
}

func (e *Engine) restartContainer(ctx context.Context, inst *Instance, exitCode *int) {
	time.Sleep(restartDelay)

	containerRemove(ctx, inst.ContainerName)

	opts := RunOpts{
		Name:     inst.ContainerName,
		Image:    inst.ImageTag,
		Port:     inst.Port,
	}
	if err := containerRun(ctx, opts); err != nil {
		log.Printf("engine: restart %s: %v", inst.ContainerName, err)
		e.mu.Lock()
		inst.Status = "failed"
		inst.LastEvent = "restart failed: " + err.Error()
		e.mu.Unlock()
		e.emitInstanceEvent(ctx, inst, exitCode)
		return
	}

	e.mu.Lock()
	inst.Status = "running"
	inst.LastEvent = "restarted"
	e.mu.Unlock()
}

func (e *Engine) emitInstanceEvent(ctx context.Context, inst *Instance, exitCode *int) {
	if e.beacon == nil {
		return
	}

	eventType := "instance.failed"
	severity := model.BeaconCritical
	title := inst.App + " " + inst.Process + " failed"
	body := "Container " + inst.ContainerName + " failed"

	if inst.OOMKilled {
		eventType = "instance.oom_killed"
		title = inst.App + " " + inst.Process + " OOM killed"
		body = "Container " + inst.ContainerName + " was killed by the OOM killer"
	}

	if exitCode != nil {
		body += " (exit code " + itoa2(*exitCode) + ")"
	}
	body += ". Restarts: " + itoa2(inst.Restarts) + "."

	correlationKey := inst.App + ":" + inst.Process + ":instance"
	_, err := e.beacon.Emit(ctx, model.BeaconEvent{
		App:       inst.App,
		Type:      eventType,
		Severity:  severity,
		Title:     title,
		Body:      body,
		DedupeKey: inst.ContainerName + ":" + inst.Status,
		Metadata: map[string]interface{}{
			"container":      inst.ContainerName,
			"process":        inst.Process,
			"restarts":       inst.Restarts,
			"oomKilled":      inst.OOMKilled,
			"lastEvent":      inst.LastEvent,
			"correlationKey": correlationKey,
		},
	})
	if err != nil {
		log.Printf("engine: beacon emit: %v", err)
	}
}

func itoa2(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	s := make([]byte, 0, 10)
	for i > 0 {
		s = append([]byte{byte('0' + i%10)}, s...)
		i /= 10
	}
	if neg {
		s = append([]byte{'-'}, s...)
	}
	return string(s)
}

// TaskRestartSummary returns restart info for all running instances of an app.
func (e *Engine) TaskRestartSummary(appID string) ([]TaskRestartInfo, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var out []TaskRestartInfo
	for _, inst := range e.instances {
		if inst.App != appID {
			continue
		}
		if inst.Restarts == 0 && !inst.OOMKilled {
			continue
		}
		out = append(out, TaskRestartInfo{
			App:         inst.App,
			TaskGroup:   inst.Process,
			AllocID:     ShortID(inst.ContainerName),
			Task:        inst.Process,
			Restarts:    uint64(inst.Restarts),
			LastRestart: e.lastRestartTime(inst.ContainerName),
			OOMKilled:   inst.OOMKilled,
			LastEvent:   inst.LastEvent,
		})
	}
	return out, nil
}

func (e *Engine) lastRestartTime(name string) time.Time {
	if tracker, ok := e.restarts[name]; ok {
		return tracker.lastRestart
	}
	return time.Time{}
}
