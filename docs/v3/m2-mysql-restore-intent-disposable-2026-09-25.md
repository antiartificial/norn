# M2 MySQL restore intent on disposable engines — 2026-09-25

The local `v2/scripts/test-mysql-dump-restore.sh` harness starts MySQL 8.4 in
Docker and PostgreSQL 16.15 in a private Unix-socket data directory. It runs
the direct adapter dump/restore tests and
`TestMySQLRestoreIntentAgainstDisposableEngines` before removing both engines
and their scratch files. The script now requires `mysql` and `mysqldump` on
`PATH`, so the store integration cannot silently pass by skipping for absent
client tools. This run used the DBngin MySQL 8.0.27 client tools.

Command: `bash v2/scripts/test-mysql-dump-restore.sh` from the repository root.

Result: both `TestMySQLExactTargetDumpRestore` and
`TestMySQLRuntimeComponentsReachDeclaredTarget` passed; the store restore
intent test passed in 3.53 seconds. The store test retained the signed source
artifact through the local S3 emulator, removed the original staged file,
materialized from retained storage, imported through a separate restore
identity while runtime login was locked, verified the restored marker and
durable completion, and exercised the retained runtime fence and signed
recovery path. The post-import artifact check added at `8b1fd50` ran on this
real import path. Script commit `5ea7912` made this combined run repeatable.

This is local two-engine integration evidence. It does not qualify hosted
object storage, a separate-node recovery, a managed MySQL service, a stock
application startup, Mini promotion, or a Fleet release. Those M2/M8 gates
remain open.
