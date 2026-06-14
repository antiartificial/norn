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

### 5. Service Manifest

```bash
source ~/.config/norn/cli.env && norn services
```

This is the platform's service directory — what's hosted, what type each process is, whether it's reachable, and from where. Report:

- Total services by type (service, worker, cron, function)
- Health status (passing, warning, critical, unknown)
- Public endpoints and their URLs
- Any services with `unknown` status or `none` reachability (usually means no running allocation)
- Network mode (local, tailnet, public)

For the raw JSON contract (useful for agents and MCP discovery):
```bash
source ~/.config/norn/cli.env && norn services manifest
```

### 6. Recent Deploys and Operations

```bash
source ~/.config/norn/cli.env && norn operations --limit 10
```

Check for:
- Failed or stuck operations — what app, what step failed, when
- Dirty deploys (local working tree changes that weren't committed)
- Recent rollbacks

For deploy history of a specific app:
```bash
source ~/.config/norn/cli.env && norn app <app-name>
```

This shows the latest deployment status, image tag, commit SHA, source kind (git/local), dirty state, and saga ID.

### 7. Cluster Stats

```bash
source ~/.config/norn/cli.env && norn stats
```

Shows allocation counts and uptime leaderboard — which allocations have been running longest and on which nodes.

### 8. Platform Ops Summary

```bash
source ~/.config/norn/cli.env && norn ops platform
```

The single-command platform overview. Surfaces service exposure, deploy provenance, snapshot retention, access events, observability status, secret hygiene, and recent beacon events all in one view. Use this when you want the full picture in one pass.

### 9. Deeper Inspection (if problems found)

For a specific app's full status, allocations, and endpoints:
```bash
source ~/.config/norn/cli.env && norn app <app-name>
```

For live log streaming:
```bash
source ~/.config/norn/cli.env && norn logs <app-name>
```

For deployment step-by-step history (saga trail):
```bash
source ~/.config/norn/cli.env && norn saga <saga-id>
```

For validation issues across all apps:
```bash
source ~/.config/norn/cli.env && norn validate
```

## Reporting Format

After running the checks, report:

1. **Platform health** — one line: all services up, or which are down. Include network mode.
2. **Service inventory** — total apps and services by type/status. Call out any with unknown status or no running allocations. Mention public endpoints by name.
3. **Problems** — any critical/warning events, unhealthy apps, at-risk resources, failed deploys. Include the app name, what happened, and a recommended action.
4. **Resource summary** — how many apps are right-sized, overprovisioned, or at risk. Call out specific apps that need attention with their declared vs used memory.
5. **Recent activity** — notable deploys, restarts, or events. Include dirty deploys and rollbacks.
6. **Recommendations** — concrete next steps if any problems were found.

Keep it concise. Don't dump raw command output — synthesize it into a readable report. Use tables for the service inventory and resource summary when there are more than a few entries.

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
| App detail | `norn app <app>` |
| Service manifest | `norn services` |
| Service manifest JSON | `norn services manifest` |
| Deploy history | `norn operations --limit 20` |
| Saga trail | `norn saga <saga-id>` |
| View secrets status | `norn secrets status <app>` |
| Acknowledge event | `norn events ack <event-id>` |
| Validate all | `norn validate` |
| Platform ops | `norn ops platform` |
| Resource suggestions | `norn resources` |

Always source `~/.config/norn/cli.env` before running any `norn` command.

## What to Watch For

- **OOM kills** — the most urgent signal. Check `norn resources` for the app, recommend increasing `resources.memory` in its infraspec
- **Restart loops** — multiple restarts in quick succession. Check logs for crash reason
- **Failed deploys** — check `norn operations` and the saga trail
- **Service health transitions** — Consul health checks going critical or warning
- **Disk pressure** — `norn_host_disk_free_bytes` dropping below 10%
- **Overprovisioned apps** — not urgent, but waste capacity on constrained hosts
