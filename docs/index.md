---
layout: home

hero:
  name: Norn
  text: Control plane for self-hosted infrastructure
  tagline: Named after the three Norse fates - Urd (past), Verdandi (present), and Skuld (future)
  actions:
    - theme: brand
      text: Get Started
      link: /v2/guide/getting-started
    - theme: alt
      text: Architecture
      link: /v2/architecture/overview
    - theme: alt
      text: CLI Reference
      link: /v2/cli/commands

features:
  - title: Discover
    details: Scans ~/projects for infraspec.yaml files and automatically registers apps with their infrastructure dependencies.
  - title: Deploy
    details: Nine-step pipeline - clone, build, test, snapshot, migrate, submit, healthy, forge, cleanup - with real-time saga events over WebSocket.
  - title: Orchestrate
    details: Translates infraspec processes to Nomad jobs - services, periodic batch for cron, one-shot batch for functions - with Consul service discovery.
  - title: Monitor
    details: Health checks, allocation status, log streaming, and deployment history - from the dashboard or the terminal.
  - title: Functions
    details: HTTP-triggered ephemeral containers - invoke from the dashboard, CLI, or API with automatic execution tracking via Nomad batch jobs.
  - title: Saga Events
    details: Append-only event log for every operation - deploys, restarts, scales - providing an immutable audit trail.
---

## What is Norn?

Norn is a personal control plane for self-hosted infrastructure. It discovers your apps, shows their health, and lets you deploy, restart, roll back, and stream logs - from a React dashboard or a Charm-powered CLI.

![Norn Dashboard](/screenshots/dashboard.png)

Each app declares its needs in an `infraspec.yaml`: processes, ports, services, secrets, migrations. Norn reads these specs and handles the rest - building Docker images, running migrations, resolving secrets, submitting Nomad jobs, and routing traffic through Cloudflare tunnels.

### One file. Entire app.

Drop an `infraspec.yaml` in your project and Norn handles the rest - build pipeline, service discovery, database snapshots, secret injection, Cloudflare tunnel routing:

```yaml
name: auricle

repo:
  url: git@github.com:antiartificial/auricle.git
  branch: main
  autoDeploy: true               # push to main - auto deploy

build:
  dockerfile: Dockerfile
  test: go test ./...             # tests gate every deploy

processes:
  api:                            # - Nomad service, Consul registered
    port: 8080
    command: ./auricle serve
    health:
      path: /health
    scaling:
      min: 2                      # always 2 instances
    resources:
      cpu: 200                    # MHz
      memory: 512                 # MB
    drain:
      signal: SIGTERM
      timeout: 30s

  worker:                         # - Nomad service (no port)
    command: ./auricle worker
    scaling:
      min: 1
      auto:
        metric: kafka_lag         # scale on consumer lag
        target: 100
        topic: audio.transcribed

  digest:                         # - Nomad periodic batch
    schedule: "0 7 * * *"         # daily at 7am
    command: ./auricle digest

  reindex:                        # - Nomad batch (HTTP-triggered)
    function:
      timeout: 10m
      memory: 1024
    command: ./auricle reindex

secrets:
  - DATABASE_URL
  - OPENAI_API_KEY
  - S3_ACCESS_KEY

migrations: ./migrations          # run before every deploy

env:
  LOG_LEVEL: info
  TZ: America/Chicago

infrastructure:
  postgres:
    database: auricle_db          # auto snapshot + migrate
  redis:
    namespace: auricle            # key-prefixed isolation
  kafka:
    topics:                       # auto-created via Redpanda
      - audio.uploaded
      - audio.transcribed

endpoints:
  - url: auricle.0xadb.com       # - cloudflared tunnel, TLS included

volumes:
  - name: audio-cache
    mount: /var/cache/auricle
```

That's it. `norn deploy auricle HEAD` runs the full pipeline - clone, build, test, snapshot, migrate, submit to Nomad, wait for healthy, provision Cloudflare tunnel, cleanup - with real-time progress in the dashboard and CLI.

### Quick start

```bash
cd norn/v2
make build   # build API + CLI
make dev     # start API (:8800) + UI (:5173)
```

### From the terminal

```bash
norn status                   # list all apps
norn deploy myapp HEAD        # deploy with live pipeline progress
norn health                   # check backing services
norn logs myapp               # stream live logs
norn endpoints myapp          # view cloudflared routing status
norn endpoints toggle myapp \ # toggle a single endpoint
  myapp.example.com
```

![CLI status](/screenshots/cli-status.png)
