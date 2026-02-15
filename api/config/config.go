package config

import "os"

type Config struct {
	Port        string
	BindAddr    string // listen address (default 127.0.0.1 — localhost only)
	DatabaseURL string
	UIDir       string
	AppsDir     string // directory containing app repos with infraspec.yaml
	TunnelName  string // cloudflared tunnel name for DNS routes
	GitToken    string // HTTPS auth token for git clone
	GitSSHKey   string // path to SSH private key for git clone
	PGHost      string // postgres host for in-cluster DATABASE_URL injection
	PGUser      string // postgres user for in-cluster DATABASE_URL injection
	CronRuntime string // "docker" (default) or "incus"
	APIToken    string // optional bearer token for API auth
	RegistryURL string // container registry URL (e.g. ghcr.io/username)
	S3Endpoint  string // S3-compatible endpoint (e.g. "localhost:9000")
	S3AccessKey string
	S3SecretKey string
	S3Region    string
	S3UseSSL    bool

	AllowedOrigins     string // comma-separated allowed origins for CORS (e.g. "https://app.norn.dev")
	CFAccessTeamDomain string // CF Access team domain (e.g. "myteam.cloudflareaccess.com")
	CFAccessAUD        string // CF Access Application AUD tag

	WorkerEnabled bool // enable distributed worker dispatch
}

func Load() *Config {
	return &Config{
		Port:        envOr("NORN_PORT", "8800"),
		BindAddr:    envOr("NORN_BIND_ADDR", "127.0.0.1"),
		DatabaseURL: envOr("NORN_DATABASE_URL", "postgres://norn:norn@localhost:5432/norn_db?sslmode=disable"),
		UIDir:       envOr("NORN_UI_DIR", ""),
		AppsDir:     envOr("NORN_APPS_DIR", os.Getenv("HOME")+"/projects"),
		TunnelName:  envOr("NORN_TUNNEL_NAME", "norn"),
		GitToken:    os.Getenv("NORN_GIT_TOKEN"),
		GitSSHKey:   os.Getenv("NORN_GIT_SSH_KEY"),
		PGHost:      envOr("NORN_PG_HOST", "localhost"),
		PGUser:      envOr("NORN_PG_USER", os.Getenv("USER")),
		CronRuntime: envOr("NORN_CRON_RUNTIME", "docker"),
		APIToken:    os.Getenv("NORN_API_TOKEN"),
		RegistryURL: os.Getenv("NORN_REGISTRY_URL"),
		S3Endpoint:  os.Getenv("NORN_S3_ENDPOINT"),
		S3AccessKey: os.Getenv("NORN_S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("NORN_S3_SECRET_KEY"),
		S3Region:    envOr("NORN_S3_REGION", "auto"),
		S3UseSSL:    os.Getenv("NORN_S3_USE_SSL") != "false",

		AllowedOrigins:     os.Getenv("NORN_ALLOWED_ORIGINS"),
		CFAccessTeamDomain: os.Getenv("NORN_CF_ACCESS_TEAM_DOMAIN"),
		CFAccessAUD:        os.Getenv("NORN_CF_ACCESS_AUD"),

		WorkerEnabled: os.Getenv("NORN_WORKER_ENABLED") == "true",
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
