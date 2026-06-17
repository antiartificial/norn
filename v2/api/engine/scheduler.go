package engine

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/cronexpr"

	"norn/v2/api/model"
)

// RegisterCron registers a periodic job for a scheduled process.
func (e *Engine) RegisterCron(spec *model.InfraSpec, procName string, proc model.Process, imageTag string, env map[string]string) error {
	jobID := fmt.Sprintf("%s-%s", spec.App, procName)

	e.mu.Lock()
	e.cronJobs[jobID] = &CronEntry{
		JobID:    jobID,
		App:      spec.App,
		Process:  procName,
		Schedule: proc.Schedule,
		Timezone: proc.Timezone,
		ImageTag: imageTag,
		Env:      env,
		Spec:     spec,
		Paused:   false,
	}
	e.mu.Unlock()

	log.Printf("engine: registered cron %s: %s (%s)", jobID, proc.Schedule, proc.Timezone)
	return nil
}

// UnregisterCron removes a cron entry (pause).
func (e *Engine) UnregisterCron(jobID string) {
	e.mu.Lock()
	if entry, ok := e.cronJobs[jobID]; ok {
		entry.Paused = true
	}
	e.mu.Unlock()
}

// ResumeCron re-enables a paused cron entry.
func (e *Engine) ResumeCron(jobID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry, ok := e.cronJobs[jobID]
	if !ok {
		return fmt.Errorf("cron %s not found", jobID)
	}
	entry.Paused = false
	return nil
}

// UpdateCronSchedule updates the schedule for a cron entry.
func (e *Engine) UpdateCronSchedule(jobID, schedule string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry, ok := e.cronJobs[jobID]
	if !ok {
		return fmt.Errorf("cron %s not found", jobID)
	}
	entry.Schedule = schedule
	return nil
}

// CronForce manually triggers a cron job immediately.
func (e *Engine) CronForce(ctx context.Context, jobID string) (string, error) {
	e.mu.RLock()
	entry, ok := e.cronJobs[jobID]
	if !ok {
		e.mu.RUnlock()
		return "", fmt.Errorf("cron %s not found", jobID)
	}
	entryCopy := *entry
	e.mu.RUnlock()

	return e.dispatchCron(ctx, &entryCopy)
}

// CronHistory returns recent runs for a cron parent job.
func (e *Engine) CronHistory(parentJobID string) ([]CronRun, error) {
	e.mu.RLock()
	entry, ok := e.cronJobs[parentJobID]
	if !ok {
		e.mu.RUnlock()
		return nil, nil
	}
	app, process := entry.App, entry.Process
	e.mu.RUnlock()

	// Query the database for run history
	if e.db == nil || e.db.Pool == nil {
		return e.cronHistoryFromInstances(app, process), nil
	}

	return e.cronHistoryFromDB(context.Background(), app, process)
}

func (e *Engine) cronHistoryFromInstances(app, process string) []CronRun {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var out []CronRun
	for _, inst := range e.instances {
		if inst.App == app && inst.Process == process && inst.Kind == "cron" {
			run := CronRun{
				ID:        inst.ContainerName,
				App:       inst.App,
				Process:   inst.Process,
				Container: inst.ContainerName,
				Status:    inst.Status,
				StartedAt: inst.StartedAt.Format(time.RFC3339),
			}
			if inst.ExitCode != nil {
				run.ExitCode = inst.ExitCode
			}
			out = append(out, run)
		}
	}
	return out
}

func (e *Engine) cronHistoryFromDB(ctx context.Context, app, process string) ([]CronRun, error) {
	// This will be wired to the cron_runs Postgres table.
	// For now, fall back to instance tracking.
	return e.cronHistoryFromInstances(app, process), nil
}

// CronScheduleInfo returns schedule info for a cron job.
func (e *Engine) CronScheduleInfo(jobID string) (*CronJobInfo, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	entry, ok := e.cronJobs[jobID]
	if !ok {
		return nil, nil
	}

	status := "running"
	if entry.Paused {
		status = "dead"
	}

	return &CronJobInfo{
		JobID:    entry.JobID,
		App:      entry.App,
		Process:  entry.Process,
		Schedule: entry.Schedule,
		TimeZone: entry.Timezone,
		Paused:   entry.Paused,
		Status:   status,
	}, nil
}

func (e *Engine) runCronScheduler(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	lastCheck := make(map[string]time.Time)

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.mu.RLock()
			entries := make([]CronEntry, 0, len(e.cronJobs))
			for _, entry := range e.cronJobs {
				if !entry.Paused {
					entries = append(entries, *entry)
				}
			}
			e.mu.RUnlock()

			now := time.Now()
			for _, entry := range entries {
				expr, err := parseCronExpr(entry.Schedule)
				if err != nil {
					continue
				}

				location := loadLocation(entry.Timezone)
				localNow := now.In(location)

				last, ok := lastCheck[entry.JobID]
				if !ok {
					lastCheck[entry.JobID] = localNow
					continue
				}

				nextRun := expr.Next(last)
				if !nextRun.IsZero() && localNow.After(nextRun) {
					lastCheck[entry.JobID] = localNow
					go e.dispatchCron(ctx, &entry)
				}
			}
		}
	}
}

func (e *Engine) dispatchCron(ctx context.Context, entry *CronEntry) (string, error) {
	ts := time.Now().Unix()
	name := CronRunName(entry.App, entry.Process, ts)

	proc, ok := entry.Spec.Processes[entry.Process]
	if !ok {
		return "", fmt.Errorf("process %s not in spec for %s", entry.Process, entry.App)
	}

	opts := buildRunOpts(name, entry.Spec, entry.Process, proc, entry.ImageTag, entry.Env)
	if err := containerRun(ctx, opts); err != nil {
		return "", fmt.Errorf("dispatch cron %s: %w", name, err)
	}

	e.registerInstance(name, entry.App, entry.Process, 0, "cron", entry.ImageTag, proc.Port)

	runID := uuid.New().String()
	log.Printf("engine: cron dispatched %s (run: %s)", name, runID)

	// Wait for completion in the background
	go func() {
		waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		status, exitCode, err := e.WaitBatchComplete(waitCtx, name, 30*time.Minute)
		if err != nil {
			log.Printf("engine: cron %s wait: %v", name, err)
			return
		}
		log.Printf("engine: cron %s completed: %s (exit: %d)", name, status, exitCode)
	}()

	return runID, nil
}

func parseCronExpr(schedule string) (*cronexpr.Expression, error) {
	var expr *cronexpr.Expression
	var parseErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				parseErr = fmt.Errorf("invalid cron: %v", r)
			}
		}()
		expr = cronexpr.MustParse(schedule)
	}()
	return expr, parseErr
}

func loadLocation(timezone string) *time.Location {
	if timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}
