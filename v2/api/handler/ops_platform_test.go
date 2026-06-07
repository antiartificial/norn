package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"norn/v2/api/config"
)

func TestPlatformOpsSummarizesSecretsSnapshotsAndAccess(t *testing.T) {
	root := t.TempDir()
	appsDir := filepath.Join(root, "apps")
	appDir := filepath.Join(appsDir, "contextdb")
	if err := os.MkdirAll(filepath.Join(root, "snapshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := []byte(`
name: contextdb
deploy: true
secrets:
  - CONTEXTDB_DSN
snapshots:
  keep: 1
infrastructure:
  postgres:
    database: hermes_contextdb
processes:
  web:
    port: 7701
    health:
      path: /v1/ping
endpoints:
  - url: http://127.0.0.1:7701
`)
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), spec, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"hermes_contextdb_aaaaaa_20260605T171500.dump",
		"hermes_contextdb_bbbbbb_20260606T171500.dump",
	} {
		if err := os.WriteFile(filepath.Join(root, "snapshots", name), []byte("dump"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	access := NewAccessLog(10)
	access.Record(AccessEvent{Method: http.MethodGet, Path: "/api/apps", Status: 200, ClientIP: "127.0.0.1"})
	h := &Handler{cfg: &config.Config{AppsDir: appsDir, NetworkMode: "local"}, access: access}
	req := httptest.NewRequest(http.MethodGet, "/api/ops/platform", nil)
	rec := httptest.NewRecorder()

	h.PlatformOps(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var summary platformOpsSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Services.Total != 1 || summary.Services.Local != 1 {
		t.Fatalf("services = %+v, want one local service", summary.Services)
	}
	if summary.Secrets.NeedsAttention != 1 {
		t.Fatalf("secrets = %+v, want one app needing attention", summary.Secrets)
	}
	if len(summary.Snapshots) != 1 || summary.Snapshots[0].OverLimit != 1 {
		t.Fatalf("snapshots = %+v, want one over-limit snapshot status", summary.Snapshots)
	}
	if summary.Access.ByStatus["2xx"] != 1 {
		t.Fatalf("access = %+v, want one 2xx event", summary.Access)
	}
}
