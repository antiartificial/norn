package cloudflared

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestRestartTargetsManagedCloudflaredAgent(t *testing.T) {
	bin := t.TempDir()
	output := filepath.Join(t.TempDir(), "launchctl-args")
	stub := filepath.Join(bin, "launchctl")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncase \"$1\" in\n  kickstart) printf '%s\\n' \"$@\" > \"$NORN_TEST_LAUNCHCTL_ARGS\" ;;\n  print) printf 'state = %s\\n' \"$NORN_TEST_LAUNCHCTL_STATE\" ;;\n  *) exit 1 ;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NORN_TEST_LAUNCHCTL_ARGS", output)
	t.Setenv("NORN_TEST_LAUNCHCTL_STATE", "running")
	if err := Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("kickstart\n-k\ngui/%d/com.norn.cloudflared\n", os.Getuid())
	if string(data) != want {
		t.Fatalf("launchctl args=%q want=%q", strings.TrimSpace(string(data)), strings.TrimSpace(want))
	}
	t.Setenv("NORN_TEST_LAUNCHCTL_STATE", "waiting")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := Restart(ctx); err == nil {
		t.Fatal("waiting managed agent was treated as restarted")
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
	previous := configPath
	t.Cleanup(func() { configPath = previous })
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
