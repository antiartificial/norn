---
layout: home

hero:
  name: Norn
  text: Control plane for self-hosted apps
  tagline: Detect a failing service, rehearse a fix, deploy it, and upgrade the platform without taking your apps down.
  actions:
    - theme: brand
      text: Get Started
      link: /v2/guide/getting-started
    - theme: alt
      text: Why Norn
      link: /v2/guide/why-norn
    - theme: alt
      text: Operations
      link: /v2/operations/operations
    - theme: alt
      text: Upgrades
      link: /v2/operations/upgrading

features:
  - title: Discover
    details: Scans deployable infraspec apps and shows health, endpoints, services, containers, secrets, snapshots, and metrics targets.
  - title: Rehearse
    details: Runs preflight checks through validation, source prep, Docker build, secrets checks, and tests before any runtime mutation.
  - title: Deploy
    details: Queues app deploys with stage checkpoints, snapshots, deploy groups, canary evaluation, auto-rollback, Nomad health gates, and route updates.
  - title: Drain
    details: Uses active operations as the platform drain gate before upgrades, rollbacks, or proxy cutovers.
  - title: Coordinate
    details: Provisions dependencies such as Postgres, Garage buckets, Valkey, Redpanda topics, cron jobs, functions, and Cloudflare endpoints from the app spec.
  - title: Recover
    details: Keeps Beacon events, restart/OOM tracking, notifications, webhook deliveries, deployment history, logs, and rollback releases close to the operator.
---

## The Operator Story

`signal-sideband` was the kind of app that makes a local platform earn its keep: one allocation stayed alive, the other kept restarting, and the fix needed a new image plus careful route and dependency handling. Norn turns that from a pile of tabs into one flow.

![Norn dashboard showing signal-sideband unhealthy with update and dependency badges](/screenshots/dashboard.png)

First, the dashboard shows the real shape of the app: repo state, update availability, health, instances, endpoints, Postgres, KV, object storage, event topics, secrets, and live logs. It is not just "is a container running?" It answers what the service is, what it depends on, and whether the deployed commit is behind the repo.

## Deploy Without Guessing

Before touching runtime state, run a preflight:

```bash
norn preflight signal-sideband HEAD
```

Preflight goes through validation, source prep, Docker build, and tests. When the fix is ready, deploy queues into the same durable worker lane as webhooks and rollbacks:

```bash
norn deploy signal-sideband HEAD
```

![Norn deploy panel showing signal-sideband build, snapshot, migrate, and Nomad drain progress](/screenshots/deploy-panel.png)

The live deploy panel and CLI both stream the pipeline. Norn records detailed stage evidence in `deployment_steps`, while the operations ledger stays compact enough for drain checks, metrics, and incident review.

![Norn deployment history with signal-sideband failure evidence expanded](/screenshots/operations-history.png)

If an API restart interrupts read-only work, Norn can retry it. If a mutable stage has already started, such as snapshot, migration, Nomad submit, health, forge, or cleanup, Norn fails visibly for operator review instead of blindly replaying side effects.

## Upgrade The Platform Around Apps

Norn upgrades itself separately from the apps it runs. The platform lane builds a candidate release, checks it on an alternate port, promotes the release, and restarts only the Norn API process. Nomad, Consul, Postgres, Garage, Redpanda, and hosted app allocations keep running.

```bash
norn operations --active
norn platform preflight HEAD
norn platform upgrade HEAD
norn smoke platform
```

![CLI operations showing active signal-sideband deploy as the platform drain signal](/screenshots/cli-operations.png)

For proxy-fronted hosts, the same release path can switch a managed upstream instead of restarting launchd:

![CLI proxy plan showing old and candidate Norn API ports with rollback path](/screenshots/cli-proxy-plan.png)

That gives the platform a clean answer to "can I upgrade Norn while this app is deploying?" Active operations are the drain source. Finished releases remain visible and rollbackable.

## Apps, Dependencies, And Routes

Each app declares its needs in an `infraspec.yaml`: processes, ports, health checks, tests, services, secrets, migrations, volumes, cron schedules, functions, metrics, and endpoints. Norn reads the spec and coordinates the runtime pieces.

```yaml
name: signal-sideband

repo:
  url: git@github.com:antiartificial/signal-sideband.git
  branch: main
  autoDeploy: true

build:
  dockerfile: Dockerfile
  test: go test ./...

processes:
  web:
    port: 8080
    command: ./sideband serve
    health:
      path: /healthz
    scaling:
      min: 2
    drain:
      signal: SIGTERM
      timeout: 30s
    metrics:
      enabled: true
      path: /metrics

  media-worker:
    command: ./sideband media-worker
    scaling:
      min: 1

infrastructure:
  postgres:
    database: signal_sideband
  kv:
    namespace: signal-sideband
  objectStorage:
    bucket: signal-sideband-media
  kafka:
    topics:
      - signal.received
      - signal.replayed

secrets:
  - SIGNAL_NUMBER
  - DATABASE_URL
  - GARAGE_ACCESS_KEY

endpoints:
  - url: sideband.slopistry.com

volumes:
  - name: signal-sideband-media
    mount: /var/lib/signal-sideband
```

The same model covers web services, workers, cron, and functions:

![Norn cron panel showing scheduled field-harbor digest history and output](/screenshots/cron-panel.png)

![Norn function panel showing archive-thumb invocation history and request body](/screenshots/function-panel.png)

Routes are inspectable before and after deployment:

![CLI endpoints for signal-sideband showing external and Consul routes](/screenshots/cli-endpoints.png)

## Daily Recovery Surfaces

When the fix is not obvious, the operator surfaces stay close:

- Health history shows if the failure is transient, sustained, or tied to a deploy.
- Logs stream from the affected app card.
- Deployment history keeps the failing stage and output.
- Beacon events and alerts make deploy failures, service degradation, cron failures, and recoveries durable.
- Webhook deliveries are replayable as deploys or read-only preflights.
- Observability bundle generation gives Prometheus and Grafana a bounded local setup.

![Norn health panel showing recent signal-sideband health checks](/screenshots/health-panel.png)

![Norn log viewer showing signal-sideband restart and health output](/screenshots/log-viewer.png)

## From The Terminal

```bash
norn status
norn app signal-sideband
norn preflight signal-sideband HEAD
norn deploy signal-sideband HEAD
norn deploy steps <deployment-id>
norn operations --active
norn events --app signal-sideband
norn endpoints signal-sideband
norn platform proxy-plan
norn smoke platform
```

![CLI status showing current v2 apps, health, update availability, and containers](/screenshots/cli-status.png)

Norn is local-first infrastructure with enough memory to be trusted: specs declare intent, workers execute operations, stages leave evidence, upgrades respect active work, and the dashboard/CLI tell the same story.
