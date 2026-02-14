# CLI Commands

Full reference for every `norn` command.

## status

List all apps with their health, current commit, endpoints, and services.

```bash
norn status
```

Displays a table of all discovered apps with live health indicators.

## app

Detailed view of a single app including processes, recent deployments, and infrastructure.

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

## health

Check the health of all backing services (Nomad, Consul, PostgreSQL, S3).

```bash
norn health
# alias:
norn doctor
```

Displays a checklist of service statuses with pass/fail indicators.

## stats

Display deployment and cluster statistics.

```bash
norn stats
```

Shows total apps, recent deployments, active allocations, and other cluster metrics.

## secrets

Manage SOPS-encrypted secrets for an app.

```bash
# List secret keys
norn secrets <app>

# Set a secret
norn secrets set <app> KEY=VALUE

# Delete a secret
norn secrets delete <app> KEY
```

| Subcommand | Description |
|------------|-------------|
| (none) | List secret key names (values are not shown) |
| `set` | Set or update a secret key-value pair |
| `delete` | Remove a secret |

## snapshots

Manage PostgreSQL database snapshots.

```bash
# List snapshots
norn snapshots <app>

# Restore a snapshot
norn snapshots <app> restore <timestamp>
```

| Subcommand | Description |
|------------|-------------|
| (none) | List available snapshots with timestamps |
| `restore` | Restore from a snapshot at the given timestamp |

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

Reports errors and warnings for each infraspec field.

## forge

Set up cloudflared tunnel routing for an app's endpoints.

```bash
norn forge <app>
```

Configures cloudflared ingress rules based on the app's `endpoints` in its infraspec.

## teardown {#teardown}

Remove cloudflared tunnel routing for an app.

```bash
norn teardown <app>
```

Removes the app's entries from the cloudflared ingress configuration.

## version

Display CLI version and API endpoint.

```bash
norn version
```

## Global Flags

| Flag | Description |
|------|-------------|
| `--api` | Override the Norn API URL (default: `NORN_URL` or `http://localhost:8800`) |
