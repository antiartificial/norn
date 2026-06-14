---
name: norn
description: Observe and operate the Norn platform — check health, resource usage, events, restarts, deploys, and surface actionable recommendations. Trigger when the user asks about platform status, app health, recent events, resource usage, or operational state.
---

# Norn Platform Observation

You are operating the Norn v2 control plane. The API runs locally at `http://127.0.0.1:8800` and the CLI is `norn`. Authentication requires the token from `~/.config/norn/cli.env` — source it before running CLI commands:

```bash
source ~/.config/norn/cli.env
```

## Observation Checklist

Run these commands to build a complete picture of platform state. Report findings concisely — lead with problems, then healthy state.

### 1. Health and Version

```bash
source ~/.config/norn/cli.env && norn version
source ~/.config/norn/cli.env && norn health
```

Check: all backing services (nomad, consul, postgres, sops) should be `up`. Flag any that aren't.

### 2. App Status

```bash
source ~/.config/norn/cli.env && norn status
```

Check: every app should show healthy. Flag unhealthy apps, pending allocations, or missing services.

### 3. Recent Events (Beacon)

```bash
source ~/.config/norn/cli.env && norn events --limit 10
```

Check for: `nomad.task.oom_killed`, `nomad.task.restarted`, `nomad.allocation.failed`, `nomad.allocation.lost`, `service.health.critical`, `deploy.failed`, `cron.failed`. Any critical or warning events are actionable — show the event type, app, severity, and timestamp.

### 4. Resource Usage

```bash
source ~/.config/norn/cli.env && norn resources
```

Check for:
- **at risk** — app is close to OOM, needs memory limit increase in infraspec
- **overprovisioned** — app is using much less than declared, limit could be lowered
- **right sized** — no action needed

### 5. Recent Deploys

```bash
source ~/.config/norn/cli.env && norn operations --limit 5
```

Check for failed or stuck operations. Note any dirty deploys.

### 6. Cluster Stats

```bash
source ~/.config/norn/cli.env && norn stats
```

Shows allocation counts and uptime leaderboard.

### 7. Deeper Inspection (if problems found)

For a specific app:
```bash
source ~/.config/norn/cli.env && norn app <app-name>
source ~/.config/norn/cli.env && norn logs <app-name>
```

For platform-wide ops summary:
```bash
source ~/.config/norn/cli.env && norn ops platform
```

For observability stack status:
```bash
source ~/.config/norn/cli.env && norn observability bundle
```

## Reporting Format

After running the checks, report:

1. **Platform health** — one line: all services up, or which are down
2. **Problems** — any critical/warning events, unhealthy apps, at-risk resources, failed deploys. Include the app name, what happened, and a recommended action
3. **Resource summary** — how many apps are right-sized, overprovisioned, or at risk. Call out specific apps that need attention
4. **Recent activity** — notable deploys, restarts, or events in the last 24h
5. **Recommendations** — concrete next steps if any problems were found

Keep it concise. Don't dump raw command output — synthesize it.

## Common Operations

If the user asks you to take action:

| Task | Command |
|------|---------|
| Deploy an app | `norn deploy <app> HEAD` |
| Preflight (dry run) | `norn preflight <app> HEAD` |
| Restart an app | `norn restart <app>` |
| Rollback | `norn rollback <app>` |
| Stream logs | `norn logs <app>` |
| Scale | `norn scale <app> --group <group> --count <n>` |
| View secrets status | `norn secrets status <app>` |
| Acknowledge event | `norn events ack <event-id>` |
| Validate all | `norn validate` |

Always source `~/.config/norn/cli.env` before running any `norn` command.

## What to Watch For

- **OOM kills** — the most urgent signal. Check `norn resources` for the app, recommend increasing `resources.memory` in its infraspec
- **Restart loops** — multiple restarts in quick succession. Check logs for crash reason
- **Failed deploys** — check `norn operations` and the saga trail
- **Service health transitions** — Consul health checks going critical or warning
- **Disk pressure** — `norn_host_disk_free_bytes` dropping below 10%
- **Overprovisioned apps** — not urgent, but waste capacity on constrained hosts
