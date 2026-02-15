---
layout: home

hero:
  name: Norn
  text: Control plane for self-hosted infrastructure
  tagline: Named after the three Norse fates — Urd (past), Verdandi (present), and Skuld (future)
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
    details: Nine-step pipeline — clone, build, test, snapshot, migrate, submit, healthy, forge, cleanup — with real-time saga events over WebSocket.
  - title: Orchestrate
    details: Translates infraspec processes to Nomad jobs — services, periodic batch for cron, one-shot batch for functions — with Consul service discovery.
  - title: Monitor
    details: Health checks, allocation status, log streaming, and deployment history — from the dashboard or the terminal.
  - title: Functions
    details: HTTP-triggered ephemeral containers — invoke from the dashboard, CLI, or API with automatic execution tracking via Nomad batch jobs.
  - title: Saga Events
    details: Append-only event log for every operation — deploys, restarts, scales — providing an immutable audit trail.
---

## What is Norn?

Norn is a personal control plane for self-hosted infrastructure. It discovers your apps, shows their health, and lets you deploy, restart, roll back, and stream logs — from a React dashboard or a Charm-powered CLI.

![Norn Dashboard](/screenshots/dashboard.png)

Each app declares its needs in an `infraspec.yaml`: processes, ports, services, secrets, migrations. Norn reads these specs and handles the rest — building Docker images, running migrations, resolving secrets, submitting Nomad jobs, and routing traffic through Cloudflare tunnels.

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
