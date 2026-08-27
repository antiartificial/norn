package cloudflared

import (
	"context"
	"fmt"
	"net"
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

// IsPublicEndpoint reports whether an endpoint is suitable for a Cloudflare
// tunnel ingress rule. Private discovery names, tailnet names, localhost, and
// literal IP addresses must be handled by their native routing layers instead.
func IsPublicEndpoint(raw string) bool {
	hostname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(NormalizeHostname(raw))), ".")
	if hostname == "" || hostname == "localhost" || net.ParseIP(hostname) != nil || !strings.Contains(hostname, ".") {
		return false
	}
	for _, suffix := range []string{".internal", ".local", ".localhost", ".norn", ".ts.net"} {
		if strings.HasSuffix(hostname, suffix) {
			return false
		}
	}
	return true
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

var (
	configPath  string
	binaryPath  string
	launchLabel = "com.norn.cloudflared"
)

// SetConfigPath sets the path to the local cloudflared config file.
func SetConfigPath(path string) {
	configPath = path
}

// SetBinaryPath sets the cloudflared executable used for config validation.
func SetBinaryPath(path string) {
	binaryPath = path
}

// SetLaunchLabel sets the launchd service label restarted after config changes.
func SetLaunchLabel(label string) {
	if strings.TrimSpace(label) != "" {
		launchLabel = label
	}
}

func getConfigPath() string {
	if configPath != "" {
		return configPath
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cloudflared", "config.yml")
}

func getBinaryPath() string {
	if binaryPath != "" {
		return binaryPath
	}
	for _, candidate := range []string{"/opt/homebrew/bin/cloudflared", "/usr/local/bin/cloudflared"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "cloudflared"
}

func launchTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel)
}

func validateConfig(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, getBinaryPath(), "--config", path, "tunnel", "ingress", "validate")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("validate cloudflared config: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
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

// PrunePrivateIngress removes hostname rules that do not belong in a public
// Cloudflare tunnel while preserving the catch-all rule.
func PrunePrivateIngress(cfg *Config) bool {
	filtered := make([]IngressRule, 0, len(cfg.Ingress))
	changed := false
	for _, rule := range cfg.Ingress {
		if rule.Hostname != "" && !IsPublicEndpoint(rule.Hostname) {
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

// ApplyConfig validates a candidate config before atomically replacing the
// live cloudflared config. A validation failure leaves the previous file intact.
func ApplyConfig(ctx context.Context, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	path := getConfigPath()
	temporary, err := os.CreateTemp(filepath.Dir(path), ".cloudflared-config-*")
	if err != nil {
		return fmt.Errorf("reserve cloudflared config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure cloudflared config: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write cloudflared config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync cloudflared config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close cloudflared config: %w", err)
	}
	if err := validateConfig(ctx, temporaryPath); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish cloudflared config: %w", err)
	}
	if directory, err := os.Open(filepath.Dir(path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

// Restart restarts the cloudflared tunnel via launchctl kickstart -k,
// which kills the running process and immediately relaunches it with
// the updated config. This avoids the KeepAlive/SuccessfulExit issue
// where a clean SIGTERM exit (code 0) would not trigger auto-restart.
func Restart(ctx context.Context) error {
	if err := validateConfig(ctx, getConfigPath()); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "launchctl", "kickstart", "-k", launchTarget())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl kickstart cloudflared: %s: %w", string(out), err)
	}
	return nil
}
