# M0 Mini control-store budget proposal — 2026-09-26

Status: review proposal, not an accepted release budget or M0 sign-off. It
covers Mini's control PostgreSQL only. Application databases, Nomad/Consul
state, object archives, logs, secrets, and Fleet etcd need separate budgets.

## Measured input

Read-only PostgreSQL aggregates at 2026-09-26 23:02:05 UTC put `norn_v2` at
250,031,251 bytes (238.45 MiB), including 241,016,832 bytes of user tables,
150,151,168 bytes of heap, and 89,800,704 bytes of indexes. Four connections
were present at that instant. The 2026-09-24 15:59 UTC baseline was
240,323,731 bytes. The 9,707,520-byte increase over about 2.294 days
linearizes to 4.04 MiB/day at this observed workload. Prior samples spanning
two calendar days were near the same rate. This is not a peak or a guaranteed
future rate.

A current-head, read-only Mini source-copy rehearsal restored 254,849 original
rows, applied migrations 1–43, preserved original-table counts and digests, and
passed passive startup. Its command finished in about nine seconds on this
host, excluding candidate build/transfer, protected-backup retrieval, service
fencing, traffic checks, and rollback decisions. A separate measured v2 custom
dump was about 13.2 MB and took 0.82 seconds; a local disposable restore took
3.81 seconds. Those measurements are stage bounds, not an end-to-end RTO.

At 23:02 UTC, `archive_mode=off` and `archive_command=(disabled)` on Mini's
PostgreSQL. `pg_stat_archiver` showed zero archived files. Cluster-wide
`pg_stat_wal` counters cannot attribute WAL volume to the Norn control
database, so no control-specific WAL retention estimate or 15-minute PITR
proof follows from them.

## Proposed control-store triggers

| Item | Proposed threshold | Basis and action |
| --- | --- | --- |
| Growth allowance | 8 MiB/day for planning | About 2× the observed 4.04 MiB/day. Re-measure after workload changes and over a representative cycle. |
| Size warning | 384 MiB physical database size | At the planning rate this is about 18 days from the measured size; review table mix, archive, backup, and disk trend. |
| Size action | 512 MiB physical database size | At the planning rate this is about 34 days from the measured size. Require an explicit retention/capacity decision before further growth is treated as routine. This is a review trigger, not permission to prune evidence. |
| Private restore scratch | At least 2 GiB free plus the protected archive bytes | Four times the 512 MiB review size, before PostgreSQL initialization and restore. Validate actual free space and object sizes at the maintenance window; leave source backup and production data untouched. |
| Control restore RTO | Keep the draft ≤30 minutes, unqualified | Measure the full protected-backup retrieval, exact candidate transition, API/auth checks, and operator decision. Local dump/restore timing alone cannot prove it. |
| Control backup RPO | Decision required | The draft ≤15 minutes is unsupported while WAL archiving is off and no 15-minute off-host full-backup cadence is proved. Either build and qualify that recovery path or explicitly approve a different Mini backup-only RPO. Do not infer HA failover RPO from a backup target. |

These thresholds do not set evidence or log retention. A safe retention policy
needs reader/call-site audit, archive verification and index recovery, legal or
operator retention choices, and observed byte growth for each store. Current
snapshot-retention warnings for `field-harbor` and `turnkey-offer-intake` are
application concerns, not proof that the control-store budget is satisfied.

## Acceptance work

1. Name the owner who may accept the Mini control RPO/RTO and the retention
   policy. Decide whether the initial Mini release must meet the draft
   15-minute backup-only RPO or a separately stated target. Record the chosen
   backup cadence, off-host destination, immutable/retention behavior, and
   restore test.
2. Measure WAL or full-backup growth on the chosen mechanism and reserve its
   actual off-host bytes. A single 13.2 MB dump and cluster-wide WAL counter
   cannot size that store.
3. Run the protected production-key backup and private restore using an exact
   signed candidate; measure total elapsed time and verify data/identity.
   Rehearse the one-way legacy fence and recovery boundary in a scheduled
   maintenance window.
4. Re-sample control and application ownership after representative workload
   cycles, then accept or revise the warning/action triggers. Until then M0 and
   M5 remain open.
