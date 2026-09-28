# Mini Postgres.app disposable PITR check — 2026-09-26

This is a synthetic, local-only check of the Mac PostgreSQL mechanics needed
if the owner selects the 15-minute off-host PITR option. It did not connect to
`norn_v2`, change the Mini's PostgreSQL settings, upload an archive, or use a
production credential.

The [fixture](../../v2/scripts/mini-postgresapp-pitr-fixture.py) creates a
temporary Postgres.app cluster on port 15551 under `/tmp`, archives WAL to a
second temporary directory, takes a physical base backup, writes one row
after that backup, records a recovery target, then writes another row after
the target. It stops the source and restores from the base backup and archived
WAL. The expected restore contains the first two rows and excludes the third.
Both temporary clusters are stopped and removed after success.

Run it on a Mac with Postgres.app 17 installed, against synthetic data only:

```sh
python3 v2/scripts/mini-postgresapp-pitr-fixture.py
```

The 2026-09-26 Mini run used Postgres.app 17.7 and passed: the restored table
contained the pre-backup and post-backup rows, excluded the post-target row,
and five WAL files reached the local archive. The fixture's measured elapsed
time was 1.79 seconds; it omits protected source backup, off-host transfer,
clean-host retrieval, candidate startup, identity checks, and operator work.
It is neither a Mini control RPO/RTO result nor a substitute for the
[control recovery decision](m0-mini-control-recovery-decision.md).

The test also exposed a PITR fixture constraint: a target timestamp after the
last replayed commit cannot be reached. It deliberately writes and archives a
later transaction so recovery can stop at the intended time and prove the
exclusion boundary.

The fixture now copies the base backup and WAL into a separate retained-input
directory, removes the source cluster and original backup/archive directories,
and restores only from that retained copy. A local rerun with Homebrew
PostgreSQL 16.15 passed: two expected rows, four archived WAL files and no
remaining disposable cluster directory. Postgres.app is unavailable on this
workstation, so this rerun does not refresh the earlier Mini Postgres.app
receipt. The retained directory is still on the same machine, not an off-host
repository; no production data or protected Mini host was touched.

Next, choose the Mini control RPO, off-host destination, and retention. For
the 15-minute path, configure protected WAL archiving, base backups, lag and
failure alerts, and a timed clean-host restore from the remote repository.
Keep the existing one-hour legacy-upgrade proof separate from that recurring
disaster-recovery catalog.

This next step was superseded by the 2026-09-28
[dev Mini decision](m0-mini-control-recovery-decision.md): an on-host upgrade
backup is sufficient for the development machine. Production disaster recovery
still needs its own off-host qualification.
