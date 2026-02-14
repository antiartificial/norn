# Infraspec Reference

Complete field reference for `infraspec.yaml`.

## Top-Level Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | App identifier (used as Nomad job ID, Consul service prefix) |
| `deploy` | bool | no | Set `false` to disable deploys for this app |
| `repo` | [RepoSpec](#repo) | no | Git repository configuration |
| `build` | [BuildSpec](#build) | no | Docker build configuration |
| `processes` | map[string][Process](#process) | yes | Named process definitions |
| `services` | string[] | no | Legacy service list (v1 compat) |
| `secrets` | string[] | no | Expected secret key names |
| `migrations` | string | no | Path to migrations directory (relative to repo root) |
| `env` | map[string]string | no | Static environment variables |
| `infrastructure` | [Infrastructure](#infrastructure) | no | Backing service declarations |
| `endpoints` | [Endpoint](#endpoints)[] | no | External URL mappings |
| `volumes` | [VolumeSpec](#volumes)[] | no | Host volume mounts |

## Process

Each key in the `processes` map is the process name. The process type is inferred from which fields are set.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `port` | int | — | Listen port (makes this a service process) |
| `command` | string | — | Override the Docker CMD |
| `schedule` | string | — | Cron expression (makes this a periodic batch job) |
| `function` | [FunctionSpec](#functionspec) | — | Function configuration (makes this a batch job) |
| `health` | [HealthSpec](#health) | — | HTTP health check (only for processes with a port) |
| `scaling` | [Scaling](#scaling) | — | Instance count and autoscaling |
| `drain` | [Drain](#drain) | — | Graceful shutdown configuration |
| `resources` | [Resources](#resources) | `cpu: 100, memory: 128` | CPU (MHz) and memory (MB) limits |

## Health

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `path` | string | — | HTTP health check path (e.g. `/health`) |
| `interval` | string | `10s` | Check interval (Go duration) |
| `timeout` | string | `5s` | Check timeout (Go duration) |

## Scaling

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `min` | int | `1` | Minimum instance count |
| `max` | int | — | Maximum instance count (for autoscaling) |
| `per_region` | int | — | Instances per region |
| `auto` | [AutoScale](#autoscale) | — | Autoscaling configuration |

### AutoScale

| Field | Type | Description |
|-------|------|-------------|
| `metric` | string | Scaling metric: `cpu`, `memory`, `kafka_lag`, `custom` |
| `target` | int | Target value for the metric |
| `topic` | string | Kafka topic (required when metric is `kafka_lag`) |

## Drain

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `signal` | string | `SIGTERM` | Signal sent to the process on shutdown |
| `timeout` | string | — | Time to wait after signal before force-killing |

## Resources

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cpu` | int | `100` | CPU allocation in MHz |
| `memory` | int | `128` | Memory allocation in MB |

## FunctionSpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `timeout` | string | — | Maximum execution time (Go duration) |
| `memory` | int | — | Memory override in MB (takes precedence over `resources.memory`) |

## Repo

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `url` | string | — | Git clone URL |
| `branch` | string | `main` | Default branch |
| `autoDeploy` | bool | `false` | Auto-deploy on webhook push |
| `repoWeb` | string | — | Web URL for the repo (used in UI links) |

## Build

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `dockerfile` | string | `Dockerfile` | Path to Dockerfile |
| `test` | string | — | Test command (runs before deploy, fails pipeline on error) |

## Infrastructure

| Field | Type | Description |
|-------|------|-------------|
| `postgres.database` | string | Database name for snapshots, migrations, and `DATABASE_URL` |
| `redis.namespace` | string | Redis key namespace |
| `kafka.topics` | string[] | Kafka topics to declare |
| `nats.streams` | string[] | NATS JetStream stream names |

## Endpoints

| Field | Type | Description |
|-------|------|-------------|
| `url` | string | External hostname (maps to cloudflared ingress rule) |
| `region` | string | Optional region hint |

## Volumes

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | — | Nomad host volume name |
| `mount` | string | — | Mount path inside the container |
| `readOnly` | bool | `false` | Mount as read-only |

## Defaults Summary

| Setting | Default Value |
|---------|---------------|
| `resources.cpu` | 100 MHz |
| `resources.memory` | 128 MB |
| `health.interval` | 10s |
| `health.timeout` | 5s |
| `scaling.min` | 1 |
| `repo.branch` | main |

## Full Example

A real-world infraspec for an app with a web process, background worker, cron job, and database:

```yaml
name: signal-sideband

repo:
  url: git@github.com:antiartificial/signal-sideband.git
  branch: master
  autoDeploy: true
  repoWeb: https://github.com/antiartificial/signal-sideband

build:
  dockerfile: Dockerfile
  test: go test ./...

processes:
  web:
    port: 8080
    command: ./signal-sideband
    health:
      path: /health
    scaling:
      min: 1
    resources:
      cpu: 200
      memory: 256
  poller:
    command: ./signal-sideband --mode=poller
    resources:
      cpu: 100
      memory: 128
  digest:
    schedule: "0 8 * * *"
    command: ./signal-sideband --mode=digest

secrets:
  - DATABASE_URL
  - OPENAI_API_KEY
  - FILTER_GROUP_ID

migrations: ./migrations

env:
  LOG_LEVEL: info
  TZ: America/New_York

infrastructure:
  postgres:
    database: signal_sideband

endpoints:
  - url: signal.example.com

volumes:
  - name: signal-data
    mount: /var/lib/signal-cli
```
