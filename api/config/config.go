package config

import "os"

type Config struct {
	Port        string
	DatabaseURL string
	UIDir       string
	AppsDir     string // directory containing app repos with infraspec.yaml
	TunnelName  string // cloudflared tunnel name for DNS routes
	GitToken    string // HTTPS auth token for git clone
	GitSSHKey   string // path to SSH private key for git clone
}

func Load() *Config {
	return &Config{
		Port:        envOr("NORN_PORT", "8800"),
		DatabaseURL: envOr("NORN_DATABASE_URL", "postgres://norn:norn@localhost:5432/norn_db?sslmode=disable"),
		UIDir:       envOr("NORN_UI_DIR", ""),
		AppsDir:     envOr("NORN_APPS_DIR", os.Getenv("HOME")+"/projects"),
		TunnelName:  envOr("NORN_TUNNEL_NAME", "norn"),
		GitToken:    os.Getenv("NORN_GIT_TOKEN"),
		GitSSHKey:   os.Getenv("NORN_GIT_SSH_KEY"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
