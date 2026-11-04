# Observability

Norn exposes Prometheus-compatible metrics and OpenTelemetry traces/logs. A local Prometheus/Grafana pair remains available for development; production Fleet telemetry should be collected by a node-local Grafana Alloy agent and forwarded to the shared Grafana Cloud stack. Norn owns instrumentation and signal semantics; the collector owns secure scraping, environment labels, buffering, and export.

## Local Retention

For the Mac mini, start with a small Prometheus TSDB budget:

```text
--storage.tsdb.retention.time=30d
--storage.tsdb.retention.size=8GB
```

This is enough for Norn, node/container metrics, and a small number of app metrics when labels are controlled. Use `10GB` if you want more slack. Avoid high-cardinality labels such as request IDs, commit SHAs, user IDs, object keys, or full URLs.

## Norn Metrics

Norn exposes Prometheus text at both:

```text
/metrics
/api/metrics
```

In development mode these endpoints remain available for local scraping. When
`NORN_REQUIRE_EXPLICIT_AUTH=true` (required by the production profile), metrics
and `/api/services/manifest` require an authenticated bearer or Cloudflare
Access principal because they reveal host, process, and service inventory. Give
Prometheus a scoped token through a protected `bearer_token_file`; do not place
the token directly in a checked-in scrape configuration.

The endpoint includes low-cardinality control-plane metrics:

| Metric | Meaning |
|--------|---------|
| `norn_apps_total` | Discovered deployable apps |
| `norn_app_info` | App metadata marker |
| `norn_process_info` | Process metadata marker, including whether app metrics are enabled |
| `norn_app_health` | App health derived from the service manifest |
| `norn_deploys_total` | Deployments by app and status |
| `norn_deploy_duration_seconds_count` | Completed deploy count by app and status |
| `norn_deploy_duration_seconds_sum` | Total deploy duration by app and status |
| `norn_deploy_last_started_timestamp_seconds` | Last deployment start timestamp |
| `norn_object_storage_buckets` | Declared object-storage buckets by app/provider |
| `norn_snapshots_total` | Local snapshots by app/database |
| `norn_snapshot_over_limit_total` | Snapshot retention pressure by app/database |
| `norn_access_events_recent_total` | Recent in-memory API access events by status bucket |
| `norn_service_status` | Live service status by app/process/service/status |
| `norn_beacon_events_total` | Beacon event count by type and severity |
| `norn_beacon_last_occurred_timestamp_seconds` | Last Beacon event time by type and severity |
| `norn_host_disk_total_bytes` | Host disk capacity visible to the API process |
| `norn_host_disk_free_bytes` | Host disk free space visible to the API process |

Prometheus scrape traffic is excluded from Norn's recent access event buffer so it does not dominate `norn ops platform`.

## Generated Scrape Config

Norn exposes a generated Prometheus scrape config at:

```text
/api/observability/prometheus.yml
```

It always includes Norn itself and adds app scrape targets for processes that declare `metrics.enabled: true` and have live service instances.

Example Prometheus config:

```yaml
global:
  scrape_interval: 30s
  evaluation_interval: 30s

scrape_config_files:
  - /etc/prometheus/norn-generated.yml

remote_write: []
```

The repo includes a starter config at `v2/dev/prometheus.yml`. Refresh generated Norn/app targets with:

```bash
curl -fsS http://127.0.0.1:8800/api/observability/prometheus.yml > v2/dev/norn-prometheus.generated.yml
```

Keep `remote_write` empty for local-only development. Fleet Alloy is the production export path so the monitoring backend remains available when Norn or a cluster is unhealthy.

## Grafana Cloud and OpenTelemetry

On Fleet nodes, configure Norn to send traces to the local Alloy OTLP/HTTP
receiver. Keep the Grafana Cloud write credential on Alloy, not in Norn's
environment or generated scrape configuration. The generic OTLP endpoint is a
base URL: Norn appends `/v1/traces` and `/v1/logs`. Signal-specific
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` and
`OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` values are used as complete endpoints.
The etcd Fleet runtime exposes `/metrics` only when its dedicated
`NORN_METRICS_TOKEN_FILE` is configured. That mode-0600 bearer is accepted on
`/metrics` alone and does not grant the broader `api:read` scope.

Set bounded resource attributes on the Norn service, for example:

```text
OTEL_SERVICE_NAME=norn-api
OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=staging,service.namespace=norn.infrastructure,norn.cluster=tk-staging-nyc3
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
NORN_OTEL_ENABLED=true
NORN_OTEL_LOGS=false
```

Use `deployment.environment.name=staging` or `production` consistently. Add
equivalent labels to Prometheus scrape targets in Alloy; OpenTelemetry resource
attributes do not automatically label independently scraped Prometheus series.
Do not export raw access logs until request-URI query values are redacted.
Exclude request IDs, user IDs, allocation IDs that rotate, URLs, object keys,
and secret values from metric labels.

Alloy should scrape Norn and the local Nomad, Consul, etcd, and host endpoints
over loopback or the private network using dedicated read-only credentials and
the existing TLS material. Alert on missing/down targets as well as unhealthy
cluster signals so target filtering cannot hide failures. Keep the Alloy WAL
bounded; it is not a substitute for
Norn's signed mutation and recovery evidence.

## Observability Bundle

Norn also exposes a value-safe starter bundle for local Prometheus, Grafana, and cAdvisor:

```text
/api/observability/bundle
/api/observability/alerts.yml
```

The CLI can inspect it or write the files to disk:

```bash
norn observability bundle
norn observability bundle --out ./norn-observability
norn observability install
```

The bundle contains:

| File | Purpose |
|------|---------|
| `prometheus/prometheus.yml` | Norn plus app scrape config with rule loading |
| `prometheus/rules/norn-alerts.yml` | Prometheus alert rules for Norn service health, deploy failures, cron failures, snapshot pressure, and low disk headroom |
| `grafana/provisioning/datasources/norn-prometheus.json` | Starter Grafana datasource |
| `grafana/dashboards/norn-platform.json` | Starter platform dashboard |
| `services/*.infraspec.yaml` | Norn service specs for Prometheus, Grafana, and cAdvisor |

`norn observability install` writes managed app directories under `NORN_APPS_DIR`:

| App | Purpose |
|-----|---------|
| `norn-prometheus` | Scrapes Norn, app metrics services, and alert rules with 30-day/8GB retention |
| `norn-grafana` | Provisions the Norn Prometheus datasource and starter platform dashboard |
| `norn-cadvisor` | Provides container-level metrics for Prometheus when host policy allows it |

The generated apps are normal Norn apps. Review ports, container privileges, and host policy before deploying:

```bash
norn validate norn-prometheus
norn preflight norn-prometheus HEAD
norn deploy norn-prometheus HEAD
```

When the API binds to loopback, the managed Prometheus config targets `host.docker.internal:<port>` so Prometheus can scrape the host-local Norn API from inside Docker. Override this with `NORN_OBSERVABILITY_NORN_TARGET` if the Docker host path differs.

## App Metrics

For long-running HTTP servers and workers, expose a Prometheus-compatible endpoint and declare it in `infraspec.yaml`:

```yaml
processes:
  web:
    port: 8080
    health:
      path: /health
    metrics:
      enabled: true
      path: /metrics

  worker:
    command: ./worker
    metrics:
      enabled: true
      port: 9090
      path: /metrics
```

If `metrics.port` is omitted, Norn assumes the metrics endpoint is on the process port. If `metrics.port` is set, Norn maps it as an internal dynamic Nomad port and registers a companion Consul service named:

```text
<app>-<process>-metrics
```

Use app-level metrics for domain signals:

| Type | Examples |
|------|----------|
| Counter | jobs processed, uploads completed, failures by reason |
| Histogram | HTTP latency, job duration, inference latency |
| Gauge | queue depth, active workers, backlog age |

## Container Metrics

Use cAdvisor or an equivalent container collector for container-level signals:

| Signal | Source |
|--------|--------|
| CPU and memory | cAdvisor / Nomad allocation stats |
| Container network ingress/egress bytes | cAdvisor |
| Filesystem usage | cAdvisor / node exporter |
| Restarts and allocation state | Nomad / Norn |

Container metrics answer "is this process using resources?" App metrics answer "what useful work is it doing?"

## Batch Jobs

For cron and short-lived jobs, prefer Norn-recorded outcomes first: deployment steps, saga events, function execution history, cron history, and Beacon events. Use Pushgateway only when a batch job produces metrics that would disappear before Prometheus can scrape them.

## Restart and OOM Tracking

Norn tracks task-level restarts and OOM kills from Nomad allocation state. The allocation watcher inspects `TaskStates` every 60 seconds and emits Beacon events when restarts are detected:

| Event Type | Severity | Trigger |
|------------|----------|---------|
| `nomad.task.oom_killed` | critical | A task was killed by the OOM killer |
| `nomad.task.restarted` | warning | A task restarted for any reason |

Each event includes the task name, task group, allocation ID, restart count, and last event message from Nomad.

The `/metrics` endpoint exposes corresponding gauges:

| Metric | Labels | Description |
|--------|--------|-------------|
| `norn_task_restarts_total` | app, process, task | Current restart count for running tasks |
| `norn_task_oom_kills_total` | app, process, task | Tasks OOM killed in current allocations |

The observability bundle includes Prometheus alert rules for these signals:

- **NornTaskOOMRisk** — fires when a task has been OOM killed
- **NornTaskRestartLoop** — fires when a task has restarted more than 3 times

## Cron Missed-Run Detection

Norn compares cron schedule expressions against actual periodic job dispatch history. When a scheduled run does not appear within 5 minutes of its expected time, a `cron.missed_run` Beacon event fires with critical severity.

The `/metrics` endpoint exposes:

| Metric | Labels | Description |
|--------|--------|-------------|
| `norn_cron_missed_runs_total` | app, process | 1 when a cron process missed its expected run, 0 otherwise |

The alert catalogue includes a `cron-missed-run` rule for this signal. Paused and stopped periodic jobs are excluded from detection.

## Resource Right-sizing

Norn can compare declared resource limits from `infraspec.yaml` against live Nomad allocation stats:

```bash
norn resources
```

The output classifies each running process:

| Status | Meaning |
|--------|---------|
| `at risk` | Peak memory exceeds 80% of declared limit |
| `right sized` | Memory usage is between 30% and 80% of limit |
| `overprovisioned` | Peak memory is below 30% of declared limit |

The API endpoint is `GET /api/resources/suggestions`.

Use this to catch underprovisioned apps before they OOM and overprovisioned apps that waste capacity. Adjust `resources.memory` in the app's `infraspec.yaml` and redeploy.

## Event Notifications

Beacon events can push notifications to external channels. Norn supports three providers:

| Provider | Delivery | Configuration |
|----------|----------|---------------|
| Discord | Webhook with color-coded severity embeds | Webhook URL |
| ntfy | HTTP POST with priority headers | Topic URL |
| Pushover | Form POST with priority mapping | App token + user key |

Each channel can filter by severity (`info`, `warning`, `critical`). Channels without a severity filter receive all events.

Manage channels from the CLI:

```bash
norn notifications list
norn notifications add discord ops https://discord.com/api/webhooks/...
norn notifications add ntfy alerts https://ntfy.sh/norn-alerts
norn notifications add pushover mobile https://api.pushover.net/1/messages.json --token <token> --user-key <key>
norn notifications test <channel-id>
norn notifications remove <channel-id>
```

Or through the API:

```text
GET    /api/notifications/channels
POST   /api/notifications/channels
POST   /api/notifications/channels/{id}/test
DELETE /api/notifications/channels/{id}
```

Notifications dispatch asynchronously after each Beacon event is emitted. A failing channel does not block event recording or other channels.

## External Plumbing

The local-first path is:

```text
Norn /metrics
App /metrics
cAdvisor /metrics
Prometheus local TSDB, 30d/8GB
Grafana local dashboards
```

For local Prometheus, external support can be added with:

```yaml
remote_write:
  - url: https://example.remote.write/api/v1/write
```

Keep remote-write disabled by default. If enabled, send only coarse platform health, deploy failures, and heartbeat-style metrics.
