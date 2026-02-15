package cloudflared

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Tunnel          string        `yaml:"tunnel"`
	CredentialsFile string        `yaml:"credentials-file"`
	Ingress         []IngressRule `yaml:"ingress"`
}

type IngressRule struct {
	Hostname string `yaml:"hostname,omitempty"`
	Service  string `yaml:"service"`
}

const (
	configMapName = "cloudflared"
	namespace     = "cloudflared"
	deployment    = "cloudflared"
)

// ReadConfig reads the cloudflared config from the Kubernetes ConfigMap.
func ReadConfig(ctx context.Context) (*Config, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "configmap", configMapName,
		"-n", namespace, "-o", "jsonpath={.data.config\\.yaml}")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl get configmap: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		return nil, fmt.Errorf("parse cloudflared config: %w", err)
	}
	return &cfg, nil
}

// AddIngress adds or updates an ingress rule for the given hostname.
// Returns true if the config was changed.
func AddIngress(cfg *Config, hostname, service string) bool {
	// Check if rule already exists with same service
	for i, rule := range cfg.Ingress {
		if rule.Hostname == hostname {
			if rule.Service == service {
				return false
			}
			cfg.Ingress[i].Service = service
			return true
		}
	}

	// Insert before the catch-all rule (last entry has no hostname)
	rule := IngressRule{Hostname: hostname, Service: service}
	if len(cfg.Ingress) > 0 && cfg.Ingress[len(cfg.Ingress)-1].Hostname == "" {
		cfg.Ingress = append(cfg.Ingress[:len(cfg.Ingress)-1], rule, cfg.Ingress[len(cfg.Ingress)-1])
	} else {
		cfg.Ingress = append(cfg.Ingress, rule)
	}
	return true
}

// RemoveIngress removes ingress rules matching the given hostname.
// Returns true if the config was changed.
func RemoveIngress(cfg *Config, hostname string) bool {
	var filtered []IngressRule
	changed := false
	for _, rule := range cfg.Ingress {
		if rule.Hostname == hostname {
			changed = true
			continue
		}
		filtered = append(filtered, rule)
	}
	if changed {
		cfg.Ingress = filtered
	}
	return changed
}

// ApplyConfig writes the config back to the Kubernetes ConfigMap.
func ApplyConfig(ctx context.Context, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	manifest := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
data:
  config.yaml: |
%s`, configMapName, namespace, indent(string(data), "    "))

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kubectl apply: %s: %w", string(out), err)
	}
	return nil
}

// Restart triggers a rolling restart of the cloudflared deployment.
func Restart(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "kubectl", "rollout", "restart",
		"deployment/"+deployment, "-n", namespace)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kubectl rollout restart: %s: %w", string(out), err)
	}
	return nil
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}
