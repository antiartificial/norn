package pipeline

import (
	"context"
	"fmt"
	"time"

	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/saga"
)

func (p *Pipeline) healthy(ctx context.Context, st *state, sg *saga.Saga) error {
	for _, region := range st.spec.ResolvedRegions() {
		if regionalServiceProcessCount(st.spec, region.Name) == 0 {
			continue
		}
		if err := p.waitHealthyRegion(ctx, st, sg, region); err != nil {
			_ = p.DB.UpdateDeploymentRegion(ctx, st.deploymentID, region.Name, model.StatusFailed, st.regionEvals[region.Name], err.Error(), 0)
			return err
		}
		// Readiness gates traffic: only healthy regions receive their desired
		// global weight. The persisted value is consumed by the traffic
		// reconciler and remains zero for failed/pending regions.
		_ = p.DB.UpdateDeploymentRegion(ctx, st.deploymentID, region.Name, model.StatusHealthy, st.regionEvals[region.Name], "", region.TrafficWeight)
	}
	return nil
}

func (p *Pipeline) waitHealthyRegion(ctx context.Context, st *state, sg *saga.Saga, region model.ResolvedRegion) error {
	deadline := time.After(5 * time.Minute)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	prev := make(map[string]string) // allocID → "clientStatus:healthy"

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for %s to become healthy in region %s", st.spec.App, region.Name)
		case <-ticker.C:
			allocs, err := p.Nomad.PollAllocationsRegion(st.spec.App, region.NomadRegion)
			if err != nil {
				continue
			}
			if len(allocs) == 0 {
				continue
			}

			healthy := 0
			pending := 0

			for _, a := range allocs {
				key := fmt.Sprintf("%s:%v", a.ClientStatus, a.Healthy)
				if prev[a.ID] == key {
					// No state change — skip
					if a.ClientStatus == "running" && a.Healthy != nil && *a.Healthy {
						healthy++
					} else {
						pending++
					}
					continue
				}
				prev[a.ID] = key

				var msg string
				switch {
				case a.ClientStatus != "running":
					msg = fmt.Sprintf("allocation %s pending on %s", a.ID, a.NodeName)
					pending++
				case a.Healthy == nil || !*a.Healthy:
					msg = fmt.Sprintf("allocation %s running on %s, awaiting health check", a.ID, a.NodeName)
					pending++
				default:
					msg = fmt.Sprintf("allocation %s health check passed on %s", a.ID, a.NodeName)
					healthy++
				}

				meta := map[string]string{
					"step":        "healthy",
					"allocId":     a.ID,
					"node":        a.NodeName,
					"allocStatus": a.ClientStatus,
					"region":      region.Name,
				}
				if a.Healthy != nil {
					meta["healthy"] = fmt.Sprintf("%v", *a.Healthy)
				}

				sg.Log(ctx, "alloc.progress", msg, meta)
				p.WS.Broadcast(hub.Event{
					Type:  "deploy.progress",
					AppID: st.spec.App,
					Payload: map[string]string{
						"step":        "healthy",
						"message":     msg,
						"allocId":     a.ID,
						"node":        a.NodeName,
						"allocStatus": a.ClientStatus,
						"region":      region.Name,
					},
				})
			}

			if healthy > 0 && pending == 0 {
				return nil
			}
		}
	}
}
