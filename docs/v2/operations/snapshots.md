# Snapshots

Norn automatically creates PostgreSQL database snapshots during deploys and supports manual listing and restoration.

## Automatic Snapshots

During the **snapshot** step of the deploy pipeline, Norn runs `pg_dump` on the app's database (from `infrastructure.postgres.database`). This happens before migrations run, providing a safety net for schema changes.

Snapshots are only created for apps that declare postgres infrastructure:

```yaml
infrastructure:
  postgres:
    database: myapp
```

## Listing Snapshots

### CLI

```bash
norn snapshots myapp
```

Displays a table of available snapshots with timestamps and sizes.

### API

```bash
curl http://localhost:8800/api/apps/myapp/snapshots
```

## Restoring a Snapshot

### CLI

```bash
norn snapshots myapp restore 2025-01-15T14:30:00
```

### API

```bash
curl -X POST http://localhost:8800/api/apps/myapp/snapshots/2025-01-15T14:30:00/restore
```

::: warning
Restoring a snapshot replaces the current database contents. Make sure to snapshot the current state first if you need to preserve it.
:::

## Storage

Snapshots are stored via the S3-compatible storage backend. Configure with:

| Variable | Description |
|----------|-------------|
| `NORN_S3_ENDPOINT` | S3-compatible endpoint URL |
| `NORN_S3_ACCESS_KEY` | Access key |
| `NORN_S3_SECRET_KEY` | Secret key |
| `NORN_S3_REGION` | Region (default: `auto`) |
| `NORN_S3_USE_SSL` | Use SSL (default: `true`) |

If S3 is not configured, the snapshot step is skipped.
