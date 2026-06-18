package pipeline

import (
	"context"
	"fmt"
	"time"

	"norn/v2/api/engine"
	"norn/v2/api/hub"
	"norn/v2/api/saga"
)

func (p *Pipeline) healthy(ctx context.Context, st *state, sg *saga.Saga) error {
	deadline := time.After(5 * time.Minute)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	prev := make(map[string]string) // allocID → "clientStatus:healthy"

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for %s to become healthy", st.spec.App)
		case <-ticker.C:
			instances, err := p.Engine.PollInstances(st.spec.App)
			if err != nil {
				continue
			}
			if len(instances) == 0 {
				continue
			}

			healthy := 0
			pending := 0

			for _, inst := range instances {
				key := fmt.Sprintf("%s:%v", inst.Status, inst.Healthy)
				allocID := engine.ShortID(inst.ContainerName)
				if prev[allocID] == key {
					if inst.IsRunning() && inst.Healthy != nil && *inst.Healthy {
						healthy++
					} else {
						pending++
					}
					continue
				}
				prev[allocID] = key

				var msg string
				switch {
				case !inst.IsRunning():
					msg = fmt.Sprintf("instance %s pending", allocID)
					pending++
				case inst.Healthy == nil || !*inst.Healthy:
					msg = fmt.Sprintf("instance %s running, awaiting health check", allocID)
					pending++
				default:
					msg = fmt.Sprintf("instance %s health check passed", allocID)
					healthy++
				}

				meta := map[string]string{
					"step":        "healthy",
					"allocId":     allocID,
					"allocStatus": inst.Status,
				}
				if inst.Healthy != nil {
					meta["healthy"] = fmt.Sprintf("%v", *inst.Healthy)
				}

				sg.Log(ctx, "alloc.progress", msg, meta)
				p.WS.Broadcast(hub.Event{
					Type:  "deploy.progress",
					AppID: st.spec.App,
					Payload: map[string]string{
						"step":        "healthy",
						"message":     msg,
						"allocId":     allocID,
						"allocStatus": inst.Status,
					},
				})
			}

			if healthy > 0 && pending == 0 {
				return nil
			}
		}
	}
}
