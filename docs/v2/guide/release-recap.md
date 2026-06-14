---
title: Norn v2 Release Recap
---

# Norn v2 Release Recap

This recap summarizes the current Norn v2 release line: the Nomad/Consul control plane, the operator-facing dashboard and CLI, the ContextDB worker deployment path, Beacon operational events, and the upgrade posture for the local LaunchAgent install.

## What Shipped

| Area | Surface | Why it matters |
|:-----|:--------|:---------------|
| Runtime platform | Nomad, Consul, local Docker builds, cloudflared | Replaces the v1 Kubernetes path with a smaller local control plane suited to self-hosted apps |
| App model | Multi-process `infraspec.yaml` | Lets one app define web, worker, cron, and function processes without splitting deployment ownership |
| Deploy pipeline | Clone, build, test, snapshot, migrate, submit, healthy, forge, cleanup | Makes deploys repeatable and auditable from the CLI, API, and dashboard |
| Preflight pipeline | `norn preflight`, `norn check`, `/api/apps/{id}/preflight` | Rehearses validation, source prep, Docker build, and tests before runtime mutation |
| Service discovery | `/api/services/manifest` and `norn services` | Gives operators and agents a compact view of hosted services, process reachability, endpoint scope, and health |
| Operations dashboard | Platform and ContextDB ops panels | Surfaces service exposure, deploy provenance, snapshot retention, access events, OTEL/Grafana status, and ContextDB worker posture |
| CLI operations | `norn ops platform`, `norn ops contextdb`, `norn smoke contextdb` | Turns platform health and ContextDB worker readiness into repeatable terminal checks |
| ContextDB worker hosting | Separate `web` and `review-worker` processes | Lets ContextDB run centralized claim review as a durable Norn-managed worker instead of embedding worker logic per application |
| Worker policy checks | `norn contextdb policy`, `review`, `worker-runs`, `evaluator-smoke`, `audit` | Shows dry-run state, evaluator mode, provider readiness, queue posture, run summaries, and feedback audit events before mutation rollout |
| Secrets hygiene | SOPS/age, secret status checks, plaintext env warnings | Keeps secret values out of API/UI responses while making missing or unsafe secret wiring visible |
| Garage object storage | `infrastructure.objectStorage`, managed buckets, app-scoped S3 env | Lets one local Garage service host many app buckets without making each app manually own storage credentials |
| Snapshot operations | Snapshot listing, restore confirmation, pre-restore safety snapshots, retention reporting | Adds safer database restore flows and platform-level visibility into snapshot drift and over-limit state |
| Beacon events | `/api/events`, event detail, ack, snooze, reopen, signed sink forwarding | Records Norn-observed operational events and lets operators manage event state without changing the event payload |
| Alert catalogue | `/api/alerts/rules`, `norn alerts` | Defines the built-in event-to-alert contract for deploy, service, cron, and recovery signals |
| Cron eventing | `job.triggered`, `job.paused`, `job.resumed`, `job.schedule_updated` | Makes operator-level scheduled-work changes visible as durable operational events |
| Deploy eventing | `deploy.succeeded`, `deploy.failed` | Turns deployment outcomes into durable events that can feed notification and incident workflows |
| Observability | OTEL logs/traces, `/metrics`, generated Prometheus scrape config, observability bundle, managed observability app install | Keeps local logs useful while enabling bounded local Prometheus/Grafana metrics |
| Operations ledger | `norn operations`, `/api/operations`, operation metrics | Records app preflights, deploys, and rollbacks as compact durable rows for drain gates and operator summaries |
| Durable operation queue | Postgres-backed operation claims, API worker, retries | Moves app deploy and preflight requests out of in-memory request goroutines and into a first-class worker lane |
| Deploy checkpoints | `deployment_steps`, deploy/rollback stage markers | Records durable stage evidence and safely requeues interrupted deploys only before mutable stages |
| Webhook inbox | `norn webhooks`, replay, preflight replay, webhook metrics | Makes webhook auto-deploy deliveries inspectable and replayable without scraping logs |
| Platform release surface | Platform tab releases, `/api/platform/releases`, `norn platform releases`, rollback | Makes the installed Norn release history visible from API, CLI, and dashboard |
| Beacon event visibility | `norn events`, `norn events show/ack/snooze/open`, Platform tab actions | Makes Norn-emitted operational events visible and operable from terminal and dashboard |
| Platform smoke | `norn smoke platform`, `norn platform smoke`, `norn platform env` | Checks health, drain, release marker, and events after upgrades, including SOPS-backed auth env shells |
| Runtime watcher | Beacon events for failed/lost/unhealthy allocations, Consul health transitions, and cron success/failure/hung runs | Turns service, allocation, and scheduled-work transitions into durable operational events |
| Proxy cutover lane | `norn platform proxy-plan/status/render/switch`, `norn platform upgrade --proxy` | Supports stable ingress and private old/new API release ports on proxy-fronted hosts |
| Assurance surfaces | `norn observability install`, `norn secrets migrate-plan`, `norn validate --strict-secrets`, `norn network` | Packages local monitoring, secret drift gates, and network truth into first-class operator commands |
| Restart and OOM tracking | Task-level restart/OOM Beacon events, `norn_task_restarts_total` and `norn_task_oom_kills_total` metrics, Prometheus alert rules | Detects task restarts and OOM kills from Nomad allocation state with cause classification |
| Resource right-sizing | `norn resources`, `/api/resources/suggestions` | Compares declared infraspec resource limits against live Nomad allocation stats to flag overprovisioned and at-risk apps |
| Event notifications | `norn notifications`, `/api/notifications/channels` | Pushes Beacon events to Discord webhooks, ntfy topics, and Pushover with severity filtering and per-channel configuration |
| Auto-rollback | `deployPolicy.autoRollback` in infraspec | Automatically rolls back to the last successful deployment when the healthy step fails, with Beacon event and saga trail |
| Snapshot export | `norn snapshots export/remote/import`, `snapshots.exportBucket` | Archives database snapshots to S3-compatible object storage and imports them back for disaster recovery |
| Deploy groups | `norn deploy-groups`, `deploy-groups/*.yaml` | Defines ordered multi-app deployment sequences with optional wait-ready gates between apps |
| Canary deploys | `canary` process config, `norn canary/promote` | Deploys canary allocations first, evaluates health after a configurable window, then promotes or fails automatically |
| Upgrade path | `norn platform preflight`, `upgrade`, `releases`, `rollback` | Upgrades Norn API, CLI, and built UI without stopping Nomad, Consul, Postgres, or hosted apps |

## Operator Impact

Norn v2 is now useful as a real local operations surface rather than just a deploy script. The dashboard and CLI both answer the daily questions: what is hosted, what is healthy, what changed recently, which services are reachable, and whether the platform is safe to upgrade.

The biggest practical change is that Norn can host long-lived background work beside web processes. ContextDB is the proving case: its web API and review worker run as separate processes, while Norn exposes worker health, evaluator readiness, dry-run policy posture, audit events, and recent worker runs.

Beacon adds the first durable event surface for notification-oriented operations. Norn now records events it can observe directly, such as deploy outcomes, cron control actions, manual test events, Nomad allocation transitions, Consul health transitions, and cron run outcomes. Those events can stay local for audit/debugging or be forwarded to a signed sink. Norn also supports local operator state: events can be acknowledged, snoozed, and reopened from the CLI and Platform tab. Beacon events can now push notifications to Discord webhooks, ntfy topics, and Pushover channels with per-channel severity filtering.

Auto-rollback is now built into the deploy pipeline. When a deployment's healthy step fails and the app has not explicitly disabled auto-rollback, Norn queues a rollback to the last successful deployment, emits a `deploy.auto_rollback` Beacon event, and records the sequence in the saga trail.

Canary deploys let operators roll out new versions to a subset of allocations before promoting to the full group. The canary count and evaluation window are declared in `infraspec.yaml`; Norn inserts a canary evaluation step between healthy and forge, waits the configured duration, checks allocation health, and either promotes or fails the Nomad deployment automatically. Manual promotion is available via `norn promote <app>` or the API.

Deploy groups define ordered multi-app deployment sequences in YAML files under `deploy-groups/`. Each group lists apps with optional `waitReady` gates. `norn deploy-group <name> [ref]` deploys each app in order, optionally waiting for health before continuing to the next.

Snapshot export adds off-site durability for database snapshots. Apps can declare an `exportBucket` in their snapshot policy; Norn auto-exports snapshots to S3-compatible storage after successful `pg_dump`. Manual export, listing, and import are available through the CLI and API.

Object storage now follows the same local-infra posture as the rest of v2: Garage can run as a platform-scoped service, while app specs declare buckets and Norn provisions them during deploy. Apps receive S3-compatible env vars, including Garage path-style flags, without hardcoding bucket credentials into plaintext specs.

Metrics now follow the same local-first model: Norn exposes control-plane counters at `/metrics`, apps can opt into process-level scrape targets with `metrics.enabled`, and `/api/observability/prometheus.yml` generates a Prometheus config for Norn plus live app targets. `/api/observability/bundle` and `norn observability bundle --out <dir>` package Prometheus config, alert rules, Grafana provisioning, a starter dashboard, and service specs for a 30-day/8GB local setup. `norn observability install` writes `norn-prometheus`, `norn-grafana`, and `norn-cadvisor` app directories into `NORN_APPS_DIR` so they can be validated, preflighted, and deployed as normal Norn apps.

Secret migration is now easier to do deliberately. `norn secrets migrate-plan [app]` reports plaintext secret-like env keys, declared state, encrypted state, and recommended action without printing values. The platform rollup and UI include the count so teams can see when insecure env drift is still present. `norn validate --strict-secrets` and `NORN_STRICT_SECRETS=true` turn those findings into an opt-in validation/preflight gate.

Network truth is now easier to read from the terminal. `norn network` summarizes service exposure, endpoint scope, instance scope, validation hints, and guidance for local, tailnet, and public network modes.

Snapshot restores can now create a safety snapshot immediately before destructive restore. The restore receipt includes the pre-restore filename, and snapshot restore/retention actions emit Beacon events.

The operations ledger gives platform upgrades a real drain source. Deploys and preflights are now queued in control-plane Postgres and claimed by the API worker with leases, attempts, retry timing, and saga links. Platform upgrades can fail, wait, or force based on active rows. Webhook deliveries now get their own inbox, which makes ignored branches, signature failures, unmatched repositories, auto-deploy saga ids, replay, and preflight replay visible through the API and CLI.

The durable worker lane now records deploy and rollback stage checkpoints. Read-only preflight jobs can retry safely. App deploy jobs can requeue after an API restart only when no mutable stage has started; once snapshot, migration, submit, health, forge, or cleanup has started, interruption fails visibly for operator review instead of blindly replaying runtime mutation.

Proxy-backed platform upgrades are available for hosts that intentionally run Norn behind the managed Caddy upstream. `norn platform upgrade --proxy` keeps old and new APIs side by side on private ports, switches the upstream, then stops the previous proxy-managed API after postflight succeeds.

## Verification

The current release line has been exercised with:

- `cd v2/ui && pnpm build`
- `cd v2 && make build`
- `go test ./...` in `v2/api`
- `go test ./...` in `v2/cli`
- `norn preflight <app> HEAD`
- `go test ./...` in `api`
- `cd docs && pnpm build`
- `launchctl kickstart -k gui/$(id -u)/com.norn.api`
- `norn version`
- `norn ops platform`
- `norn operations`
- `norn events`
- `norn events show <event-id>`
- `norn alerts`
- `norn observability bundle`
- `norn observability install`
- `norn secrets migrate-plan`
- `norn validate --strict-secrets`
- `norn network`
- `norn smoke platform`
- `norn platform smoke`
- `norn webhooks`
- `norn webhooks replay <delivery-id> --preflight`
- `norn platform releases`
- `norn platform proxy-plan`
- `norn platform proxy-status`
- `norn services`
- `norn status`
- `norn smoke contextdb`
- `curl -sf http://127.0.0.1:8800/api/health`
- `curl -sf -X POST http://127.0.0.1:8800/api/events/test -H 'Content-Type: application/json' -d '{"app":"field-harbor"}'`
- `norn notifications list`
- `norn canary <app>`
- `norn deploy-groups`
- `norn snapshots export <app>`
- `norn snapshots remote <app>`

## Compatibility

Norn v2 is the active development path and is intentionally separate from the v1 Kubernetes documentation. The v2 upgrade path replaces only the Norn API binary, CLI binary, and built UI assets when Norn is installed as `com.norn.api`. It does not require stopping Nomad, Consul, Postgres, or hosted allocations.

## Read Next

- [Getting Started](/v2/guide/getting-started)
- [Next Steps](/v2/guide/next-steps)
- [Deploy Pipeline](/v2/architecture/deploy-pipeline)
- [Object Storage](/v2/infrastructure/object-storage)
- [Observability](/v2/infrastructure/observability)
- [Beacon Events](/v2/operations/beacon)
- [CLI Commands](/v2/cli/commands)
- [Upgrading Norn](/v2/operations/upgrading)
