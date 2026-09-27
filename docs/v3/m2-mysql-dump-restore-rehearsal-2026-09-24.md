# M2 MySQL exact-target dump/restore rehearsal

Date: 2026-09-24. Scope: disposable local MySQL 8.4 container.

`v2/scripts/test-mysql-dump-restore.sh` starts a disposable MySQL container
and runs `TestMySQLExactTargetDumpRestore`. The test creates two unique
databases and distinct least-scope users, seeds only the source, resolves both
through the versioned database catalog, and opens sessions that probe the
declared database and role. It rejects a stale target generation before any
restore. It also asserts that both MySQL snapshot and restore remain rejected
by the implemented-capability gate.

The rehearsal uses `mysqldump --single-transaction --quick` to write an
owner-only SQL file, records its SHA-256, and feeds that file to `mysql` with
the destination database explicitly selected. The clients use separate
owner-only option files, isolated process environments, and no password
arguments. The test verifies the marker in the destination and that the
source remains intact. On this run, MySQL 8.4.11 and the local MySQL client
passed in 2.14 seconds. The disposable databases, users, and container were
removed after the run.

This is a toolchain and target-selection qualification, **not** an enabled
v3 MySQL recovery adapter. Before exposing snapshot or restore as a runtime
capability, the service needs a durable operation record, a fenced exact
catalog revision, source write quiescence or a documented consistency
boundary, artifact encryption and retention, checksum verification before
restore, an empty destination or explicit replace policy, post-restore
application reconnect checks, retry/crash reconciliation, and TLS-capable
client material. The test establishes no point-in-time recovery guarantee.

## Read-only restore preparation slice

`database.StageMySQLSQLSnapshot` now supplies a local staging primitive. It
requires an exact source identity, a SHA-256-pinned `mysqldump` executable,
and an owner-only staging directory. It probes the source role and database,
rejects nontransactional tables, uses private client options, bounds the SQL
output to 64 GiB, syncs the file, and returns a source-bound size and SHA-256
record. The caller owns removal of the staged file after publication or
failure reconciliation.

`database.PrepareMySQLRestore` supplies a read-only preflight for a future
restore executor. It re-resolves the exact expected target identity,
rejects a source artifact that names the same target, verifies a bounded
owner-only regular SQL file and its SHA-256, probes the destination database
and role, and confirms the destination has no tables, views, routines, or
events. Symlink artifacts, altered bytes, stale generations, and a nonempty
destination fail in the disposable integration test. No SQL write is performed
by the preflight, and MySQL snapshot/restore stay out of
`implementedCapabilities`.

This preparation is not durable authorization. A writer may appear after its
empty-destination check, and a path may change after checksum verification.
The eventual executor must hold a durable exclusive target fence, bind a
signed artifact record to the accepted operation and catalog revision, verify
the same opened artifact inode immediately before consumption, and reconcile
crashes before permitting another writer or retry. Private local SQL staging
also needs encrypted publication and retention controls before release. The
`--single-transaction` dump assumes InnoDB and requires DDL quiescence from a
future lifecycle controller; staging cannot enforce that condition by itself.
No production restore write path, reconnect verification, retry protocol, or
point-in-time recovery is claimed.

## Verified snapshot CLI transport

Snapshot staging now passes the source catalog's TLS policy to `mysqldump`.
It stages CA and optional client certificate/key bytes in the session's
owner-only directory, forces `VERIFY_CA` or endpoint-bound `VERIFY_IDENTITY`,
and refuses unsupported modes. Disabled TLS is explicit rather than relying
on the client's opportunistic default. Session cleanup removes the private
material.

The disposable MySQL 8.4.11 source user required SSL. With the server CA,
the actual MySQL 8.0.27 `mysqldump` staged and hashed the expected data; the
same CLI rejected an unrelated CA. The staging artifact then passed the
exact-target restore preflight and disposable restore test. This qualifies
the verify-CA snapshot path only. A live verify-full/client-certificate
snapshot and TLS-protected restore subprocess remain open, alongside the
durable recovery lifecycle and protected artifact publication.
