package startup

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	ControlBackendEnv           = "NORN_CONTROL_BACKEND"
	EtcdEndpointsEnv            = "NORN_ETCD_ENDPOINTS"
	EtcdPrefixEnv               = "NORN_ETCD_PREFIX"
	EtcdSourceValidationModeEnv = "NORN_ETCD_SOURCE_VALIDATION"
	ControlBackendProbeArgument = "--norn-control-backend-probe"
	BackendPostgres             = "postgres"
	BackendEtcd                 = "etcd"
)

type ControlBackendConfig struct {
	Backend       string   `json:"backend"`
	EtcdEndpoints []string `json:"etcdEndpoints,omitempty"`
	EtcdPrefix    string   `json:"etcdPrefix,omitempty"`
	// SourceValidation is an intentionally narrow API runtime. It is not a
	// general etcd control-plane enablement and is consumed only by norn-api.
	SourceValidation bool `json:"sourceValidation,omitempty"`
}

func ParseControlBackend(getenv func(string) string) (ControlBackendConfig, error) {
	backend := strings.ToLower(strings.TrimSpace(getenv(ControlBackendEnv)))
	if backend == "" {
		backend = BackendPostgres
	}
	if backend != BackendPostgres && backend != BackendEtcd {
		return ControlBackendConfig{}, fmt.Errorf("%s must be postgres or etcd (got %q)", ControlBackendEnv, backend)
	}
	prefix := strings.TrimSpace(getenv(EtcdPrefixEnv))
	if prefix == "" {
		prefix = "/norn"
	}
	var endpoints []string
	for _, endpoint := range strings.Split(getenv(EtcdEndpointsEnv), ",") {
		if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
			endpoints = append(endpoints, endpoint)
		}
	}
	if backend == BackendEtcd && len(endpoints) == 0 {
		return ControlBackendConfig{}, fmt.Errorf("%s=etcd requires %s", ControlBackendEnv, EtcdEndpointsEnv)
	}
	return ControlBackendConfig{Backend: backend, EtcdEndpoints: endpoints, EtcdPrefix: prefix, SourceValidation: strings.EqualFold(strings.TrimSpace(getenv(EtcdSourceValidationModeEnv)), "true")}, nil
}

// RequireRuntimeCapabilities rejects etcd before either executable opens PostgreSQL.
func RequireRuntimeCapabilities(cfg ControlBackendConfig) error {
	if cfg.Backend == BackendEtcd {
		return fmt.Errorf("etcd control backend is not available: operation recovery, lease-backed app locks, and API aggregate consumers are not yet backend-neutral")
	}
	return nil
}

// WriteControlBackendProbe proves backend-first selection without opening PostgreSQL or dialing etcd.
func WriteControlBackendProbe(args []string, getenv func(string) string, w io.Writer) (bool, error) {
	if len(args) != 1 || args[0] != ControlBackendProbeArgument {
		return false, nil
	}
	cfg, err := ParseControlBackend(getenv)
	if err != nil {
		return true, err
	}
	if err = RequireRuntimeCapabilities(cfg); err != nil {
		return true, err
	}
	if err = json.NewEncoder(w).Encode(cfg); err != nil {
		return true, fmt.Errorf("write control backend probe: %w", err)
	}
	return true, nil
}
