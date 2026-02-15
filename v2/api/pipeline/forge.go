package pipeline

import (
	"context"
	"fmt"

	"norn/v2/api/cloudflared"
	"norn/v2/api/hub"
	"norn/v2/api/saga"
)

func (p *Pipeline) forge(ctx context.Context, st *state, sg *saga.Saga) error {
	if len(st.spec.Endpoints) == 0 {
		sg.Log(ctx, "forge.skip", "no endpoints configured, skipping forge", nil)
		return nil
	}

	if p.RegistryURL == "" {
		sg.Log(ctx, "forge.skip", "dev mode — skipping cloudflared (no K8s)", nil)
		return nil
	}

	// Get node address from running allocations
	allocs, err := p.Nomad.PollAllocations(st.spec.App)
	if err != nil {
		return fmt.Errorf("poll allocations: %w", err)
	}
	if len(allocs) == 0 {
		return fmt.Errorf("no running allocations for %s", st.spec.App)
	}

	// Find the first allocation's node address
	nodeInfo, err := p.Nomad.NodeInfo(allocs[0].NodeID)
	if err != nil {
		return fmt.Errorf("node info: %w", err)
	}

	// Find the static port from the spec
	var port int
	for _, proc := range st.spec.Processes {
		if proc.Port > 0 {
			port = proc.Port
			break
		}
	}
	if port == 0 {
		return fmt.Errorf("no port found in spec for cloudflared routing")
	}

	service := fmt.Sprintf("http://%s:%d", nodeInfo.Address, port)

	// Read current cloudflared config
	cfg, err := cloudflared.ReadConfig(ctx)
	if err != nil {
		return fmt.Errorf("read cloudflared config: %w", err)
	}

	changed := false
	for _, ep := range st.spec.Endpoints {
		if cloudflared.AddIngress(cfg, ep.URL, service) {
			changed = true
			sg.Log(ctx, "forge.route", fmt.Sprintf("routing %s → %s", ep.URL, service), nil)
			p.WS.Broadcast(hub.Event{Type: "deploy.progress", AppID: st.spec.App, Payload: map[string]string{
				"step":    "forge",
				"message": fmt.Sprintf("routing %s → %s", ep.URL, service),
				"sagaId":  sg.ID,
			}})
		}
	}

	if !changed {
		sg.Log(ctx, "forge.skip", "cloudflared routes already up to date", nil)
		return nil
	}

	if err := cloudflared.ApplyConfig(ctx, cfg); err != nil {
		return fmt.Errorf("apply cloudflared config: %w", err)
	}

	if err := cloudflared.Restart(ctx); err != nil {
		return fmt.Errorf("restart cloudflared: %w", err)
	}

	sg.Log(ctx, "forge.applied", "cloudflared config updated and restarted", nil)
	return nil
}
