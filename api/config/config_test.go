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
