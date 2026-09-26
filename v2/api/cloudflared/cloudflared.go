package cloudflared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigDigest binds a host mutation to the exact config observed at admission.
func ConfigDigest(cfg *Config) (string, error) {
	data, err := marshalConfig(cfg)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func ConfigPath() string { return getConfigPath() }

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
	rawDocument     []byte
}

type IngressRule struct {
	Hostname string `yaml:"hostname,omitempty"`
	Service  string `yaml:"service"`
}

// marshalConfig keeps fields and comments outside the ingress edit when the
// config came from disk. A newly constructed Config still uses the normal
// typed representation.
func marshalConfig(cfg *Config) ([]byte, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cloudflared config is nil")
	}
	if len(cfg.rawDocument) == 0 {
		return yaml.Marshal(cfg)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(cfg.rawDocument, &document); err != nil {
		return nil, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("cloudflared config root is not a mapping")
	}
	root := document.Content[0]
	var ingress *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "ingress" {
			ingress = root.Content[i+1]
			break
		}
	}
	if ingress == nil {
		ingress = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "ingress"}, ingress)
	}
	if ingress.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("cloudflared ingress is not a sequence")
	}
	original := ingress.Content
	used := make([]bool, len(original))
	updated := make([]*yaml.Node, 0, len(cfg.Ingress))
	for _, desired := range cfg.Ingress {
		var rule *yaml.Node
		for i, candidate := range original {
			if used[i] || candidate.Kind != yaml.MappingNode {
				continue
			}
			var existing IngressRule
			if err := candidate.Decode(&existing); err != nil {
				return nil, err
			}
			if existing.Hostname == desired.Hostname {
				used[i], rule = true, candidate
				break
			}
		}
		if rule == nil {
			rule = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
		if desired.Hostname != "" {
			setCloudflaredScalar(rule, "hostname", desired.Hostname)
		}
		setCloudflaredScalar(rule, "service", desired.Service)
		updated = append(updated, rule)
	}
	ingress.Content = updated
	return yaml.Marshal(&document)
}

func setCloudflaredScalar(mapping *yaml.Node, key, value string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1].Value = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
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
func ReadConfig(ctx context.Context) (*Config, error) {
	cfg, _, err := ReadConfigSnapshot(ctx)
	return cfg, err
}

// ReadConfigSnapshot hashes the bytes that will be replaced, including fields
// and comments unknown to this client, so a worker cannot miss an intervening
// edit to the local file.
func ReadConfigSnapshot(_ context.Context) (*Config, string, error) {
	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		return nil, "", fmt.Errorf("read cloudflared config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, "", fmt.Errorf("parse cloudflared config: %w", err)
	}
	cfg.rawDocument = append([]byte(nil), data...)
	sum := sha256.Sum256(data)
	return &cfg, hex.EncodeToString(sum[:]), nil
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

// ApplyConfig writes the config to the local cloudflared config file.
func ApplyConfig(_ context.Context, cfg *Config) error {
	data, err := marshalConfig(cfg)
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish cloudflared config: %w", err)
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
