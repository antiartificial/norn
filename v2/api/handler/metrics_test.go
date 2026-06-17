package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
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

func TestObservabilityBundleIncludesAlertsAndServiceSpecs(t *testing.T) {
	h := &Handler{cfg: &config.Config{AppsDir: t.TempDir(), NetworkMode: "local", BindAddr: "127.0.0.1", Port: "8800"}}
	req := httptest.NewRequest(http.MethodGet, "/api/observability/bundle", nil)
	rec := httptest.NewRecorder()

	h.ObservabilityBundle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var bundle observabilityBundle
	if err := json.Unmarshal(rec.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Retention != "30d or 8GB" {
		t.Fatalf("retention = %q", bundle.Retention)
	}
	for _, want := range []string{"NornServiceDown", "NornDiskLow"} {
		if !strings.Contains(bundle.AlertRules, want) {
			t.Fatalf("alert rules missing %q:\n%s", want, bundle.AlertRules)
		}
	}
	for _, name := range []string{"prometheus", "grafana", "cadvisor"} {
		if bundle.ServiceSpecs[name] == "" {
			t.Fatalf("missing service spec %q: %+v", name, bundle.ServiceSpecs)
		}
	}
	if !strings.Contains(bundle.PrometheusConfig, "host.docker.internal:8800") {
		t.Fatalf("bundle prometheus config should target host from containers:\n%s", bundle.PrometheusConfig)
	}
}

func TestObservabilityServicesInstallWritesManagedAppDirs(t *testing.T) {
	appsDir := t.TempDir()
	h := &Handler{cfg: &config.Config{AppsDir: appsDir, NetworkMode: "local", BindAddr: "127.0.0.1", Port: "8800"}}
	req := httptest.NewRequest(http.MethodPost, "/api/observability/services/install", nil)
	rec := httptest.NewRecorder()

	h.ObservabilityServicesInstall(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var receipt observabilityInstallReceipt
	if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Installed) != 3 {
		t.Fatalf("installed = %+v, want 3 apps", receipt.Installed)
	}
	for _, path := range []string{
		"norn-prometheus/infraspec.yaml",
		"norn-prometheus/prometheus.yml",
		"norn-grafana/provisioning/datasources/norn-prometheus.yaml",
		"norn-cadvisor/Dockerfile",
	} {
		if _, err := os.Stat(filepath.Join(appsDir, path)); err != nil {
			t.Fatalf("expected generated file %s: %v", path, err)
		}
	}
}

func TestObservabilityServicesInstallRejectsExistingWithoutOverwrite(t *testing.T) {
	appsDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(appsDir, "norn-prometheus"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{AppsDir: appsDir, NetworkMode: "local", BindAddr: "127.0.0.1", Port: "8800"}}
	req := httptest.NewRequest(http.MethodPost, "/api/observability/services/install", nil)
	rec := httptest.NewRecorder()

	h.ObservabilityServicesInstall(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCronMissedRunTreatsPrunedNomadChildrenAsHistory(t *testing.T) {
	spec := &model.InfraSpec{App: "field-harbor", Env: map[string]string{"TZ": "America/Chicago"}}
	proc := model.Process{Schedule: "10 20 * * *"}
	info := &nomad.PeriodicJobInfo{
		JobID:        "field-harbor-field-harbor-sync-pm",
		Schedule:     "10 20 * * *",
		TimeZone:     "America/Chicago",
		SubmittedAt:  "2026-06-16T12:42:47-05:00",
		Status:       "running",
		ChildrenDead: 28,
	}
	now, err := time.Parse(time.RFC3339, "2026-06-17T01:20:00-05:00")
	if err != nil {
		t.Fatal(err)
	}

	if got := cronMissedRun(now, spec, proc, info, nil); got != 0 {
		t.Fatalf("cronMissedRun = %d, want 0 for pruned child history", got)
	}
}

func TestCronMissedRunFlagsMissingFirstDispatch(t *testing.T) {
	spec := &model.InfraSpec{App: "field-harbor", Env: map[string]string{"TZ": "America/Chicago"}}
	proc := model.Process{Schedule: "10 20 * * *"}
	info := &nomad.PeriodicJobInfo{
		JobID:       "field-harbor-field-harbor-sync-pm",
		Schedule:    "10 20 * * *",
		TimeZone:    "America/Chicago",
		SubmittedAt: "2026-06-16T12:42:47-05:00",
		Status:      "running",
	}
	now, err := time.Parse(time.RFC3339, "2026-06-17T01:20:00-05:00")
	if err != nil {
		t.Fatal(err)
	}

	if got := cronMissedRun(now, spec, proc, info, []nomad.CronRun{}); got != 1 {
		t.Fatalf("cronMissedRun = %d, want 1 without dispatch evidence", got)
	}
}
