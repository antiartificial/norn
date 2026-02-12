# Architecture Overview

Norn is structured as three independent modules: an API server, a React UI, and a CLI. They communicate through a REST API and WebSocket connection.

## System diagram

```mermaid
graph TB
    Browser["Browser (React UI)"]
    CLI["CLI (Charm TUI)"]

    Browser -->|"REST + WS"| API
    CLI -->|"REST + WS"| API

    subgraph API["API Server (Go + chi)"]
        Handler["Handlers"]
        Pipeline["Deploy Pipeline"]
        Forge["Forge Pipeline"]
        Hub["WebSocket Hub"]
        Secrets["SOPS Manager"]
        Scheduler["Cron Scheduler"]
    end

    API -->|"pgx"| PG["PostgreSQL"]
    API -->|"client-go"| K8s["Kubernetes"]
    API -->|"sops + age"| SOPS["Encrypted Secrets"]
    API -->|"exec"| Docker["Docker"]
    API -->|"cloudflared"| CF["Cloudflare Tunnel"]

    K8s --> Valkey["Valkey (KV)"]
    K8s --> Redpanda["Redpanda (Events)"]

    style API fill:#1e1b4b,stroke:#7c3aed,color:#f9fafb
    style PG fill:#1f2937,stroke:#10b981,color:#f9fafb
    style K8s fill:#1f2937,stroke:#06b6d4,color:#f9fafb
```

## Module boundaries

### API (`api/`)

The Go backend — a single binary that runs the HTTP server, WebSocket hub, deploy/forge pipelines, cron scheduler, and health checker.

```
api/
├── handler/     REST + WebSocket handlers (chi router)
├── hub/         WebSocket broadcast hub (gorilla/websocket)
├── k8s/         Kubernetes API client (client-go)
├── model/       infraspec parser, data models
├── pipeline/    Deploy, Forge, and Teardown pipelines
├── secrets/     SOPS encrypt/decrypt + K8s sync
├── cron/        Cron scheduler (in-container execution)
└── store/       PostgreSQL persistence (pgx)
```

### CLI (`cli/`)

Charm-powered terminal client. Connects to the API via HTTP and WebSocket.

```
cli/
├── api/         HTTP + WebSocket API client
├── cmd/         Cobra commands (status, deploy, logs, etc.)
├── style/       Lip Gloss color palette and component styles
└── terraform/   Terraform runner for cluster commands
```

### UI (`ui/`)

React 19 + Vite + TypeScript single-page application.

```
ui/src/
├── components/  AppCard, LogViewer, DeployPanel, Welcome, StatusBar
├── hooks/       useApps, useWebSocket
└── types/       TypeScript interfaces
```

## Request flow

A typical deploy request flows through the system like this:

```mermaid
sequenceDiagram
    actor User
    participant CLI
    participant API
    participant DB as PostgreSQL
    participant WS as WebSocket Hub
    participant Docker
    participant K8s as Kubernetes

    User->>CLI: norn deploy myapp abc123
    CLI->>API: POST /api/apps/myapp/deploy
    API->>DB: INSERT deployment (status: queued)
    API->>WS: broadcast deploy.queued
    API-->>CLI: 200 {id, status: queued}
    CLI->>API: WS connect /ws

    Note over API: Pipeline runs async (goroutine)

    loop Each step: clone, build, test, snapshot, migrate, deploy, cleanup
        API->>WS: broadcast deploy.step {step, status}
        WS->>CLI: deploy.step event
    end

    API->>Docker: docker build -t myapp:abc123
    API->>K8s: SetImage(myapp, myapp:abc123)
    API->>DB: UPDATE deployment (status: deployed)
    API->>WS: broadcast deploy.completed
    WS->>CLI: deploy.completed event
```

## API endpoints

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
| GET | `/api/apps/:id/health-history` | Health check history |
| POST | `/api/apps/:id/forge` | Trigger forge pipeline |
| DELETE | `/api/apps/:id/forge` | Trigger teardown pipeline |
| GET | `/api/apps/:id/forge` | Get forge state |
| GET | `/api/deployments` | List all deployments (filterable) |
| WS | `/ws` | Real-time events |

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `NORN_PORT` | `8800` | API server port |
| `NORN_DATABASE_URL` | `postgres://norn:norn@localhost:5432/norn_db?sslmode=disable` | PostgreSQL connection |
| `NORN_APPS_DIR` | `~/projects` | Directory to scan for infraspec.yaml |
| `NORN_UI_DIR` | *(empty)* | Path to built UI static files (production only) |
| `NORN_URL` | `http://localhost:8800` | CLI: API server URL |
