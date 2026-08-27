package cloudflared

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// HTTPServiceURL formats an origin address for a cloudflared ingress rule.
// net.JoinHostPort brackets IPv6 literals while preserving IPv4 and hostnames.
func HTTPServiceURL(address string, port int) string {
	return "http://" + net.JoinHostPort(address, strconv.Itoa(port))
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

// ApplyConfig validates a candidate config before atomically replacing the
// live cloudflared config. A validation failure leaves the previous file intact.
func ApplyConfig(ctx context.Context, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	destination := getConfigPath()
	temp, err := os.CreateTemp(filepath.Dir(destination), ".norn-cloudflared-*.yml")
	if err != nil {
		return fmt.Errorf("create cloudflared config candidate: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return fmt.Errorf("set cloudflared config candidate permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write cloudflared config candidate: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync cloudflared config candidate: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close cloudflared config candidate: %w", err)
	}
	if err := validateConfig(ctx, tempPath); err != nil {
		return err
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return fmt.Errorf("replace cloudflared config: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(destination)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
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
