# Infraspec Reference

The `infraspec.yaml` file is the single source of truth for an app's infrastructure requirements. Place it in the root of your project directory.

## Complete example

```yaml
app: mail-agent
role: webserver
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
alerts:
  window: 5m
  threshold: 3
```

## Field reference

### Top-level fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `app` | string | Yes | — | Unique app identifier. Must match the directory name under `NORN_APPS_DIR`. |
| `role` | string | Yes | — | One of `webserver`, `worker`, `cron`. Determines infrastructure behavior. |
| `core` | bool | No | `false` | Marks the app as a Norn infrastructure component. |
| `port` | int | No | — | Container port. Required for `webserver` role. |
| `healthcheck` | string | No | — | HTTP path for health checks (e.g. `/health`). |
| `replicas` | int | No | `1` | Number of pod replicas in the K8s Deployment. |
| `schedule` | string | No | — | Cron expression for `cron` role apps (e.g. `"*/5 * * * *"`). |
| `command` | string | No | — | Command to run in the container (used by cron apps). |
| `runtime` | string | No | `"docker"` | Container runtime: `"docker"` or `"incus"`. |
| `timeout` | int | No | `300` | Max seconds per execution (cron apps). |
| `deploy` | bool | No | `false` | Must be `true` for the app to appear in Norn. |

### `hosts`

Networking configuration for the app.

| Field | Type | Description |
|-------|------|-------------|
| `hosts.external` | string | Public hostname (e.g. `mail.slopistry.com`). Configures Cloudflare tunnel routing. |
| `hosts.internal` | string | Internal K8s service name (e.g. `mail-agent-service`). |

### `build`

Build configuration for Docker images.

| Field | Type | Description |
|-------|------|-------------|
| `build.dockerfile` | string | Path to Dockerfile relative to app directory. |
| `build.test` | string | Test command to run after build (e.g. `npm test`). |

### `repo`

Remote Git repository configuration. If set, Norn clones the repo during deploy instead of copying local files.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `repo.url` | string | — | Git clone URL (HTTPS or SSH). |
| `repo.branch` | string | `"main"` | Branch to clone. |
| `repo.autoDeploy` | bool | `false` | Enable webhook-triggered deploys. |
| `repo.repoWeb` | string | — | Web URL for the repository (for UI links). |
| `repo.webhookSecret` | string | — | Shared secret for webhook verification. Not included in API responses. |

### `services`

Shared service dependencies. Each service is provisioned with per-app isolation.

#### `services.postgres`

| Field | Type | Description |
|-------|------|-------------|
| `services.postgres.database` | string | Database name (e.g. `mailagent_db`). |
| `services.postgres.migrations` | string | Migration directory path. |

#### `services.kv`

| Field | Type | Description |
|-------|------|-------------|
| `services.kv.namespace` | string | Valkey key prefix namespace. |

#### `services.events`

| Field | Type | Description |
|-------|------|-------------|
| `services.events.topics` | string[] | Redpanda topic names. |

### `secrets`

A list of secret key names the app requires. Values are stored encrypted via SOPS + age in `secrets.enc.yaml`.

```yaml
secrets:
  - DATABASE_URL
  - SMTP_API_KEY
  - WEBHOOK_SECRET
```

### `migrations`

Database migration configuration.

| Field | Type | Description |
|-------|------|-------------|
| `migrations.command` | string | Shell command to run migrations (e.g. `npm run db:migrate`). |
| `migrations.database` | string | Database name for the migration target. |

### `artifacts`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `artifacts.retain` | int | `5` | Number of Docker image tags to retain. |

### `volumes`

List of volume mounts for the K8s Deployment.

| Field | Type | Description |
|-------|------|-------------|
| `volumes[].name` | string | Volume identifier. |
| `volumes[].mountPath` | string | Container mount path. |
| `volumes[].size` | string | PVC size (e.g. `10Gi`). Creates a PersistentVolumeClaim. |
| `volumes[].hostPath` | string | Host directory path. Used instead of PVC when set. |

### `env`

Static environment variables injected into the K8s Deployment.

```yaml
env:
  NORN_DATABASE_URL: "postgres://norn:norn@localhost:5432/norn_db?sslmode=disable"
  NORN_BIND_ADDR: "0.0.0.0"
```

### `alerts`

Health check alert configuration.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `alerts.window` | string | `"5m"` | Time window for counting failures. |
| `alerts.threshold` | int | `3` | Number of failures in window to trigger alert. |

## Real example: Norn itself

Norn manages its own infrastructure via `infraspec.yaml`:

```yaml
app: norn
role: webserver
core: true
port: 8800
healthcheck: /api/health
hosts:
  internal: norn-service
build:
  dockerfile: Dockerfile
artifacts:
  retain: 3
env:
  NORN_DATABASE_URL: "postgres://norn:norn@localhost:5432/norn_db?sslmode=disable"
  NORN_BIND_ADDR: "0.0.0.0"
  NORN_APPS_DIR: "/projects"
volumes:
  - name: projects
    mountPath: /projects
    hostPath: /Users/0xadb/projects
```

## Interactive builder

Build an infraspec interactively and copy the result:

<InfraspecBuilder />
