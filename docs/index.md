---
layout: home

hero:
  name: Norn
  text: Control plane for self-hosted apps
  tagline: Rehearse and recover app changes, plan fleet capacity, and upgrade the platform without taking workloads down.
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
      text: Fleet GitOps
      link: /v2/infrastructure/fleet
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
  - title: Plan the fleet
    details: Validates a private norn-fleet repository, creates signed capacity plans, opens deterministic review pull requests, and follows enrollment, readiness, and drain evidence without holding provider credentials.
  - title: Recover
    details: Queues exact-file snapshot restores, pruning, schema changes, and rollback with durable receipts, and restores the host runtime plus required apps, routes, and user-facing probes after restart.
---

## The Operator Story

`signal-sideband` was the kind of app that makes a local platform earn its keep: one allocation stayed alive, the other kept restarting, and the fix needed a new image plus careful route and dependency handling. Norn turns that from a pile of tabs into one flow.

![Norn Overview workspace showing fleet health, active incidents, running operations, recent deploys, and platform status](/screenshots/dashboard.png)

First, the Overview workspace puts fleet health, correlated incidents, active operations, recent deploys, and platform status in one place. From the Apps workspace, each service opens into its own overview, logs, deploys, snapshots, cron, functions, and shell tabs. It is not just "is a container running?" It answers what the service is, what it depends on, and what needs operator attention.

The Fleet workspace keeps infrastructure changes in that same operator story
without moving cloud authority into Norn. It validates the private
`norn-fleet` desired state, records a source-bound capacity plan, opens or
recovers the deterministic GitHub review, and follows apply, enrollment,
readiness, and drain checkpoints. Provider and Terraform state credentials stay
inside the protected infrastructure runner.

![Norn Fleet workspace showing node-pool capacity, durable plans, GitHub review, and reconciliation state](/screenshots/fleet.png)

![Norn fleet CLI showing desired node-pool capacity and replacement strategy](/screenshots/cli-fleet.png)

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

![Norn Deploys workspace showing current and earlier signal-sideband deployment records](/screenshots/operations-history.png)

If an API restart interrupts read-only work, Norn can retry it. If a mutable stage has already started, such as snapshot, migration, Nomad submit, health, forge, or cleanup, Norn fails visibly for operator review instead of blindly replaying side effects.

The same durable lane covers recovery outside a deploy. From an app's snapshot
surface, an operator can create a snapshot, preview and confirm pruning, restore
an exact inventory filename with a mandatory safety snapshot, run the declared
schema migration independently, or roll back to the previous successful
deployment. Request-bound idempotency keys reconnect browser and native clients
to the original PostgreSQL receipt after a refresh, relaunch, or ambiguous
response instead of duplicating the mutation.

![Norn app data recovery workspace showing snapshot inventory, retention preview, schema changes, and rollback controls](/screenshots/data-recovery.png)

![Norn snapshots CLI showing inventory filenames and unique compact UTC timestamps for restore selection](/screenshots/cli-snapshots.png)

## Upgrade The Platform Around Apps

Norn upgrades itself separately from the apps it runs. The platform lane builds a candidate release, checks it on an alternate port, promotes the release, and restarts only the Norn API process. Nomad, Consul, Postgres, Garage, Redpanda, and hosted app allocations keep running.

```bash
norn operations --active
release_sha=$(git rev-parse origin/main)
norn platform preflight "$release_sha"
norn platform upgrade "$release_sha"
norn smoke platform
```

![CLI operations showing active signal-sideband deploy as the platform drain signal](/screenshots/cli-operations.png)

For proxy-fronted hosts, the same release path can switch a managed upstream instead of restarting launchd:

![CLI proxy plan showing old and candidate Norn API ports with rollback path](/screenshots/cli-proxy-plan.png)

That gives the platform a clean answer to "can I upgrade Norn while this app is deploying?" Active operations are the drain source. Finished releases remain visible and rollbackable.

## Recover And Assure The Host

On macOS, the host lane restores Docker, Consul, Nomad, and the Norn API in
dependency order. Recovery then runs bounded catch-ups and an explicit
assurance policy. The same idempotent pass repeats every five minutes by
default, so a restored Nomad database is not mistaken for a working public
service.

```bash
norn host recover
norn host assure
norn host status
```

Assurance can deploy an explicitly required missing app, restart an unhealthy
one, reconcile Cloudflare and Tailscale routes, and probe the entrypoints users
actually reach. Persistent failures and recovery become correlated Beacon
events. See [Host Recovery and Assurance](/v2/operations/host-recovery) for the
policy reference and safety boundaries.

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
  - url: https://sideband.example.com

volumes:
  - name: signal-sideband-media
    mount: /var/lib/signal-sideband
```

The same model covers web services, workers, cron, and functions:

![Norn app Cron tab showing the field-harbor digest schedule, controls, and recent runs](/screenshots/cron-panel.png)

![Norn app Functions tab showing an archive-thumb request body and execution history](/screenshots/function-panel.png)

Routes are inspectable before and after deployment:

![CLI endpoints for signal-sideband showing external and Consul routes](/screenshots/cli-endpoints.png)

## Daily Recovery Surfaces

When the fix is not obvious, the operator surfaces stay close:

- The app overview combines process, allocation, infrastructure, service, secret, and idle-analysis context.
- Logs stream from the affected app's Logs tab.
- Deployment history keeps the failing stage and output.
- Snapshot and schema controls expose reviewed pruning, exact-file restore,
  standalone migration, rollback, and their durable receipts.
- Fleet planning links desired capacity, protected GitHub review, apply,
  enrollment, readiness, and drain evidence.
- Beacon events and alerts make deploy failures, service degradation, cron failures, and recoveries durable.
- Webhook deliveries are replayable as deploys or read-only preflights.
- Observability bundle generation gives Prometheus and Grafana a bounded local setup.
- The host runtime lane persists Nomad and Consul state, orders launchd recovery,
  adapts to DHCP address changes, and can trigger bounded cron catch-ups.

![Norn signal-sideband app overview showing processes, allocations, infrastructure, services, secrets, and idle analysis](/screenshots/health-panel.png)

![Norn signal-sideband Logs tab showing restart and health output](/screenshots/log-viewer.png)

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
