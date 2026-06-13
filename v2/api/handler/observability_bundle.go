package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
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

func (h *Handler) ObservabilityBundle(w http.ResponseWriter, r *http.Request) {
	bundle, err := h.buildObservabilityBundle()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, bundle)
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
	fmt.Fprintf(&prometheus, "  - job_name: norn\n    metrics_path: /metrics\n    static_configs:\n      - targets: [%q]\n", h.cfg.BindAddr+":"+h.cfg.Port)
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
processes:
  web:
    port: 9090
    command: prometheus --config.file=/etc/prometheus/prometheus.yml --storage.tsdb.path=/prometheus --storage.tsdb.retention.time=30d --storage.tsdb.retention.size=8GB
    health:
      path: /-/healthy
    resources:
      cpu: 200
      memory: 512
volumes:
  - name: prometheus-data
    mount: /prometheus
`
}

func grafanaServiceSpec() string {
	return `name: norn-grafana
processes:
  web:
    port: 3000
    command: grafana server --homepath=/usr/share/grafana
    health:
      path: /api/health
    resources:
      cpu: 200
      memory: 512
volumes:
  - name: grafana-data
    mount: /var/lib/grafana
`
}

func cadvisorServiceSpec() string {
	return `name: norn-cadvisor
processes:
  web:
    port: 8080
    command: cadvisor
    metrics:
      enabled: true
      path: /metrics
    health:
      path: /healthz
    resources:
      cpu: 200
      memory: 256
`
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
