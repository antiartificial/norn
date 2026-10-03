# M5 protected control backup private restore

The [private-copy rehearsal](https://github.com/antiartificial/norn/blob/master/v2/scripts/mini-private-copy-rehearsal) has a
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
NORN_CANDIDATE_VERSION=<exact-NORN_RELEASE_VERSION-from-verified-release.env> \
NORN_REHEARSAL_LEGACY_RELEASE_SHA=<full-installed-release-sha> \
NORN_REHEARSAL_BACKUP_PROOF=/absolute/private/path/control-proof.json \
NORN_REHEARSAL_BACKUP_ARTIFACT=/absolute/private/path/control.dump \
NORN_DATABASE_URL='<exact-control-database-url>' \
NORN_AUDIT_SIGNING_KEY='<existing-audit-signing-key>' \
v2/scripts/mini-private-copy-rehearsal /absolute/path/to/disposable/norn-api
```

Before invoking the rehearsal, the caller must verify the signed candidate
manifest for `NORN_CANDIDATE_SHA` and read both `NORN_RELEASE_SHA` and
`NORN_RELEASE_VERSION` from that verified release's `release.env`. The
rehearsal checks that the candidate returns that exact version label from its
passive `/api/version` and `/api/schema` responses; it does not replace the
manifest's SHA-to-version binding. Describe-style labels must also carry a
short SHA prefix matching `NORN_CANDIDATE_SHA`.

Keep the URL and signing key in the normal protected environment rather than
typing them into a shared shell history. The script rejects a source-database
URL alongside the backup input. Its only source-file operations are reads;
after the rehearsal it verifies the protected artifact again to detect a
change during the run. All migrations and passive candidate startup use the
disposable database. The ordinary private-copy checks require a nonempty
public-table inventory from the exact restored backup, unchanged row counts,
ordered primary-key and full-row fingerprints for every original table, two
successful migrate-only runs, a complete schema ledger, and
passive health/version/schema responses from the exact candidate commit.

The proof validator has disposable positive and negative fixture tests:

```sh
python3 v2/scripts/test-mini-protected-backup.py
python3 v2/scripts/test-mini-private-copy-source.py
bash -n v2/scripts/mini-private-copy-rehearsal
```

This proof is intentionally fresh for the one-way upgrade transition: the
default maximum age is one hour. It is not a recurring off-host disaster
recovery catalog. The [M0 recovery decision](m0-mini-control-recovery-decision.md)
requires a separate retained-backup path and clean-host restore qualification
for either proposed RPO.

The read-only source-dump mode now carries supported libpq URL settings,
including host address and TLS certificate paths, into `pg_dump`. It rejects
unknown or repeated URL parameters and clears inherited `PG*` variables before
loading the selected source connection, so a rehearsal cannot silently use an
unrelated libpq service or endpoint. A fake-`pg_dump` fixture verifies those
settings and early rejection; it does not replace the fresh Mini copy or
protected-artifact restore rehearsal.

This command must be run against the **actual protected Mini backup** before
it counts as M5 restore evidence. Record the artifact digest and size, proof
creation time, legacy release SHA, candidate source and binary digests,
PostgreSQL version, original row/table counts, migration ledger, and cleanup.
Do not commit the artifact, proof, database URL, or signing key. A passing
private restore still leaves the scheduled maintenance, actual service fence,
promotion, traffic observation, and supported rollback/roll-forward procedure
as separate gates.

## Signed shadow LaunchAgent rehearsal

`mini-signed-shadow-launchagent-rehearsal` composes the protected-backup
private restore with a separately bootstrapped macOS LaunchAgent. It verifies
the exact candidate release manifest and Ed25519 artifact before use. Only
after the private restore, two migrate-only passes, and direct passive check
does it start `com.norn.m5.shadow.<candidate-prefix>` against the socket-only
private database. The generated plist, launcher, runtime environment, logs,
and cleanup state stay in a fresh mode-`0700` temporary directory. It never
names `com.norn.api`, `~/Library/LaunchAgents`, port `8800`, or the current
release link.

Run the following only in a scheduled macOS rehearsal with a fresh protected
backup; keep the URL and audit key in the protected shell environment:

```sh
NORN_M5_SIGNED_SHADOW_REHEARSAL=1 \
NORN_REHEARSAL_BACKUP_ARTIFACT=/absolute/private/control.dump \
NORN_REHEARSAL_BACKUP_PROOF=/absolute/private/control-proof.json \
NORN_REHEARSAL_LEGACY_RELEASE_SHA=<installed-legacy-sha> \
NORN_M5_SHADOW_RECEIPT=/absolute/private/shadow-rehearsal-receipt.json \
NORN_DATABASE_URL='<protected-maintenance-url>' \
NORN_AUDIT_SIGNING_KEY='<protected-maintenance-key>' \
v2/scripts/mini-signed-shadow-launchagent-rehearsal run \
  --candidate-release /absolute/releases/<candidate-sha> \
  --public-key /absolute/pinned-release-public-key.pem
```

The shadow agent must report the release-derived version, `passive`/`check`,
and disabled deployment recovery, operation recovery, operation worker, and
Nomad watcher. It is an unrun runtime gate until it succeeds on the selected
macOS host. Source checks and fixtures do not prove Mini LaunchAgent behavior,
traffic continuity, rollback, production release qualification, or host-loss
recovery.

The receipt path must be new and absolute. The harness atomically writes a
mode-`0600` sanitized receipt with the candidate SHA, unique shadow label,
port, exit code, and whether the runtime check passed. It copies the requested
release into the private scratch root and verifies that snapshot, so release
contents cannot change between signature verification and shadow execution.
The receipt is published without replacement; a concurrent claimant makes the
rehearsal fail visibly. Its runtime state is `not-run`, `failed`, or `passed`,
so an early source/restore failure is not presented as a completed macOS run.
Argument, path, and candidate-snapshot identity rejection occurs before an
attempt exists and intentionally creates no receipt.

## Create the protected input

The checked
[`mini-create-protected-backup`](https://github.com/antiartificial/norn/blob/master/v2/scripts/mini-create-protected-backup)
helper creates a custom-format dump and exact proof in an existing absolute
directory owned by the invoking user with mode `0700`. It requires the same
`NORN_DATABASE_URL` and `NORN_AUDIT_SIGNING_KEY` that the maintenance command
will use. Load those values through the protected environment; do not type
them into shared shell history. With the exact installed legacy SHA:

```sh
v2/scripts/mini-create-protected-backup \
  --legacy-release <full-installed-release-sha> \
  --output-dir /absolute/private/backup-directory
```

The helper forces a read-only source transaction, writes both files with mode
`0600` and exclusive creation, checks the custom archive's listing, computes
the digest and database-identity HMAC, and calls the independent proof
verifier before returning their paths and safe fingerprints. It cleans up its
own partial files on failure. It passes supported URL connection parameters,
including TLS CA and client material, to libpq and rejects parameters it
cannot preserve. A disposable PostgreSQL 16.15 run passed with a
Unix-socket source and a synthetic row. That run validates the producer and
verifier, not a Mini production-key backup. The development Mini's current
[recovery decision](m0-mini-control-recovery-decision.md) permits an owner-only
on-host backup for the upgrade window, with a private restore of those exact
bytes before transition. At the time of the rehearsal below, Mini's launcher
lacked the required URL and audit key; both were installed later, and a
protected artifact and separate-Mac restore are recorded in the
[current handoff](session-resume-2026-09-29.md).
The [2026-09-25 staged-binding rehearsal](m5-mini-staged-binding-backup-rehearsal-2026-09-25.md)
proved this producer and private restore against live Mini data using an
inactive encrypted key candidate. It did not itself prove the later protected
backup, and it carries no host-loss recovery claim.
