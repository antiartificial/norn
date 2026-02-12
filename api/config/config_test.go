package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	// Clear any env vars that would override defaults
	os.Unsetenv("NORN_PORT")
	os.Unsetenv("NORN_DATABASE_URL")
	os.Unsetenv("NORN_UI_DIR")
	os.Unsetenv("NORN_APPS_DIR")

	cfg := Load()

	if cfg.Port != "8800" {
		t.Errorf("Port = %q, want 8800", cfg.Port)
	}
	if cfg.DatabaseURL != "postgres://norn:norn@localhost:5432/norn_db?sslmode=disable" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.UIDir != "" {
		t.Errorf("UIDir = %q, want empty", cfg.UIDir)
	}
}

func TestPGHostDefault(t *testing.T) {
	os.Unsetenv("NORN_PG_HOST")

	cfg := Load()
	if cfg.PGHost != "localhost" {
		t.Errorf("PGHost = %q, want localhost", cfg.PGHost)
	}
}

func TestRegistryURL(t *testing.T) {
	t.Setenv("NORN_REGISTRY_URL", "ghcr.io/testuser")

	cfg := Load()
	if cfg.RegistryURL != "ghcr.io/testuser" {
		t.Errorf("RegistryURL = %q, want ghcr.io/testuser", cfg.RegistryURL)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("NORN_PORT", "9999")
	t.Setenv("NORN_DATABASE_URL", "postgres://test:test@db:5432/test_db")
	t.Setenv("NORN_UI_DIR", "/srv/ui")
	t.Setenv("NORN_APPS_DIR", "/opt/apps")

	cfg := Load()

	if cfg.Port != "9999" {
		t.Errorf("Port = %q, want 9999", cfg.Port)
	}
	if cfg.DatabaseURL != "postgres://test:test@db:5432/test_db" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.UIDir != "/srv/ui" {
		t.Errorf("UIDir = %q", cfg.UIDir)
	}
	if cfg.AppsDir != "/opt/apps" {
		t.Errorf("AppsDir = %q, want /opt/apps", cfg.AppsDir)
	}
}

func TestCFAccessConfig(t *testing.T) {
	t.Setenv("NORN_ALLOWED_ORIGINS", "https://app.norn.dev,https://norn-abc.vercel.app")
	t.Setenv("NORN_CF_ACCESS_TEAM_DOMAIN", "myteam.cloudflareaccess.com")
	t.Setenv("NORN_CF_ACCESS_AUD", "some-aud-tag")

	cfg := Load()

	if cfg.AllowedOrigins != "https://app.norn.dev,https://norn-abc.vercel.app" {
		t.Errorf("AllowedOrigins = %q", cfg.AllowedOrigins)
	}
	if cfg.CFAccessTeamDomain != "myteam.cloudflareaccess.com" {
		t.Errorf("CFAccessTeamDomain = %q", cfg.CFAccessTeamDomain)
	}
	if cfg.CFAccessAUD != "some-aud-tag" {
		t.Errorf("CFAccessAUD = %q", cfg.CFAccessAUD)
	}
}
