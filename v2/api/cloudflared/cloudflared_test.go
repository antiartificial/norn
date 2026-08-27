package cloudflared

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPServiceURL(t *testing.T) {
	tests := []struct {
		name    string
		address string
		port    int
		want    string
	}{
		{name: "IPv4", address: "192.168.4.124", port: 8144, want: "http://192.168.4.124:8144"},
		{name: "IPv6", address: "::1", port: 3000, want: "http://[::1]:3000"},
		{name: "hostname", address: "localhost", port: 8800, want: "http://localhost:8800"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HTTPServiceURL(tt.address, tt.port); got != tt.want {
				t.Fatalf("HTTPServiceURL(%q, %d) = %q, want %q", tt.address, tt.port, got, tt.want)
			}
		})
	}
}

func TestApplyConfigValidatesBeforeReplacing(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(destination, []byte("tunnel: previous\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	validator := filepath.Join(dir, "cloudflared")
	if err := os.WriteFile(validator, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previousConfigPath, previousBinaryPath := configPath, binaryPath
	SetConfigPath(destination)
	SetBinaryPath(validator)
	t.Cleanup(func() {
		configPath = previousConfigPath
		binaryPath = previousBinaryPath
	})

	cfg := &Config{Tunnel: "next", Ingress: []IngressRule{{Service: "http_status:404"}}}
	if err := ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "tunnel: next") {
		t.Fatalf("config was not replaced: %s", data)
	}
}

func TestApplyConfigKeepsPreviousFileWhenValidationFails(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "config.yml")
	previous := []byte("tunnel: previous\n")
	if err := os.WriteFile(destination, previous, 0o644); err != nil {
		t.Fatal(err)
	}
	validator := filepath.Join(dir, "cloudflared")
	if err := os.WriteFile(validator, []byte("#!/bin/sh\necho invalid >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previousConfigPath, previousBinaryPath := configPath, binaryPath
	SetConfigPath(destination)
	SetBinaryPath(validator)
	t.Cleanup(func() {
		configPath = previousConfigPath
		binaryPath = previousBinaryPath
	})

	err := ApplyConfig(context.Background(), &Config{Tunnel: "invalid"})
	if err == nil || !strings.Contains(err.Error(), "validate cloudflared config") {
		t.Fatalf("ApplyConfig error = %v, want validation error", err)
	}
	data, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatal(readErr)
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
