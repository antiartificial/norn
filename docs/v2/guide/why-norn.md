# Why Norn

Norn is a control plane for self-hosted infrastructure. It turns a Mac mini, a single VPS, or a small cluster into a platform that discovers, deploys, monitors, and operates your apps — with the operational surface you'd expect from a production PaaS but without the overhead of Kubernetes or the limitations of raw Docker.

## The Home Lab Problem

Running services at home usually means one of two paths:

**Docker Compose** gets you running fast but leaves you on your own for everything after `docker compose up`: deployments are manual, there's no health checking, no rollback, no secret management, no observability, and no operational history. When something breaks at 2am, you're reading container logs with no context.

**Minikube / k3s / full Kubernetes** gives you the operational surface but at enormous cost: etcd, kube-apiserver, controller-manager, scheduler, CoreDNS, kube-proxy, and the CNI plugin are running before you've deployed a single app. Resource overhead is 1-2GB of RAM just for the control plane. The learning curve is steep, the YAML is verbose, and the failure modes are complex. It's built for teams running hundreds of services across data centers — not for one person running a handful of apps on a Mac mini.

## What Norn Does Differently

Norn sits in the middle: real operational capabilities with minimal resource cost.

### One file per app

Drop an `infraspec.yaml` in your project directory. Norn discovers it automatically — no registration, no Helm charts, no 200-line Kubernetes manifests.

```yaml
name: myapp
build:
  dockerfile: Dockerfile
processes:
  web:
    port: 8080
    command: ./server
    health:
      path: /health
    resources:
      cpu: 100
      memory: 256
endpoints:
  - url: myapp.example.com
```

That single file gives you: container builds, health checks, Consul service discovery, Cloudflare tunnel routing with TLS, resource limits, and a deploy pipeline.

### Real deploy pipeline

`norn deploy myapp HEAD` runs 9 steps: clone, build, test, snapshot, migrate, submit, healthy, forge, cleanup. Each step emits real-time progress over WebSocket to the dashboard and CLI. If the deploy fails, you get a saga event trail that tells you exactly which step broke and why.

`norn preflight myapp HEAD` rehearses the entire pipeline without touching runtime state — catches build failures, test failures, and missing secrets before anything gets submitted.

`norn rollback myapp` rolls back to the previous deployment.

### Multi-process apps

A single infraspec can define web servers, background workers, cron jobs, and HTTP-triggered functions. They deploy together, share secrets, and appear as one app in the dashboard:

```yaml
processes:
  api:
    port: 8080
    command: ./server
    scaling:
      min: 2
  worker:
    command: ./worker
  cleanup:
    schedule: "0 3 * * *"
    command: ./cleanup
  reindex:
    function:
      timeout: 10m
    command: ./reindex
```

### Built-in observability

Norn exposes Prometheus metrics, installs a local Prometheus/Grafana/cAdvisor stack with one command, and tracks operational events:

- **Restart and OOM tracking** — detects task restarts and OOM kills from Nomad allocation state, classifies the cause, and emits Beacon events
- **Resource right-sizing** — `norn resources` compares declared memory limits against live usage and flags apps that are overprovisioned or at risk of OOM
- **Prometheus alert rules** — service down, deploy failed, cron failed, disk low, OOM risk, restart loops
- **Grafana dashboard** — app health, operations, disk, and snapshot status out of the box

```bash
norn observability install   # writes Prometheus, Grafana, cAdvisor app specs
norn deploy norn-prometheus HEAD
norn deploy norn-grafana HEAD
```

### Operational events

Norn records everything it observes: deploy outcomes, allocation failures, restarts, OOM kills, service health transitions, cron results. These events are durable, queryable, and actionable:

```bash
norn events                    # list recent events
norn events show <id>          # event detail with metadata
norn events ack <id>           # acknowledge
```

Events can be forwarded to an external sink for alerting and incident workflows.

### Secret management

Secrets are encrypted with SOPS + age and stored alongside the infraspec. During deploy, Norn decrypts and injects them as environment variables. Secret values never appear in API responses, CLI output, or the dashboard.

```bash
norn secrets status myapp      # shows declared vs encrypted vs missing
norn validate --strict-secrets # fails if plaintext secrets detected
```

### Database snapshots

For apps with PostgreSQL, Norn snapshots the database before every deploy. Restores are explicit and create safety snapshots first:

```bash
norn snapshots myapp
norn snapshots restore myapp <timestamp> --pre-restore --yes
```

## Norn vs Docker Compose

| Capability | Docker Compose | Norn |
|:-----------|:---------------|:-----|
| Container builds | Manual `docker build` | Automatic per deploy |
| Health checks | Basic container healthcheck | Consul-backed with recovery events |
| Deploys | `docker compose up -d` | 9-step pipeline with preflight, rollback, saga trail |
| Secrets | `.env` files or Docker secrets | SOPS/age encrypted, injected at deploy |
| Observability | None built-in | Prometheus, Grafana, cAdvisor, Beacon events |
| Restart tracking | Docker restart policy | OOM cause classification, Beacon events, metrics |
| Database backups | Manual | Auto-snapshot before deploy, restore with safety net |
| Cron jobs | Separate compose service or host cron | First-class process type with execution history |
| Service discovery | Docker DNS | Consul with health, reachability, manifest |
| TLS / routing | Reverse proxy setup | Cloudflare tunnel with auto-forge |
| Resource limits | Docker resource constraints | Declared in infraspec, live usage comparison |
| Dashboard | Portainer or similar | Built-in React dashboard with WebSocket updates |
| CLI | `docker compose` commands | Purpose-built CLI with deploy progress, logs, events |

## Norn vs Minikube / k3s

| Capability | Minikube / k3s | Norn |
|:-----------|:---------------|:-----|
| Control plane overhead | 1-2 GB RAM | ~50 MB (Norn API + CLI) |
| App spec | Deployment + Service + Ingress + ConfigMap + Secret + HPA | Single `infraspec.yaml` |
| Learning curve | Kubernetes concepts, kubectl, Helm | infraspec.yaml + `norn deploy` |
| Deploy pipeline | kubectl apply or Helm upgrade | Built-in 9-step with preflight and rollback |
| Secrets | Kubernetes secrets (base64, not encrypted) | SOPS/age encrypted at rest |
| Observability | kube-prometheus-stack (~2 GB) | Lightweight local stack (~400 MB) |
| Database snapshots | No built-in support | Auto-snapshot before deploy |
| Failure debugging | `kubectl describe pod`, events scattered across resources | Saga trail, Beacon events, `norn events show` |
| Scaling | HPA with metrics-server | Infraspec scaling with min/max |
| Host compatibility | Needs VM on macOS | Native Docker on macOS |

## What You Get

After setup (`make build && make dev`), you have:

- A dashboard at `localhost:5173` showing all your apps, their health, and deploy history
- A CLI that deploys, rolls back, streams logs, and shows operational events
- Automatic database snapshots before every deploy
- Encrypted secret management
- Health monitoring with Beacon events for failures, restarts, and OOM kills
- Resource usage tracking that tells you when to resize
- Cloudflare tunnel routing with TLS — no reverse proxy configuration
- A local Prometheus/Grafana stack installable with one command

The platform runs on a Mac mini with Nomad and Consul alongside your apps. Total overhead for the control plane is under 100 MB of RAM.

## Getting Started

```bash
git clone git@github.com:antiartificial/norn.git
cd norn/v2
make build
make dev
```

See [Getting Started](/v2/guide/getting-started) for the full setup guide.
