# Norn

A personal control plane for self-hosted infrastructure. Named after the three Norse fates who determine the destiny of all beings — **Urd** (past), **Verdandi** (present), and **Skuld** (future).

Norn discovers your apps, shows their health, and lets you deploy, restart, and roll back from a dashboard or the terminal. [Read the docs](https://antiartificial.github.io/norn/)!

## Quick start

```bash
make setup   # check prereqs, create database, install deps
make dev     # start API (:8800) + UI (:5173)
```

Open [localhost:5173](http://localhost:5173). A welcome tour will walk you through the basics.

### CLI

```bash
make cli              # build the CLI to bin/norn
bin/norn status       # list all apps
bin/norn deploy <app> <sha>   # deploy with live progress
bin/norn logs <app>   # stream pod logs (fullscreen)
bin/norn health       # check all backing services
```

The CLI uses [Charm](https://charm.sh) libraries for a rich terminal experience — spinners, progress tracking, styled tables, and live-updating deploy pipelines.

## How it works

Norn scans `~/projects/` for directories containing an `infraspec.yaml`. Each file declares an app and its infrastructure dependencies:

```yaml
app: mail-agent
role: webserver           # webserver | worker | cron
port: 80
healthcheck: /health
hosts:
  external: mail.slopistry.com
  internal: mail-agent-service
build:
  dockerfile: Dockerfile
  test: npm test
services:
  postgres:
    database: mailagent_db
  kv:
    namespace: mail-agent
  events:
    topics: [mail.inbound, mail.processed]
secrets:
  - DATABASE_URL
  - SMTP_API_KEY
migrations:
  command: npm run db:migrate
  database: mailagent_db
artifacts:
  retain: 5
```

The dashboard shows each app's health, pods, commit SHA, hostnames, and connected services. Actions are one click away.

## Deploy pipeline

When you deploy a commit, Norn runs a five-step pipeline:

| Step | What happens | On failure |
|------|-------------|------------|
| **Build** | `docker build -t app:sha .` | Pipeline stops |
| **Test** | Runs your test command in the built image | Pipeline stops |
| **Snapshot** | `pg_dump` of the app's database | Pipeline stops |
| **Migrate** | Runs schema migrations | Pipeline stops, snapshot available for restore |
| **Deploy** | Updates the K8s deployment image | Rollback available |

Every step is persisted in PostgreSQL. If Norn crashes mid-deploy, it marks in-flight deploys as failed on restart — no inconsistent state.

Progress streams to the UI in real-time over WebSocket. The CLI shows the same pipeline with spinners and step-by-step progress.

## Secrets

Secrets are encrypted at rest with [SOPS](https://github.com/getsops/sops) + [age](https://github.com/FiloSottile/age). Each app can have a `secrets.enc.yaml`:

```bash
# Create/edit secrets for an app
echo 'DATABASE_URL: postgres://...' > ~/projects/mail-agent/secrets.enc.yaml.tmp
sops --encrypt --input-type yaml --output-type yaml \
  ~/projects/mail-agent/secrets.enc.yaml.tmp > ~/projects/mail-agent/secrets.enc.yaml

# Or update via API
curl -X PUT http://localhost:8800/api/apps/mail-agent/secrets \
  -H 'Content-Type: application/json' \
  -d '{"DATABASE_URL": "postgres://..."}'
```

The UI shows secret *names* only. Values never leave the server. On deploy, Norn syncs decrypted values to a K8s Secret.

## Shared infrastructure

One instance of each service, namespaced per app:

| Service | Purpose | Multi-tenancy |
|---------|---------|---------------|
| **PostgreSQL** | Application databases | Per-app database (`mailagent_db`, etc.) |
| **Valkey** | Key-value store (Redis-compatible) | ACL users with key prefix restrictions |
| **Redpanda** | Event streaming (Kafka-compatible) | Topic prefixes + ACLs |

```bash
make infra       # start Valkey + Redpanda
make infra-stop  # stop them
```

PostgreSQL runs via Postgres.app on the host.

## Commands

```
make setup       One-time setup: check tools, create DB, install deps
make dev         Start API + UI for local development
make cli         Build the CLI to bin/norn
make test        Run all tests (Go + TypeScript)
make doctor      Check health of all services
make infra       Start Valkey + Redpanda (docker compose)
make build       Production build (API server + CLI + UI static)
make docker      Build Docker image
make clean       Remove build artifacts
```

Kubernetes is optional for local development — the API starts in local-only mode when no cluster is available. K8s-dependent actions (deploy, restart, rollback, logs) return a clear 503 error.

## CLI reference

```
norn status            List all apps with health, commit, hosts, services
norn status <app>      Detailed view: pods, deployments, services, secrets
norn deploy <app> <sha> Deploy a commit with live pipeline progress
norn restart <app>     Rolling restart with spinner
norn rollback <app>    Rollback to previous deployment
norn logs <app>        Stream pod logs (fullscreen, scrollable)
norn secrets <app>     List secret names (values stay encrypted)
norn health            Check all backing services (PG, K8s, Valkey, Redpanda, SOPS)
norn version           Version and API endpoint info
```

Set `NORN_URL` or use `--api` to point at a different API server.

## Architecture

```
norn/
├── api/                 Go backend (chi router, K8s client, pipeline)
│   ├── handler/         REST + WebSocket handlers
│   ├── hub/             WebSocket broadcast hub
│   ├── k8s/             Kubernetes API client
│   ├── model/           infraspec parser, data models
│   ├── pipeline/        Build → test → snapshot → migrate → deploy
│   ├── secrets/         SOPS encrypt/decrypt + K8s sync
│   └── store/           PostgreSQL persistence
├── cli/                 Charm-powered terminal client
│   ├── api/             HTTP + WebSocket API client
│   ├── cmd/             Cobra commands (status, deploy, logs, etc.)
│   └── style/           Lip Gloss color palette and component styles
├── ui/                  React + Vite + pnpm frontend
│   └── src/
│       ├── components/  AppCard, LogViewer, DeployPanel, Welcome, StatusBar
│       ├── hooks/       useApps, useWebSocket
│       └── types/       TypeScript interfaces
├── infra/               Docker Compose for Valkey + Redpanda
├── infraspec.yaml       Norn manages itself
├── .sops.yaml           SOPS encryption rules
├── Dockerfile           Multi-stage production image
└── Makefile             Everything you need
```

## API

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/health` | Service health check |
| GET | `/api/apps` | List all discovered apps |
| GET | `/api/apps/:id` | App detail + recent deployments |
| GET | `/api/apps/:id/logs` | Stream pod logs |
| POST | `/api/apps/:id/deploy` | Trigger deploy pipeline |
| POST | `/api/apps/:id/restart` | Rolling restart |
| POST | `/api/apps/:id/rollback` | Rollback to previous image |
| GET | `/api/apps/:id/artifacts` | List retained image tags |
| GET | `/api/apps/:id/secrets` | List secret names |
| PUT | `/api/apps/:id/secrets` | Update secrets |
| GET | `/api/apps/:id/snapshots` | List DB snapshots |
| WS | `/ws` | Real-time events |

## v2 (Nomad/Consul/Tailscale)

v2 replaces Kubernetes with Nomad + Consul for orchestration and Tailscale for networking. See `v2/` for the full source.

### Quick start (dev mode)

```bash
cd v2
make up       # starts consul, nomad, api in background
make logs     # tail all logs
make down     # stop everything
```

### Access points (dev mode)

| Service | URL |
|---------|-----|
| Norn API | http://localhost:8800 |
| Norn UI | http://localhost:5173 (vite dev) |
| Nomad UI | http://localhost:4646/ui |
| Consul UI | http://localhost:8500/ui |
| signal-sideband | http://localhost:3001 |
| mail-agent | http://localhost:80 |
| mail-indexer | http://localhost:8090 |
| signal-cli | http://localhost:8080 |
| gitea | http://localhost:{dynamic} |

Apps with `endpoints` in their infraspec get static ports. Gitea uses a dynamic port — check the Nomad UI for the current assignment.

### v2 CLI

```bash
cd v2 && make build    # builds bin/norn-api + bin/norn
bin/norn status        # list all apps
bin/norn deploy <app> HEAD   # deploy latest commit
bin/norn scale <app> <n>     # scale up/down
bin/norn logs <app>          # stream allocation logs
```

### v2 infraspec format

```yaml
name: my-app
deploy: true
repo:
  url: http://host.docker.internal:3000/norn/my-app.git
  branch: master
  autoDeploy: true
build:
  dockerfile: Dockerfile
processes:
  web:
    port: 8080
    health:
      path: /health
    scaling:
      min: 1
    resources:
      cpu: 200
      memory: 256
env:
  KEY: "value"
secrets:
  - SECRET_NAME
endpoints:
  - url: https://my-app.example.com
volumes:
  - name: my-data
    mount: /data
infrastructure:
  postgres:
    database: myapp_db
```

See [docs/v2/guide/dev-environment.md](docs/v2/guide/dev-environment.md) for the full dev environment guide.

---

## v1 (Kubernetes/minikube) — frozen

> v1 is tagged as `v1.0` and is no longer actively developed. The docs below describe the v1 architecture.

## Configuration

| Environment variable | Default | Description |
|---------------------|---------|-------------|
| `NORN_PORT` | `8800` | API server port |
| `NORN_DATABASE_URL` | `postgres://norn:norn@localhost:5432/norn_db?sslmode=disable` | PostgreSQL connection |
| `NORN_APPS_DIR` | `~/projects` | Directory to scan for infraspec.yaml |
| `NORN_UI_DIR` | *(empty)* | Path to built UI static files (production only) |
| `NORN_URL` | `http://localhost:8800` | CLI: API server URL |
