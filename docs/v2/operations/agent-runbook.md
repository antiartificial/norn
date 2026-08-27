# Agent Runbook

This page is the repo-owned operating guide for agents and automation working on Norn v2. Keep it generic and portable: do not add personal hostnames, local user paths, API tokens, private tunnel domains, or machine-specific aliases here. Local Codex skills can layer those details on top.

## Orientation

Before editing or deploying a Norn-managed app, gather live platform evidence. Prefer the API and CLI over assumptions from checked-in specs.

Useful surfaces:

| Need | Surface |
|------|---------|
| Hosted services, reachability, placement | `GET /api/v1/services/manifest`, `norn services manifest` |
| Platform rollup | `GET /api/ops/platform`, `norn ops platform` |
| Active queue/drain state | `GET /api/operations/active`, `norn operations --active` |
| Deployment history | `GET /api/v1/deployments` |
| Stage checkpoints | `GET /api/v1/deployments/{id}/steps` |
| Fleet runner liveness | `GET /api/v1/fleet/plans/{planID}/attempts`, `norn fleet attempts <plan-id>` |
| App specs and runtime status | `GET /api/apps`, `GET /api/apps/{id}`, local `infraspec.yaml` |
| Durable app recovery | `GET/POST /api/v1/apps/{id}/snapshots`, versioned retention/restore/migration/rollback routes, `norn snapshots <app>` |
| Validation and rehearsal | `GET /api/validate`, `POST /api/apps/{id}/preflight`, `norn preflight <app> [ref]` |
| Deploy progress | `POST /api/apps/{id}/deploy`, `GET /api/saga/{sagaId}`, `norn saga <saga-id>` |
| Webhook delivery triage | `GET /api/webhooks/deliveries`, `norn webhooks` |
| Platform release history | `GET /api/platform/releases`, `norn platform releases` |
| Versioned client contract | `GET /api/v1/capabilities`, `WS /api/v1/events`, `GET /api/v1/operations/{id}` |
| Host boot/recovery state | `norn host status`, `norn host doctor`, `norn host assure`, `norn host queue-assure` |
| Production admission | `GET /api/v1/production/readiness`, `norn production check`, `norn production audit`, `norn production drills`, `norn host security plan` |
| Operational events | `GET /api/events`, `GET /api/events/{id}`, `norn events`, `norn alerts` |
| Control-plane health | `/api/health`, `/api/version`, `/metrics`, `norn smoke platform`, `norn platform smoke` |
| Observability bundle/services | `GET /api/observability/bundle`, `POST /api/observability/services/install`, `norn observability install` |
| Secret migration plan | `GET /api/secrets/migration-plan`, `norn secrets migrate-plan` |
| Networking truth | `GET /api/services/manifest`, `norn network` |

If a protected endpoint returns `401`, do not assume the platform is unhealthy. Verify auth context separately and fall back to public health/version endpoints, local DB checks, process manager state, or an authenticated shell when available.

## Durable Operations

App deploys, preflights, rollbacks, manual snapshots, pruning, restores, and
standalone migrations are queued in control-plane Postgres and claimed by the
API worker. Operation rows include status, kind, app, ref, saga id, payload,
attempts, max attempts, lock owner, lock expiry, next attempt, and last error.

Use active operations as the drain source before invasive work:

```bash
norn operations --active
```

Semantics:

- `app.preflight` is read-only and can retry safely.
- `app.deploy` is queued and drain-visible.
- Mutable work for one app is serialized across API replicas by a PostgreSQL
  advisory lock.
- Snapshot restore and standalone migration run once after mutation begins;
  interruption fails visibly for review rather than replaying unknown database
  side effects.
- Versioned recovery mutations require request-bound idempotency keys so a
  reconnect can recover the original operation instead of duplicating it.
- Deploy and rollback stages are recorded in `deployment_steps`.
- Use `norn deploy steps <deployment-id>` to inspect versioned checkpoint evidence.
- Use `norn fleet attempts <plan-id>` before calling a Fleet phase active.
  Dispatch alone is handoff evidence; only an unexpired attempt proves liveness.
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
norn platform preflight <pushed-commit-sha>
norn platform upgrade <pushed-commit-sha>
norn platform upgrade <pushed-commit-sha> --proxy
norn platform rebuild <full-sha> --verify
norn platform queue-preflight <pushed-commit-sha>
norn platform queue-upgrade <pushed-commit-sha>
norn platform releases
norn platform rollback <sha-prefix>
norn platform smoke
norn platform env -- <command>
norn platform proxy-plan
norn platform proxy-status
norn platform proxy-render
norn platform proxy-switch <port|host:port>
```

Resolve and push the exact commit before preflight. `HEAD` is acceptable for a
local development rehearsal, but operational promotion should use the same
immutable SHA for review, preflight, and upgrade. Platform subcommands add
existing Homebrew binary directories to the managed child process, so they can
find release tools through a thin SSH shell without a machine-specific `PATH`
prefix.

The default platform lane builds an isolated release, boots a candidate API on an alternate port, checks health/version, promotes the release symlink, restarts only the Norn API process, and runs postflight health.

For immutable-release recovery, use `norn platform rebuild <full-sha> --verify`.
It requires the full source SHA, the exact retained Go/Node/pnpm inputs, and
artifact checksum/signature verification. Prefer a verified local rollback
target. Under `require-signed`, rollback, preflight, and upgrade call the
configured `NORN_RELEASE_FETCH_HOOK` when the exact immutable target is absent,
then verify it before any candidate starts. Set
`NORN_RELEASE_SIGNATURE_POLICY=require-signed` for production. A
platform rollback does not undo database schema changes, so keep releases
backward compatible with the active schema across the rollback window.

When a local release is unavailable, configure the host-only GitHub Release
fallback: `NORN_RELEASE_FETCH_HOOK=platform-release-fetch-github`,
`NORN_RELEASE_VERIFY_HOOK=platform-release-verify-github`,
`NORN_RELEASE_REPOSITORY=owner/norn`, and
`NORN_RELEASE_PUBLIC_KEY=/secure/path/norn-release.pub`. Set
`NORN_RELEASE_SIGNATURE_POLICY=require-signed` in production. Public
repositories download anonymously. Private repositories should use only an
owned, non-symlink, exact-mode-`0600` Contents-read token file. The adapter
accepts only tag `platform-<fullsha>` with its matching archive, manifest,
signature, and SBOM assets. Keep provider, immutability-check, and
release-signing credentials in the protected workflow, never on a Norn host or
in the operator CLI.

When direct maintenance runs from a checkout, remember that checkout-local
`v2/scripts/platform-upgrade` is considered before the synchronized managed
copy. If the checkout is not proven to contain the same release tooling, pass
`--script /path/to/managed/platform-upgrade` explicitly. Never make an
immutable release writable merely because an older script tries to replace it.

On macOS, importer `f830b3c` and later accommodates release parents carrying
`com.apple.provenance`: it atomically publishes the owner-writable staging root
and immediately seals the installed tree. Do not remove provenance metadata as
a workaround. Verify the installed root is read-only and both manifest and
external signature checks report success.

The queued lane records `platform.preflight`, `platform.upgrade`, and
`platform.smoke` in the durable operations table. `com.norn.host-agent` claims
those allow-listed kinds and survives an API restart. Prefer the queued lane
for remote clients and UI-driven maintenance; keep direct script commands for
bootstrap and repair.

On a proxy-fronted host, `norn platform upgrade --proxy` keeps old and new APIs on private ports, switches the managed Caddy upstream, then stops the previous proxy-managed API after postflight succeeds. Do not use it on a direct LaunchAgent `:8800` install until the host has intentionally moved to proxy-fronted ingress.

`norn smoke platform` is the preferred post-upgrade smoke command when authenticated API access is available. It checks health, operation drain, current release marker, and recent warning/critical Beacon events. Use `norn platform smoke` when auth lives in the API runtime env rather than the interactive shell.

`norn platform proxy-plan` prints a no-blip reverse-proxy cutover plan. `proxy-status`, `proxy-render`, and `proxy-switch` manage optional local proxy state; they do not move a direct LaunchAgent install by themselves.

Candidate APIs must not claim live queue work. The platform script runs candidates with operation recovery and operation workers disabled.

Before `upgrade` or `rollback`, check active operations when auth is available. `NORN_DRAIN_MODE` controls the platform script's behavior:

| Mode | Behavior |
|------|----------|
| `fail` | Refuse to proceed while active operations exist |
| `wait` | Wait for active operations to finish |
| `force` | Skip the drain gate |

## Host Runtime Recovery

On a persistent macOS host, use the host runtime lane instead of ad hoc agents
whose state lives under `/tmp`:

```bash
norn host install --repo /path/to/norn
norn host status
norn host doctor
```

Before a one-time state migration, drain active operations and important batch
allocations. Stop the current Nomad and Consul agents, migrate both state
directories, then recover in dependency order:

```bash
norn host migrate-state \
  --from-nomad /path/to/current/nomad-data \
  --from-consul /path/to/current/consul-data
norn host recover
```

The managed launchd supervisor starts Docker, renders the current advertise
address, restores Consul and Nomad, restarts the Norn API, runs configured
bounded cron catch-ups, and invokes the assurance stage. A periodic LaunchAgent
repeats assurance: it verifies required IPv4 services, repairs explicitly
allowed missing or unhealthy apps, reconciles public and tailnet routes, and
probes the real user-facing endpoints. Persistent failures and recovery are
reported through correlated Beacon events. Run `norn host assure` for an
operator-triggered pass. Only apps in the installed `--required` policy may be
deployed or restarted automatically. Restart repair replaces active Nomad
allocations rather than re-registering an unchanged job, ensuring task network
and port bindings are recreated before assurance checks service health. User
LaunchAgents begin after login; use a system service or Linux host when the
runtime must recover before a user session exists.

## Production Admission

Run `norn production check` before enabling `NORN_PROFILE=production`. The
profile adds strict-secret, source, digest-pinned artifact, and live substrate
admission to deploys and preflights. Use `norn host security init` only to create
an inactive macOS PKI stage; it never authorizes a live cutover. Host-local
fragment activation remains refused until it has the same fleet token bootstrap
and rollback guarantees exercised by the Linux HA acceptance lane. See
[Production readiness](./production-readiness.md).

Production mutations reserve durable, integrity-signed audit receipts before
side effects and recheck live Nomad/Consul quorum plus external PostgreSQL
PITR/replica posture. Use `norn production audit` for receipts. Production
readiness also requires recent database restore, registry-digest rollback, and
node failover drills; bracket each exercise with `norn production drill start`
and `norn production drill complete`, then inspect with
`norn production drills`.

Use the [Linux HA acceptance lab](./ha-lab.md) for reproducible DigitalOcean
quorum, PostgreSQL failover/off-host PITR, signed/vulnerability-admitted deploys,
verified PostgreSQL/Consul/Nomad TLS, default-deny ACLs, workload identity,
private observability, control failover, guarded production activation, and
node-replacement exercises. It is isolated from the legacy k3s Terraform root;
secrets stay out of cloud-init and state uses a versioned locked backend.

The HA-lab convergence script disables Go VCS stamping explicitly. Preserve
that build invariant: linked worktrees or incomplete surrounding Git metadata
can otherwise stop Go before compilation. If convergence is interrupted, fix
the prerequisite and rerun the full convergence command; its Ansible work is
idempotent and safely rechecks already-completed tasks.

## Runtime Watchers

The API starts a runtime watcher when Nomad or Consul and Beacon are available. It emits Beacon events when allocations transition to failed, lost, or unhealthy; when Consul service health changes to warning, critical, or recovered; and when periodic child jobs succeed, fail, are lost, or appear hung. Missed-run detection requires additional schedule-aware logic.

Beacon events can be acknowledged, snoozed, or reopened from the CLI, API, and Platform tab. Treat event state as operator workflow state, not a replacement for the original event payload or saga.

## Assurance Surfaces

Use `norn observability install` when you need managed local Prometheus/Grafana/cAdvisor app directories. Review the generated app ports and host policy before deployment.

Use `norn secrets migrate-plan [app]` before touching plaintext env secrets. It prints only keys, locations, declared/encrypted status, and recommended action; it never prints values.

Use `norn validate --strict-secrets` or `NORN_STRICT_SECRETS=true` when a repo or host is ready for plaintext secret-like env findings to block validation/preflight.

Use `norn network` when endpoint reachability is confusing. It summarizes service exposure, endpoint scope, instance scope, and mode-specific guidance.

For destructive database restores, prefer the versioned `/api/v1` control route
and the exact `filename` returned by its snapshot inventory; web and native
clients use that durable path and it always creates a safety snapshot. The
current CLI uses the legacy synchronous route:
`norn snapshots <app> restore <compact-utc-timestamp> --yes --pre-restore`.
Use it only when that timestamp identifies exactly one inventory entry.

## Safe Repo Guidance

Keep this runbook portable:

- Use generic paths such as "the host that owns the Norn checkout".
- Use environment variables such as `NORN_API`, `NORN_TOKEN`, `NORN_API_TOKEN`, and `NORN_DRAIN_MODE`.
- Do not include private host aliases, personal usernames, local tunnel hostnames, bearer tokens, or machine-only paths.
- Put machine-specific shortcuts in local agent skills or private operator notes.
