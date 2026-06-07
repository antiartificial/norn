# Snapshots

Norn automatically creates PostgreSQL database snapshots during deploys and supports manual listing and restoration.

## Automatic Snapshots

During the **snapshot** step of the deploy pipeline, Norn runs `pg_dump` on the app's database (from `infrastructure.postgres.database`). This happens before migrations run, providing a safety net for schema changes.

Snapshots are only created for apps that declare postgres infrastructure:

```yaml
infrastructure:
  postgres:
    database: myapp

snapshots:
  keep: 3
```

## Listing Snapshots

### CLI

```bash
norn snapshots myapp
```

Displays a table of available snapshots with timestamps, source commit, created time, size, and filename.

## Retention

```bash
norn snapshots myapp retention --keep 3
norn snapshots myapp retention --keep 3 --execute --yes
```

Retention previews by default. The command marks the newest snapshots as `keep` and older snapshots as `would-prune` without deleting files. If `--keep` is omitted, Norn uses `snapshots.keep` from `infraspec.yaml`, falling back to 3. Add `--execute --yes` to delete older local snapshot files and print an applied retention receipt.

`norn ops platform` also reports per-app snapshot counts, policy keep counts, and over-limit totals.

### API

```bash
curl http://localhost:8800/api/apps/myapp/snapshots
```

## Restoring a Snapshot

### CLI

```bash
norn snapshots myapp restore 2025-01-15T14:30:00 --yes
```

### API

```bash
curl -X POST 'http://localhost:8800/api/apps/myapp/snapshots/2025-01-15T14:30:00/restore?confirm=true'
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
