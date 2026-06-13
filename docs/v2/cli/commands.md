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
```

| Argument | Default | Description |
|----------|---------|-------------|
| `app` | — | App name |
| `ref` | `HEAD` | Git ref (commit SHA, branch, tag) |

Connects to the WebSocket and renders a real-time progress display showing each pipeline step. The saga ID is printed on completion for later inspection.

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
```

The table includes request time, status, method, path, client IP, Cloudflare Access user metadata when present, and duration. Norn does not expose request bodies, authorization headers, or secret values in this view.

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
```

| Subcommand | Description |
|------------|-------------|
| (none) | List secret key names (values are not shown) |
| `status` | Show declared-vs-encrypted drift and plaintext env warnings |
| `set` | Set or update a secret key-value pair |
| `delete` | Remove a secret |

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

# Preview retention
norn snapshots <app> retention --keep 3
norn snapshots <app> retention

# Execute retention
norn snapshots <app> retention --keep 3 --execute --yes
```

| Subcommand | Description |
|------------|-------------|
| (none) | List available snapshots with timestamps, source commit, created time, size, and filename |
| `restore` | Restore from a snapshot at the given timestamp; requires `--yes` and prints a restore receipt |
| `retention` | Preview newest-N retention without deleting snapshots; defaults to `snapshots.keep` from the app spec or 3; add `--execute --yes` to prune and print a receipt |

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
```

Reports errors and warnings for each infraspec field. Validation warns when secret-like values such as DSNs, passwords, tokens, API keys, or client secrets appear in plain `env` blocks. Move those values to `secrets.enc.yaml` and list the key under `secrets`. Validation also uses `NORN_NETWORK_MODE` to warn when endpoint hosts look mismatched for the active mode, such as localhost endpoints in `tailnet` or `public` mode.

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
