# Concepts

## App Discovery

Norn scans `NORN_APPS_DIR` (default: `~/projects`) for directories containing an `infraspec.yaml` file. Each discovered spec becomes an app in the dashboard and CLI.

There is no registration step — drop an `infraspec.yaml` into your project and Norn picks it up on the next scan.

## Infraspec Anatomy

The infraspec is a YAML file that declares everything Norn needs to manage your app:

```yaml
name: myapp

repo:
  url: git@github.com:you/myapp.git
  branch: main
  autoDeploy: true

build:
  dockerfile: Dockerfile
  test: make test

processes:
  web:
    port: 8080
    command: ./server
    health:
      path: /health
    scaling:
      min: 2
  worker:
    command: ./worker
  cleanup:
    schedule: "0 3 * * *"
    command: ./cleanup

secrets:
  - DATABASE_URL
  - API_KEY

migrations: ./migrations

env:
  LOG_LEVEL: info

infrastructure:
  postgres:
    database: myapp

endpoints:
  - url: myapp.example.com

volumes:
  - name: data
    mount: /data
```

### v1 vs v2

In v1, apps declared a single `role` (webserver, worker, cron, function). In v2, an app declares a **processes map** — multiple named processes of different types coexist in one spec. This means a single app can have a web server, a background worker, and a cron job without splitting into separate specs.

## Process Types

The process type is inferred from its fields:

| Type | Identifying Field | Nomad Job Type | Description |
|------|-------------------|----------------|-------------|
| **Service** | `port` | Service | Long-running process with a port, registered in Consul |
| **Worker** | neither `port` nor `schedule` nor `function` | Service | Long-running process without a port |
| **Cron** | `schedule` | Periodic Batch | Runs on a cron schedule |
| **Function** | `function` | Batch (one-shot) | HTTP-triggered ephemeral execution |

## Infrastructure Dependencies

The `infrastructure` block declares backing services your app needs. Norn uses this for health checks, connection string injection, and snapshot management.

| Service | Fields | Purpose |
|---------|--------|---------|
| `postgres` | `database` | PostgreSQL database name — used for snapshots, migrations, and `DATABASE_URL` injection |
| `redis` | `namespace` | Redis namespace prefix |
| `kafka` | `topics` | Kafka topic list |
| `nats` | `streams` | NATS JetStream stream names |

## Volumes

Volumes map to Nomad host volumes. The Nomad client must have a matching `host_volume` stanza in its configuration.

```yaml
volumes:
  - name: signal-data
    mount: /var/lib/signal-cli
    readOnly: false
```

See [Volumes](/v2/infrastructure/volumes) for setup details.

## Secrets

Secrets are encrypted with SOPS + age and stored alongside the infraspec in a `secrets.enc.yaml` file. During deploy, the pipeline decrypts secrets and injects them as environment variables into Nomad tasks.

The `secrets` list in the infraspec declares which secret keys the app expects:

```yaml
secrets:
  - DATABASE_URL
  - API_KEY
  - SMTP_PASSWORD
```

See [Secrets](/v2/infrastructure/secrets) for the full workflow.

## Deploy Pipeline

When you deploy an app, Norn runs a 9-step pipeline:

1. **Clone** — checkout the repo at the specified ref
2. **Build** — build the Docker image
3. **Test** — run the test command (if defined)
4. **Snapshot** — pg_dump the database (if postgres infrastructure defined)
5. **Migrate** — run database migrations (if migrations path defined)
6. **Submit** — translate infraspec to Nomad jobs and submit
7. **Healthy** — poll Nomad allocations until healthy
8. **Forge** — update cloudflared ingress rules (if endpoints defined)
9. **Cleanup** — remove temp files

Each step emits saga events and WebSocket broadcasts for real-time progress tracking.

See [Deploy Pipeline](/v2/architecture/deploy-pipeline) for the full architecture.

## Endpoints

Endpoints map to Cloudflare Tunnel ingress rules. Defining an endpoint causes Norn to use static ports (for predictable routing) and configure cloudflared during the forge step.

```yaml
endpoints:
  - url: myapp.example.com
  - url: myapp-staging.example.com
    region: us-east
```

See [Cloudflare](/v2/infrastructure/cloudflare) for tunnel configuration.
