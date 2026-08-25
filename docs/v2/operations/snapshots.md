# Snapshots

Norn automatically creates PostgreSQL database snapshots during deploys and exposes manual snapshot, retention, restore, and schema-migration controls as durable app operations. The web and macOS Apps views use the same versioned receipts as automated clients.

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
curl -H "Authorization: Bearer $NORN_API_TOKEN" \
  http://localhost:8800/api/v1/apps/myapp/snapshots

curl -X POST -H "Authorization: Bearer $NORN_API_TOKEN" \
  -H "Idempotency-Key: snapshot-$(uuidgen)" \
  http://localhost:8800/api/v1/apps/myapp/snapshots
```

The Apps view marks the exact snapshots outside the selected keep window as `will prune`. Pruning is not sent until the operator reviews that preview and confirms it.

## Restoring a Snapshot

### CLI

```bash
norn snapshots myapp restore 2025-01-15T14:30:00 --yes
norn snapshots myapp restore 2025-01-15T14:30:00 --yes --pre-restore
```

### API

```bash
curl -X POST -H "Authorization: Bearer $NORN_API_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: restore-$(uuidgen)" \
  -d '{"confirm":true}' \
  http://localhost:8800/api/v1/apps/myapp/snapshots/20260825T143000/restore
```

::: warning
The versioned restore route always creates a fresh safety snapshot immediately before the destructive restore. The older CLI flags remain for compatibility with the legacy route.
:::

The accepted response is an `app.snapshot-restore` operation. Its terminal typed receipt includes the restored snapshot, safety snapshot, and database. It is safe to close either UI after queuing; the operation is reconstructed from PostgreSQL.

## Standalone schema changes

When `migrations` and `infrastructure.postgres` are declared, either Apps view can queue that command without deploying application allocations. Norn prepares the requested source ref, creates a pre-migration snapshot, runs the declared command once, and records an `app.migrate` receipt:

```bash
curl -X POST -H "Authorization: Bearer $NORN_API_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: migrate-$(uuidgen)" \
  -d '{"ref":"HEAD","confirm":true}' \
  http://localhost:8800/api/v1/apps/myapp/migrations
```

Schema mutations and restores intentionally have one execution attempt. If the API stops after mutation begins, Norn records visible failure for operator review rather than assuming an unknown migration is replay-safe.

## Serialization and recovery

All mutable app operations acquire a PostgreSQL advisory lock keyed by app. This prevents deploy, rollback, snapshot retention, restore, and migration work for the same app from overlapping across API replicas. A replica that cannot acquire the lock returns its claim to the queue without consuming an execution attempt. Every mutation requires an idempotency key and rejects reuse for a different request.

The web and macOS clients persist each unacknowledged intent before sending it.
After a browser refresh, application relaunch, or ambiguous network failure,
the client reuses the same request-bound key and receives the original receipt
instead of creating a duplicate. The local intent is cleared only after Norn
acknowledges the durable operation.

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
