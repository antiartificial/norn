# Upgrading Norn

Use this runbook when Norn itself is installed as the local LaunchAgent `com.norn.api`.

The safe upgrade path restarts only the Norn API process. Do not use `make down` for a production-ish local upgrade because it also stops Nomad and Consul, which can disrupt hosted apps.

## First-Class Platform Lane

```bash
cd /Users/0xadb/projects/norn

# Build into a versioned release directory and boot a candidate API on :18800.
norn platform preflight HEAD

# Promote the release, restart only com.norn.api, and rollback if postflight fails.
norn platform upgrade HEAD

# On proxy-fronted hosts, switch the managed upstream instead of restarting launchd.
NORN_PROXY_RELOAD=true norn platform upgrade HEAD --proxy
norn platform releases
norn platform rollback <sha-prefix>
norn platform smoke
norn platform env -- norn smoke platform
norn platform proxy-plan
norn platform proxy-status
norn platform proxy-render
norn platform proxy-switch 18802
norn smoke platform
```

The platform lane builds from an isolated git worktree into `$HOME/norn/releases/<sha>`, writes a `$HOME/norn/current` symlink, installs compatibility binaries into `$HOME/go/bin`, and health-checks a candidate API with recovery and operation workers disabled so preflight does not mark running work failed or claim queued jobs.

Use these environment variables when the repo or host layout differs:

| Variable | Default | Description |
|----------|---------|-------------|
| `NORN_PLATFORM_REPO` | script repo | Norn checkout to build |
| `NORN_RELEASES_DIR` | `$HOME/norn/releases` | Versioned release directory |
| `NORN_CURRENT_LINK` | `$HOME/norn/current` | Current-release symlink |
| `NORN_BIN_DIR` | `$HOME/go/bin` | Compatibility install directory |
| `NORN_CANDIDATE_PORT` | `18800` | Alternate-port candidate API |
| `NORN_TOKEN` / `NORN_API_TOKEN` | — | Optional bearer token for active-operation drain checks |
| `NORN_DRAIN_MODE` | `fail` | `fail`, `wait`, or `force` for active-operation drains |
| `NORN_SKIP_CANDIDATE_API` | `false` | Skip side-by-side candidate boot |
| `NORN_PLATFORM_UPGRADE_MODE` | `restart` | `restart` or `proxy`; `--proxy` sets this for upgrades |
| `NORN_API_ENV_FILE` | `$HOME/.config/norn/api.env.enc.json` | SOPS JSON env file for `platform smoke` and `platform env` |
| `NORN_SOPS_BIN` | `sops` | SOPS executable used for encrypted env loading |
| `NORN_PROXY_DIR` | `$HOME/norn/proxy` | Managed proxy state directory |
| `NORN_PROXY_CANDIDATE_PORT` | `18802` | Private candidate API port used by proxy upgrade mode |
| `NORN_PROXY_PID_FILE` | `$NORN_PROXY_DIR/api.pid` | Current proxy-managed API pid |
| `NORN_PROXY_RELOAD` | `false` | Reload Caddy after `proxy-switch` |

## Manual Fallback

The old direct-binary path still works when the platform lane itself is broken:

```bash
cd /Users/0xadb/projects/norn/v2
cd ui && pnpm build
cd ..
make build
install -m 0755 bin/norn-api /Users/0xadb/go/bin/norn-api
install -m 0755 bin/norn /Users/0xadb/go/bin/norn
launchctl kickstart -k gui/$(id -u)/com.norn.api
```

## Smoke Checks

```bash
norn version
norn ops platform
norn services
norn status
curl -sf http://127.0.0.1:8800/api/health
```

Open `http://127.0.0.1:8800` and check the Platform tab. The Platform tab should show service counts, deployment provenance, snapshot retention state, access counts, and observability status.

The Platform tab also lists installed platform releases and can start a rollback through the same `platform-upgrade` script used by the CLI.

When an authenticated API token is available, run:

```bash
norn smoke platform
```

This checks health, operation drain, current release metadata, and recent warning/critical Beacon events.

If the API token is only available through the LaunchAgent/SOPS runtime env, use:

```bash
norn platform smoke
```

For arbitrary authenticated checks under the same env:

```bash
norn platform env -- norn events --severity critical --limit 5
```

## Rollback

`norn platform upgrade` rolls back automatically when postflight health fails and `$HOME/norn/current` pointed at a previous release.

Prefer the first-class rollback command:

```bash
norn platform releases
norn platform rollback <sha-prefix>
```

Manual rollback is still a symlink flip plus compatibility binary install:

```bash
backup=$HOME/norn/releases/<previous-sha>

install -m 0755 "$backup/bin/norn-api" /Users/0xadb/go/bin/norn-api
install -m 0755 "$backup/bin/norn" /Users/0xadb/go/bin/norn
launchctl kickstart -k gui/$(id -u)/com.norn.api
```

Then rerun the smoke checks.

## Notes

- The root `Makefile` still targets the older non-v2 tree. Use `v2/Makefile` for v2 releases.
- `NORN_UI_DIR` should point at `/Users/0xadb/projects/norn/v2/ui/dist` when the API serves the built dashboard.
- Keep Nomad, Consul, Postgres, and app allocations running during a Norn API upgrade unless you are intentionally rebuilding the whole dev environment.
- A normal candidate API is a preflight check, not the active control plane. On proxy-fronted hosts, `norn platform upgrade --proxy` performs a managed upstream cutover; see [Platform Upgrades](/v2/architecture/platform-upgrades).
- App deploys, preflights, and rollbacks are queued in control-plane Postgres. The drain gate checks those active rows before platform upgrades; read-only preflights can retry, while interrupted mutable deploy stages fail visibly rather than being replayed blindly.
- `norn platform proxy-plan` prints the no-blip proxy design. `proxy-status`, `proxy-render`, and `proxy-switch` manage an optional local proxy config and upstream state. `platform upgrade --proxy` uses that state only when the host is already proxy-fronted and `NORN_PROXY_RELOAD=true`.
