# Getting Started

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.25+ | API and CLI |
| pnpm | 10+ | UI dependencies |
| Node.js | 22+ | UI build |
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
| `NORN_PORT` | `8800` | API listen port |
| `NORN_BIND_ADDR` | `127.0.0.1` | API bind address |
| `NORN_DATABASE_URL` | `postgres://norn:norn@localhost:5432/norn_v2?sslmode=disable` | PostgreSQL connection string |
| `NORN_UI_DIR` | — | Path to built UI assets (for embedded serving) |
| `NORN_APPS_DIR` | `~/projects` | Directory to scan for `infraspec.yaml` files |
| `NORN_GIT_TOKEN` | — | GitHub token for cloning private repos |
| `NORN_GIT_SSH_KEY` | — | SSH key path for git operations |
| `NORN_API_TOKEN` | — | Bearer token for API authentication |
| `NORN_REGISTRY_URL` | — | Container registry URL (e.g. `ghcr.io/username`) |
| `NORN_NETWORK_MODE` | `local` | Reachability mode used by health, manifest, and validation (`local`, `tailnet`, or `public`) |
| `NORN_NOMAD_ADDR` | `http://localhost:4646` | Nomad API address |
| `NORN_CONSUL_ADDR` | `http://localhost:8500` | Consul API address |
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
repo:
  url: git@github.com:you/hello-world.git
  autoDeploy: true
build:
  dockerfile: Dockerfile
processes:
  web:
    port: 3000
    command: ./server
    health:
      path: /health
    scaling:
      min: 1
```

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

The pipeline runs 9 steps with real-time progress: clone, build, test, snapshot, migrate, submit, healthy, forge, cleanup.

## Next Steps

- [Concepts](/v2/guide/concepts) — understand the infraspec model and process types
- [Release Recap](/v2/guide/release-recap) — review what the current v2 release line includes
- [Infraspec Reference](/v2/guide/infraspec-reference) — every field documented
- [Architecture Overview](/v2/architecture/overview) — how the pieces fit together
- [Object Storage](/v2/infrastructure/object-storage) — Garage-backed buckets for local apps
- [Observability](/v2/infrastructure/observability) — local Prometheus/Grafana metrics with bounded retention
- [CLI Commands](/v2/cli/commands) — full command reference
- [Next Steps](/v2/guide/next-steps) — current development priorities
