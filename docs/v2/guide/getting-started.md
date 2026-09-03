# Getting Started

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.26.6 | API, CLI, and host agent |
| pnpm | 10+ | UI dependencies |
| Node.js | 24 LTS | UI and docs builds |
| PostgreSQL | 16+ | Application database |
| Docker | 24+ | Container builds |
| Nomad | 1.9+ | Job scheduling |
| Consul | 1.20+ | Service discovery |
| sops | 3.9+ | Secret encryption |
| age | 1.2+ | Encryption keys |

## Installation

```bash
git clone git@github.com:antiartificial/norn.git
cd norn/v2
make build
cd ui && pnpm install && cd ..
```

This produces three binaries in `bin/`:

- `bin/norn-api` — the API server
- `bin/norn` — the CLI
- `bin/norn-host-agent` — the independent durable platform/host maintenance worker

Install the CLI to your PATH:

```bash
mkdir -p "$HOME/go/bin"
install -m 0755 bin/norn "$HOME/go/bin/norn"
export PATH="$HOME/go/bin:$PATH"
```

The v2 Makefile intentionally has no `install` target. You can also leave the
binary in place and run it as `bin/norn`.

## Database Setup

For local development, create the database as your current PostgreSQL user and
connect over the local Unix socket. Norn runs its schema migrations on startup:

```bash
createdb norn_v2
export NORN_DATABASE_URL='postgresql:///norn_v2?sslmode=disable'
```

Run Norn from the same shell so it inherits `NORN_DATABASE_URL`. This avoids
documenting a database owned by the current user while attempting to connect as
an unrelated role. The code default,
`postgres://norn:norn@localhost:5432/norn_v2?sslmode=disable`, is available for
installations that deliberately create that login and database ownership.

## Development Mode

```bash
# Starts local Consul, Nomad, and the API in the background.
make up

# Start the UI separately.
cd ui
pnpm dev
```

The API serves at `http://localhost:8800` and the UI at
`http://localhost:5173`. `make dev` is an API-only foreground target; it does
not start the UI. Use `make down` to stop the background API, Nomad, and Consul
started by `make up`.

## Pair a native app

The macOS app should use device enrollment instead of receiving the root
`NORN_API_TOKEN`. In the app, choose **Add Server**, enter the HTTPS control
plane URL, review the requested scopes, and start pairing. Then approve its
ten-minute code from an existing administrator session:

```bash
export NORN_URL=https://norn.example.com
export NORN_TOKEN='<administrator credential>'
norn access enrollments --status pending
norn access approve ABCD-EFGH --scope api:read,events:read
```

The app completes the exchange automatically. Its device identity is protected
by Secure Enclave when available (with a device-only Keychain fallback), the
bearer is stored in Keychain, and the 30-day credential renews while the app is
active within seven days of expiry. Pairing cannot grant `admin`; add operation
scopes only when that Mac needs them:

| Scope | Native capability |
|---|---|
| `api:read,events:read` | Observe control state and live events |
| `api:write` | Create apps and manage app recovery |
| `platform:operate` | Queue upgrades, rollback, and smoke checks |
| `host:operate` | Queue host recovery and assurance |
| `fleet:operate` | Plan and hand off fleet changes |
| `apps:exec` | Request audited, step-up-protected exec sessions |

Use `norn access devices` to audit enrolled clients and
`norn access revoke-device <device-id> --confirm` to revoke a lost or retired
device. Manual scoped-token entry remains a compatibility/recovery option in
the app, but it does not have native renewal or device-level revocation.

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `NORN_PROFILE` | `development` | Runtime admission profile. `production` fails startup unless explicit auth, verified TLS substrate/database endpoints, registry, audit signing, strict secrets, and legacy-signing retirement are configured; it also enables live substrate and immutable artifact admission. |
| `NORN_ENVIRONMENT` | `development` | Release lane: `development`, `staging`, or `production`. `NORN_PROFILE=production` requires this variable to be explicitly set to `staging` or `production`; an omitted or `development` value fails closed during migration. `NORN_ENVIRONMENT=production` also requires `NORN_PROFILE=production`. |
| `NORN_PORT` | `8800` | API listen port |
| `NORN_BIND_ADDR` | `127.0.0.1` | API bind address |
| `NORN_DATABASE_URL` | `postgres://norn:norn@localhost:5432/norn_v2?sslmode=disable` | PostgreSQL connection string |
| `NORN_FLEET_CONFIG` | — | Read-only checked-out `norn-fleet` Cluster document |
| `NORN_FLEET_GITHUB_APP_ID` | — | Repository-scoped GitHub App ID or client ID for fleet GitOps actions |
| `NORN_FLEET_GITHUB_INSTALLATION_ID` | — | Installation ID restricted to the private fleet repository |
| `NORN_FLEET_GITHUB_PRIVATE_KEY_FILE` | — | Mode-`0600` GitHub App private key path; key contents are never returned by Norn |
| `NORN_FLEET_GITHUB_REPOSITORY` | — | Exact `owner/repository` allowlist for fleet mutations |
| `NORN_FLEET_GITHUB_ENVIRONMENT` | — | Required when the Fleet GitHub bridge is configured. Exact lane: `staging` or `production`; it must equal `NORN_ENVIRONMENT`, and Norn never chooses a default. |
| `NORN_FLEET_GITHUB_CONFIG_PATH` | — | Repository-relative Cluster YAML changed by plan pull requests |
| `NORN_UI_DIR` | — | Path to built UI assets (for embedded serving) |
| `NORN_APPS_DIR` | `~/projects` | Directory to scan for `infraspec.yaml` files |
| `NORN_GIT_TOKEN` | — | GitHub token for cloning private repos |
| `NORN_GIT_SSH_KEY` | — | SSH key path for git operations |
| `NORN_API_TOKEN` | — | Bearer/root signing secret; minimum 32 bytes and required for non-loopback binds |
| `NORN_AUDIT_SIGNING_KEY` | — | Separate HMAC key for durable mutation receipt integrity; minimum 32 bytes and required in production |
| `NORN_AUDIT_PREVIOUS_SIGNING_KEYS` | — | Comma-separated previous audit HMAC keys retained temporarily so historical receipts remain verifiable during rotation |
| `NORN_AUDIT_RETENTION_DAYS` | `365` | Completed mutation receipt retention; production requires at least 90 days |
| `NORN_REQUIRE_EXPLICIT_AUTH` | `false` | Require bearer or validated Cloudflare Access credentials on non-public routes, including loopback; disables temporary IP-grant compatibility |
| `NORN_STRICT_SECRETS` | `false` | Promote plaintext secret-like env findings to validation errors. Automatically enabled by the production profile. |
| `NORN_LEGACY_TOKEN_SIGNING_UNTIL` | `2026-08-15T00:00:00Z` | Retirement deadline for the pre-v2.17 raw-key JWT signature; invalid values fail closed |
| `NORN_REGISTRY_URL` | — | Container registry URL (e.g. `ghcr.io/username`) |
| `NORN_ARTIFACT_SIGNING_PUBLIC_KEY` | — | Cosign public key used for production deploy and rollback admission |
| `NORN_ARTIFACT_DENY_SEVERITIES` | `HIGH,CRITICAL` | Comma-separated Trivy severities that reject a production artifact |
| `NORN_COSIGN_PATH` | `cosign` | Cosign executable used by artifact admission. Production requires an absolute path; development may use PATH lookup. |
| `NORN_TRIVY_PATH` | `trivy` | Trivy executable used by artifact admission. Production requires an absolute path; development may use PATH lookup. |
| `NORN_NETWORK_MODE` | `local` | Reachability mode used by health, manifest, and validation (`local`, `tailnet`, or `public`) |
| `NORN_WORKLOAD_CONNECTOR` | `nomad-consul` | Explicit scheduler/runtime connector. `apple-container` is optional local macOS development mode; production requires the default |
| `NORN_NOMAD_ADDR` | `http://localhost:4646` | Nomad API address |
| `NORN_CONSUL_ADDR` | `http://localhost:8500` | Consul API address |
| `NORN_INGRESS_URL` | — | Stable regional Traefik origin used for endpoint routing, such as `http://127.0.0.1:18080` |
| `NORN_EXTERNAL_INGRESS` | `false` | Leave DNS/global-edge routing to an external controller and skip local cloudflared mutation after regional readiness |
| `NORN_S3_ENDPOINT` | — | S3-compatible storage endpoint |
| `NORN_S3_ACCESS_KEY` | — | S3 access key |
| `NORN_S3_SECRET_KEY` | — | S3 secret key |
| `NORN_S3_REGION` | `auto` | S3 region |
| `NORN_S3_USE_SSL` | `true` | Use SSL for S3 (set `false` to disable) |
| `NORN_S3_PROVIDER` | `s3` | Storage provider hint (`garage` enables path-style defaults) |
| `NORN_S3_FORCE_PATH_STYLE` | `false` | Force path-style S3 bucket lookup |
| `NORN_GARAGE_ADMIN_ENDPOINT` | — | Garage admin API URL for managed buckets and app keys |
| `NORN_GARAGE_ADMIN_TOKEN` | — | Garage admin API token |
| `NORN_ALLOWED_ORIGINS` | — | Comma-separated additional CORS origins |
| `NORN_CF_ACCESS_TEAM_DOMAIN` | — | Cloudflare Access team domain |
| `NORN_CF_ACCESS_AUD` | — | Cloudflare Access AUD tag |
| `NORN_WEBHOOK_SECRET` | — | Shared secret for GitHub webhook validation |

## First Deploy

1. Create an `infraspec.yaml` in your app directory (must be under `NORN_APPS_DIR`):

```yaml
name: hello-world
deploy: false
repo:
  url: git@github.com:you/hello-world.git
  autoDeploy: true
build:
  dockerfile: Dockerfile
  # Production uses a CI-published, signed digest here:
  # image: ghcr.io/you/hello-world@sha256:...
processes:
  web:
    port: 3000
    command: ./server
    health:
      path: /health
    scaling:
      min: 1
```

New services begin with `deploy: false`. The web dashboard can create a
named endpoint or worker draft with conservative health, scaling, and resource
defaults. Native clients can adopt the same API contract independently. Review
the generated InfraSpec and run preflight before using the explicit **Enable
deployments** action.

2. Open the dashboard at `http://localhost:5173` — your app should appear automatically.

3. Rehearse the deploy from the CLI:

```bash
norn preflight hello-world HEAD
```

Preflight validates the infraspec, prepares source, builds locally, and runs tests without touching Nomad or cloudflared.

4. Enable deployment from the Apps dashboard, or use the versioned control API:

```bash
curl --fail-with-body -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"enabled":true}' \
  http://127.0.0.1:8800/api/v1/apps/hello-world/deployment
```

Add `Authorization: Bearer $NORN_API_TOKEN` when explicit authentication is
enabled. The current CLI has no deployment-gate subcommand; `norn preflight`
can inspect a disabled draft, while `norn deploy` sees it only after this gate
is enabled.

5. Deploy from the CLI:

```bash
norn deploy hello-world HEAD
```

The pipeline runs the source and artifact admission gates plus clone, build,
test, snapshot, migrate, regional submit/readiness, forge, and cleanup with
real-time progress.

## Next Steps

- [Concepts](/v2/guide/concepts) — understand the infraspec model and process types
- [Release Recap](/v2/guide/release-recap) — review what the current v2 release line includes
- [Infraspec Reference](/v2/guide/infraspec-reference) — every field documented
- [Architecture Overview](/v2/architecture/overview) — how the pieces fit together
- [Object Storage](/v2/infrastructure/object-storage) — Garage-backed buckets for local apps
- [Observability](/v2/infrastructure/observability) — local Prometheus/Grafana metrics with bounded retention
- [CLI Commands](/v2/cli/commands) — full command reference
- [Next Steps](/v2/guide/next-steps) — current development priorities
