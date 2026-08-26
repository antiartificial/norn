package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// #nosec G101 -- this is a compatibility expiry timestamp, not a credential.
const defaultLegacyTokenSigningUntil = "2026-08-15T00:00:00Z"

type Config struct {
	Profile     string
	Port        string
	BindAddr    string
	DatabaseURL string
	UIDir       string
	AppsDir     string
	FleetConfig string // checked-out norn-fleet Cluster document; read-only to Norn
	// FleetGitHubApp configures an installation-scoped GitHub App used only to
	// open reviewed fleet pull requests and dispatch the protected apply
	// workflow. Provider and Terraform state credentials remain in GitHub.
	FleetGitHubAppID          string
	FleetGitHubInstallationID int64
	FleetGitHubPrivateKeyFile string
	FleetGitHubRepository     string
	FleetGitHubDefaultBranch  string
	FleetGitHubConfigPath     string
	FleetGitHubPlanWorkflow   string
	FleetGitHubApplyWorkflow  string
	FleetGitHubAPIBaseURL     string
	GitToken                  string
	GitSSHKey                 string
	APIToken                  string
	// RequireExplicitAuth disables compatibility access based only on a direct
	// loopback peer or a temporary IP grant.
	RequireExplicitAuth      bool
	StrictSecrets            bool
	AuditSigningKey          string
	AuditPreviousSigningKeys []string
	AuditRetentionDays       int
	LegacyTokenSigningUntil  time.Time
	RegistryURL              string // GHCR registry (e.g. ghcr.io/username)
	ArtifactSigningPublicKey string
	ArtifactDenySeverities   []string
	CosignPath               string
	TrivyPath                string
	NetworkMode              string // local, tailnet, public
	// WorkloadConnector selects the scheduler/runtime boundary. nomad-consul
	// remains the production default; apple-container is an explicit local
	// macOS connector and is never selected by auto-detection.
	WorkloadConnector string

	NomadAddr  string // Nomad API address
	ConsulAddr string // Consul API address
	IngressURL string // Regional Traefik origin used by Cloudflared
	// ExternalIngress disables local cloudflared mutation when DNS/global edge
	// routing is owned outside this Norn process.
	ExternalIngress bool
	// These mirror the HashiCorp client environment switches so production
	// startup can reject encrypted-but-unverified control-plane transport.
	NomadTLSSkipVerify  bool
	ConsulTLSSkipVerify bool

	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Region    string
	S3UseSSL    bool
	S3Provider  string
	S3ForcePath bool

	GarageAdminEndpoint string
	GarageAdminToken    string

	RedpandaBrokers []string
	RedpandaRPKPath string

	BeaconEnvironment string
	BeaconSinkURL     string
	BeaconSinkKeyID   string
	BeaconSinkSecret  string

	AllowedOrigins     string
	CFAccessTeamDomain string
	CFAccessAUD        string

	WebhookSecret          string // NORN_WEBHOOK_SECRET
	CloudflaredConfig      string // NORN_CLOUDFLARED_CONFIG
	CloudflareAPIToken     string // NORN_CLOUDFLARE_API_TOKEN
	CloudflareZoneID       string // NORN_CLOUDFLARE_ZONE_ID
	CloudflareLogpushToken string // NORN_CLOUDFLARE_LOGPUSH_TOKEN
	CloudflareAPIBaseURL   string // NORN_CLOUDFLARE_API_BASE_URL
}

func Load() *Config {
	return &Config{
		Profile:                   strings.ToLower(envOr("NORN_PROFILE", "development")),
		Port:                      envOr("NORN_PORT", "8800"),
		BindAddr:                  envOr("NORN_BIND_ADDR", "127.0.0.1"),
		DatabaseURL:               envOr("NORN_DATABASE_URL", "postgres://norn:norn@localhost:5432/norn_v2?sslmode=disable"),
		UIDir:                     uiDir(),
		AppsDir:                   envOr("NORN_APPS_DIR", os.Getenv("HOME")+"/projects"),
		FleetConfig:               os.Getenv("NORN_FLEET_CONFIG"),
		FleetGitHubAppID:          strings.TrimSpace(os.Getenv("NORN_FLEET_GITHUB_APP_ID")),
		FleetGitHubInstallationID: envInt64Or("NORN_FLEET_GITHUB_INSTALLATION_ID", 0),
		FleetGitHubPrivateKeyFile: strings.TrimSpace(os.Getenv("NORN_FLEET_GITHUB_PRIVATE_KEY_FILE")),
		FleetGitHubRepository:     strings.TrimSpace(os.Getenv("NORN_FLEET_GITHUB_REPOSITORY")),
		FleetGitHubDefaultBranch:  envOr("NORN_FLEET_GITHUB_DEFAULT_BRANCH", "main"),
		FleetGitHubConfigPath:     strings.TrimSpace(os.Getenv("NORN_FLEET_GITHUB_CONFIG_PATH")),
		FleetGitHubPlanWorkflow:   envOr("NORN_FLEET_GITHUB_PLAN_WORKFLOW", "plan.yml"),
		FleetGitHubApplyWorkflow:  envOr("NORN_FLEET_GITHUB_APPLY_WORKFLOW", "apply.yml"),
		FleetGitHubAPIBaseURL:     envOr("NORN_FLEET_GITHUB_API_BASE_URL", "https://api.github.com"),
		GitToken:                  os.Getenv("NORN_GIT_TOKEN"),
		GitSSHKey:                 os.Getenv("NORN_GIT_SSH_KEY"),
		APIToken:                  os.Getenv("NORN_API_TOKEN"),
		RequireExplicitAuth:       os.Getenv("NORN_REQUIRE_EXPLICIT_AUTH") == "true",
		StrictSecrets:             os.Getenv("NORN_STRICT_SECRETS") == "true" || strings.EqualFold(os.Getenv("NORN_PROFILE"), "production"),
		AuditSigningKey:           os.Getenv("NORN_AUDIT_SIGNING_KEY"),
		AuditPreviousSigningKeys:  splitNonEmpty(os.Getenv("NORN_AUDIT_PREVIOUS_SIGNING_KEYS")),
		AuditRetentionDays:        envIntOr("NORN_AUDIT_RETENTION_DAYS", 365),
		LegacyTokenSigningUntil:   envTimeOr("NORN_LEGACY_TOKEN_SIGNING_UNTIL", defaultLegacyTokenSigningUntil),
		RegistryURL:               os.Getenv("NORN_REGISTRY_URL"),
		ArtifactSigningPublicKey:  os.Getenv("NORN_ARTIFACT_SIGNING_PUBLIC_KEY"),
		ArtifactDenySeverities:    splitCSV(envOr("NORN_ARTIFACT_DENY_SEVERITIES", "HIGH,CRITICAL")),
		CosignPath:                envOr("NORN_COSIGN_PATH", "cosign"),
		TrivyPath:                 envOr("NORN_TRIVY_PATH", "trivy"),
		NetworkMode:               networkMode(envOr("NORN_NETWORK_MODE", "local")),
		WorkloadConnector:         workloadConnector(envOr("NORN_WORKLOAD_CONNECTOR", "nomad-consul")),

		NomadAddr:           envOr("NORN_NOMAD_ADDR", "http://localhost:4646"),
		ConsulAddr:          envOr("NORN_CONSUL_ADDR", "http://localhost:8500"),
		IngressURL:          strings.TrimRight(os.Getenv("NORN_INGRESS_URL"), "/"),
		ExternalIngress:     envBoolOr("NORN_EXTERNAL_INGRESS", false),
		NomadTLSSkipVerify:  envBoolOr("NOMAD_SKIP_VERIFY", false),
		ConsulTLSSkipVerify: !envBoolOr("CONSUL_HTTP_SSL_VERIFY", true),

		S3Endpoint:          os.Getenv("NORN_S3_ENDPOINT"),
		S3AccessKey:         os.Getenv("NORN_S3_ACCESS_KEY"),
		S3SecretKey:         os.Getenv("NORN_S3_SECRET_KEY"),
		S3Region:            envOr("NORN_S3_REGION", "auto"),
		S3UseSSL:            os.Getenv("NORN_S3_USE_SSL") != "false",
		S3Provider:          envOr("NORN_S3_PROVIDER", "s3"),
		S3ForcePath:         os.Getenv("NORN_S3_FORCE_PATH_STYLE") == "true" || envOr("NORN_S3_PROVIDER", "s3") == "garage",
		GarageAdminEndpoint: os.Getenv("NORN_GARAGE_ADMIN_ENDPOINT"),
		GarageAdminToken:    os.Getenv("NORN_GARAGE_ADMIN_TOKEN"),

		RedpandaBrokers: splitCSV(os.Getenv("NORN_REDPANDA_BROKERS")),
		RedpandaRPKPath: envOr("NORN_RPK_PATH", "rpk"),

		BeaconEnvironment: envOr("NORN_BEACON_ENVIRONMENT", "mini"),
		BeaconSinkURL:     os.Getenv("NORN_BEACON_SINK_URL"),
		BeaconSinkKeyID:   os.Getenv("NORN_BEACON_SINK_KEY_ID"),
		BeaconSinkSecret:  os.Getenv("NORN_BEACON_SINK_SECRET"),

		AllowedOrigins:     os.Getenv("NORN_ALLOWED_ORIGINS"),
		CFAccessTeamDomain: os.Getenv("NORN_CF_ACCESS_TEAM_DOMAIN"),
		CFAccessAUD:        os.Getenv("NORN_CF_ACCESS_AUD"),

		WebhookSecret:          os.Getenv("NORN_WEBHOOK_SECRET"),
		CloudflaredConfig:      envOr("NORN_CLOUDFLARED_CONFIG", os.Getenv("HOME")+"/.cloudflared/config.yml"),
		CloudflareAPIToken:     firstEnv("NORN_CLOUDFLARE_API_TOKEN", "CLOUDFLARE_API_TOKEN"),
		CloudflareZoneID:       firstEnv("NORN_CLOUDFLARE_ZONE_ID", "CLOUDFLARE_ZONE_ID"),
		CloudflareLogpushToken: os.Getenv("NORN_CLOUDFLARE_LOGPUSH_TOKEN"),
		CloudflareAPIBaseURL:   envOr("NORN_CLOUDFLARE_API_BASE_URL", "https://api.cloudflare.com/client/v4"),
	}
}

func (c *Config) Production() bool {
	return c != nil && strings.EqualFold(strings.TrimSpace(c.Profile), "production")
}

func uiDir() string {
	if explicit := strings.TrimSpace(os.Getenv("NORN_UI_DIR")); explicit != "" {
		if validUIDir(explicit) {
			return explicit
		}
	}
	return defaultUIDir()
}

func defaultUIDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	candidate := home + "/norn/current/ui"
	if validUIDir(candidate) {
		return candidate
	}
	return ""
}

func validUIDir(dir string) bool {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() {
		return false
	}
	index, err := root.Stat("index.html")
	return err == nil && !index.IsDir()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt64Or(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func envTimeOr(key, fallback string) time.Time {
	raw := envOr(key, fallback)
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func envBoolOr(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

func envIntOr(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitNonEmpty(raw string) []string {
	values := []string{}
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func networkMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "tailnet", "tailscale":
		return "tailnet"
	case "public":
		return "public"
	default:
		return "local"
	}
}

func workloadConnector(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "apple", "container", "apple-container":
		return "apple-container"
	case "", "nomad", "nomad-consul":
		return "nomad-consul"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}
