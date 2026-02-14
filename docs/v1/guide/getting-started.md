# Getting Started

## Prerequisites

Norn requires the following tools. Run `make prereqs` to check which are installed:

| Tool | Purpose | Install |
|------|---------|---------|
| **Go** (1.25+) | API server and CLI | `brew install go` |
| **pnpm** | UI package manager | `npm i -g pnpm` |
| **PostgreSQL** | Primary datastore | [Postgres.app](https://postgresapp.com) |
| **Docker** | Container builds | [Docker Desktop](https://docker.com/products/docker-desktop) |
| **kubectl** | Kubernetes CLI | `brew install kubectl` |
| **sops** | Secret encryption | `brew install sops` |
| **age** | Encryption backend | `brew install age` |

Kubernetes is **optional for local development**. The API starts in local-only mode when no cluster is available — K8s-dependent actions (deploy, restart, rollback, logs) return a clear 503 error.

## Setup

```bash
git clone git@github.com:antiartificial/norn.git
cd norn
make setup
```

This runs three steps:

1. **`make prereqs`** — checks that all required tools are installed
2. **`make db`** — creates the `norn` PostgreSQL user and `norn_db` database (idempotent)
3. **`make deps`** — installs UI dependencies (pnpm), downloads Go modules for API and CLI

## Start development

```bash
make dev
```

This starts two processes in parallel:

- **API** at [localhost:8800](http://localhost:8800) — Go server with chi router
- **UI** at [localhost:5173](http://localhost:5173) — React + Vite dev server

Open the UI and you'll see a welcome tour walking you through the basics.

![Norn Dashboard](/screenshots/dashboard.png)

## Build the CLI

```bash
make cli        # build to bin/norn
make install    # build + symlink to /usr/local/bin/norn
```

Verify the installation:

```bash
norn version
```

![norn version](/screenshots/cli-version.png)

The CLI connects to the API at `http://localhost:8800` by default. Override with `NORN_URL` or `--api`:

```bash
export NORN_URL=https://norn.example.com
norn status
```

## First deploy walkthrough

### 1. Create an infraspec

Create `~/projects/myapp/infraspec.yaml`:

```yaml
app: myapp
role: webserver
port: 3000
healthcheck: /health
hosts:
  internal: myapp-service
build:
  dockerfile: Dockerfile
  test: npm test
```

### 2. Verify discovery

```bash
norn status
```

You should see `myapp` listed with a health indicator.

### 3. Forge infrastructure

Before deploying, forge the Kubernetes resources:

```bash
norn forge myapp
```

This creates the K8s deployment, service, and optionally configures Cloudflare tunnel routing.

### 4. Deploy

```bash
norn deploy myapp HEAD
```

The CLI shows a live pipeline with spinners for each step: clone, build, test, snapshot, migrate, deploy, cleanup.

### 5. Check health

```bash
norn health          # all backing services
norn status myapp    # detailed app view with pods
norn logs myapp      # stream live logs
```

## Shared infrastructure

Some apps need Valkey (Redis-compatible KV) or Redpanda (Kafka-compatible event streaming):

```bash
make infra       # start Valkey (:6379) + Redpanda (:19092)
make infra-stop  # stop them
```

These run via Docker Compose in the `infra/` directory.

## Diagnostics

```bash
make doctor
```

Checks the health of PostgreSQL, the API, UI, Valkey, Redpanda, Kubernetes, and SOPS age key — with clear status indicators and fix hints.

## Next steps

- [Concepts](/v1/guide/concepts) — understand infraspec anatomy and app roles
- [Infraspec Reference](/v1/guide/infraspec-reference) — every field documented
- [Architecture Overview](/v1/architecture/overview) — how the pieces fit together
- [CLI Commands](/v1/cli/commands) — full command reference
