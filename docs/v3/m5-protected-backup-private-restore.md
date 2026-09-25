# M5 protected control backup private restore

The [private-copy rehearsal](../../v2/scripts/mini-private-copy-rehearsal) has a
protected-backup input mode for the one-way
[legacy-baseline transition](m5-legacy-baseline-transition.md). It does not
connect to the source database in this mode. It validates the exact
`norn.legacy-control-backup/v1` proof against the artifact's SHA-256 and byte
count, source release SHA, database-identity HMAC, age, ownership, and mode.
The database URL is used only as HMAC input. It then copies the verified bytes
to owner-only scratch storage, validates that copy, and restores it through
`pg_restore` into PostgreSQL listening only on a private Unix socket.

Set these values from the protected Mini change record and run on the host
with a disposable candidate binary built from the exact candidate commit:

```sh
NORN_CANDIDATE_SHA=<full-candidate-commit-sha> \
NORN_REHEARSAL_LEGACY_RELEASE_SHA=<full-installed-release-sha> \
NORN_REHEARSAL_BACKUP_PROOF=/absolute/private/path/control-proof.json \
NORN_REHEARSAL_BACKUP_ARTIFACT=/absolute/private/path/control.dump \
NORN_DATABASE_URL='<exact-control-database-url>' \
NORN_AUDIT_SIGNING_KEY='<existing-audit-signing-key>' \
v2/scripts/mini-private-copy-rehearsal /absolute/path/to/disposable/norn-api
```

Keep the URL and signing key in the normal protected environment rather than
typing them into a shared shell history. The script rejects a source-database
URL alongside the backup input. Its only source-file operations are reads;
after the rehearsal it verifies the protected artifact again to detect a
change during the run. All migrations and passive candidate startup use the
disposable database. The ordinary private-copy checks still require 28
original public tables, unchanged original row counts and ordered primary-key
fingerprints, two successful migrate-only runs, a complete schema ledger, and
passive health/version/schema responses from the exact candidate commit.

The proof validator has disposable positive and negative fixture tests:

```sh
python3 v2/scripts/test-mini-protected-backup.py
bash -n v2/scripts/mini-private-copy-rehearsal
```

This command must be run against the **actual protected Mini backup** before
it counts as M5 restore evidence. Record the artifact digest and size, proof
creation time, legacy release SHA, candidate source and binary digests,
PostgreSQL version, original row/table counts, migration ledger, and cleanup.
Do not commit the artifact, proof, database URL, or signing key. A passing
private restore still leaves the scheduled maintenance, actual service fence,
promotion, traffic observation, and supported rollback/roll-forward procedure
as separate gates.
