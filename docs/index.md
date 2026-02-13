---
layout: home

hero:
  name: Norn
  text: Control plane for self-hosted infrastructure
  tagline: Named after the three Norse fates — Urd (past), Verdandi (present), and Skuld (future)
  actions:
    - theme: brand
      text: Get Started
      link: /guide/getting-started
    - theme: alt
      text: Architecture
      link: /architecture/overview
    - theme: alt
      text: CLI Reference
      link: /cli/commands

features:
  - title: Discover
    details: Scans ~/projects for infraspec.yaml files and automatically registers apps with their infrastructure dependencies.
  - title: Deploy
    details: Seven-step pipeline — clone, build, test, snapshot, migrate, deploy, cleanup — with real-time progress over WebSocket.
  - title: Forge
    details: Provisions Kubernetes infrastructure for an app — deployment, service, Cloudflare tunnel, DNS — in a single command.
  - title: Monitor
    details: Health checks, pod status, log streaming, and deployment history — from the dashboard or the terminal.
  - title: Functions
    details: HTTP-triggered ephemeral containers — invoke from the dashboard, CLI, or API with automatic execution tracking.
  - title: Object Storage
    details: S3-compatible storage with Cloudflare R2, AWS S3, GCS, or any S3-compatible provider.
---

## What is Norn?

Norn is a personal control plane for self-hosted infrastructure. It discovers your apps, shows their health, and lets you deploy, restart, roll back, and stream logs — from a React dashboard or a Charm-powered CLI.

![Norn Dashboard](/screenshots/dashboard.png)

Each app declares its needs in an `infraspec.yaml`: role, port, services, secrets, migrations. Norn reads these specs and handles the rest — building Docker images, running migrations, syncing secrets, managing Kubernetes deployments, and routing traffic through Cloudflare tunnels.

### Quick start

```bash
make setup   # check prereqs, create database, install deps
make dev     # start API (:8800) + UI (:5173)
```

### From the terminal

```bash
norn status            # list all apps
norn deploy myapp HEAD # deploy with live pipeline progress
norn health            # check backing services
norn logs myapp        # stream pod logs
```

![CLI status](/screenshots/cli-status.png)
