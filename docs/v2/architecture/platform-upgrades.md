# Platform Upgrades

Norn treats upgrades to Norn itself as a separate platform lane. App deploys mutate Nomad jobs, snapshots, migrations, and routes. Platform upgrades mutate the control plane binary, CLI, and dashboard assets while leaving Nomad, Consul, Postgres, cloudflared, and app allocations running.

## Current Lane

```bash
norn platform preflight HEAD
norn platform upgrade HEAD
```

The command shells out to `v2/scripts/platform-upgrade` on the host that owns the Norn checkout. The script:

1. Resolves the requested git ref.
2. Creates an isolated git worktree for that exact commit.
3. Builds UI, API, and CLI into `$HOME/norn/releases/<sha>`.
4. Starts the candidate API on `127.0.0.1:18800`.
5. Sets `NORN_SKIP_DEPLOYMENT_RECOVERY=true` for the candidate so it does not mark running deployments failed.
6. Checks `/api/health` and `/api/version`.
7. On upgrade, flips `$HOME/norn/current`, installs compatibility binaries to `$HOME/go/bin`, restarts `com.norn.api`, and runs postflight health.
8. If postflight fails and a previous current release exists, flips back, reinstalls the previous binaries, and restarts again.

This is low-invasive: active dashboard sessions and websocket streams reconnect, but hosted apps continue running.

## Durable API Jobs

The durable queue should live in Norn's control-plane Postgres, not in Nomad, Redis, Valkey, or an app container.

Reasons:

- Platform jobs must still work when Nomad is degraded.
- Norn already requires Postgres for deployments and saga events.
- The API can claim rows with `FOR UPDATE SKIP LOCKED`, making workers safe across restart or future multiple API instances.
- Saga events remain the immutable user-facing log; queue rows only track claim state, retries, and resumability.

Proposed table:

```sql
CREATE TABLE platform_jobs (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  ref TEXT NOT NULL DEFAULT '',
  saga_id TEXT NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}',
  attempts INT NOT NULL DEFAULT 0,
  locked_by TEXT NOT NULL DEFAULT '',
  locked_until TIMESTAMPTZ,
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ
);
```

The first worker should run inside `norn-api` and execute one job at a time. API handlers enqueue work and return a saga id immediately. A restarted API reclaims expired jobs, resumes from persisted stage markers, or marks a non-resumable stage failed with a clear saga event.

Good first queued job types:

| Kind | Purpose |
|------|---------|
| `platform.preflight` | Build and candidate-health-check a platform release |
| `platform.upgrade` | Promote a preflighted release and run rollback-capable postflight |
| `app.preflight` | Move the current in-memory app preflight goroutine into the durable queue |
| `app.deploy` | Move app deploys into the durable queue after stages are restart-aware |

## Old/New API Side By Side

The implemented preflight already runs old and new APIs side by side on different ports:

| API | Port | Role |
|-----|------|------|
| Current | `8800` | Serves users and active CLI commands |
| Candidate | `18800` | Serves local health/version preflight only |

That confirms the candidate can boot against the live environment before restart. It is not yet no-blip traffic switching.

Two no-blip designs are viable:

1. **Local reverse proxy.** Run stable ingress on `8800`, run API releases on private ports, prewarm the candidate, then atomically update the proxy upstream. Caddy, nginx, HAProxy, or Tailscale Serve can do this. Websockets still reconnect, but new requests stop hitting the old process before it is terminated.
2. **launchd socket activation.** Let launchd own the listening socket and pass it to the API process. The replacement process can accept on the same socket after launchd restarts it. This is elegant on macOS but requires API support for inherited sockets.

The proxy path is the more straightforward next step because it does not require changing Go's listener startup model. The durable queue is still necessary for truly graceful operations, because a proxy can preserve traffic availability but cannot make an in-memory deploy goroutine survive process exit.
