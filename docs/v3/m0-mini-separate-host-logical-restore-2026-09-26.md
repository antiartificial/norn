# Mini-to-Mac synthetic logical restore — 2026-09-26

This checks a shared recovery mechanic for either Mini control-backup option:
transport a backup away from the Mini, start a clean PostgreSQL instance on a
second host, and verify restored content. It uses only synthetic rows. It does
not read `norn_v2`, carry a production credential, or select an off-host
repository, backup schedule, RPO, or retention policy.

The [export fixture](https://github.com/antiartificial/norn/blob/master/v2/scripts/mini-postgresapp-offhost-export-fixture.py)
starts a temporary Postgres.app 17 cluster under `/tmp` on the Mini, inserts
two known rows, and writes a custom-format dump to stdout. It stops and
removes the cluster. The dump was transferred over SSH to a mode-`0600` file
on this Mac. The [restore fixture](https://github.com/antiartificial/norn/blob/master/v2/scripts/mini-postgresapp-offhost-restore-fixture.py)
requires that small synthetic dump, starts a clean local cluster using
Postgres.app 17, restores it, checks both rows, and removes the cluster.

Example, with a private destination path and PostgreSQL 17 installed on the
restore Mac:

```sh
umask 077
ssh mini 'python3 -' < v2/scripts/mini-postgresapp-offhost-export-fixture.py > /absolute/private/synthetic.dump
python3 v2/scripts/mini-postgresapp-offhost-restore-fixture.py /absolute/private/synthetic.dump
```

The 2026-09-26 run used Postgres.app 17.7 on both arm64 Macs. The target
restored both rows from the 1,965-byte dump; its SHA-256 was
`d60af2a586931cf5c6acb1d71adb844406dba71af83b1663cbe0823543c84930`.
The fixture's target init, restore and row check took 0.76 seconds. These
numbers are synthetic stage measurements, not an end-to-end control RTO.

The target fixture uses `pg_restore -O -x` because its synthetic source role
does not exist on the clean target. That intentionally does not qualify
production role, ownership, or ACL preservation. The logical restore also
does not test physical base backup, WAL replay, encryption, versioned remote
storage, clean-host production credentials, or a source-host-loss drill. The
local [PITR fixture](m0-mini-postgresapp-disposable-pitr-2026-09-26.md)
tests timestamped WAL recovery separately; neither fixture qualifies the
Mini's proposed 15-minute RPO or 30-minute RTO.
