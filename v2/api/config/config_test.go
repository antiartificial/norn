package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyTokenSigningDeadline(t *testing.T) {
	t.Setenv("NORN_LEGACY_TOKEN_SIGNING_UNTIL", "2026-08-10T12:00:00Z")
	if got := Load().LegacyTokenSigningUntil; !got.Equal(time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("LegacyTokenSigningUntil = %s", got)
	}
	t.Setenv("NORN_LEGACY_TOKEN_SIGNING_UNTIL", "not-a-time")
	if got := Load().LegacyTokenSigningUntil; !got.IsZero() {
		t.Fatalf("invalid deadline should fail closed, got %s", got)
	}
}

func TestHashiCorpTLSVerificationEnvironment(t *testing.T) {
	t.Setenv("NOMAD_SKIP_VERIFY", "true")
	t.Setenv("CONSUL_HTTP_SSL_VERIFY", "false")
	cfg := Load()
	if !cfg.NomadTLSSkipVerify || !cfg.ConsulTLSSkipVerify {
		t.Fatalf("TLS verification flags = nomad:%v consul:%v, want both insecure", cfg.NomadTLSSkipVerify, cfg.ConsulTLSSkipVerify)
	}
}

func TestAuditVerificationKeyRotationConfig(t *testing.T) {
	t.Setenv("NORN_AUDIT_SIGNING_KEY", strings.Repeat("n", 32))
	t.Setenv("NORN_AUDIT_PREVIOUS_SIGNING_KEYS", strings.Repeat("a", 32)+", "+strings.Repeat("b", 32))
	t.Setenv("NORN_AUDIT_RETENTION_DAYS", "")
	cfg := Load()
	if len(cfg.AuditPreviousSigningKeys) != 2 || cfg.AuditPreviousSigningKeys[1] != strings.Repeat("b", 32) || cfg.AuditRetentionDays != 365 {
		t.Fatalf("previous audit keys were not parsed: %d", len(cfg.AuditPreviousSigningKeys))
	}
}

func TestBeaconConfig(t *testing.T) {
	t.Setenv("NORN_BEACON_ENVIRONMENT", "mini")
	t.Setenv("NORN_BEACON_SINK_URL", "https://vigil.example.test/events")
	t.Setenv("NORN_BEACON_SINK_KEY_ID", "norn-mini")
	t.Setenv("NORN_BEACON_SINK_SECRET", "secret")

	cfg := Load()

	if cfg.BeaconEnvironment != "mini" {
		t.Fatalf("BeaconEnvironment = %q, want mini", cfg.BeaconEnvironment)
	}
	if cfg.BeaconSinkURL != "https://vigil.example.test/events" {
		t.Fatalf("BeaconSinkURL = %q", cfg.BeaconSinkURL)
	}
	if cfg.BeaconSinkKeyID != "norn-mini" {
		t.Fatalf("BeaconSinkKeyID = %q", cfg.BeaconSinkKeyID)
	}
	if cfg.BeaconSinkSecret != "secret" {
		t.Fatalf("BeaconSinkSecret = %q", cfg.BeaconSinkSecret)
	}
}

func TestNetworkMode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "default", in: "", want: "local"},
		{name: "local", in: "local", want: "local"},
		{name: "tailnet", in: "tailnet", want: "tailnet"},
		{name: "tailscale alias", in: "tailscale", want: "tailnet"},
		{name: "public", in: "public", want: "public"},
		{name: "unknown", in: "other", want: "local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := networkMode(tt.in); got != tt.want {
				t.Fatalf("networkMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDefaultUIDirUsesCurrentReleaseUI(t *testing.T) {
	home := t.TempDir()
	uiDir := filepath.Join(home, "norn", "current", "ui")
	if err := os.MkdirAll(uiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uiDir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("NORN_UI_DIR", "")

	cfg := Load()

	if cfg.UIDir != uiDir {
		t.Fatalf("UIDir = %q, want %q", cfg.UIDir, uiDir)
	}
}

func TestExistingExplicitUIDirOverridesCurrentReleaseUI(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "norn", "current", "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	explicit := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(explicit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(explicit, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("NORN_UI_DIR", explicit)

	cfg := Load()

	if cfg.UIDir != explicit {
		t.Fatalf("UIDir = %q, want explicit %q", cfg.UIDir, explicit)
	}
}

func TestMissingExplicitUIDirFallsBackToCurrentReleaseUI(t *testing.T) {
	home := t.TempDir()
	uiDir := filepath.Join(home, "norn", "current", "ui")
	if err := os.MkdirAll(uiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uiDir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("NORN_UI_DIR", filepath.Join(t.TempDir(), "missing"))

	cfg := Load()

	if cfg.UIDir != uiDir {
		t.Fatalf("UIDir = %q, want fallback %q", cfg.UIDir, uiDir)
	}
}

func TestExplicitUIDirWithoutIndexFallsBackToCurrentReleaseUI(t *testing.T) {
	home := t.TempDir()
	uiDir := filepath.Join(home, "norn", "current", "ui")
	if err := os.MkdirAll(uiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uiDir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	explicit := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(explicit, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("NORN_UI_DIR", explicit)

	cfg := Load()

	if cfg.UIDir != uiDir {
		t.Fatalf("UIDir = %q, want fallback %q", cfg.UIDir, uiDir)
	}
}

func TestRedpandaConfig(t *testing.T) {
	t.Setenv("NORN_REDPANDA_BROKERS", "127.0.0.1:9092, redpanda.service.consul:9092")
	t.Setenv("NORN_RPK_PATH", "/opt/redpanda/bin/rpk")

	cfg := Load()

	if got, want := strings.Join(cfg.RedpandaBrokers, ","), "127.0.0.1:9092,redpanda.service.consul:9092"; got != want {
		t.Fatalf("RedpandaBrokers = %q, want %q", got, want)
	}
	if cfg.RedpandaRPKPath != "/opt/redpanda/bin/rpk" {
		t.Fatalf("RedpandaRPKPath = %q", cfg.RedpandaRPKPath)
	}
}
