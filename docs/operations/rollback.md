# Rollback

When a deployment introduces bugs or breaks functionality, Norn provides rollback capabilities to quickly revert to a previous version.

## How Rollback Works

Norn's rollback mechanism finds the second most recent deployment and updates the Kubernetes deployment to use that version's image. This is a fast operation that only modifies the image tag, without rebuilding or running migrations.

## CLI Rollback

Roll back an application to its previous version:

```bash
norn rollback myapp
```

This automatically finds the last known-good deployment and switches to it.

## API Rollback

You can also trigger rollback via the API:

```bash
POST /api/apps/:id/rollback
```

### Specifying a Version

To roll back to a specific version instead of the previous one:

```bash
POST /api/apps/:id/rollback
{
  "imageTag": "myapp:abc123"
}
```

If `imageTag` is not specified, Norn uses the previous deployment's image automatically.

## Snapshot Restore

Rollback only reverts the application code, not the database. If a migration in the new version caused issues, you may need to manually restore the database from a snapshot.

### Restore Process

1. Locate the snapshot file in the `snapshots/` directory:
   ```
   snapshots/<db>_<sha>_<timestamp>.dump
   ```

2. Restore using `pg_restore`:
   ```bash
   pg_restore -d <database> snapshots/<db>_<sha>_<timestamp>.dump
   ```

3. Verify the restored data is correct before resuming normal operations.

## Artifact Retention

Norn automatically manages snapshot and artifact retention based on your infraspec configuration:

```yaml
artifacts:
  retain: 5  # Keep last 5 versions (default)
```

Older snapshots are automatically cleaned up to prevent disk space exhaustion. Adjust this value based on your rollback needs and available storage.

## When Rollback Isn't Enough

Rollback is designed for quick recovery from bad deployments. However, it may not be sufficient if:

- **Database migrations are irreversible**: Some schema changes (like dropping columns) can't be undone by simply restoring a snapshot
- **External dependencies changed**: If the new version modified external APIs or services, rolling back code alone won't fix integration issues
- **Data corruption occurred**: Rollback won't fix data that was corrupted by the bad version while it was running

In these cases, you may need manual intervention to fully recover.
