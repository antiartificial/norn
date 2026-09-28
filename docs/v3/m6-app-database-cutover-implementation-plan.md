# M6 application database cutover implementation plan

Status: proposed implementation contract. No application database cutover is
implemented or authorized. The ordinary deploy guard in
`v2/api/pipeline/database_guard.go` must keep refusing a live target change
until this lane has been independently qualified.

## First supported unit

Qualify one PostgreSQL-to-PostgreSQL application target replacement with a
synthetic web and worker workload. The same state machine is intended for a
selected MySQL path, but engine-specific backup, final synchronization,
privilege fencing and restore evidence must pass separately. A database
credential rotation on the same target remains an ordinary deployment; this
lane is for a change in `database.TargetIdentity`.

The coordinator's durable journal belongs in the active Norn control store,
outside both application databases. A protected recovery copy of its manifest
and evidence references must also be retrievable when the control API is
unavailable. Each record binds the app, logical database name, source and
target service/binding IDs and generations, exact candidate release, accepted
operation, source writer inventory, and an immutable authority generation.
Persist credential references, never DSNs or secret values. An app operation
lock alone is insufficient: it does not stop already running web, worker,
cron or external integration writers.

## Ordered checkpoints

| Phase | Required durable evidence before advance | Interruption response |
| --- | --- | --- |
| Prepare | Target identity and engine compatibility, schema/extensions/roles, capacity, credentials, network path and a verified initial restore | Keep source authoritative; remove or reuse the passive target only after comparing its recorded identity. |
| Quiesce | Exact source allocation and external-writer inventory, disabled schedules, drained acknowledged work, and a source-side write fence with readback | Keep source fenced if the fence result is uncertain; do not start target writers. |
| Final sync | Source checkpoint/LSN or engine-specific equivalent, target import receipt, row/key and application integrity checks | Repeat only an explicitly idempotent transfer step; reconcile an ambiguous commit by its recorded identity. |
| Activate | One transaction selects the new consumer generation after verified source fence and final sync; target jobs receive generation-bound credentials | A lost response is resolved from the journal and target job readback. Never activate a second generation from an ambiguous reply. |
| Verify | Web, worker and scheduled work use only the target; old credentials and network path remain fenced; data and acknowledged work reconcile | Stop promotion and repair forward while preserving the source fence. |
| Accept | Operator-reviewed observation window and recovery choice are recorded | Retain source and evidence; retirement is a separate operation. |

Before target writes, a coordinated return may reactivate the source only
after proving the target has not accepted writes and fencing the target.
After target writes, returning to the source requires a separately qualified
reverse synchronization and reconciliation path. Restoring old credentials or
switching DNS is not rollback.

## Implementation boundaries

1. Add a versioned cutover intent and journal with compare-and-swap phase
   transitions, app lock and operation-claim fencing, immutable source/target
   identities, and one active consumer generation. Preserve the existing
   `app.deploy` target-change refusal.
2. Build read-only inventory and preflight for all app writers, including
   Nomad allocations, schedules, connection pools and declared integrations.
   An unknown writer blocks the cutover.
3. Implement provider-specific source write fencing and its independent
   readback. A stopped application process is not proof that a database user
   or external integration cannot still write.

   A disposable PostgreSQL rehearsal in
   `v2/api/database/postgres_writer_fence_rehearsal_test.go` passed on
   2026-09-27. It proves that `ALTER ROLE ... NOLOGIN` rejects fresh runtime
   authentication but leaves an existing runtime session usable until its
   backend is terminated. The first PG fence must therefore disable login,
   terminate every session for the exact dedicated runtime role, and read back
   both `rolcanlogin = false` and zero sessions before final sync. This
   On disposable PostgreSQL 16, the same sequence passed through a dedicated
   non-superuser account with `CREATEROLE`, admin authority for the exact
   runtime role without `INHERIT` or `SET`, `pg_signal_backend`, and
   `pg_read_all_stats`. The fixture also rejects `SET ROLE` into the runtime
   data role. This establishes a candidate privilege shape, not a qualified
   production fence account: provider support, credential binding, exact
   account identity, and revocation/readback still need implementation.
   Other roles, pools, and integrations may still write; they belong in the
   writer inventory and must block promotion until each is fenced and verified.
4. Add initial restore, final transfer and integrity adapters for one exact
   engine/provider pair. Checkpoint each external effect before retrying it.
5. Switch generation-bound runtime secrets and consumers only after the final
   source fence and target integrity receipt. Prove stale allocations and
   credentials cannot write either target after activation.
6. Rehearse every phase with process termination, lost responses and a
   successor coordinator. Verify journal recovery without trusting a copied
   lease, then repeat on the selected MySQL path.

## Release evidence and decisions

The M6 gate needs a real-engine PG and selected MySQL backup/restore,
planned engine upgrade or cluster replacement, exact writer-fence readback,
target-only activation, and interruption recovery at every checkpoint.
Record measured write pause, application request impact, data/work
reconciliation and the accepted post-write recovery boundary for each path.
The v3 A-to-B product rolling upgrade is a separate M6 rehearsal.

Before implementation can promote a real app, owners must choose the first
source/target provider pair and app consistency group, name every writer and
fence mechanism, set the observation and retention windows, and accept the
measured interruption and recovery targets. Until then, M6 remains open and
the ordinary deploy guard remains the supported behavior.
