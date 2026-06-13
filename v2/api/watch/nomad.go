package watch

import (
	"context"
	"fmt"
	"log"
	"time"

	"norn/v2/api/beacon"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

type NomadAllocationWatcher struct {
	nomad   *nomad.Client
	beacon  *beacon.Service
	appsDir string
	poll    time.Duration
	seen    map[string]string
}

func NewNomadAllocationWatcher(n *nomad.Client, b *beacon.Service, appsDir string) *NomadAllocationWatcher {
	return &NomadAllocationWatcher{
		nomad:   n,
		beacon:  b,
		appsDir: appsDir,
		poll:    60 * time.Second,
		seen:    map[string]string{},
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
	}
}

func shortAlloc(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
