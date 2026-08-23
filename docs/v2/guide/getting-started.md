# Getting Started

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.25+ | API and CLI |
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
```

This produces two binaries in `bin/`:

- `bin/norn-api` — the API server
- `bin/norn` — the CLI

Install the CLI to your PATH:

```bash
make install   # copies bin/norn to ~/go/bin/
```

## Database Setup

Create the database (Norn runs auto-migrations on startup):

```bash
createdb norn_v2
```

The default connection string is `postgres://norn:norn@localhost:5432/norn_v2?sslmode=disable`. Override with `NORN_DATABASE_URL`.

## Development Mode

```bash
make dev   # starts API (:8800) + UI (:5173)
```

The API serves at `http://localhost:8800` and the UI at `http://localhost:5173`.

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `NORN_PROFILE` | `development` | Runtime admission profile. `production` fails startup unless explicit auth, verified TLS substrate/database endpoints, registry, audit signing, strict secrets, and legacy-signing retirement are configured; it also enables live substrate and immutable artifact admission. |
| `NORN_PORT` | `8800` | API listen port |
| `NORN_BIND_ADDR` | `127.0.0.1` | API bind address |
| `NORN_DATABASE_URL` | `postgres://norn:norn@localhost:5432/norn_v2?sslmode=disable` | PostgreSQL connection string |
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
| `NORN_COSIGN_PATH` | `cosign` | Cosign executable used by artifact admission |
| `NORN_TRIVY_PATH` | `trivy` | Trivy executable used by artifact admission |
| `NORN_NETWORK_MODE` | `local` | Reachability mode used by health, manifest, and validation (`local`, `tailnet`, or `public`) |
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
deploy: true
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

New services should begin with `deploy: false`. The web dashboard can create a
named endpoint or worker draft with conservative health, scaling, and resource
defaults. Native clients can adopt the same API contract independently. Review
the generated InfraSpec, run preflight, then use the explicit **Enable
deployments** action. The example above is already enabled only to keep this
first-deploy walkthrough direct.

2. Open the dashboard at `http://localhost:5173` — your app should appear automatically.

3. Rehearse the deploy from the CLI:

```bash
norn preflight hello-world HEAD
```

Preflight validates the infraspec, prepares source, builds locally, and runs tests without touching Nomad or cloudflared.

4. Deploy from the CLI:

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
