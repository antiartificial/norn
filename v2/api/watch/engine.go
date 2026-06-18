package watch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hashicorp/cronexpr"
	"norn/v2/api/beacon"
	"norn/v2/api/engine"
	"norn/v2/api/model"
)

type EngineWatcher struct {
	engine    *engine.Engine
	beacon    *beacon.Service
	appsDir   string
	poll      time.Duration
	seen      map[string]string
	hungAfter time.Duration
}

func NewEngineWatcher(eng *engine.Engine, b *beacon.Service, appsDir string) *EngineWatcher {
	return &EngineWatcher{
		engine:    eng,
		beacon:    b,
		appsDir:   appsDir,
		poll:      60 * time.Second,
		seen:      map[string]string{},
		hungAfter: 30 * time.Minute,
	}
}

func (w *EngineWatcher) Run(ctx context.Context) {
	if w.engine == nil || w.beacon == nil {
		return
	}
	log.Println("engine watcher started")
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("engine watcher stopped")
			return
		case <-timer.C:
			w.check(ctx)
			timer.Reset(w.poll)
		}
	}
}

func (w *EngineWatcher) check(ctx context.Context) {
	specs, err := model.DiscoverApps(w.appsDir)
	if err != nil {
		log.Printf("engine watcher: discover apps: %v", err)
		return
	}
	for _, spec := range specs {
		w.checkInstances(ctx, spec)
		w.checkTaskRestarts(ctx, spec)
		w.checkCron(ctx, spec)
		w.checkCronMissedRuns(ctx, spec)
		w.checkServiceHealth(ctx, spec)
	}
}

func (w *EngineWatcher) checkInstances(ctx context.Context, spec *model.InfraSpec) {
	instances, err := w.engine.JobInstances(spec.App)
	if err != nil {
		return
	}
	for _, inst := range instances {
		state := inst.Status
		if inst.Healthy != nil && !*inst.Healthy && inst.IsRunning() {
			state = "unhealthy"
		}
		if state != "failed" && state != "unhealthy" {
			continue
		}
		key := fmt.Sprintf("%s:%s:%s", spec.App, engine.ShortID(inst.ContainerName), inst.Process)
		prev := w.seen[key]
		if prev == state {
			continue
		}
		w.seen[key] = state
		severity := model.BeaconWarning
		if state == "failed" {
			severity = model.BeaconCritical
		}
		correlationKey := fmt.Sprintf("%s:%s:instance", spec.App, inst.Process)
		_, err := w.beacon.Emit(ctx, model.BeaconEvent{
			App:       spec.App,
			Type:      "instance." + state,
			Severity:  severity,
			Title:     fmt.Sprintf("%s instance %s", spec.App, state),
			Body:      fmt.Sprintf("Instance %s process %s is %s.", engine.ShortID(inst.ContainerName), inst.Process, state),
			DedupeKey: fmt.Sprintf("%s:%s:%s", spec.App, engine.ShortID(inst.ContainerName), state),
			Metadata: map[string]interface{}{
				"container":      inst.ContainerName,
				"process":        inst.Process,
				"status":         inst.Status,
				"correlationKey": correlationKey,
				"previousState":  prev,
			},
		})
		if err != nil {
			log.Printf("engine watcher: beacon emit: %v", err)
		}
	}
}

func (w *EngineWatcher) checkServiceHealth(ctx context.Context, spec *model.InfraSpec) {
	for processName := range spec.Processes {
		serviceName := spec.App + "-" + processName
		health, err := w.engine.ServiceHealthChecks(serviceName)
		if (err != nil || len(health) == 0) && spec.App != serviceName {
			health, err = w.engine.ServiceHealthChecks(spec.App)
		}
		if err != nil || len(health) == 0 {
			continue
		}
		state := aggregateHealth(health)
		key := fmt.Sprintf("health:%s:%s", spec.App, processName)
		previous := w.seen[key]
		if previous == state {
			continue
		}
		w.seen[key] = state
		if previous == "" && state == "passing" {
			continue
		}
		eventType := "service.health." + state
		severity := model.BeaconWarning
		title := fmt.Sprintf("%s %s health %s", spec.App, processName, state)
		if state == "critical" {
			severity = model.BeaconCritical
		}
		if state == "passing" {
			eventType = "service.health.recovered"
			severity = model.BeaconInfo
			title = fmt.Sprintf("%s %s recovered", spec.App, processName)
		}
		correlationKey := fmt.Sprintf("%s:%s:health", spec.App, processName)
		previousEventType := ""
		if previous != "" && previous != "passing" {
			previousEventType = "service.health." + previous
		}
		_, err = w.beacon.Emit(ctx, model.BeaconEvent{
			App:       spec.App,
			Type:      eventType,
			Severity:  severity,
			Title:     title,
			Body:      fmt.Sprintf("Service %s changed from %s to %s.", serviceName, emptyState(previous), state),
			DedupeKey: correlationKey,
			Metadata: map[string]interface{}{
				"process":           processName,
				"service":           serviceName,
				"previous":          previous,
				"status":            state,
				"instanceCount":     len(health),
				"correlationKey":    correlationKey,
				"previousEventType": previousEventType,
			},
		})
		if err != nil {
			log.Printf("engine watcher: health beacon emit: %v", err)
		}
	}
}

func emptyState(state string) string {
	if state == "" {
		return "unknown"
	}
	return state
}

func aggregateHealth(health []engine.ServiceHealth) string {
	state := "passing"
	for _, instance := range health {
		switch instance.Status {
		case "critical":
			return "critical"
		case "warning":
			state = "warning"
		}
	}
	return state
}

func (w *EngineWatcher) checkCron(ctx context.Context, spec *model.InfraSpec) {
	for process, proc := range spec.Processes {
		if strings.TrimSpace(proc.Schedule) == "" {
			continue
		}
		parentJobID := fmt.Sprintf("%s-%s", spec.App, process)
		runs, err := w.engine.CronHistory(parentJobID)
		if err != nil {
			continue
		}
		for _, run := range runs {
			state := run.Status
			startedAt, _ := time.Parse(time.RFC3339, run.StartedAt)
			if state == "running" && !startedAt.IsZero() && time.Since(startedAt) > w.hungAfter {
				state = "hung"
			}
			switch state {
			case "complete", "failed", "hung":
			default:
				continue
			}
			key := fmt.Sprintf("cron:%s:%s:%s", spec.App, process, run.ID)
			if w.seen[key] == state {
				continue
			}
			w.seen[key] = state
			severity := model.BeaconInfo
			eventType := "cron.succeeded"
			if state == "failed" || state == "hung" {
				severity = model.BeaconCritical
				eventType = "cron." + state
			}
			_, err := w.beacon.Emit(ctx, model.BeaconEvent{
				App:       spec.App,
				Type:      eventType,
				Severity:  severity,
				Title:     fmt.Sprintf("%s %s cron %s", spec.App, process, state),
				Body:      fmt.Sprintf("Cron process %s run %s is %s.", process, run.ID, state),
				DedupeKey: fmt.Sprintf("%s:%s:%s:%s", spec.App, process, run.ID, state),
				Metadata: map[string]interface{}{
					"process":   process,
					"runId":     run.ID,
					"status":    run.Status,
					"startedAt": run.StartedAt,
				},
			})
			if err != nil {
				log.Printf("engine watcher: cron beacon emit: %v", err)
			}
		}
	}
}

func (w *EngineWatcher) checkTaskRestarts(ctx context.Context, spec *model.InfraSpec) {
	infos, err := w.engine.TaskRestartSummary(spec.App)
	if err != nil {
		return
	}
	for _, info := range infos {
		if info.Restarts == 0 {
			continue
		}
		key := fmt.Sprintf("restart:%s:%s:%s:%s", spec.App, info.TaskGroup, info.AllocID, info.Task)
		prevStr := w.seen[key]
		prev := uint64(0)
		if prevStr != "" {
			fmt.Sscanf(prevStr, "%d", &prev)
		}
		if info.Restarts <= prev {
			continue
		}
		w.seen[key] = fmt.Sprintf("%d", info.Restarts)
		correlationKey := fmt.Sprintf("%s:%s:%s:restarts", spec.App, info.TaskGroup, info.Task)

		if info.OOMKilled {
			_, err := w.beacon.Emit(ctx, model.BeaconEvent{
				App:       spec.App,
				Type:      "instance.oom_killed",
				Severity:  model.BeaconCritical,
				Title:     fmt.Sprintf("%s %s OOM killed", spec.App, info.Task),
				Body:      fmt.Sprintf("Task %s in %s was killed by the OOM killer (restarts: %d). %s", info.Task, info.TaskGroup, info.Restarts, info.LastEvent),
				DedupeKey: fmt.Sprintf("%s:%s:%s:oom:%d", spec.App, info.AllocID, info.Task, info.Restarts),
				Metadata: map[string]interface{}{
					"task":           info.Task,
					"taskGroup":      info.TaskGroup,
					"allocId":        info.AllocID,
					"restarts":       info.Restarts,
					"lastEvent":      info.LastEvent,
					"correlationKey": correlationKey,
				},
			})
			if err != nil {
				log.Printf("engine watcher: oom beacon emit: %v", err)
			}
		} else {
			_, err := w.beacon.Emit(ctx, model.BeaconEvent{
				App:       spec.App,
				Type:      "instance.restarted",
				Severity:  model.BeaconWarning,
				Title:     fmt.Sprintf("%s %s restarted", spec.App, info.Task),
				Body:      fmt.Sprintf("Task %s in %s restarted (count: %d). %s", info.Task, info.TaskGroup, info.Restarts, info.LastEvent),
				DedupeKey: fmt.Sprintf("%s:%s:%s:restart:%d", spec.App, info.AllocID, info.Task, info.Restarts),
				Metadata: map[string]interface{}{
					"task":           info.Task,
					"taskGroup":      info.TaskGroup,
					"allocId":        info.AllocID,
					"restarts":       info.Restarts,
					"lastEvent":      info.LastEvent,
					"correlationKey": correlationKey,
				},
			})
			if err != nil {
				log.Printf("engine watcher: restart beacon emit: %v", err)
			}
		}
	}
}

const cronMissedGracePeriod = 5 * time.Minute

func cronEvaluationLocation(spec *model.InfraSpec, proc model.Process, info *engine.CronJobInfo) *time.Location {
	timezone := ""
	if info != nil {
		timezone = strings.TrimSpace(info.TimeZone)
	}
	if timezone == "" {
		timezone = strings.TrimSpace(model.ResolveProcessTimezone(spec, proc))
	}
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (w *EngineWatcher) checkCronMissedRuns(ctx context.Context, spec *model.InfraSpec) {
	for process, proc := range spec.Processes {
		if strings.TrimSpace(proc.Schedule) == "" {
			continue
		}
		parentJobID := fmt.Sprintf("%s-%s", spec.App, process)
		missedKey := fmt.Sprintf("missed:%s:%s", spec.App, process)

		info, err := w.engine.CronScheduleInfo(parentJobID)
		if err != nil || info == nil {
			continue
		}
		if info.Paused || info.Status == "dead" {
			delete(w.seen, missedKey)
			continue
		}
		schedule := strings.TrimSpace(info.Schedule)
		if schedule == "" {
			continue
		}

		var expr *cronexpr.Expression
		func() {
			defer func() { recover() }()
			expr = cronexpr.MustParse(schedule)
		}()
		if expr == nil {
			continue
		}
		location := cronEvaluationLocation(spec, proc, info)
		now := time.Now().In(location)

		runs, err := w.engine.CronHistory(parentJobID)
		if err != nil {
			continue
		}
		var lastRunTime time.Time
		for _, run := range runs {
			t, parseErr := time.Parse(time.RFC3339, run.StartedAt)
			if parseErr != nil {
				continue
			}
			t = t.In(location)
			if t.After(lastRunTime) {
				lastRunTime = t
			}
		}

		reference := lastRunTime
		if info.SubmittedAt != "" {
			if submittedAt, parseErr := time.Parse(time.RFC3339, info.SubmittedAt); parseErr == nil && submittedAt.In(location).After(reference) {
				reference = submittedAt.In(location)
			}
		}
		if reference.IsZero() {
			reference = now.Add(-24 * time.Hour)
		}

		expectedNextRun := expr.Next(reference)
		if expectedNextRun.IsZero() {
			continue
		}

		deadline := expectedNextRun.Add(cronMissedGracePeriod)
		if now.Before(deadline) {
			continue
		}

		windowKey := expectedNextRun.Format(time.RFC3339)
		if w.seen[missedKey] == windowKey {
			continue
		}
		w.seen[missedKey] = windowKey

		_, emitErr := w.beacon.Emit(ctx, model.BeaconEvent{
			App:      spec.App,
			Type:     "cron.missed_run",
			Severity: model.BeaconCritical,
			Title:    fmt.Sprintf("%s %s cron missed run", spec.App, process),
			Body: fmt.Sprintf(
				"Cron process %s was expected to run at %s but no dispatch was recorded.",
				process, expectedNextRun.UTC().Format(time.RFC3339),
			),
			DedupeKey: fmt.Sprintf("%s:%s:missed:%s", spec.App, process, windowKey),
			Metadata: map[string]interface{}{
				"process":        process,
				"jobId":          parentJobID,
				"schedule":       schedule,
				"timezone":       location.String(),
				"expectedRunAt":  expectedNextRun.UTC().Format(time.RFC3339),
				"lastRunAt":      lastRunTime.UTC().Format(time.RFC3339),
				"gracePeriod":    cronMissedGracePeriod.String(),
				"correlationKey": fmt.Sprintf("%s:%s:cron", spec.App, process),
			},
		})
		if emitErr != nil {
			log.Printf("engine watcher: missed-run beacon emit: %v", emitErr)
		}
	}
}
