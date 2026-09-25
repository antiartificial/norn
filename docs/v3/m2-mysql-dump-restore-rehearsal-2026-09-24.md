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
