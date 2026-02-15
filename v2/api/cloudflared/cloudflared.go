package cloudflared

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// NormalizeHostname strips the scheme and path from a URL, returning just the
// hostname. Bare hostnames are returned as-is. This ensures cloudflared ingress
// rules always contain plain hostnames (e.g. "sideband.slopistry.com") rather
// than full URLs (e.g. "https://sideband.slopistry.com").
func NormalizeHostname(raw string) string {
	if !strings.Contains(raw, "://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Hostname()
}

type Config struct {
	Tunnel          string        `yaml:"tunnel"`
	CredentialsFile string        `yaml:"credentials-file"`
	Ingress         []IngressRule `yaml:"ingress"`
}

type IngressRule struct {
	Hostname string `yaml:"hostname,omitempty"`
	Service  string `yaml:"service"`
}

var configPath string

// SetConfigPath sets the path to the local cloudflared config file.
func SetConfigPath(path string) {
	configPath = path
}

func getConfigPath() string {
	if configPath != "" {
		return configPath
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cloudflared", "config.yml")
}

// ReadConfig reads the cloudflared config from the local config file.
func ReadConfig(_ context.Context) (*Config, error) {
	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		return nil, fmt.Errorf("read cloudflared config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse cloudflared config: %w", err)
	}
	return &cfg, nil
}

// AddIngress adds or updates an ingress rule for the given hostname.
// The hostname is normalized (scheme/path stripped) before storing.
// Returns true if the config was changed.
func AddIngress(cfg *Config, hostname, service string) bool {
	hostname = NormalizeHostname(hostname)

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
// The hostname is normalized (scheme/path stripped) before matching.
// Returns true if the config was changed.
func RemoveIngress(cfg *Config, hostname string) bool {
	hostname = NormalizeHostname(hostname)

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

// ApplyConfig writes the config to the local cloudflared config file.
func ApplyConfig(_ context.Context, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(getConfigPath(), data, 0644); err != nil {
		return fmt.Errorf("write cloudflared config: %w", err)
	}
	return nil
}

// Restart restarts the cloudflared tunnel via launchctl kickstart -k,
// which kills the running process and immediately relaunches it with
// the updated config. This avoids the KeepAlive/SuccessfulExit issue
// where a clean SIGTERM exit (code 0) would not trigger auto-restart.
func Restart(_ context.Context) error {
	cmd := exec.Command("launchctl", "kickstart", "-k", "gui/501/homebrew.mxcl.cloudflared")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl kickstart cloudflared: %s: %w", string(out), err)
	}
	return nil
}
