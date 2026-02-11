package pipeline

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"norn/api/hub"
	"norn/api/k8s"
	"norn/api/model"
	"norn/api/store"
)

type TeardownPipeline struct {
	DB         *store.DB
	Kube       *k8s.Client
	WS         *hub.Hub
	TunnelName string
}

type teardownStep struct {
	name string
	fn   func(ctx context.Context, res *model.ForgeResources) (string, error)
}

func (t *TeardownPipeline) Run(state *model.ForgeState) {
	ctx := context.Background()
	steps := []teardownStep{
		{name: "remove-dns-route", fn: t.removeDNSRoute},
		{name: "unpatch-cloudflared", fn: t.unpatchCloudflared},
		{name: "restart-cloudflared", fn: t.restartCloudflared},
		{name: "delete-service", fn: t.deleteService},
		{name: "delete-deployment", fn: t.deleteDeployment},
	}

	state.Steps = nil // Reset steps for teardown

	for _, s := range steps {
		t.WS.Broadcast(hub.Event{Type: "teardown.step", AppID: state.App, Payload: map[string]string{
			"step":   s.name,
			"status": "running",
		}})

		start := time.Now()
		output, err := s.fn(ctx, &state.Resources)
		elapsed := time.Since(start).Milliseconds()

		if err != nil {
			state.Steps = append(state.Steps, model.ForgeStepLog{
				Step:       s.name,
				Status:     "failed",
				DurationMs: elapsed,
				Output:     err.Error(),
			})
			state.Status = model.ForgeFailed
			state.Error = fmt.Sprintf("%s: %v", s.name, err)
			t.DB.UpdateForgeState(ctx, state.App, state.Status, state.Steps, state.Resources, state.Error)

			t.WS.Broadcast(hub.Event{Type: "teardown.failed", AppID: state.App, Payload: map[string]string{
				"step":  s.name,
				"error": state.Error,
			}})
			return
		}

		state.Steps = append(state.Steps, model.ForgeStepLog{
			Step:       s.name,
			Status:     "completed",
			DurationMs: elapsed,
			Output:     output,
		})
		t.DB.UpdateForgeState(ctx, state.App, model.ForgeTearingDown, state.Steps, state.Resources, "")

		t.WS.Broadcast(hub.Event{Type: "teardown.step", AppID: state.App, Payload: map[string]string{
			"step":       s.name,
			"status":     "completed",
			"output":     output,
			"durationMs": fmt.Sprintf("%d", elapsed),
		}})
	}

	state.Status = model.ForgeUnforged
	state.Error = ""
	state.Resources = model.ForgeResources{}
	t.DB.UpdateForgeState(ctx, state.App, state.Status, state.Steps, state.Resources, "")

	t.WS.Broadcast(hub.Event{Type: "teardown.completed", AppID: state.App, Payload: map[string]string{}})
}

func (t *TeardownPipeline) removeDNSRoute(ctx context.Context, res *model.ForgeResources) (string, error) {
	if !res.DNSRoute || res.ExternalHost == "" {
		return "skipped (no DNS route to remove)", nil
	}

	if _, err := exec.LookPath("cloudflared"); err != nil {
		res.DNSRoute = false
		return "skipped (cloudflared CLI not found)", nil
	}

	cmd := exec.CommandContext(ctx, "cloudflared", "tunnel", "route", "dns", "--remove", t.TunnelName, res.ExternalHost)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		// DNS route removal is best-effort — log warning but don't fail
		res.DNSRoute = false
		return fmt.Sprintf("warning: %s (non-fatal)", output), nil
	}
	res.DNSRoute = false
	return output, nil
}

func (t *TeardownPipeline) unpatchCloudflared(ctx context.Context, res *model.ForgeResources) (string, error) {
	if !res.CloudflaredRule || res.ExternalHost == "" {
		return "skipped (no cloudflared rule to remove)", nil
	}

	err := t.Kube.PatchConfigMap(ctx, "cloudflared", "cloudflared", "config.yaml", func(data string) (string, error) {
		var cfg map[string]interface{}
		if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
			return "", fmt.Errorf("parse cloudflared config: %w", err)
		}

		ingress, ok := cfg["ingress"].([]interface{})
		if !ok {
			return data, nil // nothing to do
		}

		var filtered []interface{}
		for _, rule := range ingress {
			if ruleMap, ok := rule.(map[string]interface{}); ok {
				if hostname, _ := ruleMap["hostname"].(string); hostname == res.ExternalHost {
					continue // remove this rule
				}
			}
			filtered = append(filtered, rule)
		}
		cfg["ingress"] = filtered

		out, err := yaml.Marshal(cfg)
		if err != nil {
			return "", fmt.Errorf("marshal cloudflared config: %w", err)
		}
		return string(out), nil
	})
	if err != nil {
		if k8s.IsNotFound(err) {
			res.CloudflaredRule = false
			return "skipped (cloudflared not deployed)", nil
		}
		return "", err
	}
	res.CloudflaredRule = false
	return fmt.Sprintf("removed ingress rule for %s", res.ExternalHost), nil
}

func (t *TeardownPipeline) restartCloudflared(ctx context.Context, res *model.ForgeResources) (string, error) {
	if res.ExternalHost == "" {
		return "skipped (no external host)", nil
	}
	if err := t.Kube.RestartDeployment(ctx, "cloudflared", "cloudflared"); err != nil {
		if k8s.IsNotFound(err) {
			return "skipped (cloudflared not deployed)", nil
		}
		return "", err
	}
	return "restarted cloudflared deployment", nil
}

func (t *TeardownPipeline) deleteService(ctx context.Context, res *model.ForgeResources) (string, error) {
	if res.ServiceName == "" {
		return "skipped (no service to delete)", nil
	}
	ns := res.ServiceNS
	if ns == "" {
		ns = "default"
	}
	if err := t.Kube.DeleteService(ctx, ns, res.ServiceName); err != nil {
		return "", err
	}
	name := res.ServiceName
	res.ServiceName = ""
	res.ServiceNS = ""
	res.InternalHost = ""
	return fmt.Sprintf("deleted service %s", name), nil
}

func (t *TeardownPipeline) deleteDeployment(ctx context.Context, res *model.ForgeResources) (string, error) {
	if res.DeploymentName == "" {
		return "skipped (no deployment to delete)", nil
	}
	ns := res.DeploymentNS
	if ns == "" {
		ns = "default"
	}
	if err := t.Kube.DeleteDeployment(ctx, ns, res.DeploymentName); err != nil {
		return "", err
	}
	name := res.DeploymentName
	res.DeploymentName = ""
	res.DeploymentNS = ""
	res.ExternalHost = ""
	return fmt.Sprintf("deleted deployment %s", name), nil
}
