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
  review-worker:
    port: 7790
    command: /contextdb worker review --dry-run --health-addr :7790
    health:
      path: /v1/ping
  nightly:
    schedule: "0 3 * * *"
    command: /contextdb snapshot export
endpoints:
  - url: http://127.0.0.1:7701
`)
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), spec, 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{cfg: &config.Config{AppsDir: appsDir}}
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
