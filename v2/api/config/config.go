package config

import (
	"os"
	"strings"
)

type Config struct {
	Port        string
	BindAddr    string
	DatabaseURL string
	UIDir       string
	AppsDir     string
	GitToken    string
	GitSSHKey   string
	APIToken    string
	RegistryURL string // GHCR registry (e.g. ghcr.io/username)
	NetworkMode string // local, tailnet, public

	NomadAddr  string // Nomad API address
	ConsulAddr string // Consul API address

	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Region    string
	S3UseSSL    bool
	S3Provider  string
	S3ForcePath bool

	GarageAdminEndpoint string
	GarageAdminToken    string

	BeaconEnvironment string
	BeaconSinkURL     string
	BeaconSinkKeyID   string
	BeaconSinkSecret  string

	AllowedOrigins     string
	CFAccessTeamDomain string
	CFAccessAUD        string

	WebhookSecret     string // NORN_WEBHOOK_SECRET
	CloudflaredConfig string // NORN_CLOUDFLARED_CONFIG
}

func Load() *Config {
	return &Config{
		Port:        envOr("NORN_PORT", "8800"),
		BindAddr:    envOr("NORN_BIND_ADDR", "127.0.0.1"),
		DatabaseURL: envOr("NORN_DATABASE_URL", "postgres://norn:norn@localhost:5432/norn_v2?sslmode=disable"),
		UIDir:       envOr("NORN_UI_DIR", ""),
		AppsDir:     envOr("NORN_APPS_DIR", os.Getenv("HOME")+"/projects"),
		GitToken:    os.Getenv("NORN_GIT_TOKEN"),
		GitSSHKey:   os.Getenv("NORN_GIT_SSH_KEY"),
		APIToken:    os.Getenv("NORN_API_TOKEN"),
		RegistryURL: os.Getenv("NORN_REGISTRY_URL"),
		NetworkMode: networkMode(envOr("NORN_NETWORK_MODE", "local")),

		NomadAddr:  envOr("NORN_NOMAD_ADDR", "http://localhost:4646"),
		ConsulAddr: envOr("NORN_CONSUL_ADDR", "http://localhost:8500"),

		S3Endpoint:          os.Getenv("NORN_S3_ENDPOINT"),
		S3AccessKey:         os.Getenv("NORN_S3_ACCESS_KEY"),
		S3SecretKey:         os.Getenv("NORN_S3_SECRET_KEY"),
		S3Region:            envOr("NORN_S3_REGION", "auto"),
		S3UseSSL:            os.Getenv("NORN_S3_USE_SSL") != "false",
		S3Provider:          envOr("NORN_S3_PROVIDER", "s3"),
		S3ForcePath:         os.Getenv("NORN_S3_FORCE_PATH_STYLE") == "true" || envOr("NORN_S3_PROVIDER", "s3") == "garage",
		GarageAdminEndpoint: os.Getenv("NORN_GARAGE_ADMIN_ENDPOINT"),
		GarageAdminToken:    os.Getenv("NORN_GARAGE_ADMIN_TOKEN"),

		BeaconEnvironment: envOr("NORN_BEACON_ENVIRONMENT", "mini"),
		BeaconSinkURL:     os.Getenv("NORN_BEACON_SINK_URL"),
		BeaconSinkKeyID:   os.Getenv("NORN_BEACON_SINK_KEY_ID"),
		BeaconSinkSecret:  os.Getenv("NORN_BEACON_SINK_SECRET"),

		AllowedOrigins:     os.Getenv("NORN_ALLOWED_ORIGINS"),
		CFAccessTeamDomain: os.Getenv("NORN_CF_ACCESS_TEAM_DOMAIN"),
		CFAccessAUD:        os.Getenv("NORN_CF_ACCESS_AUD"),

		WebhookSecret:     os.Getenv("NORN_WEBHOOK_SECRET"),
		CloudflaredConfig: envOr("NORN_CLOUDFLARED_CONFIG", os.Getenv("HOME")+"/.cloudflared/config.yml"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
