package handler

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/cronexpr"
	"norn/v2/api/model"
)

func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b bytes.Buffer

	manifest, _ := h.buildServiceManifest()
	specs, _ := model.DiscoverApps(h.cfg.AppsDir)
	sort.Slice(specs, func(i, j int) bool { return specs[i].App < specs[j].App })

	writeMetricHeader(&b, "norn_info", "Norn control plane build and network information.", "gauge")
	fmt.Fprintf(&b, "norn_info{network_mode=%q} 1\n", promLabel(h.cfg.NetworkMode))

	writeMetricHeader(&b, "norn_apps_total", "Number of discovered Norn applications.", "gauge")
	fmt.Fprintf(&b, "norn_apps_total %d\n", len(specs))

	writeMetricHeader(&b, "norn_app_info", "Discovered Norn application metadata.", "gauge")
	writeMetricHeader(&b, "norn_process_info", "Discovered Norn process metadata.", "gauge")
	writeMetricHeader(&b, "norn_app_health", "Application health derived from the service manifest, 1 for healthy/passing.", "gauge")
	writeMetricHeader(&b, "norn_service_status", "Service health status from the service manifest, 1 for the current status label.", "gauge")
	writeMetricHeader(&b, "norn_object_storage_buckets", "Declared object storage buckets per application.", "gauge")
	writeMetricHeader(&b, "norn_snapshots_total", "Local PostgreSQL snapshots per application.", "gauge")
	writeMetricHeader(&b, "norn_snapshot_over_limit_total", "Local PostgreSQL snapshots above declared retention per application.", "gauge")

	serviceStatusByApp := map[string]string{}
	for _, svc := range manifest.Services {
		if serviceStatusByApp[svc.App] == "" {
			serviceStatusByApp[svc.App] = "passing"
		}
		if svc.Status == "critical" {
			serviceStatusByApp[svc.App] = "critical"
		} else if svc.Status == "warning" && serviceStatusByApp[svc.App] != "critical" {
			serviceStatusByApp[svc.App] = "warning"
		} else if svc.Status == "unknown" && serviceStatusByApp[svc.App] == "passing" {
			serviceStatusByApp[svc.App] = "unknown"
		}
		for _, status := range []string{"passing", "warning", "critical", "unknown"} {
			value := 0
			if svc.Status == status {
				value = 1
			}
			fmt.Fprintf(&b, "norn_service_status{app=%q,process=%q,service=%q,status=%q} %d\n",
				promLabel(svc.App), promLabel(svc.Process), promLabel(svc.Name), promLabel(status), value)
		}
	}
	for _, spec := range specs {
		fmt.Fprintf(&b, "norn_app_info{app=%q} 1\n", promLabel(spec.App))
		healthy := 0
		if serviceStatusByApp[spec.App] == "passing" {
			healthy = 1
		}
		fmt.Fprintf(&b, "norn_app_health{app=%q,status=%q} %d\n", promLabel(spec.App), promLabel(serviceStatusByApp[spec.App]), healthy)
		processNames := sortedProcessNames(spec.Processes)
		for _, name := range processNames {
			proc := spec.Processes[name]
			fmt.Fprintf(&b, "norn_process_info{app=%q,process=%q,type=%q,metrics_enabled=%q} 1\n",
				promLabel(spec.App), promLabel(name), promLabel(manifestProcessType(name, proc)), promLabel(strconv.FormatBool(proc.Metrics != nil && proc.Metrics.Enabled)))
		}
		if spec.Infrastructure != nil && spec.Infrastructure.ObjectStorage != nil {
			provider := spec.Infrastructure.ObjectStorage.Provider
			if provider == "" {
				provider = "garage"
			}
			fmt.Fprintf(&b, "norn_object_storage_buckets{app=%q,provider=%q} %d\n", promLabel(spec.App), promLabel(provider), len(spec.Infrastructure.ObjectStorage.Buckets))
		}
		if snapshotStatus := summarizeSnapshots(spec); snapshotStatus != nil {
			fmt.Fprintf(&b, "norn_snapshots_total{app=%q,database=%q} %d\n", promLabel(spec.App), promLabel(snapshotStatus.Database), snapshotStatus.Count)
			fmt.Fprintf(&b, "norn_snapshot_over_limit_total{app=%q,database=%q} %d\n", promLabel(spec.App), promLabel(snapshotStatus.Database), snapshotStatus.OverLimit)
		}
	}

	if h.db != nil && h.db.Pool != nil {
		writeMetricHeader(&b, "norn_deploys_total", "Deployments recorded by Norn, grouped by app and status.", "counter")
		writeMetricHeader(&b, "norn_deploy_duration_seconds_count", "Completed deployments with a recorded duration.", "counter")
		writeMetricHeader(&b, "norn_deploy_duration_seconds_sum", "Total deployment duration in seconds.", "counter")
		writeMetricHeader(&b, "norn_deploy_last_started_timestamp_seconds", "Unix timestamp of the last deployment start.", "gauge")
		if deployMetrics, err := h.db.DeploymentMetrics(r.Context()); err == nil {
			for _, metric := range deployMetrics {
				labels := fmt.Sprintf("app=%q,status=%q", promLabel(metric.App), promLabel(string(metric.Status)))
				fmt.Fprintf(&b, "norn_deploys_total{%s} %d\n", labels, metric.Count)
				fmt.Fprintf(&b, "norn_deploy_duration_seconds_count{%s} %d\n", labels, metric.Count)
				fmt.Fprintf(&b, "norn_deploy_duration_seconds_sum{%s} %.3f\n", labels, metric.DurationSeconds)
				fmt.Fprintf(&b, "norn_deploy_last_started_timestamp_seconds{%s} %.0f\n", labels, metric.LastStartedUnix)
			}
		}

		writeMetricHeader(&b, "norn_operations_total", "Operations recorded by Norn, grouped by kind and status.", "counter")
		writeMetricHeader(&b, "norn_operation_duration_seconds_count", "Completed operations with a recorded duration.", "counter")
		writeMetricHeader(&b, "norn_operation_duration_seconds_sum", "Total operation duration in seconds.", "counter")
		writeMetricHeader(&b, "norn_operation_last_started_timestamp_seconds", "Unix timestamp of the last operation start.", "gauge")
		if operationMetrics, err := h.db.OperationMetrics(r.Context()); err == nil {
			for _, metric := range operationMetrics {
				labels := fmt.Sprintf("kind=%q,status=%q", promLabel(metric.Kind), promLabel(string(metric.Status)))
				fmt.Fprintf(&b, "norn_operations_total{%s} %d\n", labels, metric.Count)
				fmt.Fprintf(&b, "norn_operation_duration_seconds_count{%s} %d\n", labels, metric.Count)
				fmt.Fprintf(&b, "norn_operation_duration_seconds_sum{%s} %.3f\n", labels, metric.DurationSeconds)
				fmt.Fprintf(&b, "norn_operation_last_started_timestamp_seconds{%s} %.0f\n", labels, metric.LastStartedUnix)
			}
		}

		writeMetricHeader(&b, "norn_webhook_deliveries_total", "Webhook deliveries recorded by Norn, grouped by provider and status.", "counter")
		writeMetricHeader(&b, "norn_webhook_last_received_timestamp_seconds", "Unix timestamp of the last webhook delivery.", "gauge")
		if webhookMetrics, err := h.db.WebhookMetrics(r.Context()); err == nil {
			for _, metric := range webhookMetrics {
				labels := fmt.Sprintf("provider=%q,status=%q", promLabel(metric.Provider), promLabel(metric.Status))
				fmt.Fprintf(&b, "norn_webhook_deliveries_total{%s} %d\n", labels, metric.Count)
				fmt.Fprintf(&b, "norn_webhook_last_received_timestamp_seconds{%s} %.0f\n", labels, metric.LastReceivedUnix)
			}
		}

		writeMetricHeader(&b, "norn_beacon_events_total", "Beacon events recorded by Norn, grouped by type and severity.", "counter")
		writeMetricHeader(&b, "norn_beacon_last_occurred_timestamp_seconds", "Unix timestamp of the last Beacon event by type and severity.", "gauge")
		if beaconMetrics, err := h.db.BeaconMetrics(r.Context()); err == nil {
			for _, metric := range beaconMetrics {
				labels := fmt.Sprintf("type=%q,severity=%q", promLabel(metric.Type), promLabel(metric.Severity))
				fmt.Fprintf(&b, "norn_beacon_events_total{%s} %d\n", labels, metric.Count)
				fmt.Fprintf(&b, "norn_beacon_last_occurred_timestamp_seconds{%s} %.0f\n", labels, metric.LastOccurredUnix)
			}
		}
	}

	if h.access != nil {
		writeMetricHeader(&b, "norn_access_events_recent_total", "Recent API access events retained in memory, grouped by status bucket.", "gauge")
		byStatus := map[string]int{}
		for _, event := range h.access.Recent(defaultAccessLogLimit) {
			byStatus[statusBucket(event.Status)]++
		}
		for _, bucket := range sortedKeys(byStatus) {
			fmt.Fprintf(&b, "norn_access_events_recent_total{status_bucket=%q} %d\n", promLabel(bucket), byStatus[bucket])
		}
	}

	if h.nomad != nil {
		writeMetricHeader(&b, "norn_task_restarts_total", "Current restart count for running tasks.", "gauge")
		writeMetricHeader(&b, "norn_task_oom_kills_total", "Tasks that have been OOM killed in current allocations.", "gauge")
		oomByProc := map[string]int{}
		for _, spec := range specs {
			infos, err := h.nomad.TaskRestartSummary(spec.App)
			if err != nil {
				continue
			}
			for _, info := range infos {
				if info.Restarts > 0 {
					fmt.Fprintf(&b, "norn_task_restarts_total{app=%q,process=%q,task=%q} %d\n",
						promLabel(spec.App), promLabel(info.TaskGroup), promLabel(info.Task), info.Restarts)
				}
				if info.OOMKilled {
					key := spec.App + ":" + info.TaskGroup + ":" + info.Task
					oomByProc[key]++
					fmt.Fprintf(&b, "norn_task_oom_kills_total{app=%q,process=%q,task=%q} %d\n",
						promLabel(spec.App), promLabel(info.TaskGroup), promLabel(info.Task), oomByProc[key])
				}
			}
		}

		writeMetricHeader(&b, "norn_cron_missed_runs_total", "Whether a cron process missed its most recent expected run window (1 = missed, 0 = ok).", "gauge")
		for _, spec := range specs {
			for processName, proc := range spec.Processes {
				if strings.TrimSpace(proc.Schedule) == "" {
					continue
				}
				parentJobID := fmt.Sprintf("%s-%s", spec.App, processName)
				missed := 0
				info, err := h.nomad.PeriodicJobSchedule(parentJobID)
				if err == nil && info != nil && !info.Paused && info.Status != "dead" {
					schedule := strings.TrimSpace(info.Schedule)
					if schedule != "" {
						expr, parseErr := tryParseCronExpr(schedule)
						if parseErr == nil && expr != nil {
							location := cronMetricLocation(spec, proc, info.TimeZone)
							now := time.Now().In(location)
							runs, runsErr := h.nomad.PeriodicChildren(parentJobID)
							if runsErr == nil {
								var lastRunTime time.Time
								for _, run := range runs {
									t, tErr := time.Parse(time.RFC3339, run.StartedAt)
									if tErr != nil {
										continue
									}
									t = t.In(location)
									if t.After(lastRunTime) {
										lastRunTime = t
									}
								}
								reference := lastRunTime
								if reference.IsZero() {
									reference = now.Add(-24 * time.Hour)
								}
								expectedNext := expr.Next(reference)
								if !expectedNext.IsZero() && now.After(expectedNext.Add(5*time.Minute)) {
									missed = 1
								}
							}
						}
					}
				}
				fmt.Fprintf(&b, "norn_cron_missed_runs_total{app=%q,process=%q} %d\n",
					promLabel(spec.App), promLabel(processName), missed)
			}
		}
	}

	writeHostDiskMetrics(&b, ".")

	fmt.Fprintf(&b, "norn_metrics_generated_timestamp_seconds %.0f\n", float64(time.Now().Unix()))
	_, _ = w.Write(b.Bytes())
}

func (h *Handler) PrometheusConfig(w http.ResponseWriter, r *http.Request) {
	manifest, err := h.buildServiceManifest()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	var b bytes.Buffer
	fmt.Fprintf(&b, "global:\n  scrape_interval: 30s\n  evaluation_interval: 30s\n\n")
	fmt.Fprintf(&b, "scrape_configs:\n")
	fmt.Fprintf(&b, "  - job_name: norn\n    metrics_path: /metrics\n    static_configs:\n      - targets: [%q]\n", h.cfg.BindAddr+":"+h.cfg.Port)
	writeAppScrapeConfigs(&b, manifest)
	_, _ = w.Write(b.Bytes())
}

func writeAppScrapeConfigs(b *bytes.Buffer, manifest model.ServiceManifest) {
	for _, svc := range manifest.Services {
		if svc.Metrics == nil || !svc.Metrics.Enabled || len(svc.Metrics.Instances) == 0 {
			continue
		}
		targets := make([]string, 0, len(svc.Metrics.Instances))
		for _, inst := range svc.Metrics.Instances {
			if inst.Address == "" || inst.Port == 0 {
				continue
			}
			targets = append(targets, inst.Address+":"+strconv.Itoa(inst.Port))
		}
		sort.Strings(targets)
		if len(targets) == 0 {
			continue
		}
		fmt.Fprintf(b, "  - job_name: %s\n    metrics_path: %s\n    static_configs:\n      - targets: [%s]\n        labels:\n          app: %s\n          process: %s\n",
			yamlString("app-"+svc.App+"-"+svc.Process),
			yamlString(svc.Metrics.Path),
			quotedList(targets),
			yamlString(svc.App),
			yamlString(svc.Process),
		)
	}
}

func cronMetricLocation(spec *model.InfraSpec, proc model.Process, jobTimezone string) *time.Location {
	timezone := strings.TrimSpace(jobTimezone)
	if timezone == "" {
		timezone = strings.TrimSpace(model.ResolveProcessTimezone(spec, proc))
	}
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func writeMetricHeader(b *bytes.Buffer, name, help, typ string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func promLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

func sortedProcessNames(processes map[string]model.Process) []string {
	names := make([]string, 0, len(processes))
	for name := range processes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func quotedList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, strconv.Quote(value))
	}
	return strings.Join(quoted, ", ")
}

func yamlString(value string) string {
	return strconv.Quote(value)
}

func tryParseCronExpr(schedule string) (expr *cronexpr.Expression, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid cron expression %q: %v", schedule, r)
		}
	}()
	expr = cronexpr.MustParse(schedule)
	return expr, nil
}

func writeHostDiskMetrics(b *bytes.Buffer, path string) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return
	}
	total := float64(stat.Blocks) * float64(stat.Bsize)
	free := float64(stat.Bavail) * float64(stat.Bsize)
	writeMetricHeader(b, "norn_host_disk_total_bytes", "Total bytes on the filesystem containing Norn's working directory.", "gauge")
	writeMetricHeader(b, "norn_host_disk_free_bytes", "Free bytes available on the filesystem containing Norn's working directory.", "gauge")
	fmt.Fprintf(b, "norn_host_disk_total_bytes{path=%q} %.0f\n", promLabel(path), total)
	fmt.Fprintf(b, "norn_host_disk_free_bytes{path=%q} %.0f\n", promLabel(path), free)
}
