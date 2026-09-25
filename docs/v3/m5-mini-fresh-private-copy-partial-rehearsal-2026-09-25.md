# Mini fresh private-copy partial rehearsal — 2026-09-25

Status: the isolated database-copy and candidate migration phases passed. This
is supporting M0/M5 evidence only; it does not qualify the legacy baseline
transition or a Mini deployment.

The Mini control database was read through a fresh `pg_dump` only. The dump
was written with owner-only permissions into a newly-created temporary
directory, restored into a PostgreSQL 17 cluster using a private Unix socket,
and never exposed on TCP. No Mini database object, API process, worker,
application runtime, or launchd configuration was changed.

The current v3 candidate binary completed `NORN_SCHEMA_MODE=migrate-only`
against the restored copy with operation recovery, workers, and the Nomad
watcher disabled. A subsequent verifier accidentally queried
`schema_migrations` rather than `norn_schema_migrations`; it therefore stopped
before it printed the intended pre/post original-table count fingerprints.
The candidate migration itself had completed before that query.

The running Mini API remained alive after the rehearsal and a read-only schema
inventory still reported 28 public tables. The cleanup trap stopped the
temporary PostgreSQL cluster and removed its directory, the dump, and the
transferred candidate binary. A final cleanup check found no matching scratch
directories or candidate binary.

## Still unproven

This run did not execute the one-way legacy service fence, candidate passive
HTTP startup and postflight, a post-migration original-row fingerprint check,
or any rollback. The installed legacy API does not implement
`norn.startup/v2`, so it cannot be a supported post-migration rollback target.
M5 still requires scheduled Mini maintenance with the guarded
[legacy-to-contract baseline transition](m5-legacy-baseline-transition.md), a
current production backup proof and restore verification, and the full
maintenance/fencing procedure.
