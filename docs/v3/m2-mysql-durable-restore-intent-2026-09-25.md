# MySQL restore intent: durable fence slice

Migration 24 adds a private `mysql_restore_intents` aggregate. It is additive:
the reader floor stays 4 and the writer floor stays 19 because no existing
writer or public endpoint can execute MySQL restore. The MySQL snapshot and
restore capabilities remain disabled in `implementedCapabilities`.

`PrepareClaimedMySQLRestore` verifies the signed accepted operation, its live
generation-bound worker claim, exact request payload, active catalog revision,
resolved target identity, private artifact checksum, target connection, and
empty target. The catalog advisory lock serializes preparation with catalog
activation. The target key is unique for all time within that identity's
service/binding generations. Reusing the same operation is idempotent;
another operation cannot consume the target generation.

`BeginClaimedMySQLRestore` repeats those checks and commits `executing` before
any external SQL write. It cannot be entered twice. A crash after this commit
must leave the intent in `executing` for manual inspection: replaying a SQL
dump could duplicate or corrupt a partially applied restore.

## Qualification in this slice

The opt-in integration test uses disposable PostgreSQL 17 and MySQL 8.4. It
stages an actual `mysqldump`, accepts a signed operation, starts a real claim,
persists and replays the preparation, rejects a changed signed target, enters
the durable external-effect boundary once, and rejects reentry. The migration
also runs through the Mini synthetic upgrade test and full store tests.

## Remaining release work

- Bind source quiescence and a protected, retained artifact to signed
  acceptance. The local path is currently host-specific and ephemeral.
- Implement a supervised MySQL restore runner that consumes this intent,
  pins a checked tool and verified transport, uses the verified artifact inode,
  and records success only after independent target verification.
- Add a maintenance fence covering application writes and catalog changes for
  the entire restore, plus explicit operator reconciliation for `executing`
  after crash or timeout. An ordinary operation retry must never cross this
  boundary again.
- Rehearse TLS, wrong-CA rejection, backup retention, partial SQL failure,
  restart, and rollback with representative WordPress data and managed MySQL.

This slice does not make MySQL restore deployable or qualify M2 on its own.
