package pipeline

import (
	"context"
	"fmt"
	"sort"

	"norn/v2/api/cloudflared"
	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/saga"
)

func (p *Pipeline) forge(ctx context.Context, st *state, sg *saga.Saga) error {
	if len(st.spec.Endpoints) == 0 {
		sg.Log(ctx, "forge.skip", "no endpoints configured, skipping forge", nil)
		return nil
	}

	service, err := p.cloudflaredService(st.spec)
	if err != nil {
		return err
	}

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

func (p *Pipeline) cloudflaredService(spec *model.InfraSpec) (string, error) {
	processName, process, ok := cloudflaredProcess(spec)
	if !ok {
		return "", fmt.Errorf("no port found in spec for cloudflared routing")
	}

	serviceName := fmt.Sprintf("%s-%s", spec.App, processName)
	if p.Engine != nil {
		addr, err := p.Engine.ServiceAddress(serviceName)
		if err == nil {
			return "http://" + addr, nil
		}
	}

	return fmt.Sprintf("http://127.0.0.1:%d", process.Port), nil
}

func cloudflaredProcess(spec *model.InfraSpec) (string, model.Process, bool) {
	if process, ok := spec.Processes["web"]; ok && process.Port > 0 {
		return "web", process, true
	}
	names := make([]string, 0, len(spec.Processes))
	for name := range spec.Processes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		process := spec.Processes[name]
		if process.Port > 0 {
			return name, process, true
		}
	}
	return "", model.Process{}, false
}
