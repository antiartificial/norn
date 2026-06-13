package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type observabilityBundle struct {
	GeneratedAt       string            `json:"generatedAt"`
	Retention         string            `json:"retention"`
	PrometheusConfig  string            `json:"prometheusConfig"`
	AlertRules        string            `json:"alertRules"`
	GrafanaDatasource string            `json:"grafanaDatasource"`
	GrafanaDashboard  string            `json:"grafanaDashboard"`
	ServiceSpecs      map[string]string `json:"serviceSpecs"`
}

type observabilityInstallReceipt struct {
	Status    string   `json:"status"`
	AppsDir   string   `json:"appsDir"`
	Installed []string `json:"installed"`
	Skipped   []string `json:"skipped,omitempty"`
	Files     []string `json:"files"`
}

func (h *Handler) ObservabilityBundle(w http.ResponseWriter, r *http.Request) {
	bundle, err := h.buildObservabilityBundle()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, bundle)
}

func (h *Handler) ObservabilityServicesInstall(w http.ResponseWriter, r *http.Request) {
	overwrite := r.URL.Query().Get("overwrite") == "true"
	receipt, err := h.installObservabilityServices(overwrite)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "already exists") {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, receipt)
}

func (h *Handler) PrometheusAlerts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	_, _ = w.Write([]byte(prometheusAlertRules()))
}

func (h *Handler) buildObservabilityBundle() (observabilityBundle, error) {
	manifest, err := h.buildServiceManifest()
	if err != nil {
		return observabilityBundle{}, err
	}
	var prometheus bytes.Buffer
	fmt.Fprintf(&prometheus, "global:\n  scrape_interval: 30s\n  evaluation_interval: 30s\n\n")
	fmt.Fprintf(&prometheus, "rule_files:\n  - /etc/prometheus/rules/norn-alerts.yml\n\n")
	fmt.Fprintf(&prometheus, "scrape_configs:\n")
	fmt.Fprintf(&prometheus, "  - job_name: norn\n    metrics_path: /metrics\n    static_configs:\n      - targets: [%q]\n", h.observabilityNornTarget())
	writeAppScrapeConfigs(&prometheus, manifest)

	datasource := map[string]interface{}{
		"apiVersion": 1,
		"datasources": []map[string]interface{}{
			{
				"name":      "Norn Prometheus",
				"type":      "prometheus",
				"access":    "proxy",
				"url":       "http://prometheus.service.consul:9090",
				"isDefault": true,
			},
		},
	}
	datasourceJSON, _ := json.MarshalIndent(datasource, "", "  ")
	dashboardJSON, _ := json.MarshalIndent(grafanaDashboard(), "", "  ")

	return observabilityBundle{
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		Retention:         "30d or 8GB",
		PrometheusConfig:  prometheus.String(),
		AlertRules:        prometheusAlertRules(),
		GrafanaDatasource: string(datasourceJSON) + "\n",
		GrafanaDashboard:  string(dashboardJSON) + "\n",
		ServiceSpecs: map[string]string{
			"prometheus": prometheusServiceSpec(),
			"grafana":    grafanaServiceSpec(),
			"cadvisor":   cadvisorServiceSpec(),
		},
	}, nil
}

func (h *Handler) observabilityNornTarget() string {
	if target := strings.TrimSpace(os.Getenv("NORN_OBSERVABILITY_NORN_TARGET")); target != "" {
		return target
	}
	host := strings.TrimSpace(h.cfg.BindAddr)
	if host == "" || host == "127.0.0.1" || host == "localhost" {
		host = "host.docker.internal"
	}
	return host + ":" + h.cfg.Port
}

func (h *Handler) installObservabilityServices(overwrite bool) (observabilityInstallReceipt, error) {
	if strings.TrimSpace(h.cfg.AppsDir) == "" {
		return observabilityInstallReceipt{}, fmt.Errorf("apps dir is not configured")
	}
	bundle, err := h.buildObservabilityBundle()
	if err != nil {
		return observabilityInstallReceipt{}, err
	}
	services := observabilityServiceFiles(bundle)
	receipt := observabilityInstallReceipt{
		Status:  "installed",
		AppsDir: h.cfg.AppsDir,
	}
	apps := make([]string, 0, len(services))
	for app := range services {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	for _, app := range apps {
		files := services[app]
		appDir := filepath.Join(h.cfg.AppsDir, app)
		if _, err := os.Stat(appDir); err == nil && !overwrite {
			return receipt, fmt.Errorf("%s already exists; pass overwrite=true to replace generated files", app)
		}
		if err := os.MkdirAll(appDir, 0o755); err != nil {
			return receipt, err
		}
		names := make([]string, 0, len(files))
		for rel := range files {
			names = append(names, rel)
		}
		sort.Strings(names)
		for _, rel := range names {
			content := files[rel]
			path := filepath.Join(appDir, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return receipt, err
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return receipt, err
			}
			receipt.Files = append(receipt.Files, filepath.Join(app, rel))
		}
		receipt.Installed = append(receipt.Installed, app)
	}
	return receipt, nil
}

func observabilityServiceFiles(bundle observabilityBundle) map[string]map[string]string {
	return map[string]map[string]string{
		"norn-prometheus": {
			"infraspec.yaml": prometheusServiceSpec(),
			"Dockerfile": `FROM prom/prometheus:v2.55.1
ENTRYPOINT []
COPY prometheus.yml /etc/prometheus/prometheus.yml
COPY rules /etc/prometheus/rules
`,
			"prometheus.yml":        bundle.PrometheusConfig,
			"rules/norn-alerts.yml": bundle.AlertRules,
			"README.md":             observabilityServiceReadme("Prometheus", "norn-prometheus"),
		},
		"norn-grafana": {
			"infraspec.yaml": grafanaServiceSpec(),
			"Dockerfile": `FROM grafana/grafana:11.5.2
ENTRYPOINT []
COPY provisioning /etc/grafana/provisioning
COPY dashboards /var/lib/grafana/dashboards
`,
			"provisioning/datasources/norn-prometheus.yaml": grafanaDatasourceYAML(),
			"provisioning/dashboards/norn.yaml":             grafanaDashboardProviderYAML(),
			"dashboards/norn-platform.json":                 bundle.GrafanaDashboard,
			"README.md":                                     observabilityServiceReadme("Grafana", "norn-grafana"),
		},
		"norn-cadvisor": {
			"infraspec.yaml": cadvisorServiceSpec(),
			"Dockerfile": `FROM gcr.io/cadvisor/cadvisor:v0.49.1
ENTRYPOINT []
`,
			"README.md": observabilityServiceReadme("cAdvisor", "norn-cadvisor"),
		},
	}
}

func prometheusAlertRules() string {
	return `groups:
  - name: norn-platform
    rules:
      - alert: NornServiceDown
        expr: norn_service_status{status="critical"} == 1
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "{{ $labels.app }} {{ $labels.process }} is critical"
          description: "Consul reports a critical Norn-managed service."
      - alert: NornServiceDegraded
        expr: norn_service_status{status="warning"} == 1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "{{ $labels.app }} {{ $labels.process }} is degraded"
      - alert: NornDeployFailed
        expr: increase(norn_deploys_total{status="failed"}[30m]) > 0
        labels:
          severity: critical
        annotations:
          summary: "{{ $labels.app }} had a failed deploy"
      - alert: NornCronFailed
        expr: increase(norn_beacon_events_total{type=~"cron.failed|cron.lost|cron.hung"}[30m]) > 0
        labels:
          severity: critical
        annotations:
          summary: "A scheduled Norn process failed, was lost, or appears hung"
      - alert: NornSnapshotRetentionOverLimit
        expr: norn_snapshot_over_limit_total > 0
        for: 15m
        labels:
          severity: warning
        annotations:
          summary: "{{ $labels.app }} snapshots exceed retention"
      - alert: NornDiskLow
        expr: norn_host_disk_free_bytes / norn_host_disk_total_bytes < 0.10
        for: 10m
        labels:
          severity: critical
        annotations:
          summary: "Norn host disk free space below 10%"
`
}

func prometheusServiceSpec() string {
	return `name: norn-prometheus
deploy: true
build:
  dockerfile: Dockerfile
processes:
  web:
    port: 9090
    command: /bin/prometheus --config.file=/etc/prometheus/prometheus.yml --storage.tsdb.path=/prometheus --storage.tsdb.retention.time=30d --storage.tsdb.retention.size=8GB --web.enable-lifecycle
    metrics:
      enabled: true
      path: /metrics
    health:
      path: /-/healthy
    resources:
      cpu: 50
      memory: 256
`
}

func grafanaServiceSpec() string {
	return `name: norn-grafana
deploy: true
build:
  dockerfile: Dockerfile
processes:
  web:
    port: 3000
    command: /run.sh
    health:
      path: /api/health
    resources:
      cpu: 50
      memory: 256
`
}

func cadvisorServiceSpec() string {
	return `name: norn-cadvisor
deploy: true
build:
  dockerfile: Dockerfile
processes:
  web:
    port: 8080
    command: /usr/bin/cadvisor
    metrics:
      enabled: true
      path: /metrics
    health:
      path: /healthz
    resources:
      cpu: 50
      memory: 128
`
}

func grafanaDatasourceYAML() string {
	return `apiVersion: 1
datasources:
  - name: Norn Prometheus
    type: prometheus
    access: proxy
    url: http://norn-prometheus-web.service.consul:9090
    isDefault: true
`
}

func grafanaDashboardProviderYAML() string {
	return `apiVersion: 1
providers:
  - name: Norn
    orgId: 1
    folder: Norn
    type: file
    disableDeletion: false
    updateIntervalSeconds: 30
    options:
      path: /var/lib/grafana/dashboards
`
}

func observabilityServiceReadme(displayName, app string) string {
	return fmt.Sprintf(`# %s

Generated by Norn as a managed observability service app.

Validate and deploy with:

`+"```bash\nnorn validate %s\nnorn preflight %s HEAD\nnorn deploy %s HEAD\n```"+`

Review ports, host volume needs, and local policy before deploying on a shared host.
`, displayName, app, app, app)
}

func grafanaDashboard() map[string]interface{} {
	return map[string]interface{}{
		"title":         "Norn Platform",
		"schemaVersion": 39,
		"version":       1,
		"refresh":       "30s",
		"panels": []map[string]interface{}{
			graphPanel(1, "Discovered Apps", "stat", "norn_apps_total"),
			graphPanel(2, "App Health", "timeseries", "sum by (status) (norn_app_health)"),
			graphPanel(3, "Active Operations", "timeseries", "sum by (status) (norn_operations_total)"),
			graphPanel(4, "Beacon Critical Events", "timeseries", `sum by (type) (norn_beacon_events_total{severity="critical"})`),
			graphPanel(5, "Disk Free %", "gauge", "100 * norn_host_disk_free_bytes / norn_host_disk_total_bytes"),
			graphPanel(6, "Snapshot Over Limit", "timeseries", "sum by (app) (norn_snapshot_over_limit_total)"),
		},
	}
}

func graphPanel(id int, title, typ, expr string) map[string]interface{} {
	return map[string]interface{}{
		"id":    id,
		"title": title,
		"type":  typ,
		"targets": []map[string]interface{}{
			{"expr": expr},
		},
		"gridPos": map[string]int{
			"h": 8,
			"w": 8,
			"x": ((id - 1) % 3) * 8,
			"y": ((id - 1) / 3) * 8,
		},
	}
}
