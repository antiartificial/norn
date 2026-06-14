# Next Steps

This page tracks the current development direction for Norn v2. It is intentionally practical: each item should either make the control plane a more reliable source of truth, reduce deployment ambiguity, or improve day-to-day operations for apps hosted under Norn.

## Current Baseline

Norn v2 is the active development path. It uses Nomad for scheduling, Consul for service health, local Docker builds for images, SOPS/age for secrets, and cloudflared for external routing.

The current working feature set includes:

- App discovery from `NORN_APPS_DIR` via `infraspec.yaml`
- Multi-process app specs for web, worker, cron, and function processes
- Deploy pipeline with clone, build, test, snapshot, migrate, submit, health, forge, and cleanup steps
- Nomad service jobs, periodic jobs, and batch function jobs
- Consul-backed health and service registration
- Cloudflared endpoint forge, teardown, and endpoint toggles
- CLI workflows for status, app detail, deploy, logs, health, stats, secrets, snapshots, cron, invoke, saga, validate, forge, and endpoints
- Saga event log and WebSocket deploy progress
- Beacon operational events for cron, deploys, manual test events, and signed event sinks
- Durable Postgres-backed operation queue for app deploy and preflight jobs
- Durable deploy/rollback stage checkpoints with safe pre-mutable deploy requeue after API restart
- Webhook delivery inbox with replay and preflight replay
- Platform release history and rollback from CLI, API, and dashboard
- Beacon event CLI and Platform tab visibility
- Beacon event detail, acknowledgement, snooze, and reopen state
- Built-in alert rule catalogue for deploy, service, cron, and recovery events
- Observability bundle for local Prometheus, alert rules, Grafana provisioning, and starter service specs
- Managed observability app installation for Prometheus, Grafana, and cAdvisor
- Platform smoke checks for health, drain, release marker, and recent warning/critical events
- Authenticated platform smoke through the encrypted API runtime env
- Runtime watcher events for failed, lost, and unhealthy allocations, task restarts, OOM kills, Consul health transitions, and cron run outcomes
- Task-level restart and OOM tracking with Beacon events and Prometheus metrics
- Resource right-sizing suggestions comparing declared limits against live Nomad allocation stats
- Proxy cutover plan and optional managed proxy config/upstream commands for no-blip API upgrades
- Proxy-backed platform upgrade mode for hosts that intentionally run Norn behind the managed Caddy upstream
- Basic local snapshot listing and restore
- Pre-restore safety snapshots for destructive restores
- Value-safe secret migration planning
- Opt-in strict plaintext-secret validation gate
- Port conflict detection before Nomad submit
- Event notifications to Discord, ntfy, and Pushover with per-channel severity filtering
- Auto-rollback on deploy health failure with Beacon event and saga trail
- Snapshot export/import to S3-compatible object storage with optional auto-export after deploy
- Deploy groups for ordered multi-app deployment sequences with wait-ready gates
- Canary deploys with configurable count, evaluation window, and automatic promote/fail
- Webhook notification provider for arbitrary HTTP endpoints
- Schedule-aware missed-run detection for cron processes with Beacon events and Prometheus metric
- Automated secrets migration CLI with dry-run and apply modes
- Deploy checkpoint enrichment with read-only/mutable step classification
- Dashboard views for notification channels, deploy groups, canary status, and remote snapshots

For a compact summary of the current release line, see the [Norn v2 Release Recap](/v2/guide/release-recap).

## Immediate Norn Items

### Service Manifest

The service manifest is the next discovery surface for agents, dashboards, and external tooling. It should become the compact answer to "what is hosted, where is it, and is it healthy?"

Current state:

- `/api/services/manifest` exposes process type, status, health path, app endpoints, instances, and reachability metadata.
- The manifest includes a small contract block plus structured reachability fields for endpoint scope, instance scope, exposure, and routability.
- `norn services` renders endpoint/instance reachability in a `REACH` column.
- `norn services manifest` is the raw JSON contract for agents and MCP/tool discovery.

### Deploy Provenance

Norn can deploy from a local working tree fallback, but local dirty builds currently look like the last git commit. That can make the runtime look cleaner than it really is.

Planned work:

- Detect dirty local trees during deploy.
- Record dirty state and changed-file summary in the deployment record and saga.
- Use an image tag or metadata suffix that makes local dirty builds obvious. Local dirty builds now append `-dirty` to the image tag's commit segment.
- Show dirty/deployed provenance in `norn app`, `norn status`, `norn ops platform`, and the UI. `norn app` now surfaces the latest deployment status, image tag, resolved commit, and saga id; `norn status` shows latest image, commit, source kind, and dirty state.
- Preserve the exact commit SHA and build timestamp for every deployed image.

### Networking Truth

Norn dev mode binds Nomad container ports to `127.0.0.1`, while some app endpoints may refer to Tailscale or public hostnames. The docs and manifest need to make the difference visible.

Current state:

- The service manifest exposes `networkMode` and classifies endpoint and instance scope as `local`, `private`, `public`, or `none`.
- `norn services` renders combined reachability such as `local`, `public/private`, or `internal/local`.
- `norn health` shows the configured network mode.
- `norn app <id>` shows network mode and per-process reachability from the service manifest.
- `norn validate` warns when endpoint hosts look mismatched for the active network mode.
- `norn network` summarizes exposure, endpoint scope, instance scope, validation hints, and mode-specific guidance.

Planned work:

- Add clickable network drill-downs to the dashboard.

### Secrets Hygiene

Several app specs still carry sensitive DSNs or credentials in plain `env` blocks. Norn should make the secure path easier than the unsafe path.

Current state:

- `norn validate` warns when top-level or process-level `env` blocks contain secret-like DSNs, passwords, tokens, API keys, client secrets, or provider keys.
- App and process `env` values are parsed for validation but omitted from API JSON responses.
- `norn secrets status [app]` compares declared secret keys, encrypted keys, and plaintext env warnings without printing secret values.
- `norn secrets migrate-plan [app]` turns plaintext findings into a value-safe migration checklist with declared/encrypted status and recommended actions.
- `norn validate --strict-secrets` and `NORN_STRICT_SECRETS=true` make plaintext secret-like env findings fail validation/preflight.

Planned work:

- Move the remaining sensitive DSNs and API keys into `secrets.enc.yaml`.
- Improve deploy-time secret resolution reporting.
- Show secret names, never values, in UI and CLI app detail views.

### Snapshot Semantics

Snapshots exist, but the current implementation is closer to local operational support than production-grade backup management.

Current state:

- Snapshots are stored locally under `snapshots/` in the Norn API working directory.
- Restore requires explicit CLI confirmation with `--yes`; the API requires `confirm=true`.
- Restore responses include database, snapshot, commit, and restored-at receipt data.
- Restore can create a fresh safety snapshot first with `--pre-restore` or `preRestore=true`.
- Snapshot listing parses provenance from filenames, including source commit and RFC3339 created time even when database names include underscores.
- Snapshot retention uses `snapshots.keep` from `infraspec.yaml` when a command does not pass `--keep`.
- `norn ops platform` reports per-app snapshot counts, keep policy, and over-limit totals.
- Snapshot restore and retention actions emit Beacon events.

- Snapshot export to S3-compatible storage is available via `norn snapshots export <app>`, `norn snapshots remote <app>`, and `norn snapshots import <app> <key>`.
- Apps can declare `snapshots.exportBucket` to auto-export after deploy snapshots.
- The API exposes `POST /api/apps/{id}/snapshots/export`, `GET /api/apps/{id}/snapshots/remote`, and `POST /api/apps/{id}/snapshots/import`.

Planned work:

- Show snapshot provenance in app detail and deploy history.

### Observability And Access

The next operational layer is to show who is reaching Norn-managed services and make temporary access explicit.

Current state:

- Norn records Beacon events for deploy successes, deploy failures, cron control actions, and manual test events.
- Norn records Beacon events when Nomad allocations transition to failed, lost, or unhealthy, and when cron child runs succeed, fail, are lost, or appear hung.
- `norn events` and the Platform tab surface recent Beacon events.
- `/api/events` lists durable Beacon events with app, type, severity, limit, and offset filters.
- Beacon can forward signed event JSON to an external sink using `NORN_BEACON_*` environment variables.
- Norn records recent API access events in memory after auth middleware runs.
- `norn access [--limit N]` shows method, path, status, client IP, Cloudflare Access metadata, and duration without request bodies or authorization headers.
- `norn ops platform` and the UI Platform tab summarize recent access, service exposure, OTEL/Grafana configuration, dirty deployments, secret hygiene, and snapshot retention.
- `/metrics` and `/api/metrics` expose Prometheus-compatible Norn control-plane metrics.
- App processes can declare `metrics.enabled: true`; Norn registers companion metrics services and includes live scrape targets in `/api/observability/prometheus.yml`.
- `/api/observability/bundle`, `/api/observability/alerts.yml`, and `norn observability bundle --out <dir>` package Prometheus config, alert rules, Grafana provisioning, a starter dashboard, and starter Prometheus/Grafana/cAdvisor service specs.
- `POST /api/observability/services/install` and `norn observability install` write generated `norn-prometheus`, `norn-grafana`, and `norn-cadvisor` app directories into `NORN_APPS_DIR`.
- `norn ops platform` and the UI Platform tab show observability bundle availability, retention target, and secret migration item counts.

- Beacon events can push notifications to configured channels (Discord webhooks, ntfy topics, Pushover) with per-channel severity filtering.
- `norn notifications list|add|remove|test` manages notification channels from the CLI.
- `/api/notifications/channels` provides CRUD for notification channels.

Planned work:

- Deploy the generated observability apps on hosts that have accepted the local port and container policy choices.
- Add downstream incident-tool links for acknowledged/snoozed Beacon events.
- Extend runtime watchers with schedule-aware missed-run detection.
- Add temporary access grants, such as expiring JWT links or expiring IP allowlist entries.

### Platform Upgrade Continuity

Current state:

- App deploy and preflight requests enqueue durable operation rows and return saga ids immediately.
- App rollback requests enqueue durable operation rows and return saga ids immediately.
- The API worker claims queued operations with a lease and records attempts, next attempt, and last error.
- Deploy and rollback stages are recorded in `deployment_steps`.
- Interrupted deploys can requeue automatically only before mutable stages begin.
- Read-only preflights retry safely through the worker.
- Platform candidate APIs start with recovery and workers disabled so side-by-side preflight cannot claim live work.
- `norn platform upgrade --proxy` can boot the candidate API on a private port, switch the managed Caddy upstream, and stop the previous proxy-managed API pid after postflight.
- `/api/platform/releases`, `norn platform releases`, and the Platform tab list installed releases.
- Rollbacks can be started from the CLI or Platform tab through the same platform lane script.

Planned work:

- Add deeper stage-level resume data before enabling automatic retry after snapshot, migration, submit, or route mutation.
- Add a host setup command that moves an existing LaunchAgent install to proxy-fronted private API ports.
- Queue platform preflight and upgrade jobs themselves once the worker supports platform-scoped operations.

## ContextDB Items

ContextDB is now a useful proving ground for Norn because it has both a web process and a background review worker. The big ContextDB items split into deployment hygiene, worker operations, and product-level context quality.

### Deployment Hygiene

Current state:

- ContextDB is managed by Norn as separate `web` and `review-worker` processes.
- The service manifest reports ContextDB web as a local routable service and the review worker as an internal local worker.
- ContextDB ships a Norn smoke script that checks web health, worker health, write/retrieve behavior, review queue setup, and an in-allocation worker dry run.
- `norn smoke contextdb` promotes that smoke flow into a first-class Norn command.
- ContextDB validates cleanly under Norn's plaintext-secret checks: `CONTEXTDB_DSN` is present as a Norn secret and is not duplicated into plain `env`.

Planned work:

- Decide whether ContextDB should expose only local Norn endpoints, a Tailscale endpoint, a cloudflared endpoint, or some combination.

### Worker Operations

Current state:

- The deployed Hermes policy runs the review worker in dry-run mode with the keyless `rules` evaluator, `stale-only` policy preset, and conservative allowed actions.
- One-shot worker dry runs work through `norn exec contextdb --process review-worker -- /contextdb worker review ... --report`.
- ContextDB records durable review worker summaries through its own API surface.
- `norn contextdb review` summarizes queue counts and recent worker runs for a namespace.
- `norn contextdb policy` reads the live worker policy report, including dry-run state, policy preset, evaluator, provider key readiness, allowed actions, mutation status, warnings, and errors.
- `norn contextdb evaluator-smoke` runs the deployed evaluator smoke in the review-worker allocation without mutating claims.
- `norn contextdb audit` lists recent append-only feedback events for mutation review.
- `norn contextdb worker-runs <namespace>` surfaces those summaries in Norn CLI table or JSON output.
- `norn contextdb worker-runs <namespace> --decisions` shows dry-run decision details, including action, applied flag, node id, and reason.
- `norn ops contextdb` and the UI Ops tab combine app health, worker policy, provider rollout gates, queue size, worker runs, audit events, snapshots, secrets, and deployments.

Planned work:

- Decide when a namespace is allowed to move from dry-run to conservative execution using the live policy report as the gate.
- Promote the provider gate into an explicit guarded rollout action once a non-rules evaluator is configured.
- Add worker logs and per-decision drill-down to the UI.

### Evaluators

Current state:

- `rules` is the no-key default evaluator.
- `webhook`, `openai`, `anthropic`, `xai`, and provider-backed `hybrid` evaluators are implemented and explicitly configured.
- `--smoke-evaluator` checks evaluator configuration without opening the database or mutating claims.
- Evaluator smoke reports include the rules baseline decision and whether the configured evaluator disagrees with that baseline.
- Worker reports include the effective evaluator for each namespace.

Planned work:

- Store provider evaluator keys as Norn secrets when enabling provider-backed policies.
- Add a Norn-level provider evaluator smoke/runbook before allowing provider-backed policies in production.
- Add UI/CLI affordances that clearly distinguish rules, webhook, and provider-backed worker decisions.

### Context Quality

- Add recurring review of low-confidence, stale, contradicted, and source-anomaly claims.
- Add durable receipts for validate, refute, stale, prune, and manual review decisions.
- Add source quarantine and trust repair workflows for repeatedly refuted sources.
- Add dashboards for review queue size, stale claim count, source trust drops, and worker actions.
- Add namespace-specific review policies for Hermes agent memory versus other ContextDB users.

### Agent Integration

Current state:

- Claim execution remains centralized in the ContextDB worker rather than per application.
- Agents can discover ContextDB web and worker health through the Norn service manifest.

Planned work:

- Make Hermes use ContextDB review APIs for queue inspection, claim validation, refutation, and pruning.
- Add a "human review required" lane for decisions the worker will not execute automatically.

## Suggested Order

1. Finish moving app plaintext secrets into `secrets.enc.yaml`, using `norn validate` as the guardrail.
2. Improve networking truth in app detail and manifest output.
3. Surface ContextDB worker run summaries in Norn metrics/UI and add review metrics.
4. Wire Hermes to ContextDB review APIs for queue inspection, claim validation, refutation, and pruning.
5. Add provider-backed evaluator secrets and smoke checks only when a namespace is ready to leave the keyless rules path.
6. Build the access/traffic dashboard.
