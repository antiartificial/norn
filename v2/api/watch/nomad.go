package watch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hashicorp/cronexpr"
	"norn/v2/api/beacon"
	"norn/v2/api/consul"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

type NomadAllocationWatcher struct {
	nomad     *nomad.Client
	consul    *consul.Client
	beacon    *beacon.Service
	appsDir   string
	poll      time.Duration
	seen      map[string]string
	hungAfter time.Duration
}

func NewNomadAllocationWatcher(n *nomad.Client, c *consul.Client, b *beacon.Service, appsDir string) *NomadAllocationWatcher {
	return &NomadAllocationWatcher{
		nomad:     n,
		consul:    c,
		beacon:    b,
		appsDir:   appsDir,
		poll:      60 * time.Second,
		seen:      map[string]string{},
		hungAfter: 30 * time.Minute,
	}
}

func (w *NomadAllocationWatcher) Run(ctx context.Context) {
	if (w.nomad == nil && w.consul == nil) || w.beacon == nil {
		return
	}
	log.Println("nomad allocation watcher started")
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("nomad allocation watcher stopped")
			return
		case <-timer.C:
			w.check(ctx)
			timer.Reset(w.poll)
		}
	}
}

func (w *NomadAllocationWatcher) check(ctx context.Context) {
	specs, err := model.DiscoverApps(w.appsDir)
	if err != nil {
		log.Printf("nomad watcher: discover apps: %v", err)
		return
	}
	for _, spec := range specs {
		if w.nomad != nil {
			allocs, err := w.nomad.JobAllocations(spec.App)
			if err == nil {
				for _, alloc := range allocs {
					state := alloc.ClientStatus
					unhealthy := alloc.DeploymentStatus != nil && alloc.DeploymentStatus.Healthy != nil && !*alloc.DeploymentStatus.Healthy
					if unhealthy && state == "running" {
						state = "unhealthy"
					}
					if state != "failed" && state != "lost" && state != "unhealthy" {
						continue
					}
					key := fmt.Sprintf("%s:%s:%s", spec.App, shortAlloc(alloc.ID), alloc.TaskGroup)
					prev := w.seen[key]
					if prev == state {
						continue
					}
					w.seen[key] = state
					severity := model.BeaconWarning
					if state == "failed" || state == "lost" {
						severity = model.BeaconCritical
					}
					correlationKey := fmt.Sprintf("%s:%s:allocation", spec.App, alloc.TaskGroup)
					_, err := w.beacon.Emit(ctx, model.BeaconEvent{
						App:       spec.App,
						Type:      "nomad.allocation." + state,
						Severity:  severity,
						Title:     fmt.Sprintf("%s allocation %s", spec.App, state),
						Body:      fmt.Sprintf("Allocation %s task group %s is %s.", shortAlloc(alloc.ID), alloc.TaskGroup, state),
						DedupeKey: fmt.Sprintf("%s:%s:%s", spec.App, shortAlloc(alloc.ID), state),
						Metadata: map[string]interface{}{
							"allocationId":   alloc.ID,
							"taskGroup":      alloc.TaskGroup,
							"clientStatus":   alloc.ClientStatus,
							"nodeId":         alloc.NodeID,
							"correlationKey": correlationKey,
							"previousState":  prev,
						},
					})
					if err != nil {
						log.Printf("nomad watcher: beacon emit: %v", err)
					}
				}
			}
			w.checkTaskRestarts(ctx, spec)
			w.checkCron(ctx, spec)
			w.checkCronMissedRuns(ctx, spec)
		}
		w.checkServiceHealth(ctx, spec)
	}
}

func (w *NomadAllocationWatcher) checkServiceHealth(ctx context.Context, spec *model.InfraSpec) {
	if w.consul == nil {
		return
	}
	for processName := range spec.Processes {
		serviceName := spec.App + "-" + processName
		health, err := w.consul.ServiceHealthChecks(serviceName)
		if (err != nil || len(health) == 0) && spec.App != serviceName {
			health, err = w.consul.ServiceHealthChecks(spec.App)
		}
		if err != nil || len(health) == 0 {
			continue
		}
		state := aggregateServiceHealth(health)
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
			log.Printf("nomad watcher: health beacon emit: %v", err)
		}
	}
}

func aggregateServiceHealth(health []consul.ServiceHealth) string {
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

func emptyState(state string) string {
	if state == "" {
		return "unknown"
	}
	return state
}

func (w *NomadAllocationWatcher) checkCron(ctx context.Context, spec *model.InfraSpec) {
	if w.nomad == nil {
		return
	}
	for process, proc := range spec.Processes {
		if strings.TrimSpace(proc.Schedule) == "" {
			continue
		}
		parentJobID := fmt.Sprintf("%s-%s", spec.App, process)
		runs, err := w.nomad.PeriodicChildren(parentJobID)
		if err != nil {
			continue
		}
		for _, run := range runs {
			state := w.cronRunState(run.JobID, run.Status)
			startedAt, _ := time.Parse(time.RFC3339, run.StartedAt)
			if state == "running" && !startedAt.IsZero() && time.Since(startedAt) > w.hungAfter {
				state = "hung"
			}
			switch state {
			case "complete", "dead", "failed", "lost", "hung":
			default:
				continue
			}
			key := fmt.Sprintf("cron:%s:%s:%s", spec.App, process, run.JobID)
			if w.seen[key] == state {
				continue
			}
			w.seen[key] = state
			severity := model.BeaconInfo
			eventType := "cron.succeeded"
			if state == "failed" || state == "lost" || state == "hung" {
				severity = model.BeaconCritical
				eventType = "cron." + state
			}
			_, err := w.beacon.Emit(ctx, model.BeaconEvent{
				App:       spec.App,
				Type:      eventType,
				Severity:  severity,
				Title:     fmt.Sprintf("%s %s cron %s", spec.App, process, state),
				Body:      fmt.Sprintf("Cron process %s run %s is %s.", process, run.JobID, state),
				DedupeKey: fmt.Sprintf("%s:%s:%s:%s", spec.App, process, run.JobID, state),
				Metadata: map[string]interface{}{
					"process":   process,
					"jobId":     run.JobID,
					"status":    run.Status,
					"startedAt": run.StartedAt,
				},
			})
			if err != nil {
				log.Printf("nomad watcher: cron beacon emit: %v", err)
			}
		}
	}
}

func (w *NomadAllocationWatcher) cronRunState(jobID, jobStatus string) string {
	allocs, err := w.nomad.JobAllocations(jobID)
	if err != nil {
		return jobStatus
	}
	if len(allocs) == 0 {
		return jobStatus
	}
	running := false
	for _, alloc := range allocs {
		switch alloc.ClientStatus {
		case "failed", "lost":
			return alloc.ClientStatus
		case "running", "pending":
			running = true
		}
	}
	if running {
		return "running"
	}
	return "complete"
}

func (w *NomadAllocationWatcher) checkTaskRestarts(ctx context.Context, spec *model.InfraSpec) {
	infos, err := w.nomad.TaskRestartSummary(spec.App)
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
				App:      spec.App,
				Type:     "nomad.task.oom_killed",
				Severity: model.BeaconCritical,
				Title:    fmt.Sprintf("%s %s OOM killed", spec.App, info.Task),
				Body:     fmt.Sprintf("Task %s in %s was killed by the OOM killer (restarts: %d). %s", info.Task, info.TaskGroup, info.Restarts, info.LastEvent),
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
				log.Printf("nomad watcher: oom beacon emit: %v", err)
			}
		} else {
			_, err := w.beacon.Emit(ctx, model.BeaconEvent{
				App:      spec.App,
				Type:     "nomad.task.restarted",
				Severity: model.BeaconWarning,
				Title:    fmt.Sprintf("%s %s restarted", spec.App, info.Task),
				Body:     fmt.Sprintf("Task %s in %s restarted (count: %d). %s", info.Task, info.TaskGroup, info.Restarts, info.LastEvent),
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
				log.Printf("nomad watcher: restart beacon emit: %v", err)
			}
		}
	}
}

const cronMissedGracePeriod = 5 * time.Minute

func (w *NomadAllocationWatcher) checkCronMissedRuns(ctx context.Context, spec *model.InfraSpec) {
	if w.nomad == nil {
		return
	}
	for process, proc := range spec.Processes {
		if strings.TrimSpace(proc.Schedule) == "" {
			continue
		}
		parentJobID := fmt.Sprintf("%s-%s", spec.App, process)
		missedKey := fmt.Sprintf("missed:%s:%s", spec.App, process)

		info, err := w.nomad.PeriodicJobSchedule(parentJobID)
		if err != nil || info == nil {
			continue
		}
		if info.Paused || info.Status == "dead" {
			// Job is intentionally stopped; clear any prior missed-run alert state.
			delete(w.seen, missedKey)
			continue
		}
		schedule := strings.TrimSpace(info.Schedule)
		if schedule == "" {
			continue
		}

		// Parse the cron expression safely.
		var expr *cronexpr.Expression
		func() {
			defer func() { recover() }() //nolint:errcheck
			expr = cronexpr.MustParse(schedule)
		}()
		if expr == nil {
			continue
		}

		// Find the latest run's start time.
		runs, err := w.nomad.PeriodicChildren(parentJobID)
		if err != nil {
			continue
		}
		var lastRunTime time.Time
		for _, run := range runs {
			t, parseErr := time.Parse(time.RFC3339, run.StartedAt)
			if parseErr != nil {
				continue
			}
			if t.After(lastRunTime) {
				lastRunTime = t
			}
		}

		// If we have never seen a run, use a reference point far enough back
		// so that at least one interval has elapsed. Use one full interval
		// before now as the reference so we don't false-alert on first start.
		reference := lastRunTime
		if reference.IsZero() {
			// Seed from a point far enough in the past that Next() gives us
			// the most recently expected run.
			reference = time.Now().Add(-24 * time.Hour)
		}

		expectedNextRun := expr.Next(reference)
		if expectedNextRun.IsZero() {
			continue
		}

		deadline := expectedNextRun.Add(cronMissedGracePeriod)
		if time.Now().Before(deadline) {
			// The expected run hasn't been missed yet; clear any stale alert key
			// so we re-alert if a future window is missed.
			if w.seen[missedKey] == expectedNextRun.Format(time.RFC3339) {
				// still the same window and still within grace — do nothing
			}
			continue
		}

		// We're past the grace period. Check whether we already alerted for this window.
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
				"Cron process %s was expected to run at %s but no dispatch was recorded. The Nomad periodic dispatcher may be stuck or the job may be paused.",
				process, expectedNextRun.UTC().Format(time.RFC3339),
			),
			DedupeKey: fmt.Sprintf("%s:%s:missed:%s", spec.App, process, windowKey),
			Metadata: map[string]interface{}{
				"process":        process,
				"jobId":          parentJobID,
				"schedule":       schedule,
				"expectedRunAt":  expectedNextRun.UTC().Format(time.RFC3339),
				"lastRunAt":      lastRunTime.UTC().Format(time.RFC3339),
				"gracePeriod":    cronMissedGracePeriod.String(),
				"correlationKey": fmt.Sprintf("%s:%s:cron", spec.App, process),
			},
		})
		if emitErr != nil {
			log.Printf("nomad watcher: missed-run beacon emit: %v", emitErr)
		}
	}
}

func shortAlloc(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
