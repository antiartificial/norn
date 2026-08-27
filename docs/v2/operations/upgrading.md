# Upgrading Norn

Use this runbook when Norn itself is installed as the local LaunchAgent `com.norn.api`.

The safe upgrade path restarts only the Norn API process. Do not use `make down` for a production-ish local upgrade because it also stops Nomad and Consul, which can disrupt hosted apps.

## First-Class Platform Lane

```bash
cd /path/to/norn
git fetch origin
release_sha=$(git rev-parse origin/master)

# Build into a versioned release directory and boot a candidate API on :18800.
norn platform preflight "$release_sha"

# Promote the release, restart only com.norn.api, and rollback if postflight fails.
norn platform upgrade "$release_sha"

# On proxy-fronted hosts, switch the managed upstream instead of restarting launchd.
NORN_PROXY_RELOAD=true norn platform upgrade "$release_sha" --proxy
norn platform releases
norn platform rebuild "$release_sha" --verify
norn platform rollback <sha-prefix>
norn platform smoke
norn platform env -- norn smoke platform
norn platform proxy-plan
norn platform proxy-status
norn platform proxy-render
norn platform proxy-switch 18802
norn smoke platform

# Preferred remote/UI lane: durable and restart-safe.
norn platform queue-preflight "$release_sha"
norn platform queue-upgrade "$release_sha"
norn platform queue-rollback <sha-prefix>
norn platform queue-smoke
```

Use the exact pushed SHA intended for promotion. `HEAD` is convenient for a
local development rehearsal but can move between review and upgrade.

## Signed GitHub Release Checklist

For an operational signed release, merge the reviewed change to protected
`master` before building. Dispatch `.github/workflows/platform-release.yml`
from `master` with that exact 40-character SHA. Approve only the matching
`platform-release` environment run after authorization, UI, and all four
OS/architecture bundle jobs succeed.

Before host promotion, verify all of the following:

- tag `platform-<full-sha>` targets the same commit and reports
  `immutable=true`;
- every supported platform has one archive, manifest, signature, and SPDX SBOM;
- the host uses the protected workflow's Ed25519 public key and owner-only
  fetch/verify configuration;
- public repositories download anonymously; private mirrors use only an owned,
  non-symlink, exact-mode-`0600` Contents-read token file;
- the active-operation drain is clear and host fetch/verify hooks are executable.

With `require-signed`, rollback, preflight, and upgrade invoke the configured
fetch hook when the exact release directory is absent. They require an exact
full-SHA import, then run both the local manifest verifier and external Ed25519
verifier before candidate startup or promotion. If the command begins a local
build instead, stop: the selected script or configuration is stale.

The platform lane builds from an isolated git worktree into `$HOME/norn/releases/<sha>`, writes a `$HOME/norn/current` symlink, installs compatibility binaries into `$HOME/go/bin`, and health-checks a candidate API with recovery and operation workers disabled so preflight does not mark running work failed or claim queued jobs.

Upgrade, rollback, and manual proxy switching are host-serialized with an
advisory lock in the release root. A concurrent direct promotion fails closed;
a crashed process automatically releases the kernel lock, so rerunning the
intended operation is the recovery action.

Promotion also synchronizes and byte-verifies the persistent host agent, its
private CLI copy, `platform-upgrade`, `platform-release-manifest`, the signed
GitHub fetch/verify helpers, and `host-runtime`. This keeps unattended
recovery and prerequisite checks on the same revision as the API instead of
leaving launchd to invoke an older managed copy after an otherwise successful
upgrade. Pre-manifest releases are unverifiable and are refused. A first,
development-only migration can opt into one legacy rollback target with
`NORN_ALLOW_LEGACY_RELEASES=true`; production always refuses that escape hatch.

The queued lane is preferred when the operator is not already on the host. It
returns a durable operation ID, is executed by `com.norn.host-agent`, and can be
followed with `norn operations <operation-id>` even after the API restarts.

Platform subcommands add existing `/opt/homebrew/bin` and `/usr/local/bin`
directories to the managed child process before invoking the upgrade script.
This keeps direct platform maintenance reliable through non-interactive SSH
shells with a minimal `PATH`; queued work continues to execute in the managed
host-agent environment. The enrichment neither mutates the parent shell nor
replaces prerequisite checks, and missing tools still fail visibly.

Script discovery is portable and deterministic: an explicit `--script` or
`--repo` wins, followed by the current checkout, an adjacent installed script,
the persistent managed host copy, and finally `$HOME/projects/norn`. It does
not depend on a hard-coded operator account. Routine remote upgrades therefore
use the same managed script that the previous promotion synchronized. A
managed script likewise resolves its source checkout from an explicit
`NORN_PLATFORM_REPO`, its own repository, the current directory, or
`$HOME/projects/norn`, in that order.

A clean checkout can still be stale or historically divergent. In that case,
passing only `--repo` selects its checkout-local script. For bootstrap or repair,
pass the synchronized managed `platform-upgrade` with `--script` explicitly and
use `--repo` only for source-object resolution. If an old script attempts to
remove an immutable SHA directory, stop; do not relax directory permissions.

macOS release parents may carry `com.apple.provenance`. Importer `f830b3c` and
later keeps the staging root owner-writable through the atomic no-replace rename
and immediately seals the installed root, while descendants are sealed before
publication. Do not remove provenance metadata to work around an older
importer. A completed import must remain read-only and verify as `signed`.

For a first development migration from a pre-manifest current release, use a
temporary owner-only configuration with `allow-unsigned` and
`NORN_ALLOW_LEGACY_RELEASES=true` for that promotion only. Keep the persistent
configuration at `require-signed`, remove the temporary file on every exit, and
never use this escape under `NORN_PROFILE=production`. After two signed releases
exist locally, rehearse rollback, authenticated smoke, and re-promotion without
the escape.

Use these environment variables when the repo or host layout differs:

| Variable | Default | Description |
|----------|---------|-------------|
| `NORN_PLATFORM_REPO` | script repo | Norn checkout to build |
| `NORN_RELEASES_DIR` | `$HOME/norn/releases` | Versioned release directory |
| `NORN_NODE_BIN` | detected `node@24`, then `node` | Node.js executable; its version must exactly match the repository pin |
| `NORN_EXPECTED_NODE_VERSION` | repository pin, then `v24.19.0` | Exact Node release-build version |
| `NORN_EXPECTED_PNPM_VERSION` | repository pin, then `10.32.1` | Exact pnpm release-build version |
| `NORN_CURRENT_LINK` | `$HOME/norn/current` | Current-release symlink |
| `NORN_BIN_DIR` | `$HOME/go/bin` | Compatibility install directory |
| `NORN_HOST_CLI_BIN` | `$HOME/.config/norn/host/bin/norn` | CLI copy used by persistent host recovery and assurance |
| `NORN_CANDIDATE_PORT` | `18800` | Alternate-port candidate API |
| `NORN_TOKEN` / `NORN_API_TOKEN` | — | Optional bearer token for active-operation drain checks |
| `NORN_DRAIN_MODE` | `fail` | `fail`, `wait`, or `force` for active-operation drains |
| `NORN_DRAIN_EXCLUDE_OPERATION_ID` | — | Host-agent operation omitted from its own drain query |
| `NORN_SKIP_CANDIDATE_API` | `false` | Skip side-by-side candidate boot |
| `NORN_PLATFORM_UPGRADE_MODE` | `restart` | `restart` or `proxy`; `--proxy` sets this for upgrades |
| `NORN_API_ENV_FILE` | `$HOME/.config/norn/api.env.enc.json` | SOPS JSON env file for `platform smoke` and `platform env` |
| `NORN_SOPS_BIN` | `sops` | SOPS executable used for encrypted env loading |
| `NORN_PROXY_DIR` | `$HOME/norn/proxy` | Managed proxy state directory |
| `NORN_PROXY_CANDIDATE_PORT` | `18802` | Private candidate API port used by proxy upgrade mode |
| `NORN_PROXY_PID_FILE` | `$NORN_PROXY_DIR/api.pid` | Current proxy-managed API pid |
| `NORN_PROXY_RELOAD` | `false` | Reload Caddy after `proxy-switch` |
| `NORN_RELEASE_SIGNATURE_POLICY` | `allow-unsigned` | `require-signed` is forced by `NORN_PROFILE=production` |
| `NORN_RELEASE_FETCH_HOOK` | — | Optional missing-release fetch adapter |
| `NORN_RELEASE_VERIFY_HOOK` | — | External signature verifier; mandatory in production |
| `NORN_RELEASE_CONFIG_FILE` | `$HOME/.config/norn/release.env` | Optional mode-`0600` allowlisted host configuration loaded by direct and queued maintenance |
| `NORN_RELEASE_REPOSITORY` | — | GitHub `owner/repository` used by the fetch adapter |
| `NORN_RELEASE_PUBLIC_KEY` | — | Host path to the pinned Ed25519 public PEM |
| `NORN_RELEASE_GITHUB_TOKEN_FILE` | — | Preferred owned, non-symlink, exact mode-`0600` narrow Contents-read token file for private downloads |
| `NORN_RELEASE_GITHUB_TOKEN` | — | In-process narrow Contents-read token fallback for private downloads; public repositories need neither token form |
| `NORN_ALLOW_LEGACY_RELEASES` | `false` | Explicit one-time development migration escape; ignored in production |

For signed off-host artifacts, configure the protected workflow and host hooks
described in [Platform Upgrades](/v2/architecture/platform-upgrades). The
workflow accepts only an exact commit already merged to protected `master`,
keeps the signing key in the protected publish job, produces Linux and macOS
bundles for amd64 and arm64, and resumes an interrupted draft without replacing
an existing asset.

## Manual Fallback

The old direct-binary path still works when the platform lane itself is broken:

```bash
export NORN_PLATFORM_REPO=/path/to/norn
export NORN_BIN_DIR="${NORN_BIN_DIR:-$HOME/go/bin}"

cd "$NORN_PLATFORM_REPO/v2"
cd ui && pnpm build
cd ..
make build
mkdir -p "$NORN_BIN_DIR"
install -m 0755 bin/norn-api "$NORN_BIN_DIR/norn-api"
install -m 0755 bin/norn "$NORN_BIN_DIR/norn"
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

The following direct copies are break-glass recovery only. They bypass the
normal manifest/signature gate and do not update every managed helper:

```bash
releases_dir="${NORN_RELEASES_DIR:-$HOME/norn/releases}"
bin_dir="${NORN_BIN_DIR:-$HOME/go/bin}"
backup="$releases_dir/<previous-sha>"

mkdir -p "$bin_dir"
install -m 0755 "$backup/bin/norn-api" "$bin_dir/norn-api"
install -m 0755 "$backup/bin/norn" "$bin_dir/norn"
launchctl kickstart -k gui/$(id -u)/com.norn.api
```

Then rerun the smoke checks.

## Notes

- The root `Makefile` still targets the older non-v2 tree. Use `v2/Makefile` for v2 releases.
- `NORN_UI_DIR` can point at an explicit dashboard build. If it is unset, the API serves `$HOME/norn/current/ui` when that release directory exists.
- Keep Nomad, Consul, Postgres, and app allocations running during a Norn API upgrade unless you are intentionally rebuilding the whole dev environment.
- A normal candidate API is a preflight check, not the active control plane. On proxy-fronted hosts, `norn platform upgrade --proxy` performs a managed upstream cutover; see [Platform Upgrades](/v2/architecture/platform-upgrades).
- App deploys, preflights, and rollbacks are queued in control-plane Postgres. The drain gate checks those active rows before platform upgrades; read-only preflights can retry, while interrupted mutable deploy stages fail visibly rather than being replayed blindly.
- `norn platform proxy-plan` prints the no-blip proxy design. `proxy-status`, `proxy-render`, and `proxy-switch` manage an optional local proxy config and upstream state. `platform upgrade --proxy` uses that state only when the host is already proxy-fronted and `NORN_PROXY_RELOAD=true`.
