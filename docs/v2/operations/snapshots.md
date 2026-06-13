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
norn snapshots myapp restore 2025-01-15T14:30:00 --yes --pre-restore
```

### API

```bash
curl -X POST 'http://localhost:8800/api/apps/myapp/snapshots/2025-01-15T14:30:00/restore?confirm=true'
curl -X POST 'http://localhost:8800/api/apps/myapp/snapshots/2025-01-15T14:30:00/restore?confirm=true&preRestore=true'
```

::: warning
Restoring a snapshot replaces the current database contents. Use `--pre-restore` or `preRestore=true` to create a fresh safety snapshot immediately before the destructive restore.
:::

Restore receipts include the restored snapshot and, when requested, the pre-restore snapshot filename. Restore and retention actions also emit Beacon events so the operation appears in the same event ledger as deploy and service health changes.

## Storage

Snapshots are currently stored as local files under the Norn API working directory's `snapshots/` folder. The object-storage provider can now provision Garage-backed app buckets, but snapshot archival to Garage is a separate follow-up so restore and retention semantics stay explicit.
