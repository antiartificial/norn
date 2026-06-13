package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/config"
)

func TestMetricsEmitsPrometheusText(t *testing.T) {
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "metrics-app")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := []byte(`
name: metrics-app
deploy: true
processes:
  web:
    port: 8080
    metrics:
      enabled: true
      path: /metrics
infrastructure:
  objectStorage:
    provider: garage
    buckets:
      - name: metrics-app-media
`)
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), spec, 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{cfg: &config.Config{AppsDir: appsDir, NetworkMode: "local", BindAddr: "127.0.0.1", Port: "8800"}}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	h.Metrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# HELP norn_apps_total",
		`norn_app_info{app="metrics-app"} 1`,
		`norn_process_info{app="metrics-app",process="web",type="service",metrics_enabled="true"} 1`,
		`norn_object_storage_buckets{app="metrics-app",provider="garage"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestPrometheusConfigIncludesNornScrape(t *testing.T) {
	h := &Handler{cfg: &config.Config{AppsDir: t.TempDir(), NetworkMode: "local", BindAddr: "127.0.0.1", Port: "8800"}}
	req := httptest.NewRequest(http.MethodGet, "/api/observability/prometheus.yml", nil)
	rec := httptest.NewRecorder()

	h.PrometheusConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "job_name: norn") || !strings.Contains(body, "127.0.0.1:8800") {
		t.Fatalf("prometheus config missing norn scrape:\n%s", body)
	}
}
