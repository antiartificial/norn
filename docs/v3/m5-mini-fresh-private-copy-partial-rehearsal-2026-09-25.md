# Mini fresh private-copy candidate rehearsal — 2026-09-25

Status: the repeatable private-copy migration and passive-startup checks
passed. This is supporting M0/M5 evidence; it does not qualify a Mini upgrade,
legacy fence, rollback, or production backup restore.

The checked [rehearsal script](../../v2/scripts/mini-private-copy-rehearsal)
was syntax-checked before use and ran on the Mini with a candidate built from
exact commit `afc1b3d6c22eee89eda6f5bee1815b2eb938db88`. The candidate binary had
SHA-256 `3bb8093eb4670e708c0bb9b46c6db339fd1656d495e246f4bc573f6bf4d9dbd3`.
The script requires an explicit source database URL and verifies the
candidate's `norn.startup/v2` contract before accessing the database.

The Mini control database was read through `pg_dump` with
`default_transaction_read_only=on`. The owner-only custom-format dump was
restored into PostgreSQL 17.7 using a private Unix socket, with TCP listening
disabled. The restored copy contained the expected 28 original public tables
and 247,525 rows. For each table, the script recorded its row count and the
SHA-256 digest of its ordered primary-key projection.

The candidate completed `NORN_SCHEMA_MODE=migrate-only` twice. The resulting
`norn_schema_migrations` ledger contained the contiguous versions 1 through
23 with nonempty SHA-256 checksums. `norn_schema_compatibility` reported
current migration 23, minimum reader 4, and minimum writer 19. All 28 original
table counts and primary-key fingerprints matched after both migrations.

The same candidate then ran with `NORN_STARTUP_MODE=passive` and
`NORN_SCHEMA_MODE=check` on a loopback-only ephemeral port. Exact JSON checks
confirmed passive health, the candidate commit in `/api/version`, migration
23 and compatibility 4/19 in `/api/schema`, and disabled operation recovery,
operation worker, and Nomad watcher flags.

The exit trap stopped the passive candidate and private PostgreSQL cluster,
then removed the dump, connection environment file, fingerprints, logs,
database files, and transferred candidate. A separate post-run check found no
matching remote scratch or staging directory. The running Mini API remained
healthy and no live Mini database, API process, workload, or launchd service
was changed.

## Remaining release limits

This rehearsal used a locally built exact-commit candidate rather than an
immutable signed release artifact. It proves the copied-data migration and
passive reader path, but it does not exercise the one-way legacy service fence,
promotion, live traffic, rollback, or restore from production backup storage.
The installed legacy Mini API still lacks the `norn.startup/v2` capability, so
the protected upgrade lane must continue to refuse a normal transition. M5
still requires the guarded
[legacy-to-contract baseline transition](m5-legacy-baseline-transition.md), a
production storage backup and restore proof, and controlled maintenance.
