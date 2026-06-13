# Platform Upgrades

Norn treats upgrades to Norn itself as a separate platform lane. App deploys mutate Nomad jobs, snapshots, migrations, and routes. Platform upgrades mutate the control plane binary, CLI, and dashboard assets while leaving Nomad, Consul, Postgres, cloudflared, and app allocations running.

## Current Lane

```bash
norn platform preflight HEAD
norn platform upgrade HEAD
norn platform upgrade HEAD --proxy
```

The command shells out to `v2/scripts/platform-upgrade` on the host that owns the Norn checkout. The script:

1. Resolves the requested git ref.
2. Creates an isolated git worktree for that exact commit.
3. Builds UI, API, and CLI into `$HOME/norn/releases/<sha>`.
4. Starts the candidate API on `127.0.0.1:18800`.
5. Sets `NORN_SKIP_DEPLOYMENT_RECOVERY=true`, `NORN_SKIP_OPERATION_RECOVERY=true`, and `NORN_SKIP_OPERATION_WORKER=true` for the candidate so it does not mark running work failed or claim queued work.
6. Checks `/api/health` and `/api/version`.
7. On normal upgrade, flips `$HOME/norn/current`, installs compatibility binaries to `$HOME/go/bin`, restarts `com.norn.api`, and runs postflight health.
8. If postflight fails and a previous current release exists, flips back, reinstalls the previous binaries, and restarts again.

This is low-invasive: active dashboard sessions and websocket streams reconnect, but hosted apps continue running.

The platform lane also supports:

```bash
norn platform releases
norn platform rollback <sha-prefix>
norn platform smoke
norn platform env -- <command>
norn platform proxy-plan
norn platform proxy-status
norn platform proxy-render
norn platform proxy-switch <port|host:port>
```

Release metadata is written to each release directory as `release.env` and `release.json`.

`norn smoke platform` is the post-upgrade smoke surface for authenticated shells. It checks `/api/health`, platform operations, active operation drain, current release marker, and recent warning/critical Beacon events.

`norn platform smoke` runs the same smoke through the encrypted API runtime env. This is useful on hosts where the LaunchAgent has the token but non-interactive shells do not.

## Drain Gate

Before `upgrade` or `rollback`, the script checks `/api/operations/active` when `NORN_API_TOKEN` or `NORN_TOKEN` is available. `NORN_DRAIN_MODE` controls behavior:

| Mode | Behavior |
|------|----------|
| `fail` | Default. Stop if active operations exist |
| `wait` | Wait until active operations finish |
| `force` | Skip the drain check |

If the active API is too old or auth is unavailable, the drain check logs a warning and continues so bootstrap upgrades still work.

## Durable Operations Queue

Norn now stores a durable operations queue in control-plane Postgres. App deploys, app preflights, and app rollbacks create operation rows with compact status, risk, app, ref, saga id, timing, payload, attempts, lease owner, lease expiry, next attempt, and last error.

The queue lives in the same control-plane Postgres table family, not Nomad, Redis, Valkey, or an app container.

Reasons:

- Platform jobs must still work when Nomad is degraded.
- Norn already requires Postgres for deployments and saga events.
- The API can claim rows with `FOR UPDATE SKIP LOCKED`, making workers safe across restart or future multiple API instances.
- Saga events remain the immutable user-facing log; queue rows only track claim state, retries, and resumability.

The first worker runs inside `norn-api` and executes one job at a time. API handlers enqueue work and return a saga id immediately. A restarted API reclaims queued jobs and failed/expired preflight attempts. Read-only preflight jobs can retry safely. App deploy jobs are queued and protected by drain gates, but a process interruption during mutable deploy execution is marked failed instead of blindly replaying snapshot, migration, or Nomad submit stages.

Current queued job types:

| Kind | Purpose |
|------|---------|
| `app.preflight` | Run validation, source prep, build, and tests with safe retries |
| `app.deploy` | Queue app deploys and run them under worker/drain visibility |
| `app.rollback` | Queue app rollback through the same worker/drain lane |

Deploy and rollback execution writes durable stage rows to `deployment_steps`. On restart, interrupted deploys are requeued only if no mutable stage has started. Mutable stages include snapshot, migration, Nomad submit, health, forge, and cleanup.

Good next queued job types:

| Kind | Purpose |
|------|---------|
| `platform.preflight` | Build and candidate-health-check a platform release from the API/UI |
| `platform.upgrade` | Promote a preflighted release and run rollback-capable postflight |

## Old/New API Side By Side

The implemented preflight already runs old and new APIs side by side on different ports:

| API | Port | Role |
|-----|------|------|
| Current | `8800` | Serves users and active CLI commands |
| Candidate | `18800` | Serves local health/version preflight only |

That confirms the candidate can boot against the live environment before restart. For hosts that have moved API ingress behind the managed proxy, `norn platform upgrade --proxy` can use side-by-side traffic switching instead of a LaunchAgent restart.

Two no-blip designs are viable:

1. **Local reverse proxy.** Run stable ingress on `8800`, run API releases on private ports, prewarm the candidate, then atomically update the proxy upstream. Caddy, nginx, HAProxy, or Tailscale Serve can do this. Websockets still reconnect, but new requests stop hitting the old process before it is terminated.
2. **launchd socket activation.** Let launchd own the listening socket and pass it to the API process. The replacement process can accept on the same socket after launchd restarts it. This is elegant on macOS but requires API support for inherited sockets.

The proxy path is the more straightforward next step because it does not require changing Go's listener startup model. The durable queue is still necessary for truly graceful operations, because a proxy can preserve traffic availability but cannot make an in-memory deploy goroutine survive process exit.

`norn platform proxy-plan` prints a Caddy-style local reverse-proxy plan with stable ingress on one port and old/new API releases on private ports.

The platform script also provides optional managed proxy primitives:

| Command | Purpose |
|---------|---------|
| `norn platform proxy-status` | Show listen address, current upstream, Caddyfile path, and reload mode |
| `norn platform proxy-render` | Render the managed Caddy config to stdout |
| `norn platform proxy-switch <port|host:port>` | Update the upstream state file and write the managed Caddyfile |

`proxy-switch` reloads Caddy only when `NORN_PROXY_RELOAD=true`. This keeps the feature safe to stage before the host is intentionally moved to proxy-fronted API ports.

`norn platform upgrade --proxy` sets `NORN_PLATFORM_UPGRADE_MODE=proxy` for the platform script. In that mode, the script:

1. Boots the candidate API on `NORN_PROXY_CANDIDATE_PORT` unless `NORN_CANDIDATE_PORT` is set.
2. Promotes the release symlink and compatibility binaries.
3. Writes the managed Caddyfile with the candidate upstream.
4. Requires `NORN_PROXY_RELOAD=true` and reloads Caddy.
5. Runs postflight through the stable API base.
6. Records the new API pid in `NORN_PROXY_PID_FILE`.
7. Stops the previous proxy-managed API pid after postflight succeeds.

If postflight fails after switching, the script writes the previous upstream back, reloads Caddy best-effort, and stops the failed candidate.

## Webhook Replay

Webhook deliveries are stored with provider, event, repo, branch, ref, parsed payload, saga id, status, and reason. Operators can replay a delivery through the queue:

```bash
norn webhooks
norn webhooks replay <delivery-id>
norn webhooks replay <delivery-id> --preflight
```

Replay is authenticated through the normal API token path. GitHub and Gitea ingress endpoints remain public so signed webhook delivery still works.
