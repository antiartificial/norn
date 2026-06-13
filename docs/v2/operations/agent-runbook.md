# Agent Runbook

This page is the repo-owned operating guide for agents and automation working on Norn v2. Keep it generic and portable: do not add personal hostnames, local user paths, API tokens, private tunnel domains, or machine-specific aliases here. Local Codex skills can layer those details on top.

## Orientation

Before editing or deploying a Norn-managed app, gather live platform evidence. Prefer the API and CLI over assumptions from checked-in specs.

Useful surfaces:

| Need | Surface |
|------|---------|
| Hosted services and reachability | `GET /api/services/manifest`, `norn services manifest` |
| Platform rollup | `GET /api/ops/platform`, `norn ops platform` |
| Active queue/drain state | `GET /api/operations/active`, `norn operations --active` |
| Stage checkpoints | `GET /api/deployments/{id}/steps` |
| App specs and runtime status | `GET /api/apps`, `GET /api/apps/{id}`, local `infraspec.yaml` |
| Validation and rehearsal | `GET /api/validate`, `POST /api/apps/{id}/preflight`, `norn preflight <app> [ref]` |
| Deploy progress | `POST /api/apps/{id}/deploy`, `GET /api/saga/{sagaId}`, `norn saga <saga-id>` |
| Webhook delivery triage | `GET /api/webhooks/deliveries`, `norn webhooks` |
| Platform release history | `GET /api/platform/releases`, `norn platform releases` |
| Operational events | `GET /api/events`, `GET /api/events/{id}`, `norn events`, `norn alerts` |
| Control-plane health | `/api/health`, `/api/version`, `/metrics`, `norn smoke platform`, `norn platform smoke` |

If a protected endpoint returns `401`, do not assume the platform is unhealthy. Verify auth context separately and fall back to public health/version endpoints, local DB checks, process manager state, or an authenticated shell when available.

## Durable Operations

App deploys, app preflights, and app rollbacks are queued in control-plane Postgres and claimed by the API worker. Operation rows include status, kind, app, ref, saga id, payload, attempts, max attempts, lock owner, lock expiry, next attempt, and last error.

Use active operations as the drain source before invasive work:

```bash
norn operations --active
```

Semantics:

- `app.preflight` is read-only and can retry safely.
- `app.deploy` is queued and drain-visible.
- Deploy and rollback stages are recorded in `deployment_steps`.
- Use `norn deploy steps <deployment-id>` to inspect checkpoint evidence.
- Interrupted deploys can be requeued automatically only before mutable stages begin.
- Interrupted mutable deploy stages should be treated as failed unless there is explicit stage-level resume evidence.
- Saga events remain the detailed timeline. Operation rows are the compact queue and drain index.

## Deploy Workflow

When deploying an app:

1. Identify the authoritative runtime host or API base.
2. Check service manifest and platform rollup.
3. Check active operations.
4. Validate or preflight if the change affects build, secrets, snapshots, migrations, endpoints, or process shape.
5. Queue the deploy.
6. Follow the returned saga and operation until terminal state.
7. Smoke-test the app endpoint or health path that users actually rely on.

Do not treat an HTTP handler's immediate `queued` response as completion. The worker still has to claim and execute the operation.

## Webhook Replay

Webhook deliveries are persisted before validation decisions. The inbox records provider, event, delivery id, repository, ref, branch, matched app, saga id, status, reason, parsed payload, and metadata.

For triage:

```bash
norn webhooks
norn webhooks replay <delivery-id>
norn webhooks replay <delivery-id> --preflight
```

Use `--preflight` when you want to test the matched app/ref without mutating runtime state. Replay goes through the durable operation queue.

## Platform Upgrades

Norn control-plane upgrades should use the platform lane rather than rebuilding the whole local environment:

```bash
norn platform preflight HEAD
norn platform upgrade HEAD
norn platform releases
norn platform rollback <sha-prefix>
norn platform smoke
norn platform env -- <command>
norn platform proxy-plan
norn platform proxy-status
norn platform proxy-render
norn platform proxy-switch <port|host:port>
```

The platform lane builds an isolated release, boots a candidate API on an alternate port, checks health/version, promotes the release symlink, restarts only the Norn API process, and runs postflight health.

`norn smoke platform` is the preferred post-upgrade smoke command when authenticated API access is available. It checks health, operation drain, current release marker, and recent warning/critical Beacon events. Use `norn platform smoke` when auth lives in the API runtime env rather than the interactive shell.

`norn platform proxy-plan` prints a no-blip reverse-proxy cutover plan. `proxy-status`, `proxy-render`, and `proxy-switch` manage optional local proxy state; they do not enable proxy-fronted upgrades by themselves.

Candidate APIs must not claim live queue work. The platform script runs candidates with operation recovery and operation workers disabled.

Before `upgrade` or `rollback`, check active operations when auth is available. `NORN_DRAIN_MODE` controls the platform script's behavior:

| Mode | Behavior |
|------|----------|
| `fail` | Refuse to proceed while active operations exist |
| `wait` | Wait for active operations to finish |
| `force` | Skip the drain gate |

## Runtime Watchers

The API starts a runtime watcher when Nomad or Consul and Beacon are available. It emits Beacon events when allocations transition to failed, lost, or unhealthy; when Consul service health changes to warning, critical, or recovered; and when periodic child jobs succeed, fail, are lost, or appear hung. Missed-run detection requires additional schedule-aware logic.

Beacon events can be acknowledged, snoozed, or reopened from the CLI, API, and Platform tab. Treat event state as operator workflow state, not a replacement for the original event payload or saga.

## Safe Repo Guidance

Keep this runbook portable:

- Use generic paths such as "the host that owns the Norn checkout".
- Use environment variables such as `NORN_API`, `NORN_TOKEN`, `NORN_API_TOKEN`, and `NORN_DRAIN_MODE`.
- Do not include private host aliases, personal usernames, local tunnel hostnames, bearer tokens, or machine-only paths.
- Put machine-specific shortcuts in local agent skills or private operator notes.
