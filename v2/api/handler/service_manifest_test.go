package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"norn/v2/api/config"
	"norn/v2/api/model"
)

func TestServiceManifestClassifiesProcessesAndEndpointReachability(t *testing.T) {
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "contextdb")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := []byte(`
name: contextdb
deploy: true
processes:
  web:
    port: 7701
    health:
      path: /v1/ping
    metrics:
      enabled: true
      path: /metrics
  review-worker:
    port: 7790
    command: /contextdb worker review --dry-run --health-addr :7790
    health:
      path: /v1/ping
    metrics:
      enabled: true
      path: /metrics
      port: 7791
  nightly:
    schedule: "0 3 * * *"
    command: /contextdb snapshot export
endpoints:
  - url: http://127.0.0.1:7701
`)
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), spec, 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{cfg: &config.Config{AppsDir: appsDir, NetworkMode: "local"}}
	req := httptest.NewRequest(http.MethodGet, "/api/services/manifest", nil)
	rec := httptest.NewRecorder()

	h.ServiceManifest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var manifest model.ServiceManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.NetworkMode != "local" {
		t.Fatalf("networkMode = %q, want local", manifest.NetworkMode)
	}

	entries := map[string]model.ServiceManifestEntry{}
	for _, entry := range manifest.Services {
		entries[entry.Process] = entry
	}

	web := entries["web"]
	if web.Type != "service" {
		t.Fatalf("web type = %q, want service", web.Type)
	}
	if got := len(web.Endpoints); got != 1 {
		t.Fatalf("web endpoints = %d, want 1", got)
	}
	if web.Metadata["endpointScope"] != "local" {
		t.Fatalf("web endpointScope = %q, want local", web.Metadata["endpointScope"])
	}
	if web.Metadata["networkMode"] != "local" {
		t.Fatalf("web networkMode = %q, want local", web.Metadata["networkMode"])
	}
	if web.Reachability.EndpointScope != "local" || !web.Reachability.Routable {
		t.Fatalf("web reachability = %+v, want local routable", web.Reachability)
	}
	if web.Metrics == nil || !web.Metrics.Enabled || web.Metrics.Path != "/metrics" {
		t.Fatalf("web metrics = %+v, want enabled /metrics", web.Metrics)
	}

	worker := entries["review-worker"]
	if worker.Type != "worker" {
		t.Fatalf("worker type = %q, want worker", worker.Type)
	}
	if got := len(worker.Endpoints); got != 0 {
		t.Fatalf("worker endpoints = %d, want 0", got)
	}
	if worker.HealthPath != "/v1/ping" {
		t.Fatalf("worker health path = %q, want /v1/ping", worker.HealthPath)
	}
	if worker.Metadata["endpointScope"] != "none" {
		t.Fatalf("worker endpointScope = %q, want none", worker.Metadata["endpointScope"])
	}
	if worker.Reachability.Exposure != "internal" {
		t.Fatalf("worker exposure = %q, want internal", worker.Reachability.Exposure)
	}
	if worker.Metrics == nil || worker.Metrics.ServiceName != "contextdb-review-worker-metrics" {
		t.Fatalf("worker metrics = %+v, want metrics service", worker.Metrics)
	}

	cron := entries["nightly"]
	if cron.Type != "cron" {
		t.Fatalf("cron type = %q, want cron", cron.Type)
	}
	if cron.HealthPath != "" {
		t.Fatalf("cron health path = %q, want empty", cron.HealthPath)
	}
	if got := len(cron.Endpoints); got != 0 {
		t.Fatalf("cron endpoints = %d, want 0", got)
	}
}

func TestManifestReachabilityScopes(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "localhost", host: "localhost", want: "local"},
		{name: "loopback", host: "127.0.0.1", want: "local"},
		{name: "private ten", host: "10.0.0.5", want: "private"},
		{name: "private one seven two", host: "172.20.0.5", want: "private"},
		{name: "private one nine two", host: "192.168.1.5", want: "private"},
		{name: "public", host: "contextdb.example.test", want: "public"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyHostScope(tt.host); got != tt.want {
				t.Fatalf("classifyHostScope(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}
