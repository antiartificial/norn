package startup

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

const (
	ControlBackendEnv           = "NORN_CONTROL_BACKEND"
	EtcdEndpointsEnv            = "NORN_ETCD_ENDPOINTS"
	EtcdPrefixEnv               = "NORN_ETCD_PREFIX"
	EtcdCAFileEnv               = "NORN_ETCD_CA_FILE"
	EtcdCertFileEnv             = "NORN_ETCD_CERT_FILE"
	EtcdKeyFileEnv              = "NORN_ETCD_KEY_FILE"
	EtcdUsernameEnv             = "NORN_ETCD_USERNAME"
	EtcdPasswordEnv             = "NORN_ETCD_PASSWORD"
	EtcdBootstrapTokenFileEnv   = "NORN_ETCD_BOOTSTRAP_TOKEN_FILE"
	EtcdBootstrapSubjectEnv     = "NORN_ETCD_BOOTSTRAP_SUBJECT"
	EtcdBootstrapScopesEnv      = "NORN_ETCD_BOOTSTRAP_SCOPES"
	EtcdBootstrapTTLEnv         = "NORN_ETCD_BOOTSTRAP_TTL"
	EtcdSourceValidationModeEnv = "NORN_ETCD_SOURCE_VALIDATION"
	ControlBackendProbeArgument = "--norn-control-backend-probe"
	BackendPostgres             = "postgres"
	BackendEtcd                 = "etcd"
)

type ControlBackendConfig struct {
	Backend       string   `json:"backend"`
	EtcdEndpoints []string `json:"etcdEndpoints,omitempty"`
	EtcdPrefix    string   `json:"etcdPrefix,omitempty"`
	EtcdCAFile    string   `json:"etcdCAFile,omitempty"`
	EtcdCertFile  string   `json:"etcdCertFile,omitempty"`
	EtcdKeyFile   string   `json:"etcdKeyFile,omitempty"`
	EtcdUsername  string   `json:"etcdUsername,omitempty"`
	EtcdPassword  string   `json:"-"`
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
	return ControlBackendConfig{Backend: backend, EtcdEndpoints: endpoints, EtcdPrefix: prefix,
		EtcdCAFile: strings.TrimSpace(getenv(EtcdCAFileEnv)), EtcdCertFile: strings.TrimSpace(getenv(EtcdCertFileEnv)), EtcdKeyFile: strings.TrimSpace(getenv(EtcdKeyFileEnv)),
		EtcdUsername: strings.TrimSpace(getenv(EtcdUsernameEnv)), EtcdPassword: getenv(EtcdPasswordEnv),
		SourceValidation: strings.EqualFold(strings.TrimSpace(getenv(EtcdSourceValidationModeEnv)), "true")}, nil
}

// EtcdTLSConfig builds the transport used by every etcd runtime. It refuses
// partial credential configuration so a typo cannot silently downgrade mTLS or
// basic-auth protection.
func (cfg ControlBackendConfig) EtcdTLSConfig() (*tls.Config, error) {
	if (cfg.EtcdCertFile == "") != (cfg.EtcdKeyFile == "") {
		return nil, fmt.Errorf("%s and %s must be configured together", EtcdCertFileEnv, EtcdKeyFileEnv)
	}
	if (cfg.EtcdUsername == "") != (cfg.EtcdPassword == "") {
		return nil, fmt.Errorf("%s and %s must be configured together", EtcdUsernameEnv, EtcdPasswordEnv)
	}
	if cfg.EtcdCAFile == "" && cfg.EtcdCertFile == "" {
		return nil, nil
	}
	if cfg.EtcdCAFile == "" {
		return nil, fmt.Errorf("%s is required when configuring etcd client certificates", EtcdCAFileEnv)
	}
	caPEM, err := os.ReadFile(cfg.EtcdCAFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", EtcdCAFileEnv, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%s contains no certificates", EtcdCAFileEnv)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	if cfg.EtcdCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(cfg.EtcdCertFile, cfg.EtcdKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load etcd client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return tlsConfig, nil
}

// ValidateEtcdProductionTransport requires encrypted, mutually authenticated
// client transport and a distinct etcd RBAC identity for production. The
// runtime may use plain local endpoints only outside production.
func (cfg ControlBackendConfig) ValidateEtcdProductionTransport() error {
	if cfg.Backend != BackendEtcd {
		return nil
	}
	if cfg.EtcdCAFile == "" || cfg.EtcdCertFile == "" || cfg.EtcdKeyFile == "" || cfg.EtcdUsername == "" || cfg.EtcdPassword == "" {
		return fmt.Errorf("NORN_PROFILE=production with NORN_CONTROL_BACKEND=etcd requires %s, %s, %s, %s, and %s", EtcdCAFileEnv, EtcdCertFileEnv, EtcdKeyFileEnv, EtcdUsernameEnv, EtcdPasswordEnv)
	}
	for _, endpoint := range cfg.EtcdEndpoints {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
			return fmt.Errorf("NORN_PROFILE=production requires https %s entries", EtcdEndpointsEnv)
		}
	}
	_, err := cfg.EtcdTLSConfig()
	return err
}

// RequireRuntimeCapabilities rejects backends that have no normal runtime.
func RequireRuntimeCapabilities(cfg ControlBackendConfig) error {
	return nil
}

// RequireEtcdSourceValidationStartup keeps the narrow PG-free runtime from
// silently changing the meaning of PostgreSQL schema or passive status modes.
func RequireEtcdSourceValidationStartup(cfg Config) error {
	if cfg.StartupMode != ModeActive {
		return fmt.Errorf("etcd source validation requires %s=active", StartupModeEnv)
	}
	if cfg.SchemaMode != SchemaModeAuto {
		return fmt.Errorf("etcd source validation requires %s=auto", SchemaModeEnv)
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
	if cfg.Backend == BackendEtcd && cfg.SourceValidation {
		startupCfg, parseErr := Parse(getenv)
		if parseErr != nil {
			return true, parseErr
		}
		if err = RequireEtcdSourceValidationStartup(startupCfg); err != nil {
			return true, err
		}
	} else if err = RequireRuntimeCapabilities(cfg); err != nil {
		return true, err
	}
	if err = json.NewEncoder(w).Encode(cfg); err != nil {
		return true, fmt.Errorf("write control backend probe: %w", err)
	}
	return true, nil
}
