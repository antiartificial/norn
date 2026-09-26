package cloudflared

import (
	"fmt"
	"strings"
)

// Mutation is the public, non-secret portion of an accepted local ingress edit.
// Host and digests are bound before acceptance and checked again by the worker.
type Mutation struct {
	Action       string   `json:"action"`
	App          string   `json:"app"`
	Host         string   `json:"host"`
	ConfigPath   string   `json:"configPath"`
	Hostnames    []string `json:"hostnames"`
	Service      string   `json:"service,omitempty"`
	BeforeDigest string   `json:"beforeDigest"`
	AfterDigest  string   `json:"afterDigest"`
}

func (m Mutation) Apply(cfg *Config) (bool, error) {
	if cfg == nil || m.App == "" || m.Host == "" || m.ConfigPath == "" || len(m.Hostnames) == 0 {
		return false, fmt.Errorf("incomplete cloudflared mutation")
	}
	changed := false
	switch m.Action {
	case "forge":
		if strings.TrimSpace(m.Service) == "" {
			return false, fmt.Errorf("forge service is empty")
		}
		changed = PrunePrivateIngress(cfg)
		for _, hostname := range m.Hostnames {
			if !IsPublicEndpoint(hostname) {
				return false, fmt.Errorf("forge hostname is not public")
			}
			changed = AddIngress(cfg, hostname, m.Service) || changed
		}
	case "teardown", "disable":
		for _, hostname := range m.Hostnames {
			changed = RemoveIngress(cfg, hostname) || changed
		}
	case "enable":
		if strings.TrimSpace(m.Service) == "" {
			return false, fmt.Errorf("enable service is empty")
		}
		for _, hostname := range m.Hostnames {
			if !IsPublicEndpoint(hostname) {
				return false, fmt.Errorf("enable hostname is not public")
			}
			changed = AddIngress(cfg, hostname, m.Service) || changed
		}
	default:
		return false, fmt.Errorf("unsupported cloudflared mutation %q", m.Action)
	}
	return changed, nil
}
