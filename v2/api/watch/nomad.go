package watch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"norn/v2/api/beacon"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

type NomadAllocationWatcher struct {
	nomad     *nomad.Client
	beacon    *beacon.Service
	appsDir   string
	poll      time.Duration
	seen      map[string]string
	hungAfter time.Duration
}

func NewNomadAllocationWatcher(n *nomad.Client, b *beacon.Service, appsDir string) *NomadAllocationWatcher {
	return &NomadAllocationWatcher{
		nomad:     n,
		beacon:    b,
		appsDir:   appsDir,
		poll:      60 * time.Second,
		seen:      map[string]string{},
		hungAfter: 30 * time.Minute,
	}
}

func (w *NomadAllocationWatcher) Run(ctx context.Context) {
	if w.nomad == nil || w.beacon == nil {
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
		allocs, err := w.nomad.JobAllocations(spec.App)
		if err != nil {
			continue
		}
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
			if w.seen[key] == state {
				continue
			}
			w.seen[key] = state
			severity := model.BeaconWarning
			if state == "failed" || state == "lost" {
				severity = model.BeaconCritical
			}
			_, err := w.beacon.Emit(ctx, model.BeaconEvent{
				App:       spec.App,
				Type:      "nomad.allocation." + state,
				Severity:  severity,
				Title:     fmt.Sprintf("%s allocation %s", spec.App, state),
				Body:      fmt.Sprintf("Allocation %s task group %s is %s.", shortAlloc(alloc.ID), alloc.TaskGroup, state),
				DedupeKey: fmt.Sprintf("%s:%s:%s", spec.App, shortAlloc(alloc.ID), state),
				Metadata: map[string]interface{}{
					"allocationId": alloc.ID,
					"taskGroup":    alloc.TaskGroup,
					"clientStatus": alloc.ClientStatus,
					"nodeId":       alloc.NodeID,
				},
			})
			if err != nil {
				log.Printf("nomad watcher: beacon emit: %v", err)
			}
		}
		w.checkCron(ctx, spec)
	}
}

func (w *NomadAllocationWatcher) checkCron(ctx context.Context, spec *model.InfraSpec) {
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

func shortAlloc(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
