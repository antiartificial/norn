# Shared Services

Norn runs one instance of each backing service, with per-app isolation through namespacing and access controls.

## Overview

| Service | Purpose | Multi-tenancy | Port |
|---------|---------|---------------|------|
| **PostgreSQL** | Application databases | Per-app database | 5432 |
| **Valkey** | Key-value store (Redis-compatible) | ACL users with key prefix restrictions | 6379 |
| **Redpanda** | Event streaming (Kafka-compatible) | Topic prefixes + ACLs | 19092 |

## PostgreSQL

PostgreSQL runs via [Postgres.app](https://postgresapp.com) on the host (not Docker).

### Norn's database

Norn itself uses `norn_db` for storing deployments, forge states, health checks, cron state, and cluster nodes. Created automatically by `make db`:

```bash
# Creates user 'norn' with password 'norn' and database 'norn_db'
make db
```

### Per-app databases

Each app declares its database in the infraspec:

```yaml
services:
  postgres:
    database: mailagent_db
```

The forge pipeline auto-generates a `DATABASE_URL` environment variable:

```
postgres://<pg_user>@<pg_host>:5432/<database>?sslmode=disable
```

### Snapshots

Before every migration, the deploy pipeline runs `pg_dump` to create a backup:

```bash
pg_dump -Fc -d mailagent_db -f snapshots/mailagent_db_abc123_20240115T143022.dump
```

Custom format (`-Fc`) allows selective restore:

```bash
pg_restore -d mailagent_db snapshots/mailagent_db_abc123_20240115T143022.dump
```

## Valkey

[Valkey](https://valkey.io) is a Redis-compatible key-value store. It runs via Docker Compose.

### Configuration

Apps declare a KV namespace:

```yaml
services:
  kv:
    namespace: mail-agent
```

Each app gets an ACL user restricted to keys with its namespace prefix, preventing cross-app data access.

### Start/stop

```bash
make infra       # start Valkey on :6379
make infra-stop  # stop
```

## Redpanda

[Redpanda](https://redpanda.com) is a Kafka-compatible event streaming platform. It runs via Docker Compose alongside Valkey.

### Configuration

Apps declare topics:

```yaml
services:
  events:
    topics: [mail.inbound, mail.processed]
```

Topics are created with per-app ACLs. The Redpanda Console is available at `:8090` for debugging.

### Start/stop

```bash
make infra       # start Redpanda on :19092, Console on :8090
make infra-stop  # stop
```

## Docker Compose

Both Valkey and Redpanda are managed by a single Docker Compose file in `infra/`:

```bash
cd infra && docker compose up -d     # or: make infra
cd infra && docker compose down      # or: make infra-stop
```

## Checking health

```bash
make doctor       # checks all services
norn health       # checks from CLI
```

Both commands verify PostgreSQL, Valkey, and Redpanda connectivity.
