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
  exportBucket: myapp-snapshots
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

## Remote Export And Import

Snapshots are stored first as local files under the Norn API working directory's `snapshots/` folder. Apps can also declare `snapshots.exportBucket` to archive local dumps to S3-compatible object storage such as Garage.

```yaml
snapshots:
  keep: 5
  exportBucket: myapp-snapshots
```

Manual export uploads the latest local snapshot:

```bash
norn snapshots export myapp
```

List remote snapshots:

```bash
norn snapshots remote myapp
```

Import downloads a remote object key back into the local snapshots directory:

```bash
norn snapshots import myapp snapshots/myapp/myapp_db_abcdef_20260614T181100.dump
```

Remote export/import requires the platform S3 configuration used by managed object storage, including `NORN_S3_ENDPOINT`, `NORN_S3_ACCESS_KEY`, `NORN_S3_SECRET_KEY`, and provider-specific path-style settings when using Garage. Export and import actions emit Beacon events (`snapshot.exported`, `snapshot.imported`) so off-host backup movement is auditable.
