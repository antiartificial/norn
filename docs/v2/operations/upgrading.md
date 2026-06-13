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
norn platform releases
norn platform rollback <sha-prefix>
```

The platform lane builds from an isolated git worktree into `$HOME/norn/releases/<sha>`, writes a `$HOME/norn/current` symlink, installs compatibility binaries into `$HOME/go/bin`, and health-checks a candidate API with recovery disabled so preflight does not mark running deployments or operations failed.

Use these environment variables when the repo or host layout differs:

| Variable | Default | Description |
|----------|---------|-------------|
| `NORN_PLATFORM_REPO` | script repo | Norn checkout to build |
| `NORN_RELEASES_DIR` | `$HOME/norn/releases` | Versioned release directory |
| `NORN_CURRENT_LINK` | `$HOME/norn/current` | Current-release symlink |
| `NORN_BIN_DIR` | `$HOME/go/bin` | Compatibility install directory |
| `NORN_CANDIDATE_PORT` | `18800` | Alternate-port candidate API |
| `NORN_DRAIN_MODE` | `fail` | `fail`, `wait`, or `force` for active-operation drains |
| `NORN_SKIP_CANDIDATE_API` | `false` | Skip side-by-side candidate boot |

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
- A candidate API is a preflight check, not the active control plane. Full no-blip cutover requires a local reverse proxy or launchd socket activation; see [Platform Upgrades](/v2/architecture/platform-upgrades).
