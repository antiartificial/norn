# M5 legacy-to-contract baseline transition

The Mini release identified in the private rehearsal has no
`norn.startup/v2` capability. It can restart after a candidate migration and
would bypass the schema ledger and reader/writer floor. Use the ordinary
`platform-upgrade upgrade` path only after this one-way baseline transition
has completed.

## Preconditions

Perform this as a scheduled Mini maintenance action. Stop application writers
through the normal maintenance procedure before invoking the command. Keep the
candidate source ref, the legacy release SHA, and the backup artifact in the
change record.

Create the verified control database backup as a nonempty regular file owned
by the invoking user, at an absolute private path with mode exactly `0600`.
Create a separate mode-`0600`, user-owned, absolute-path JSON backup proof.
The proof contains exactly these fields:

```json
{
  "schema": "norn.legacy-control-backup/v1",
  "sourceReleaseSHA": "<40 lowercase hex legacy release SHA>",
  "databaseIdentity": "hmac-sha256:<database identity>",
  "backupSHA256": "<64 lowercase hex backup digest>",
  "backupBytes": 12345,
  "createdAt": "<RFC3339 timestamp with offset>"
}
```

`databaseIdentity` is the existing Norn database-identity HMAC computed with
the production `NORN_DATABASE_URL` and `NORN_AUDIT_SIGNING_KEY`. The proof is
accepted only when its mode is exactly `0600`, its release and database
identity match the command environment, and it is no more than
`NORN_LEGACY_BACKUP_MAX_AGE_SECONDS` old (one hour by default). The proof is
a machine-checkable binding to a backup artifact: the command hashes the
artifact with SHA-256 and requires it to equal `backupSHA256`; it also requires
the actual file size to equal `backupBytes`. Both checks run before it builds
and again immediately before it fences the legacy service. The change record
still must contain a successful restore verification for that artifact.

Run:

```sh
NORN_DRAIN_MODE=fail \
v2/scripts/platform-upgrade legacy-baseline <candidate-ref> \
  --legacy-release <legacy-release-sha> \
  --backup-proof /absolute/private/path/backup-proof.json \
  --backup-artifact /absolute/private/path/control-backup.dump
```

The command requires an authenticated API token, an exact zero active
operation count, the launchd service PID, and the sole loopback API listener.
It also verifies that the supervised legacy API artifact exactly equals the
declared current release.

## Irreversible boundary

Before migration, the command atomically replaces the launchd API executable
with a fence program, terminates the verified legacy PID, and confirms that
the listener is gone. It writes a persistent fence state file at
`NORN_LEGACY_FENCE_STATE_PATH` (default: beside the legacy executable). The
legacy executable cannot be restarted through its normal launchd path after
this point.

Only after the fence and strict drain check does it run the candidate schema
migration and passive candidate check. It promotes and postflights the
candidate only after that check. A postflight failure records
`candidate-postflight-failed` and keeps the legacy binary fenced. Repair or
roll forward with a contract-aware candidate; do not restore or directly run
the legacy binary against the migrated control database.

The transition has focused fixture coverage for proof rejection, service
fencing before migration, candidate promotion, and retained fence state. It
does not qualify a Mini deployment on its own. The remaining M5 gate is a
scheduled Mini rehearsal using a fresh read-only copy, a verified restore of
the actual protected backup, and the complete maintenance/fencing procedure.
