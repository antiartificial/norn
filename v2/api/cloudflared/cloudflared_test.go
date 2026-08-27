package cloudflared

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestIsPublicEndpoint(t *testing.T) {
	tests := []struct {
		endpoint string
		want     bool
	}{
		{"https://vigil.slopistry.com/health", true},
		{"api.example.com", true},
		{"https://mini.tail113139.ts.net:8144", false},
		{"https://vigil.norn", false},
		{"http://service.internal:8080", false},
		{"https://foo.localhost", false},
		{"localhost", false},
		{"127.0.0.1", false},
		{"service", false},
	}
	for _, test := range tests {
		t.Run(test.endpoint, func(t *testing.T) {
			if got := IsPublicEndpoint(test.endpoint); got != test.want {
				t.Fatalf("IsPublicEndpoint(%q) = %t, want %t", test.endpoint, got, test.want)
			}
		})
	}
}

func TestPrunePrivateIngress(t *testing.T) {
	cfg := &Config{Ingress: []IngressRule{
		{Hostname: "api.example.com", Service: "http://192.0.2.10:8080"},
		{Hostname: "api.norn", Service: "http://192.0.2.10:8080"},
		{Hostname: "host.example.ts.net", Service: "http://192.0.2.10:8080"},
		{Service: "http_status:404"},
	}}
	if !PrunePrivateIngress(cfg) {
		t.Fatal("PrunePrivateIngress did not report a change")
	}
	if len(cfg.Ingress) != 2 || cfg.Ingress[0].Hostname != "api.example.com" || cfg.Ingress[1].Hostname != "" {
		t.Fatalf("unexpected ingress after prune: %#v", cfg.Ingress)
	}
	if PrunePrivateIngress(cfg) {
		t.Fatal("second PrunePrivateIngress unexpectedly reported a change")
	}
}

func TestApplyConfigUsesPrivatePermissions(t *testing.T) {
	validator := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(validator, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := configPath
	previousBinary := binaryPath
	t.Cleanup(func() { configPath, binaryPath = previous, previousBinary })
	SetBinaryPath(validator)
	path := filepath.Join(t.TempDir(), "config.yml")
	SetConfigPath(path)
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyConfig(context.Background(), &Config{Tunnel: "test"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config permissions = %#o, want 0600", got)
	}
}

func TestApplyConfigValidationFailurePreservesPreviousFile(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "config.yml")
	previous := []byte("tunnel: previous\n")
	if err := os.WriteFile(destination, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	validator := filepath.Join(dir, "cloudflared")
	if err := os.WriteFile(validator, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previousConfig, previousBinary := configPath, binaryPath
	SetConfigPath(destination)
	SetBinaryPath(validator)
	t.Cleanup(func() { configPath, binaryPath = previousConfig, previousBinary })

	if err := ApplyConfig(context.Background(), &Config{Tunnel: "replacement"}); err == nil {
		t.Fatal("ApplyConfig succeeded with a failing validator")
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(previous) {
		t.Fatalf("previous config changed after validation failure: %q", data)
	}
}

func TestLaunchTargetUsesCurrentUIDAndConfiguredLabel(t *testing.T) {
	previous := launchLabel
	SetLaunchLabel("com.example.cloudflared")
	t.Cleanup(func() { launchLabel = previous })
	want := fmt.Sprintf("gui/%d/com.example.cloudflared", os.Getuid())
	if got := launchTarget(); got != want {
		t.Fatalf("launchTarget() = %q, want %q", got, want)
	}
}
