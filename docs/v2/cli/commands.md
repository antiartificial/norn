# CLI Commands

Full reference for every `norn` command.

## status

List all apps with their health, current commit, endpoints, and services.

```bash
norn status
```

Displays a table of all discovered apps with live health indicators, latest deployment image, and resolved commit.

## app

Detailed view of a single app including processes, object storage buckets, recent deployments, and infrastructure.
The output includes service-manifest reachability, network mode, endpoints, and instances when available.

```bash
norn app <id>
```

| Argument | Description |
|----------|-------------|
| `id` | App name (from infraspec `name` field) |

## deploy

Deploy an app at a specific git ref with live pipeline progress.

```bash
norn deploy <app> [ref]
norn deploy steps <deployment-id>
```

| Argument | Default | Description |
|----------|---------|-------------|
| `app` | — | App name |
| `ref` | `HEAD` | Git ref (commit SHA, branch, tag) |

Connects to the WebSocket and renders a real-time progress display showing each pipeline step. The saga ID is printed on completion for later inspection.

`norn deploy steps <deployment-id>` shows durable deploy or rollback checkpoints from `deployment_steps`.

## preflight

Run a deploy rehearsal without changing Norn runtime state.

```bash
norn preflight <app> [ref]
norn check <app> [ref]
```

| Argument | Default | Description |
|----------|---------|-------------|
| `app` | - | App name |
| `ref` | `HEAD` | Git ref (commit SHA, branch, tag) |

Preflight validates the infraspec, prepares the deploy source tree, checks the configured Dockerfile and declared encrypted secrets, runs a local Docker build, and runs `build.test` when configured. It does not create a deployment record, snapshot a database, run migrations, submit Nomad jobs, wait for health, or update cloudflared.

Warnings are streamed inline for conditions that are worth seeing before deploy, such as `repo.autoDeploy: false` or Go module `replace` directives that point outside the prepared build context.

## platform

Manage the Norn control plane itself with a release-oriented upgrade lane.

```bash
norn platform preflight [ref]
norn platform upgrade [ref]
norn platform upgrade [ref] --proxy
norn platform releases
norn platform rollback <sha-prefix>
norn platform smoke
norn platform env -- <command> [args...]
norn platform proxy-plan
norn platform proxy-status
norn platform proxy-render
norn platform proxy-switch <port|host:port>
```

`norn platform preflight` builds Norn from an isolated git worktree into `$HOME/norn/releases/<sha>`, starts the candidate API on `127.0.0.1:18800`, and verifies health/version without restarting the active API.

`norn platform upgrade` performs the same preflight, promotes `$HOME/norn/current`, installs compatibility binaries into `$HOME/go/bin`, restarts only `com.norn.api`, and rolls back to the previous release if postflight health fails.

`norn platform upgrade --proxy` uses the managed proxy lane. It boots the candidate API on the private candidate port, switches the managed Caddy upstream, keeps the new API alive behind the proxy, and stops the previous proxy-managed API pid after postflight succeeds. Use it only after the host is intentionally proxy-fronted and `NORN_PROXY_RELOAD=true` is configured.

`norn platform releases` lists local release directories. `norn platform rollback <sha-prefix>` promotes a previous local release and runs the same postflight health check.

`norn platform smoke` runs `norn smoke platform` with the API runtime environment loaded from the encrypted SOPS JSON env file.

`norn platform env -- <command>` runs an arbitrary command with that same API runtime environment loaded without printing secret values.

`norn platform proxy-plan` prints a no-blip reverse-proxy cutover plan. `proxy-status`, `proxy-render`, and `proxy-switch` manage an optional local Caddy config and upstream state file. They do not change the live topology unless explicitly invoked.

| Flag | Default | Description |
|------|---------|-------------|
| `--repo` | `NORN_PLATFORM_REPO` | Norn checkout containing `v2/scripts/platform-upgrade` |
| `--script` | `NORN_PLATFORM_SCRIPT` | Explicit platform-upgrade script path |
| `--proxy` | `false` | Use managed proxy cutover mode for `platform upgrade` |

## operations

List durable operation records.

```bash
norn operations
norn operations --active
```

Operations summarize long-running work such as app preflights, deploys, and rollbacks. Use `--active` before invasive platform work to see queued or running operations.

| Flag | Default | Description |
|------|---------|-------------|
| `--active` | `false` | Only show queued/running operations |
| `--limit` | `25` | Maximum operations to show |

## events

Show recent Norn Beacon events.

```bash
norn events
norn events --severity critical
norn events --app contextdb --limit 10
norn events show <event-id>
norn events ack <event-id> --note "investigating"
norn events snooze <event-id> --for 2h
norn events open <event-id>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--app` | — | Filter events by app |
| `--type` | — | Filter events by type |
| `--severity` | — | Filter events by severity |
| `--limit` | `25` | Maximum events to show |

Events include `open`, `snoozed`, or `acknowledged` state. Detail output prints related metadata such as saga, deployment, operation, process, service, or job ids when Norn recorded them.

## observability

Generate Norn's Prometheus and Grafana starter bundle.

```bash
norn observability bundle
norn observability bundle --out ./norn-observability
norn observability install
norn observability install --overwrite
```

The bundle includes Prometheus scrape config, Prometheus alert rules, a Grafana datasource, a starter dashboard, and suggested Norn service specs for Prometheus, Grafana, and cAdvisor. The default retention target is 30 days or 8GB.

`norn observability install` writes generated Norn app directories into `NORN_APPS_DIR`: `norn-prometheus`, `norn-grafana`, and `norn-cadvisor`. Review ports and host policy, then validate, preflight, and deploy them like normal Norn apps.

## network

Summarize service reachability and network-mode guidance.

```bash
norn network
```

The command combines `/api/services/manifest` with validation hints to show service exposure, endpoint scope, instance scope, and guidance for `local`, `tailnet`, or `public` mode.

## alerts

Show built-in alert rules derived from Beacon event types.

```bash
norn alerts
```

The rule catalogue is intentionally declarative. It gives CLI, UI, and downstream sinks a shared contract for deploy failure, service down/degraded, cron failure, and recovery events.

## resources

Show live memory usage against declared infraspec limits and print right-sizing suggestions.

```bash
norn resources
```

The command calls `/api/resources/suggestions` and compares Nomad allocation stats with each process's declared `resources.memory` value.

| Status | Meaning |
|--------|---------|
| `at_risk` | Live or peak memory is close to the declared limit; consider increasing `resources.memory` |
| `overprovisioned` | Declared memory is much higher than observed use; the limit may be reducible |
| `right_sized` | Live usage is comfortably inside the declared limit |

Use this after restart loops, OOM events, or a new workload rollout to decide whether an app spec needs resource changes before the next deploy.

## tune

Show advisory CPU, memory, and scale recommendations from live tuning signals.

```bash
norn tune
norn tune recommend
norn tune status
```

The command calls `/api/tuning/recommendations`. It uses live Nomad allocation signals by default, includes any process-level `tuning.signals` declarations from `infraspec.yaml`, and folds in hosted-service access patterns when `/api/access/observations` has data. Recommendations are advisory only: Norn reports the suggested target state but does not update a job or rewrite an app spec.

| Field | Meaning |
|-------|---------|
| `current` | Declared CPU, memory, and observed running allocation count |
| `recommended` | Advisory CPU, memory, and scale target after applying thresholds and `tuning.limits` |
| `signals` | Live or declared signals that informed the recommendation |
| `confidence` | `low` when only current live data is available; higher confidence is reserved for historical signal support |

Access-pattern signals add `observe_access` or `candidate_idle` actions. `observe_access` means the service has no access observations in the lookback window, so Norn needs traffic data before recommending scale-to-zero. `candidate_idle` means access was observed previously, but the service has been quiet beyond the idle threshold.

## notifications

Manage Beacon notification channels.

```bash
norn notifications list
norn notifications add discord ops https://discord.com/api/webhooks/...
norn notifications add ntfy alerts https://ntfy.sh/norn-alerts --severity warning,critical
norn notifications add pushover mobile https://api.pushover.net/1/messages.json \
  --token <app-token> --user-key <user-key> --severity critical
norn notifications add webhook vigil https://vigil.example.com/api/events --severity critical
norn notifications test <channel-id>
norn notifications remove <channel-id>
```

Supported providers are `discord`, `ntfy`, `pushover`, and `webhook`. Severity filters are optional; when omitted, the channel receives all Beacon severities. Notification channel configuration is stored by the Norn control plane and managed through `/api/notifications/channels`.

## smoke

Run operational smoke checks.

```bash
norn smoke platform
norn smoke contextdb
```

`norn smoke platform` checks API health, platform rollup, active operation drain, current release marker, and recent warning/critical Beacon events. It requires authenticated API access when the platform is protected.

Use `norn platform smoke` on hosts where the API token lives in the encrypted runtime env rather than the interactive shell.

## webhooks

List recent webhook deliveries.

```bash
norn webhooks
norn webhooks --limit 50
norn webhooks replay <delivery-id>
norn webhooks replay <delivery-id> --preflight
```

The webhook inbox shows delivery status, matched app, branch, and ignored or failed reason for GitHub and Gitea webhook deliveries.

`norn webhooks replay` queues a delivery again through the durable operation queue. Use `--preflight` to run the same matched app/ref through the read-only preflight lane instead of deploying it.

## restart

Perform a rolling restart of all allocations for an app.

```bash
norn restart <app>
```

Triggers a rolling restart via Nomad. Renders a spinner until all allocations are healthy.

## rollback

Rollback to the previous successful deployment.

```bash
norn rollback <app>
```

Finds the last successful deployment and re-deploys its image tag.

## canary

Inspect and promote active canary deployments.

```bash
norn canary <app>
norn promote <app>
```

`norn canary <app>` prints the latest Nomad deployment id, job id, deployment status, status description, and whether the active deployment still has canary allocations. `norn promote <app>` promotes the canary deployment through Nomad and emits a Beacon event.

Canary behavior is declared per process with `canary.count` and `canary.evaluateAfter` in `infraspec.yaml`. During deploy, Norn waits for the configured evaluation window after the healthy step, checks allocation health, then promotes or fails the deployment.

## deploy groups

List and run ordered multi-app deployment groups.

```bash
norn deploy-groups
norn deploy-group <name> [ref]
```

Deploy group definitions live under `deploy-groups/*.yaml` and list apps in the order they should roll out. Each app entry may request a `waitReady` gate so Norn waits for health before moving to the next app.

`norn deploy-groups` shows configured groups, apps, and wait-ready settings. `norn deploy-group <name> [ref]` starts each app deploy in order and prints the saga id or error for each app.

## scale

Scale a specific task group to a given count.

```bash
norn scale <app> <group> <count>
```

| Argument | Description |
|----------|-------------|
| `app` | App name |
| `group` | Task group / process name |
| `count` | Target instance count |

::: info
Nomad's `Jobs().Scale()` takes `*int`, not `*int64`.
:::

## logs

Stream live logs from a running app.

```bash
norn logs <app>
```

Opens a fullscreen, scrollable log viewer (Bubble Tea TUI). Press `q` or `Ctrl+C` to exit.

## exec

Run a command inside a running allocation. Use `--process` for multi-process apps so Norn targets the intended task group.

```bash
norn exec contextdb --process review-worker -- \
  /contextdb worker review --namespaces hermes-agent --mode agent_memory --dry-run --smoke-evaluator --report
```

## smoke

Run app-specific operational smoke checks.

```bash
norn smoke contextdb
```

`norn smoke contextdb` discovers ContextDB web and review-worker reachability from the service manifest, validates the infraspec, checks web and worker health, writes and retrieves a low-confidence smoke claim, verifies the review queue, runs the review worker in dry-run mode, and checks the resulting worker-run receipt.

| Flag | Default | Description |
|------|---------|-------------|
| `--namespace` | `norn-smoke-<timestamp>` | ContextDB namespace used for the smoke claim |
| `--mode` | `agent_memory` | ContextDB write/retrieve/review mode |
| `--web-url` | manifest endpoint | Override ContextDB web URL |
| `--worker-url` | manifest instance | Override ContextDB review worker health URL |
| `--low-confidence-threshold` | `0.35` | Threshold used when checking the review queue |

## contextdb

Inspect ContextDB-specific integration state from Norn.

```bash
norn contextdb review
norn contextdb policy
norn contextdb policy --json
norn contextdb audit
norn contextdb evaluator-smoke
norn contextdb evaluator-readiness
norn contextdb evaluator-readiness --json
norn contextdb rollback-feedback <event-id> --reason "bad feedback"
norn contextdb review --namespace hermes-agent
norn contextdb worker-runs <namespace>
norn contextdb worker-runs <namespace> --decisions
norn contextdb worker-runs <namespace> --json
```

`norn contextdb review` summarizes the review queue and recent worker runs for a namespace. It defaults to `hermes-agent` in `agent_memory` mode.

`norn contextdb policy` discovers the review worker instance from the service manifest and reads its live `/v1/status` policy report. The report is value-safe: it shows dry-run state, policy preset, evaluator type, whether provider keys are required/configured, allowed actions, mutation status, warnings, and errors without exposing secret values.

`norn contextdb audit` reads recent feedback events from ContextDB so operators can inspect claim validation, refutation, stale marking, and worker-applied mutation receipts.

`norn contextdb evaluator-smoke` runs the deployed review worker's configured evaluator smoke test inside the `review-worker` allocation. It does not open the database or mutate claims; provider-backed evaluators use their configured provider/webhook and report missing keys, policy blocks, malformed decisions, or rate-limit failures before rollout.

`norn contextdb evaluator-readiness` synthesizes policy, key availability, dry-run state, and evaluator configuration into a per-namespace readiness assessment. Use it before moving a namespace from the rules evaluator or dry-run mode to a provider-backed evaluator.

`norn contextdb rollback-feedback` proxies a ContextDB feedback rollback through the hosted web service and prints the rollback receipt. Use the feedback event id from `norn contextdb audit`, `norn ops contextdb`, or the Ops UI.

`norn contextdb worker-runs` discovers the ContextDB web endpoint from the service manifest and lists durable review worker summaries for a namespace. The table includes generated time, cycle id, mode, evaluator, dry-run flag, scanned/applied/skipped/error counts, and decision count. Use `--decisions` to include each decision's type, action, applied flag, node id, and reason.

| Flag | Default | Description |
|------|---------|-------------|
| `--mode` | `agent_memory` | ContextDB mode |
| `--after` | — | Only show runs after this RFC3339 timestamp |
| `--limit` | `10` | Maximum runs to show after fetching |
| `--decisions` | `false` | Print decision details below each run |
| `--json` | `false` | Print raw JSON |
| `--web-url` | manifest endpoint | Override ContextDB web URL |

## ops

Show operator rollups for hosted services.

```bash
norn ops platform
norn ops contextdb
```

`norn ops platform` calls Norn's platform operations endpoint and summarizes service exposure, recent deployment provenance, dirty local builds, secret hygiene, snapshot retention state, recent access status buckets, and OpenTelemetry/Grafana configuration.

`norn ops contextdb` calls Norn's ContextDB operations endpoint and summarizes app health, web/worker reachability, value-safe worker policy, provider rollout gate, review queue size, recent worker runs, recent feedback audit events, snapshots, secrets, and recent deployments.

## health

Check the health of all backing services (Nomad, Consul, PostgreSQL, and configured S3-compatible object storage such as Garage).

```bash
norn health
# alias:
norn doctor
```

Displays a checklist of service statuses with pass/fail indicators.
The output also shows the configured Norn network mode from `NORN_NETWORK_MODE`.

## metrics

Norn exposes Prometheus-compatible metrics over HTTP rather than a CLI command:

```bash
curl http://127.0.0.1:8800/metrics
curl http://127.0.0.1:8800/api/observability/prometheus.yml
```

The generated Prometheus config includes Norn itself and any app process that declares `metrics.enabled: true`.

## stats

Display deployment and cluster statistics.

```bash
norn stats
```

Shows total apps, recent deployments, active allocations, and other cluster metrics.

## access

Show recent Norn API access events.

```bash
norn access
norn access --limit 100
norn access patterns --window 14d --idle-after 7d
norn access observe ft-trove --process web --endpoint https://trove.example.com --source gateway --status 200
norn access cloudflare status
norn access cloudflare sync --window 14d
```

The table includes request time, status, method, path, client IP, Cloudflare Access user metadata when present, and duration. Norn does not expose request bodies, authorization headers, or secret values in this view.

`norn access patterns` summarizes durable hosted-service access observations by app and process. It reports request totals, last observed access, quiet duration, peak UTC hour, idle-candidate action, and confidence. The data comes from hourly aggregate buckets, not raw request logs.

`norn access observe` records an aggregate observation for a hosted service. It is intended for a future wake gateway, reverse proxy, cloudflared log shipper, or small operator script. Observations include app, process, endpoint/source labels, status bucket, count, and optional timestamp; they do not include request bodies or credentials.

`norn access cloudflare status` reports whether Cloudflare GraphQL sync and Logpush receiver secrets are configured, and shows the public service hostnames Norn can map back to app/process pairs.

`norn access cloudflare sync` imports hourly request observations from Cloudflare's GraphQL Analytics API for each mapped public hostname. The sync requires `NORN_CLOUDFLARE_API_TOKEN` and `NORN_CLOUDFLARE_ZONE_ID`. The token should have read access to zone analytics for the target zone. Imported observations are stored as hourly aggregates with source `cloudflare-graphql`.

The Logpush receiver is `POST /api/access/cloudflare/logpush`. It requires `NORN_CLOUDFLARE_LOGPUSH_TOKEN` and accepts the token in `X-Norn-Logpush-Token`, `X-Logpush-Secret`, or a bearer header. Configure Cloudflare HTTP Logpush to send HTTP request logs to this endpoint over HTTPS with a secret header. Imported observations are stored with source `cloudflare-logpush`.

The advisory tuner consumes these access patterns. A service with no observations in the lookback window is marked `observe_before_idle`; a service whose last access is older than `--idle-after` is marked `consider_idle`.

Temporary IP grants are managed under the same command group:

```bash
norn access grant --ip 1.2.3.4 --ttl 24h --note "CI server"
norn access grants
norn access revoke <grant-id>
```

Grants bypass bearer auth for a single IP until their TTL expires. Use them for short-lived operator or automation access, and prefer the narrowest practical TTL. The dashboard Platform tab exposes the same grant list/create/revoke flow via `/api/access/grants`.

## secrets

Manage SOPS-encrypted secrets for an app.

```bash
# List secret keys
norn secrets <app>

# Compare declared, encrypted, and plaintext secret state
norn secrets status
norn secrets status <app>

# Set a secret
norn secrets set <app> KEY=VALUE

# Delete a secret
norn secrets delete <app> KEY

# Generate migration commands for plaintext env secrets
norn secrets migrate
norn secrets migrate <app>
norn secrets migrate <app> --apply --apps-dir ~/projects
```

| Subcommand | Description |
|------------|-------------|
| (none) | List secret key names (values are not shown) |
| `status` | Show declared-vs-encrypted drift and plaintext env warnings |
| `migrate-plan` | Show value-safe plaintext env entries that should move to `secrets.enc.yaml` |
| `migrate` | Generate SOPS commands for plaintext env secrets; with `--apply`, update infraspec files by moving keys from `env` to `secrets` |
| `set` | Set or update a secret key-value pair |
| `delete` | Remove a secret |

`norn secrets migrate` is intentionally two-phase. Dry-run prints the affected keys and SOPS commands without writing files. `--apply` edits `infraspec.yaml`, but you still run the generated SOPS commands manually so secret values never pass through the API or docs output.

## services

Inspect the service manifest used by agents, dashboards, and external tooling to answer what Norn is hosting.

```bash
# Human-readable table
norn services

# Raw JSON contract
norn services manifest
```

The table separates app-level endpoints from process reachability. Service processes can list public or local endpoints; worker, cron, and function entries expose process type, status, health path, instances, network mode, and reachability metadata without inheriting unrelated app endpoints. The `REACH` column summarizes endpoint and instance scope, for example `local`, `public/private`, or `internal/local`.

## snapshots

Manage PostgreSQL database snapshots.

```bash
# List snapshots
norn snapshots <app>

# Restore a snapshot
norn snapshots <app> restore <timestamp> --yes
norn snapshots <app> restore <timestamp> --yes --pre-restore

# Preview retention
norn snapshots <app> retention --keep 3
norn snapshots <app> retention

# Execute retention
norn snapshots <app> retention --keep 3 --execute --yes

# Remote export/import
norn snapshots export <app>
norn snapshots remote <app>
norn snapshots import <app> snapshots/<app>/<filename>.dump
```

| Subcommand | Description |
|------------|-------------|
| (none) | List available snapshots with timestamps, source commit, created time, size, and filename |
| `restore` | Restore from a snapshot at the given timestamp; requires `--yes` and prints a restore receipt. `--pre-restore` creates a fresh snapshot before the restore |
| `retention` | Preview newest-N retention without deleting snapshots; defaults to `snapshots.keep` from the app spec or 3; add `--execute --yes` to prune and print a receipt |
| `export` | Upload the latest local snapshot to the app's configured `snapshots.exportBucket` |
| `remote` | List remote snapshots in the configured export bucket |
| `import` | Download a remote snapshot key back into the local snapshots directory |

## cron

Manage cron (periodic batch) jobs.

```bash
norn cron <app> [subcommand]
```

| Subcommand | Description |
|------------|-------------|
| (none) | Show cron status and schedule |
| `trigger` | Manually trigger a cron job immediately |
| `pause` | Pause a periodic job |
| `resume` | Resume a paused periodic job |
| `schedule <expr>` | Update the cron expression |

## invoke

Invoke a function process.

```bash
norn invoke <app> --process=<name> --body='{"key":"value"}'
```

| Flag | Short | Description |
|------|-------|-------------|
| `--process` | `-p` | Process name to invoke (required) |
| `--body` | `-b` | JSON body or `@file` to read from file |

Returns the execution ID and job ID. The function runs as a one-shot Nomad batch job.

## saga

View the saga event log.

```bash
# View events for a specific saga
norn saga <saga-id>

# Recent events filtered by app
norn saga --app=myapp --limit=50
```

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--app` | `-a` | — | Filter events by app name |
| `--limit` | `-l` | `20` | Maximum number of events to show |

## validate

Validate infraspec files for syntax and configuration errors.

```bash
# Validate a single app
norn validate <app>

# Validate all discovered apps
norn validate

# Treat plaintext secret-like env values as errors
norn validate --strict-secrets
```

Reports errors and warnings for each infraspec field. Validation warns when secret-like values such as DSNs, passwords, tokens, API keys, or client secrets appear in plain `env` blocks. Move those values to `secrets.enc.yaml` and list the key under `secrets`. Add `--strict-secrets`, or set `NORN_STRICT_SECRETS=true` for deploy/preflight validation, to make plaintext secret-like env values fail the gate. Validation also uses `NORN_NETWORK_MODE` to warn when endpoint hosts look mismatched for the active mode, such as localhost endpoints in `tailnet` or `public` mode.

## endpoints

List an app's configured endpoints with their cloudflared status.

```bash
norn endpoints <app>
```

Output shows each endpoint with a status indicator:

```
endpoints for signal-sideband

  ● sideband.slopistry.com    active
  ○ api.slopistry.com         inactive
```

- `●` active — hostname is routed in cloudflared
- `○` inactive — hostname is not in cloudflared ingress
- `?` unknown — cloudflared config is unavailable (dev mode)

### endpoints toggle

Toggle a single endpoint on or off in cloudflared.

```bash
norn endpoints toggle <app> <hostname>
```

| Argument | Description |
|----------|-------------|
| `app` | App name |
| `hostname` | The hostname to toggle (e.g. `sideband.slopistry.com`) |

Determines the current state from the cloudflared ingress list and flips it. If the endpoint is active, it will be disabled; if inactive, it will be enabled.

```
$ norn endpoints toggle signal-sideband sideband.slopistry.com
toggling sideband.slopistry.com → disabled

╭──────────────────────╮
│ cloudflared updated  │
╰──────────────────────╯
```

## forge

Set up cloudflared tunnel routing for all of an app's endpoints at once.

```bash
norn forge <app>
```

Configures cloudflared ingress rules based on the app's `endpoints` in its infraspec. Use `norn endpoints toggle` for per-hostname control.

## teardown {#teardown}

Remove cloudflared tunnel routing for all of an app's endpoints at once.

```bash
norn teardown <app>
```

Removes the app's entries from the cloudflared ingress configuration. Use `norn endpoints toggle` for per-hostname control.

## version

Display CLI version and API endpoint.

```bash
norn version
```

## Global Flags

| Flag | Description |
|------|-------------|
| `--api` | Override the Norn API URL (default: `NORN_URL` or `http://localhost:8800`) |
