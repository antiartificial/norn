# Infraspec Reference

Complete field reference for `infraspec.yaml`.

## Top-Level Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | App identifier (used as Nomad job ID, Consul service prefix) |
| `deploy` | bool | no | Deployment gate. Defaults to `false`; it must be explicitly enabled before deploy/recovery workflows include the app |
| `regions` | map[string][RegionTarget](#regions) | no | Regional Nomad targets. Omit for the backward-compatible local `global`/`dc1` target |
| `primaryRegion` | string | conditionally | Required when a multi-region spec has scheduled or singleton processes; otherwise defaults to the first region name in lexical order |
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
| `snapshots` | [SnapshotPolicy](#snapshotpolicy) | no | Snapshot retention defaults |
| `deployPolicy` | [DeployPolicy](#deploypolicy) | no | Deploy safety policy such as auto-rollback |
| `placement` | [Placement](#placement) | no | Logical norn-fleet node pool for all processes unless overridden |

## Process

Each key in the `processes` map is the process name. The process type is inferred from which fields are set.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `port` | int | — | Listen port (makes this a service process) |
| `hostPort` | int | — | Optional fixed host port for platform ingress; incompatible with multiple allocations in a region |
| `command` | string | — | Override the Docker CMD |
| `schedule` | string | — | Cron expression (makes this a periodic batch job) |
| `function` | [FunctionSpec](#functionspec) | — | Function configuration (makes this a batch job) |
| `health` | [HealthSpec](#health) | — | HTTP health check (only for processes with a port) |
| `metrics` | [MetricsSpec](#metricsspec) | — | Prometheus scrape endpoint for this process |
| `scaling` | [Scaling](#scaling) | — | Instance count and autoscaling |
| `drain` | [Drain](#drain) | — | Graceful shutdown configuration |
| `resources` | [Resources](#resources) | `cpu: 100, memory: 128` | CPU (MHz) and memory (MB) limits |
| `tuning` | [TuningPolicy](#tuningpolicy) | — | Advisory resource tuning policy and signal declarations |
| `canary` | [CanaryConfig](#canaryconfig) | — | Canary allocation count and evaluation window |
| `regions` | string[] | all regions | Explicit process placement. Omit for all regions, except scheduled/singleton processes default to primary |
| `singleton` | bool | `false` | Pin unscheduled singleton work to the primary region unless `regions` is explicit |
| `nomadVariables` | [Nomad variable files](#nomad-variable-files) | — | ACL-restricted job-owned Nomad variable values rendered only to private files |

### Nomad variable files

`nomadVariables` is an opt-in process-scoped transport for values that must
never enter `task.Env` or command arguments. Norn derives the source strictly
as `nomad/jobs/<generated-job-id>`; InfraSpec cannot select another variable
path or submit template text. The generated Nomad templates are always
allocation-relative `secrets/<destination>`, owner-only mode `0400`, restart
on change, and fail on a missing key. Pipeline-resolved environment values are
withheld from that process.

```yaml
processes:
  web:
    env:
      MYSQL_DSN_FILE: "${NOMAD_SECRETS_DIR}/mysql-dsn"
    nomadVariables:
      uid: 65532
      gid: 65532
      files:
        - key: MYSQL_DSN
          destination: mysql-dsn
```

`destination` is exactly one safe filename (no slash, traversal, or absolute
path); keys are uppercase variable names; `uid` and `gid` are required positive
Unix identities. Use file-path environment variables only, and make the image
read its secrets from those files.

## Placement

Application specs reference a logical pool and never a provider VM size, region slug, or cloud resource ID:

```yaml
placement:
  nodePool: app

processes:
  web:
    port: 8080
```

`nodePool` must be DNS-compatible. Standalone InfraSpec validation checks its syntax; passing a `fleetDocument` to `POST /api/v1/validate/infraspec`, or using `norn validate --file ... --fleet ...`, also requires the pool to exist in the pinned `norn.dev/fleet/v1` document. Nomad receives the value through its job-level node-pool field, so all processes in an application share the same pool. Split materially different workloads into separate InfraSpecs.

## Regions

```yaml
primaryRegion: ord
regions:
  ord:
    nomadRegion: us-central
    datacenters: [ord1]
    trafficWeight: 70
  iad:
    nomadRegion: us-east
    datacenters: [iad1]
    trafficWeight: 30

processes:
  web:                    # runs in ord and iad
    port: 8080
    scaling: { min: 3 }
  local-consumer:
    regions: [ord]        # explicit constraint
  leader:
    singleton: true       # defaults to ord
  digest:
    schedule: "0 8 * * *" # defaults to ord
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `nomadRegion` | string | region map key | Nomad federation region |
| `datacenters` | string[] | `[dc1]` | Eligible Nomad datacenters in that region |
| `trafficWeight` | int | `100` | Desired global traffic weight, activated only after regional readiness passes; set explicitly to `0` for a ready standby region |

Normal processes deploy to every declared region. `process.regions` is a
constraint, not an opt-in list required on every process. Scheduled and
singleton processes are the exception: they run only in `primaryRegion` unless
they explicitly declare another placement.

## Health

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `path` | string | — | HTTP health check path (e.g. `/health`) |
| `interval` | string | `10s` | Check interval (Go duration) |
| `timeout` | string | `5s` | Check timeout (Go duration) |

## MetricsSpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Register this process as a Prometheus scrape target |
| `path` | string | `/metrics` | Metrics path |
| `port` | int | process `port` | Internal metrics port when separate from the main process port |

## Scaling

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `min` | int | `1` | Minimum instance count |
| `max` | int | — | Maximum instance count (for autoscaling) |
| `per_region` | int | — | Instance count in each eligible region; overrides `min` for regional translation |
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

## TuningPolicy

`tuning` declares how Norn should interpret resource signals for a process. The first implementation is advisory: `norn tune` and `/api/tuning/recommendations` report current signals and recommended `resources` or `scaling` changes, but they do not mutate Nomad jobs.

```yaml
processes:
  web:
    resources:
      cpu: 25
      memory: 256
    tuning:
      mode: advisory
      cooldown: 6h
      profiles:
        quiet:
          cpu: 25
          memory: 256
          scale: 1
        normal:
          cpu: 50
          memory: 512
          scale: 1
      limits:
        min:
          cpu: 25
          memory: 128
          scale: 1
        max:
          cpu: 500
          memory: 2048
          scale: 3
      signals:
        - name: live-rss
          source: nomad
          metric: memory_rss
          aggregate: current
        - name: memory-p95
          source: prometheus
          metric: container_memory_working_set_bytes
          window: 24h
          aggregate: p95
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `mode` | string | `advisory` | `advisory` reports recommendations; `auto` is reserved for future guarded application |
| `cooldown` | duration | — | Minimum interval between automated changes when auto mode is implemented |
| `profiles` | map[string][TuningProfile](#tuningprofile) | — | Named target CPU, memory, and scale profiles such as `quiet` or `busy` |
| `limits` | [TuningLimits](#tuninglimits) | — | Minimum and maximum recommendation bounds |
| `signals` | [TuningSignal](#tuningsignal)[] | built-in Nomad live signals | Signal declarations used to explain recommendations |

### TuningProfile

| Field | Type | Description |
|-------|------|-------------|
| `cpu` | int | CPU allocation in MHz |
| `memory` | int | Memory allocation in MB |
| `scale` | int | Desired instance count |

### TuningLimits

| Field | Type | Description |
|-------|------|-------------|
| `min` | [TuningProfile](#tuningprofile) | Lower bound for recommended CPU, memory, and scale |
| `max` | [TuningProfile](#tuningprofile) | Upper bound for recommended CPU, memory, and scale |

### TuningSignal

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Human-readable signal name |
| `source` | string | `nomad`, `prometheus`, or `app` |
| `metric` | string | Signal metric, such as `memory_rss`, `memory_max`, or `cpu_percent` |
| `window` | duration | Lookback window for historical sources |
| `aggregate` | string | Aggregation such as `current`, `max`, or `p95` |

## CanaryConfig

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `count` | int | — | Number of canary allocations to start before full promotion |
| `evaluateAfter` | string | — | Duration to wait before evaluating canary health, such as `2m` |

When a process declares canary settings, Norn submits the Nomad deployment with canary allocations, waits through the normal health gate, then evaluates allocation health after `evaluateAfter`. Operators can inspect and promote with `norn canary <app>` and `norn promote <app>`.

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
| `image` | string | — | Externally published OCI image. Production requires an immutable `image@sha256:...` whose signature is bound to the resolved Git commit. |

## Infrastructure

| Field | Type | Description |
|-------|------|-------------|
| `postgres.database` | string | Database name for snapshots, migrations, and `DATABASE_URL` |
| `redis.namespace` | string | Redis key namespace |
| `kafka.topics` | string[] | Kafka topics to declare |
| `nats.streams` | string[] | NATS JetStream stream names |
| `objectStorage.provider` | string | S3-compatible provider hint; defaults to `garage` |
| `objectStorage.buckets` | ObjectStorageBucket[] | Buckets to provision and expose to the app |

### ObjectStorageBucket

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | — | DNS-compatible bucket name |
| `access` | string | `readWrite` | `readOnly`, `readWrite`, or `owner` |
| `public` | bool | `false` | Reserved for future public exposure policy |
| `prefix` | string | — | Optional object key prefix exposed as `S3_PREFIX...` |
| `env` | string | derived from bucket name | Env alias for `S3_BUCKET_<ENV>` |

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

## SnapshotPolicy

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `keep` | int | `3` | Newest local snapshots to keep when retention runs without `--keep` |
| `preRestore` | bool | `false` | Create a safety snapshot before restore when the API or CLI does not override the restore request |
| `retentionEnabled` | bool | `false` | Reserved flag for scheduled retention automation |
| `exportBucket` | string | — | S3-compatible bucket for `norn snapshots export/remote/import` |

## DeployPolicy

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `autoRollback` | bool | `true` | Queue rollback to the last successful deployment when the deploy health gate fails |

Because `autoRollback` defaults to enabled, omit `deployPolicy` for normal apps. Set `autoRollback: false` when a failed health gate should stop for manual operator review.

## Defaults Summary

| Setting | Default Value |
|---------|---------------|
| `resources.cpu` | 100 MHz |
| `resources.memory` | 128 MB |
| `health.interval` | 10s |
| `health.timeout` | 5s |
| `scaling.min` | 1 |
| `repo.branch` | main |
| `snapshots.keep` | 3 |
| `deployPolicy.autoRollback` | true |
| `deploy` | false |

## Full Example

A real-world infraspec for an app with a web process, background worker, cron job, and database:

```yaml
name: signal-sideband
deploy: true

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
    metrics:
      enabled: true
      path: /metrics
    scaling:
      min: 1
    resources:
      cpu: 200
      memory: 256
    canary:
      count: 1
      evaluateAfter: 2m
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
  objectStorage:
    provider: garage
    buckets:
      - name: signal-sideband-attachments
        access: readWrite
        env: ATTACHMENTS

snapshots:
  keep: 5
  exportBucket: signal-sideband-snapshots

deployPolicy:
  autoRollback: true

endpoints:
  - url: signal.example.com

volumes:
  - name: signal-data
    mount: /var/lib/signal-cli
```
