# Observability

Norn keeps observability local by default. The control plane exposes Prometheus-compatible metrics, app specs can declare scrapeable process metrics, and a local Prometheus/Grafana pair can run with bounded 30-day retention.

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
| `norn_access_events_recent_total` | Recent in-memory API access events by status bucket |

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

Keep `remote_write` empty for local-only operation. Add a remote backend later only for a curated low-cardinality subset if you want offsite alerting.

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

## External Plumbing

The local-first path is:

```text
Norn /metrics
App /metrics
cAdvisor /metrics
Prometheus local TSDB, 30d/8GB
Grafana local dashboards
```

External support can be added later with:

```yaml
remote_write:
  - url: https://example.remote.write/api/v1/write
```

Keep remote-write disabled by default. If enabled, send only coarse platform health, deploy failures, and heartbeat-style metrics.
