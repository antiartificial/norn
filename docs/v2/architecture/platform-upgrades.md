# Platform Upgrades

Norn treats upgrades to Norn itself as a separate platform lane. App deploys mutate Nomad jobs, snapshots, migrations, and routes. Platform upgrades mutate the control plane binary, CLI, and dashboard assets while leaving Nomad, Consul, Postgres, cloudflared, and app allocations running.

## Current Lane

```bash
git fetch origin
release_sha=$(git rev-parse origin/master)
norn platform preflight "$release_sha"
norn platform upgrade "$release_sha"
norn platform rebuild "$release_sha" --verify
norn platform upgrade "$release_sha" --proxy
norn platform queue-preflight "$release_sha"
norn platform queue-upgrade "$release_sha"
norn platform queue-rollback <sha-prefix>
```

Use the exact, pushed commit SHA intended for promotion. `HEAD` remains a useful
local development shorthand, but its meaning can move between preflight,
approval, and upgrade and therefore is not a reproducible release reference.

## Immutable artifacts and recovery

Every production platform artifact is identified by the full source commit SHA.
The immutable release lane records that binding and verifies both the published
checksum and release signature before rebuild or import. The general operator
CLI exposes `norn platform rebuild <full-sha> --verify`; it deliberately does
not package, sign, or publish release assets.

Rollback is local-first: promote an already-installed, verified release before
considering a remote fetch. When no local target matches, `platform-upgrade`
may call the configured `NORN_RELEASE_FETCH_HOOK` with the requested ref and
release-store path. That hook may only restore an artifact into the local store;
the managed verifier still requires the requested full SHA and checksum before
promotion, and production must set `NORN_RELEASE_SIGNATURE_POLICY=require-signed`.
Never treat a branch name, an ambiguous prefix, or an unverified downloaded
archive as a rollback target. A 7-40 character lowercase SHA prefix is accepted
only when it resolves to exactly one full-SHA release. Development may retain
an explicitly marked unsigned artifact under the default `allow-unsigned`
policy; it is not a production approval.

### GitHub Release fallback

The optional GitHub fallback is installed as two host-script adapters, not as
general operator CLI commands:

| Hook | Adapter | Invocation |
|---|---|---|
| `NORN_RELEASE_FETCH_HOOK` | `platform-release-fetch-github` | `<sha-or-prefix> <releases-dir>` |
| `NORN_RELEASE_VERIFY_HOOK` | `platform-release-verify-github` | `<release-dir> <release.json>` |

Set `NORN_RELEASE_REPOSITORY=owner/norn`,
`NORN_RELEASE_PUBLIC_KEY=/secure/path/norn-release.pub`, and production
`NORN_RELEASE_SIGNATURE_POLICY=require-signed`. The fetch adapter accepts a
read-only GitHub token from `NORN_RELEASE_GITHUB_TOKEN`, its safer
`NORN_RELEASE_GITHUB_TOKEN_FILE` equivalent, or `GH_TOKEN`. No token is needed
for a public repository. Optional API, cache, tag, OS, and architecture settings
remain host-runtime configuration. It resolves the release tag
`platform-<fullsha>` and fetches exactly:

```text
norn-platform-<fullsha>-<os>-<arch>.tar.gz
norn-platform-<fullsha>-<os>-<arch>.manifest.json
norn-platform-<fullsha>-<os>-<arch>.manifest.sig
norn-platform-<fullsha>-<os>-<arch>.sbom.spdx.json
```

The protected release workflow packages, signs, attaches those assets, and
publishes the GitHub Release after its own approval policy. The current public
distribution uses `NORN_RELEASE_REPOSITORY=antiartificial/norn` and requires no
download token. In a private mirror the signed assets remain private and the
host uses a narrowly scoped Contents-read token, preferably from an owned,
non-symlink regular file with exact mode `0600`. All hosts hold only the public
verification key; provider credentials and release-signing credentials stay
out of Norn hosts, and the normal operator CLI never publishes release assets.

Before the first publish, configure the repository deliberately:

1. Protect `master`, create a protected `platform-release` environment, and
   require its approval policy.
2. Enable GitHub release immutability for the repository.
3. Generate an Ed25519 key pair offline. Put only the private PEM in the
   environment secret `NORN_RELEASE_SIGNING_KEY`; put the public PEM in the
   environment variable `NORN_RELEASE_PUBLIC_KEY_PEM` and install that same
   public key on Norn hosts.
4. The normal Actions job token does not grant repository Administration read.
   Add a narrowly scoped environment secret named
   `NORN_RELEASE_IMMUTABILITY_CHECK_TOKEN` with repository Administration
   read-only access. It is used only to confirm that immutability is enabled.
5. Dispatch `.github/workflows/platform-release.yml` from `master` with an
   exact 40-character commit SHA already reachable from `origin/master`.

The workflow builds requested source without signing credentials, then gives
only the protected publish job the Ed25519 key and `contents:write`. That job
executes release tooling from the current protected workflow ref, never from
the historical commit being packaged. An interrupted upload remains an
unpublished draft. Re-running the same SHA compares every existing remote asset
byte-for-byte, uploads only missing assets, and publishes only after all 16
platform assets match. A published release, a mismatched draft, or a standalone
existing tag is a hard conflict and is never overwritten.

Platform-binary rollback does not reverse database migrations. Releases must
remain backward-compatible with the database schema across the promotion and
rollback window; incompatible schema changes need a separate, tested backup,
migration, and recovery procedure.

The UI release build uses the exact toolchain pins in `v2/ui/package.json`:
Node `24.19.0` and pnpm `10.32.1`. Update either pin only in a deliberate
release change that updates the lockfile, rebuilds the immutable artifact, and
repeats checksum/signature verification; do not silently float to newer Node
or pnpm versions during an operator rebuild.

The direct commands shell out to `v2/scripts/platform-upgrade` on the host that
owns the Norn checkout. The `queue-*` commands create durable control API
operations claimed by the independent `norn-host-agent`; the agent invokes the
same fixed script subcommands and survives the API restart. The script:

1. Resolves the requested git ref.
2. Creates an isolated git worktree for that exact commit.
3. Builds UI, API, CLI, host agent, and managed scripts into `$HOME/norn/releases/<sha>`.
4. Starts the candidate API on `127.0.0.1:18800`.
5. Sets `NORN_SKIP_DEPLOYMENT_RECOVERY=true`, `NORN_SKIP_OPERATION_RECOVERY=true`, and `NORN_SKIP_OPERATION_WORKER=true` for the candidate so it does not mark running work failed or claim queued work.
6. Checks `/api/health` and `/api/version`.
7. On normal upgrade, atomically replaces compatibility and managed-host files,
   flips `$HOME/norn/current` last, restarts `com.norn.api`, and runs postflight
   health. Detected activation failures restore the previous files before the
   restart.
8. If postflight fails and a previous current release exists, flips back, reinstalls the previous binaries, and restarts again.

Activation spans several compatibility paths, so a process or machine failure
between individual file replacements is not one filesystem transaction. The
`current` link remains the commit marker and moves last; rerunning the same
upgrade safely converges the managed copies. Removing this last interruption
boundary requires every launcher to execute through the single `current` link.
Direct upgrade, rollback, and manual proxy-switch processes are serialized by
the private advisory lock `$NORN_RELEASES_DIR/.promotion.lock`, held through
drain, activation, restart or proxy cutover, postflight, and automatic rollback.
The kernel drops the lock when a process exits or crashes, so a later invocation
can recover without deleting stale lock state.

Direct platform subcommands enrich their child-process `PATH` with existing
`/opt/homebrew/bin` and `/usr/local/bin` directories before running the managed
script. This makes preflight, upgrade, rollback, smoke, proxy, and environment
commands reliable through thin non-interactive SSH shells without requiring an
operator-specific `PATH` prefix. It does not change the parent shell or provide
missing dependencies; `norn platform env -- <command>` receives the same
bounded tool-path behavior.

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

Norn stores app, platform, and host work in a durable operations queue in
control-plane Postgres. Rows carry compact status, risk, ref, timing, payload,
attempts, lease owner, lease expiry, next attempt, and last error.

The queue lives in the same control-plane Postgres table family, not Nomad, Redis, Valkey, or an app container.

Reasons:

- Platform jobs must still work when Nomad is degraded.
- Norn already requires Postgres for deployments and saga events.
- The API can claim rows with `FOR UPDATE SKIP LOCKED`, making workers safe across restart or future multiple API instances.
- Saga events remain the immutable user-facing log; queue rows only track claim state, retries, and resumability.

The app worker runs inside `norn-api`. A separate `norn-host-agent` claims only
allow-listed platform and host kinds, renews its lease, and records bounded
receipts plus durable control events. A restarted API leaves those active
maintenance leases untouched.

Current queued job types:

| Kind | Purpose |
|------|---------|
| `app.preflight` | Run validation, source prep, build, and tests with safe retries |
| `app.deploy` | Queue app deploys and run them under worker/drain visibility |
| `app.rollback` | Queue app rollback through the same worker/drain lane |
| `platform.preflight` | Build and candidate-health-check a platform release from the API/UI |
| `platform.upgrade` | Promote a preflighted release and run rollback-capable postflight |
| `platform.smoke` | Run authenticated platform smoke outside the API process |
| `host.assure` | Repair allow-listed host services/routes and probe endpoints |

Deploy and rollback execution writes durable stage rows to `deployment_steps`.
On restart, interrupted deploys are requeued only if no mutable stage has
started. Mutable stages include snapshot, migration, Nomad submit, health,
forge, and cleanup.

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
