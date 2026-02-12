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
	"norn/api/storage"
	"norn/api/store"
)

type ForgePipeline struct {
	DB         *store.DB
	Kube       *k8s.Client
	WS         *hub.Hub
	TunnelName string
	PGHost     string
	PGUser     string
	S3         *storage.Client
}

type forgeStep struct {
	name string
	fn   func(ctx context.Context, spec *model.InfraSpec, res *model.ForgeResources) (string, error)
}

func (f *ForgePipeline) Run(state *model.ForgeState, spec *model.InfraSpec, resumeFrom int) {
	ctx := context.Background()

	// Cron and function apps skip K8s infrastructure — just mark as forged
	if spec.IsCron() || spec.IsFunction() {
		stepName := "cron-register"
		stepOutput := fmt.Sprintf("cron app registered (schedule: %s)", spec.Schedule)
		if spec.IsFunction() {
			stepName = "function-register"
			timeout := 30
			if spec.Function != nil && spec.Function.Timeout > 0 {
				timeout = spec.Function.Timeout
			}
			stepOutput = fmt.Sprintf("function app registered (timeout: %ds)", timeout)
		}
		state.Steps = append(state.Steps, model.ForgeStepLog{
			Step:   stepName,
			Status: "completed",
			Output: stepOutput,
		})
		state.Status = model.ForgeForged
		state.Error = ""
		f.DB.UpdateForgeState(ctx, state.App, state.Status, state.Steps, state.Resources, "")
		f.WS.Broadcast(hub.Event{Type: "forge.step", AppID: spec.App, Payload: map[string]string{
			"step":   stepName,
			"status": "completed",
		}})
		f.WS.Broadcast(hub.Event{Type: "forge.completed", AppID: spec.App, Payload: map[string]string{}})
		return
	}

	steps := []forgeStep{
		{name: "create-bucket", fn: f.createBucket},
		{name: "create-deployment", fn: f.createDeployment},
		{name: "create-service", fn: f.createService},
		{name: "patch-cloudflared", fn: f.patchCloudflared},
		{name: "create-dns-route", fn: f.createDNSRoute},
		{name: "restart-cloudflared", fn: f.restartCloudflared},
	}

	for i, s := range steps {
		if i < resumeFrom {
			// Already completed in a prior run — mark skipped in this run's step log
			state.Steps = append(state.Steps, model.ForgeStepLog{
				Step:   s.name,
				Status: "skipped",
			})
			f.WS.Broadcast(hub.Event{Type: "forge.step", AppID: spec.App, Payload: map[string]string{
				"step":   s.name,
				"status": "skipped",
			}})
			continue
		}

		f.WS.Broadcast(hub.Event{Type: "forge.step", AppID: spec.App, Payload: map[string]string{
			"step":   s.name,
			"status": "running",
		}})

		start := time.Now()
		output, err := s.fn(ctx, spec, &state.Resources)
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
			f.DB.UpdateForgeState(ctx, state.App, state.Status, state.Steps, state.Resources, state.Error)

			f.WS.Broadcast(hub.Event{Type: "forge.failed", AppID: spec.App, Payload: map[string]string{
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
		// Persist after each step for crash safety
		f.DB.UpdateForgeState(ctx, state.App, model.ForgeForging, state.Steps, state.Resources, "")

		f.WS.Broadcast(hub.Event{Type: "forge.step", AppID: spec.App, Payload: map[string]string{
			"step":       s.name,
			"status":     "completed",
			"output":     output,
			"durationMs": fmt.Sprintf("%d", elapsed),
		}})
	}

	state.Status = model.ForgeForged
	state.Error = ""
	f.DB.UpdateForgeState(ctx, state.App, state.Status, state.Steps, state.Resources, "")

	f.WS.Broadcast(hub.Event{Type: "forge.completed", AppID: spec.App, Payload: map[string]string{}})
}

func (f *ForgePipeline) createDeployment(ctx context.Context, spec *model.InfraSpec, res *model.ForgeResources) (string, error) {
	opts := k8s.DeploymentOpts{
		Name:        spec.App,
		Image:       spec.App + ":latest",
		Port:        spec.Port,
		Replicas:    spec.Replicas,
		Healthcheck: spec.Healthcheck,
	}

	if len(spec.Secrets) > 0 {
		opts.SecretName = spec.App + "-secrets"
	}

	for k, v := range spec.Env {
		opts.Env = append(opts.Env, corev1.EnvVar{Name: k, Value: v})
	}

	if spec.Services != nil && spec.Services.Postgres != nil {
		pgHost := f.PGHost
		if pgHost == "" {
			pgHost = "localhost"
		}
		pgUser := f.PGUser
		userPart := ""
		if pgUser != "" {
			userPart = pgUser + "@"
		}
		opts.Env = append(opts.Env, corev1.EnvVar{
			Name:  "DATABASE_URL",
			Value: fmt.Sprintf("postgres://%s%s:5432/%s?sslmode=disable", userPart, pgHost, spec.Services.Postgres.Database),
		})
	}

	if spec.Services != nil && spec.Services.Storage != nil && f.S3 != nil {
		opts.Env = append(opts.Env,
			corev1.EnvVar{Name: "S3_ENDPOINT", Value: f.S3.Endpoint()},
			corev1.EnvVar{Name: "S3_BUCKET", Value: spec.Services.Storage.Bucket},
			corev1.EnvVar{Name: "AWS_ACCESS_KEY_ID", Value: "norn"},
			corev1.EnvVar{Name: "AWS_SECRET_ACCESS_KEY", Value: "nornnorn"},
		)
	}

	// Create volume mounts (PVC or hostPath)
	for _, vol := range spec.Volumes {
		if vol.HostPath != "" {
			opts.Volumes = append(opts.Volumes, k8s.VolumeMount{
				Name:      vol.Name,
				MountPath: vol.MountPath,
				HostPath:  vol.HostPath,
			})
		} else {
			pvcName := fmt.Sprintf("%s-%s", spec.App, vol.Name)
			labels := map[string]string{"managed-by": "norn", "app": spec.App}
			err := f.Kube.CreatePVC(ctx, "default", pvcName, vol.Size, labels)
			if err != nil && !k8s.IsAlreadyExists(err) {
				return "", fmt.Errorf("create PVC %s: %w", pvcName, err)
			}
			opts.Volumes = append(opts.Volumes, k8s.VolumeMount{
				Name:      vol.Name,
				MountPath: vol.MountPath,
				PVCName:   pvcName,
			})
		}
	}

	err := f.Kube.CreateDeployment(ctx, "default", opts)
	if err != nil {
		if k8s.IsAlreadyExists(err) {
			res.DeploymentName = spec.App
			res.DeploymentNS = "default"
			return fmt.Sprintf("deployment %s already exists, skipping", spec.App), nil
		}
		return "", err
	}
	res.DeploymentName = spec.App
	res.DeploymentNS = "default"
	return fmt.Sprintf("created deployment %s with image %s", spec.App, opts.Image), nil
}

func (f *ForgePipeline) createService(ctx context.Context, spec *model.InfraSpec, res *model.ForgeResources) (string, error) {
	if spec.Hosts == nil || spec.Hosts.Internal == "" {
		return "skipped (no internal host)", nil
	}
	err := f.Kube.CreateService(ctx, "default", spec.App, spec.Hosts.Internal, spec.Port)
	if err != nil {
		if k8s.IsAlreadyExists(err) {
			res.ServiceName = spec.Hosts.Internal
			res.ServiceNS = "default"
			res.InternalHost = spec.Hosts.Internal
			return fmt.Sprintf("service %s already exists, skipping", spec.Hosts.Internal), nil
		}
		return "", err
	}
	res.ServiceName = spec.Hosts.Internal
	res.ServiceNS = "default"
	res.InternalHost = spec.Hosts.Internal
	return fmt.Sprintf("created service %s → port %d", spec.Hosts.Internal, spec.Port), nil
}

func (f *ForgePipeline) patchCloudflared(ctx context.Context, spec *model.InfraSpec, res *model.ForgeResources) (string, error) {
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

		ingressKey := "ingress"

		ingress, ok := cfg[ingressKey].([]interface{})
		if !ok {
			return "", fmt.Errorf("cloudflared config missing ingress rules")
		}

		// Check if hostname already exists in ingress (idempotent)
		for _, rule := range ingress {
			if ruleMap, ok := rule.(map[string]interface{}); ok {
				if hostname, _ := ruleMap["hostname"].(string); hostname == spec.Hosts.External {
					res.ExternalHost = spec.Hosts.External
					res.CloudflaredRule = true
					return data, nil // no change needed
				}
			}
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

		out, err := yaml.Marshal(cfg)
		if err != nil {
			return "", fmt.Errorf("marshal cloudflared config: %w", err)
		}
		return string(out), nil
	})
	if err != nil {
		if k8s.IsNotFound(err) {
			return "skipped (cloudflared not deployed)", nil
		}
		return "", err
	}
	res.ExternalHost = spec.Hosts.External
	res.CloudflaredRule = true
	return fmt.Sprintf("added ingress rule: %s → %s", spec.Hosts.External, serviceURL), nil
}

func (f *ForgePipeline) createDNSRoute(ctx context.Context, spec *model.InfraSpec, res *model.ForgeResources) (string, error) {
	if spec.Hosts == nil || spec.Hosts.External == "" {
		return "skipped (no external host)", nil
	}

	if _, err := exec.LookPath("cloudflared"); err != nil {
		return "skipped (cloudflared CLI not found)", nil
	}

	cmd := exec.CommandContext(ctx, "cloudflared", "tunnel", "route", "dns", f.TunnelName, spec.Hosts.External)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		if strings.Contains(output, "already exists") {
			res.DNSRoute = true
			return fmt.Sprintf("DNS record for %s already exists", spec.Hosts.External), nil
		}
		return output, fmt.Errorf("cloudflared tunnel route: %s", output)
	}
	res.DNSRoute = true
	return output, nil
}

func (f *ForgePipeline) restartCloudflared(ctx context.Context, spec *model.InfraSpec, res *model.ForgeResources) (string, error) {
	if spec.Hosts == nil || spec.Hosts.External == "" {
		return "skipped (no external host)", nil
	}
	if err := f.Kube.RestartDeployment(ctx, "cloudflared", "cloudflared"); err != nil {
		if k8s.IsNotFound(err) {
			return "skipped (cloudflared not deployed)", nil
		}
		return "", err
	}
	return "restarted cloudflared deployment", nil
}

func (f *ForgePipeline) createBucket(ctx context.Context, spec *model.InfraSpec, res *model.ForgeResources) (string, error) {
	if spec.Services == nil || spec.Services.Storage == nil {
		return "skipped (no storage configured)", nil
	}
	if f.S3 == nil {
		return "skipped (S3 client not configured)", nil
	}
	if err := f.S3.CreateBucket(ctx, spec.Services.Storage.Bucket); err != nil {
		return "", err
	}
	return fmt.Sprintf("bucket %s ready", spec.Services.Storage.Bucket), nil
}
