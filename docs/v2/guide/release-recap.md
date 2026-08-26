---
title: Norn v2 Release Recap
---

# Norn v2 Release Recap

This recap summarizes the current Norn v2 release line: the Nomad/Consul control plane, the operator-facing dashboard and CLI, fleet planning, production admission, Beacon operational events, and the upgrade posture for local and Linux hosts. `v2.19.0-control` adds crash-safe fleet reconciliation and staged contraction. `v2.20.0-control` adds repository-scoped GitHub App authentication so Norn can open a source-bound fleet pull request and recover or dispatch its protected apply workflow without storing provider credentials or a personal GitHub token. The line retains the backward-compatible v1 control protocol introduced in v2.17.

## Unreleased since v2.20.0-control

On 2026-08-25, the deployed development control build was observed three
commits beyond the `v2.20.0-control` tag, through `067fc8d`. This section records
that dated observation and the untagged changes; it does not assert the host's
current version. The commits do **not** constitute a new release version or tag.

| Area | Surface | Why it matters |
|:-----|:--------|:---------------|
| Durable app recovery | Versioned app snapshot, retention, exact-file restore, standalone migration, and rollback routes; Apps dashboard controls | Operators can queue reviewed database and rollback work, close the client, and later recover the same PostgreSQL-backed receipt |
| Crash-safe intent handling | Request-bound idempotency keys, typed receipts, per-app PostgreSQL advisory locks | Retries after refresh, relaunch, timeout, or API failover recover the original operation and cannot overlap another mutable operation for the same app |
| Snapshot safety | Exact inventory filenames, mandatory safety snapshots on the versioned restore path, transactional restore, private atomic files, path and database-name confinement | Avoids ambiguous restores, partial database state, symlink traversal, overwrite races, and cross-app import paths |
| Recovery serialization | App-operation leases, safe lock contention, one-attempt restore/migration semantics, minimum PostgreSQL pool validation | Mutations remain visible after interruption without assuming an unknown database side effect is replay-safe |
| Control-surface hardening | Explicit-auth protection for metrics and service inventory, configured CORS origins, safer Git credential askpass, constrained app creation and callback/tunnel destinations | Reduces inventory exposure, browser-origin confusion, credential leakage, command injection, and server-side request forgery risk |
| Evidence-based incident cleanup | `norn events reconcile --dry-run`, restart age gate, assurance-proven capacity recovery, full event IDs | Stale warnings are acknowledged only when later events or current substrate health provide deterministic proof; restart reconciliation requires current health and an event at least 15 minutes old, not proof of continuous health |
| Remote upgrade reliability | Platform CLI child-tool path enrichment for direct platform commands over SSH | Thin non-interactive shells can find Homebrew-installed build and security tools without an operator-specific `PATH` prefix |

The detailed durable-operation and database boundaries are documented in
[Operations Ledger](/v2/operations/operations) and
[Snapshots](/v2/operations/snapshots). Release automation should assign a
version only when this line is intentionally tagged.

## v2.20.0-control highlights

| Area | Surface | Why it matters |
|:-----|:--------|:---------------|
| GitHub App authentication | `NORN_FLEET_GITHUB_*`, short-lived installation tokens | Restricts Norn to one fleet repository and the minimum permissions required for each action; provider and state credentials stay in protected GitHub environments |
| Review automation | `norn fleet github status/pr`, Fleet web and native controls | Creates or recovers a deterministic, source-digest-bound pull request from a durable Norn capacity plan |
| Protected apply dispatch | `norn fleet github apply`, `/api/v1/fleet/plans/{planID}/github/dispatch` | Requires the merged PR and its successful main-branch plan artifact before dispatching the protected apply workflow |
| Crash-safe receipts | Durable `fleet.github.*` operations and plan-specific workflow names | Retries after a timeout or restart recover existing GitHub work instead of creating duplicate branches, PRs, or applies |
| Bootstrap incident closure | Revoked credentials, rewritten writable history, push protection | Removes the original cloud-init credentials from branches and tags and documents the remaining GitHub-owned cache cleanup boundary |

## v2.18.0-control highlights

| Area | Surface | Why it matters |
|:-----|:--------|:---------------|
| Fleet GitOps | `/api/v1/fleet/*`, `norn fleet ...`, Fleet dashboard | Validates the private `norn-fleet` desired-state schema, inventories pools, and creates signed, durable planning receipts without giving Norn provider credentials |
| Safe app creation | `POST /api/v1/apps`, dashboard create flow, `deploy: false` default | Creates endpoint or worker drafts with conservative health, scaling, and resource defaults; deployment requires a separate explicit enablement through `PUT /api/v1/apps/{id}/deployment` or the dashboard |
| Regional placement | InfraSpec `regions`, `primaryRegion`, process constraints, node pools | Deploys normal services to all regions by default while keeping cron and singleton work in the declared primary region |
| Regional ingress | Consul-backed Traefik template and readiness weights | Allows multiple endpoint allocations behind a stable regional origin and promotes traffic only after application readiness succeeds |
| Regional rollback | Per-region deployment state and rollback targets | Tracks readiness, traffic weight, and rollback independently for each region |
| Production admission | `NORN_PROFILE=production`, `norn production check` | Fails closed on explicit auth, TLS/ACL/quorum, external PostgreSQL recovery posture, immutable artifacts, strict secrets, and recent drills |
| Supply-chain admission | Digest-pinned images, Cosign source binding, Trivy policy | Keeps publisher credentials outside Norn and verifies the exact registry artifact on deploy and rollback |
| Mutation audit | `/api/v1/audit/mutations`, signed PostgreSQL receipts | Reserves a durable, integrity-protected receipt before production side effects and retains crash-visible incomplete records |
| Recovery evidence | `/api/v1/production/drills`, `norn production drill ...` | Records bounded database restore, artifact rollback, and node failover proof used by the production-readiness gate |
| Linux HA acceptance | OpenTofu, Ansible, fault-injection scripts, toy app | Reproduces three-member Nomad/Consul/PostgreSQL tests for quorum, failover, PITR, immutable rollback, node replacement, and regional ingress |
| Host assurance | Minimum-allocation signals and TLS/ACL-aware probes | Makes post-restart capacity drift visible while keeping repair limited to explicitly required services |
| Host metrics | `/api/v1/host/metrics` | Provides capability-gated CPU and memory observations for native and web operator clients |
| Bootstrap hygiene | Secret-free cloud-init and repository push protection | Removes embedded bootstrap credentials and blocks future supported-provider secrets at push time |

## What Shipped

| Area | Surface | Why it matters |
|:-----|:--------|:---------------|
| Runtime platform | Nomad, Consul, local Docker builds, cloudflared | Replaces the v1 Kubernetes path with a smaller local control plane suited to self-hosted apps |
| Host recovery and assurance | `norn host install/status/doctor/recover/assure`, persistent state, launchd supervisor and assurance agent | Restores the local control plane, repairs explicitly required apps and routes, probes real entrypoints, and supports bounded ingestion catch-up |
| App model | Multi-process `infraspec.yaml` | Lets one app define web, worker, cron, and function processes without splitting deployment ownership |
| Deploy pipeline | Clone, source admission, build, artifact admission, test, snapshot, migrate, regional submit, healthy, forge, cleanup | Makes deploys repeatable, policy-gated, and auditable from the CLI, API, and dashboard |
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
| Advisory tuner | `norn tune`, `/api/tuning/recommendations`, `tuning` process policy | Produces bounded CPU, memory, and scale recommendations from live Nomad usage, declared tuning signals, and hosted-service access patterns |
| Access-pattern observations | `norn access patterns`, `/api/access/patterns`, Cloudflare GraphQL/Logpush ingestion | Records hourly access aggregates so idle candidates and active windows can be based on real public traffic |
| Wake gateway | `/api/wake-gateway/{host}/*` | Records live gateway accesses, scales mapped service groups from zero to one, waits for readiness, and reverse-proxies the request |
| Event notifications | `norn notifications`, `/api/notifications/channels` | Pushes Beacon events to Discord webhooks, ntfy topics, and Pushover with severity filtering and per-channel configuration |
| Auto-rollback | `deployPolicy.autoRollback` in infraspec | Automatically rolls back to the last successful deployment when the healthy step fails, with Beacon event and saga trail |
| Snapshot export | `norn snapshots export/remote/import`, `snapshots.exportBucket` | Archives database snapshots to S3-compatible object storage and imports them back for disaster recovery |
| Deploy groups | `norn deploy-groups`, `deploy-groups/*.yaml` | Defines ordered multi-app deployment sequences with optional wait-ready gates between apps |
| Canary deploys | `canary` process config, `norn canary/promote` | Deploys canary allocations first, evaluates health after a configurable window, then promotes or fails automatically |
| Webhook notifications | `webhook` notification provider | Sends Beacon events as JSON to any HTTP endpoint with optional bearer auth, useful for wiring to custom notification services |
| Cron missed-run detection | `cron.missed_run` Beacon events, `norn_cron_missed_runs_total` metric | Detects when scheduled processes fail to dispatch by comparing cron expressions against actual run history |
| Secrets migration | `norn secrets migrate [app]` | Generates SOPS commands and optionally modifies infraspecs to move plaintext env secrets to encrypted secrets |
| Deploy checkpoint enrichment | `kind` field on `deployment_steps` | Records whether each deploy step is read-only or mutable to support future stage-level resume |
| Dashboard notifications | Platform tab notifications section | Manage notification channels from the dashboard with test, remove, and add-channel form |
| Dashboard deploy groups | Platform tab deploy groups section | View and trigger deploy groups from the dashboard |
| Dashboard canary status | Per-app canary badge in AppCard | Shows active canary deployments with promote action inline |
| Dashboard remote snapshots | Remote tab in SnapshotsPanel | Export, list, and import remote snapshots from the dashboard |
| Network truth dashboard | Clickable Service Exposure in Platform tab | Expands to show the full service manifest with per-service reachability, endpoint scope, and instance scope |
| Temporary access grants | `norn access grant/grants/revoke`, `/api/access/grants` | Time-limited IP-based API access bypass with TTL expiry, dashboard management, and CLI CRUD |
| Evaluator readiness | `norn contextdb evaluator-readiness`, `/api/ops/contextdb/evaluator-readiness` | Synthesized per-namespace readiness assessment for provider-backed evaluator rollout with blockers |
| Dashboard evaluator readiness | ContextDB ops panel evaluator section | Shows evaluator mode, key status, and readiness per namespace with ready/blocked badges |
| Dashboard access grants | Platform tab access grants section | View active grants, create new grants with IP/TTL/note, and revoke grants inline |
| Event correlation | `correlationKey` in beacon event metadata | Groups related events into incident arcs so consumers can track flare-up to resolution |
| Correlated events query | `GET /api/events/correlated`, `norn events correlated` | Retrieves chronological event timeline for a correlation key |
| Scoped access tokens | `POST /api/access/tokens`, `norn access token`, dashboard form | Time-limited bearer credentials with explicit read, event, exec, platform, and host scopes; never placed in URLs |
| Native API contract | `GET /api/v1/openapi.yaml`, `GET /api/v1/capabilities` | OpenAPI 3.1 types and negotiated feature flags for generated Swift and automation clients |
| Device enrollment | `/api/v1/enrollments`, `/api/v1/devices`, `/api/v1/auth/rotate`, `/api/v1/auth/revoke` | Pairing-code onboarding, atomic rotation, revocation, and device inventory without pasting the control-plane token |
| Event continuity | `/api/v1/events/info`, filtered `/api/v1/events` | Retention bounds, cursor gap detection, subscriptions, and opt-in heartbeats for deterministic reconnects |
| Typed operation lifecycle | v1 operation get/cancel endpoints | Queued cancellation and versioned platform, host, or app receipts with stable problem codes |
| Native exec sessions | `norn.exec/v1`, step-up challenges, exec audit records | P-256 proof of possession, one-time app-bound authorization, typed frames, expiry, cancellation, and durable audit metadata |
| Incident timeline links | `norn events show`, dashboard event detail | Shows correlation key and incident timeline command/link for events in an incident arc |
| Allocation lifecycle | `lifecycle` field on allocations, `allocationSummary` on app status | Separates active from retained allocations so CLI and dashboard show live capacity |
| Auto-ack on resolution | Beacon emit path auto-acknowledges correlated events | Keeps `norn events` focused on active incidents by clearing resolved warning/critical events |
| Notification bootstrap | `norn notifications bootstrap`, `POST /api/notifications/channels/bootstrap` | Auto-discovers vigil-gateway and creates a default webhook notification channel |
| Event dedup suppression | Beacon emit-level dedupeKey check with 1h window | Prevents event storms from repeated watcher detection after API restarts |
| Active incidents view | `GET /api/events/active`, `norn events active` | Shows unresolved incident groups collapsed by correlation key |
| Operator confidence release | `/api/operator/*`, `norn operator *`, `/api/incidents/action` | Unifies incident lifecycle, cron overview, wake targets, deploy confidence, snapshot readiness, secret-safe auth hints, and mobile-ready actions |
| Secrets migration fix | `norn secrets migrate --apply` field matching fix | Fixes `env.KEY` field matching so plaintext env secrets are correctly identified for migration |
| Secrets hygiene push | Infraspec declarations for ft-trove, its-alive-api, mail-indexer, mail-mcp | Resolves undeclared encrypted secrets warnings from platform ops |
| Upgrade path | direct and queued platform commands | Upgrades Norn API, CLI, UI, host agent, and managed scripts without stopping hosted apps; queued upgrades survive the API restart |

## Operator Impact

Norn v2 is now useful as a real local operations surface rather than just a deploy script. The dashboard and CLI both answer the daily questions: what is hosted, what is healthy, what changed recently, which services are reachable, and whether the platform is safe to upgrade.

The biggest practical change is that Norn can host long-lived background work beside web processes. ContextDB is the proving case: its web API and review worker run as separate processes, while Norn exposes worker health, evaluator readiness, dry-run policy posture, audit events, and recent worker runs.

The macOS host runtime lane now closes the reboot gap around that work. Nomad
and Consul state can move out of `/tmp`, managed LaunchAgents recover the
dependency chain, the supervisor adapts advertise addresses after DHCP changes,
and selected cron processes can run a bounded catch-up once Norn is healthy.
The assurance stage then checks required services on IPv4, safely deploys
missing apps or restarts unhealthy apps, reconciles public Cloudflare and
private Tailscale routes, and probes the real user-facing HTTP entrypoints. A
periodic LaunchAgent repeats the same idempotent pass and sends correlated
failure/recovery events through Beacon.

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

The advisory tuner now has a traffic-signal path instead of relying only on allocation resource usage. Operators can declare process-level `tuning` policy, inspect current recommendations with `norn tune`, and import hosted-service access observations through `norn access observe`, Cloudflare GraphQL sync, the Cloudflare Logpush receiver, or the live wake gateway. GraphQL syncs are chunked into day-sized requests, clamped to the available analytics lookback, and idempotent for hourly aggregate buckets so retries do not inflate counts.

The wake gateway is the first live request-path mechanism for scale-from-idle. It maps public service hostnames from the service manifest, records the access as `wake-gateway`, scales the corresponding Nomad task group to one instance when no passing instance exists, waits for Consul readiness, and then proxies the original request to the service. It supports both host-based routing for cloudflared/proxy ingress and an explicit `/api/wake-gateway/{host}` path for local testing.

The operator-confidence release ties the incident, cron, deploy, snapshot, wake,
auth, and mobile action surfaces together. `norn operator inbox` is the entry
point for "what needs attention right now?" while the narrower subcommands show
cron schedules, wake targets, deploy confidence, restore readiness, secret-safe
auth patterns, and mobile-ready action descriptors. Incident group actions now
resolve through Beacon so sinks receive a real recovery event instead of only a
local acknowledgement.

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
- `norn host status`
- `norn host doctor`
- `norn host recover`
- `norn host assure`
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
- `norn tune`
- `norn access patterns --window 7d --idle-after 7d`
- `norn access cloudflare status`
- `norn access cloudflare sync --window 14d`
- `norn operator inbox`
- `norn operator cron`
- `norn operator deploy-confidence`
- `norn operator snapshot-readiness`
- `norn operator auth-hints`

## Compatibility

Norn v2 is the active development path and is intentionally separate from the v1 Kubernetes documentation. The v2 upgrade path replaces only the Norn API binary, CLI binary, and built UI assets when Norn is installed as `com.norn.api`. It does not require stopping Nomad, Consul, Postgres, or hosted allocations.

The v2.17 control protocol supports strict explicit authentication without
breaking a Cloudflare Access-protected browser UI, enforces trusted transport
for pairing, propagates exec revocation through durable PostgreSQL state across
API replicas, and retires the legacy raw-key JWT signature after the configured
deadline. InfraSpec commands and service-registry callbacks remain intentional
trusted-code boundaries and continue to be reported by security scanners for
operator review.

## Read Next

- [Getting Started](/v2/guide/getting-started)
- [Next Steps](/v2/guide/next-steps)
- [Deploy Pipeline](/v2/architecture/deploy-pipeline)
- [Object Storage](/v2/infrastructure/object-storage)
- [Observability](/v2/infrastructure/observability)
- [Beacon Events](/v2/operations/beacon)
- [CLI Commands](/v2/cli/commands)
- [Upgrading Norn](/v2/operations/upgrading)
- [Host Recovery](/v2/operations/host-recovery)
