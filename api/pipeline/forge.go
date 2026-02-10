package pipeline

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"gopkg.in/yaml.v3"

	"norn/api/hub"
	"norn/api/k8s"
	"norn/api/model"
)

type ForgePipeline struct {
	Kube       *k8s.Client
	WS         *hub.Hub
	TunnelName string
}

type forgeStep struct {
	name string
	fn   func(ctx context.Context, spec *model.InfraSpec) (string, error)
}

func (f *ForgePipeline) Run(spec *model.InfraSpec) {
	ctx := context.Background()
	steps := []forgeStep{
		{name: "create-deployment", fn: f.createDeployment},
		{name: "create-service", fn: f.createService},
		{name: "patch-cloudflared", fn: f.patchCloudflared},
		{name: "create-dns-route", fn: f.createDNSRoute},
		{name: "restart-cloudflared", fn: f.restartCloudflared},
	}

	for _, s := range steps {
		f.WS.Broadcast(hub.Event{Type: "forge.step", AppID: spec.App, Payload: map[string]string{
			"step":   s.name,
			"status": "running",
		}})

		start := time.Now()
		output, err := s.fn(ctx, spec)
		elapsed := time.Since(start).Milliseconds()

		if err != nil {
			f.WS.Broadcast(hub.Event{Type: "forge.failed", AppID: spec.App, Payload: map[string]string{
				"step":  s.name,
				"error": fmt.Sprintf("%s: %v", s.name, err),
			}})
			return
		}

		f.WS.Broadcast(hub.Event{Type: "forge.step", AppID: spec.App, Payload: map[string]string{
			"step":       s.name,
			"status":     "completed",
			"output":     output,
			"durationMs": fmt.Sprintf("%d", elapsed),
		}})
	}

	f.WS.Broadcast(hub.Event{Type: "forge.completed", AppID: spec.App, Payload: map[string]string{}})
}

func (f *ForgePipeline) createDeployment(ctx context.Context, spec *model.InfraSpec) (string, error) {
	opts := k8s.DeploymentOpts{
		Name:        spec.App,
		Image:       spec.App + ":latest",
		Port:        spec.Port,
		Healthcheck: spec.Healthcheck,
	}

	if spec.Services != nil && spec.Services.Postgres != nil {
		opts.Env = append(opts.Env, corev1.EnvVar{
			Name:  "DATABASE_URL",
			Value: fmt.Sprintf("postgres://norn:norn@postgres:5432/%s?sslmode=disable", spec.Services.Postgres.Database),
		})
	}

	if err := f.Kube.CreateDeployment(ctx, "default", opts); err != nil {
		return "", err
	}
	return fmt.Sprintf("created deployment %s with image %s", spec.App, opts.Image), nil
}

func (f *ForgePipeline) createService(ctx context.Context, spec *model.InfraSpec) (string, error) {
	if spec.Hosts == nil || spec.Hosts.Internal == "" {
		return "skipped (no internal host)", nil
	}
	if err := f.Kube.CreateService(ctx, "default", spec.App, spec.Hosts.Internal, spec.Port); err != nil {
		return "", err
	}
	return fmt.Sprintf("created service %s → port %d", spec.Hosts.Internal, spec.Port), nil
}

func (f *ForgePipeline) patchCloudflared(ctx context.Context, spec *model.InfraSpec) (string, error) {
	if spec.Hosts == nil || spec.Hosts.External == "" {
		return "skipped (no external host)", nil
	}
	if spec.Hosts.Internal == "" {
		return "skipped (no internal host for service URL)", nil
	}

	serviceURL := fmt.Sprintf("http://%s.default.svc.cluster.local:80", spec.Hosts.Internal)

	err := f.Kube.PatchConfigMap(ctx, "cloudflared", "cloudflared", "config.yaml", func(data string) (string, error) {
		var cfg map[string]interface{}
		if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
			return "", fmt.Errorf("parse cloudflared config: %w", err)
		}

		tunnelKey := "tunnel"
		ingressKey := "ingress"

		ingress, ok := cfg[ingressKey].([]interface{})
		if !ok {
			return "", fmt.Errorf("cloudflared config missing ingress rules")
		}

		newRule := map[string]interface{}{
			"hostname": spec.Hosts.External,
			"service":  serviceURL,
		}

		// Insert before the last rule (catch-all 404)
		if len(ingress) > 0 {
			ingress = append(ingress[:len(ingress)-1], newRule, ingress[len(ingress)-1])
		} else {
			ingress = append(ingress, newRule)
		}
		cfg[ingressKey] = ingress
		_ = tunnelKey

		out, err := yaml.Marshal(cfg)
		if err != nil {
			return "", fmt.Errorf("marshal cloudflared config: %w", err)
		}
		return string(out), nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("added ingress rule: %s → %s", spec.Hosts.External, serviceURL), nil
}

func (f *ForgePipeline) createDNSRoute(ctx context.Context, spec *model.InfraSpec) (string, error) {
	if spec.Hosts == nil || spec.Hosts.External == "" {
		return "skipped (no external host)", nil
	}

	cmd := exec.CommandContext(ctx, "cloudflared", "tunnel", "route", "dns", f.TunnelName, spec.Hosts.External)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		return output, fmt.Errorf("cloudflared tunnel route: %s", output)
	}
	return output, nil
}

func (f *ForgePipeline) restartCloudflared(ctx context.Context, spec *model.InfraSpec) (string, error) {
	if spec.Hosts == nil || spec.Hosts.External == "" {
		return "skipped (no external host)", nil
	}
	if err := f.Kube.RestartDeployment(ctx, "cloudflared", "cloudflared"); err != nil {
		return "", err
	}
	return "restarted cloudflared deployment", nil
}
