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

   `v2/api/cutover/journal.go` defines the shared v2 intent and ordered phase
   transition contract. Its tests reject same-target and mixed-engine intents,
   missing receipts, stale revisions and skipped activation. Control schema
   migration 46 now reserves a private PostgreSQL journal table, classifies
   its identity and digest fields for recovery inspection, and keeps its
   complete record in the hot retention inventory. Private PG methods create,
   read and advance that record by row lock and revision CAS. A disposable PG
   16 test passed legacy/Mini schema migration, exact replay, retarget refusal,
   one-active-resource uniqueness and two concurrent advancement attempts.
   A private etcd adapter now uses one transaction to reserve both a hashed
   operation key and a hashed active-resource key. It compares the journal
   modification revision and resource owner when advancing. A disposable
   single-member etcd test passed exact replay, retarget refusal, concurrent
   advancement and active-resource loss. Both store readers reconstruct the
   complete ordered receipt chain before returning a journal. This is
   **storage only**: no verified external receipt or consumer-generation switch
   calls either adapter yet. A private PG entrypoint
   now checks the signed `database.cutover` operation's exact intent SHA-256,
   candidate release and current operation claim in the same transaction that
   creates or advances its journal. The generic operation finisher refuses
   `database.cutover` success, and claim acquisition treats the kind as mutable
   work under the runtime mutation fence. A disposable PG 16 test passed
   exact replay, stale-claim refusal, signed-intent retarget refusal, ordered
   advancement and generic-success refusal. The etcd journal now has the
   matching private claimed path: each create/advance transaction compares
   the signed accepted operation, live leased owner, fenced app lock and
   journal revision. A disposable single-member etcd test passed signed
   preparation, exact replay, retarget/stale-claim/wrong-lock refusal,
   ordered advancement and generic-success refusal. Neither path grants
   runtime authority, and the ordinary deployment guard remains in force.
   Claimed preparation now pins the active catalog revision and digest in the
   signed intent and requires the same logical database to resolve to exact
   source and target bindings in separate profiles. Source must advertise
   runtime/snapshot and target runtime/restore. PG holds the catalog activation
   advisory lock through journal commit; etcd compares the active pointer and
   immutable revision in that transaction. Claimed quiesce and final-sync
   writes now also refuse a changed active catalog revision or binding. This
   covers the early phases of the first single-control-catalog path. Activation
   still needs a separate verified consumer-switch contract and later phases
   need external-effect gates; this does not imply a cross-control-plane
   migration or a working data transfer.
2. Build read-only inventory and preflight for all app writers, including
   Nomad allocations, schedules, connection pools and declared integrations.
   An unknown writer blocks the cutover.

   `nomad.ObserveCutoverWriterJobs` now reads every job with the app ID prefix
   in one Nomad region, including periodic children, function invocations and
   unexpected IDs. It records pending/running allocations even for older job
   versions or allocations Nomad wants stopped. It rereads job definitions,
   the job list and allocation state, rejecting a changing view. A disposable
   Nomad 2.0.7 readback and mock tests for old writers and list/allocation
   races passed. This is a **read-only regional observation**, not a signed
   writer inventory or a quiescence result. The coordinator still needs every
   region, app-specific jobs outside the name prefix, queue consumers,
   connection pools, external integrations and database sessions, plus a
   stable evidence digest bound to the accepted cutover operation.
3. Implement provider-specific source write fencing and its independent
   readback. A stopped application process is not proof that a database user
   or external integration cannot still write.

   A disposable PostgreSQL rehearsal in
   `v2/api/database/postgres_writer_fence_rehearsal_test.go` passed on
   2026-09-27. It proves that `ALTER ROLE ... NOLOGIN` rejects fresh runtime
   authentication but leaves an existing runtime session usable until its
   backend is terminated. The first PG fence must therefore disable login,
   terminate every session for the exact dedicated runtime role, and read back
   both `rolcanlogin = false` and zero sessions before final sync.
   On disposable PostgreSQL 16, the same sequence passed through a dedicated
   non-superuser account with `CREATEROLE`, admin authority for the exact
   runtime role without `INHERIT` or `SET`, `pg_signal_backend`, and
   `pg_read_all_stats`. The fixture also rejects `SET ROLE` into the runtime
   data role. This establishes a candidate privilege shape, not a qualified
   production fence account: provider support, credential binding, exact
   account identity, and revocation/readback still need implementation.
   Other roles, pools, and integrations may still write; they belong in the
   writer inventory and must block promotion until each is fenced and verified.

   The application PostgreSQL binding now accepts an optional, private
   `postgresFence` identity with its own generation, role and credential
   reference. Catalog validation requires a distinct runtime role and
   credential; changing the fence identity requires a binding generation
   bump. Resolution copies the identity, while public binding inspection
   omits it. A catalog sharing that runtime role with another binding on the
   same service is rejected. A private `FencePostgresRuntimeRoleForCutover`
   effect now uses the bound maintenance credential and target TLS policy,
   verifies a delegated non-superuser principal, disables login, terminates
   runtime sessions, and reads back the role and session state. Its disposable
   PG 16 test covers an existing writer, fresh authentication and an
   idempotent retry. The coordinator does not consume this effect yet; the
   catalog cannot prove external or uncataloged users of the role, so an
   operator-reviewed writer inventory remains a promotion prerequisite.
   A separate disposable PG 16 TLS fixture verifies the maintenance path
   against the target's exact server name and CA. A wrong CA refuses the
   fence before changing the runtime role; a valid verify-full connection
   fences and reads back an existing TLS runtime session.

   A separate read-only `PreflightPostgresRuntimeRoleFenceForCutover` checks
   the catalog-bound target and delegated principal over the same TLS path.
   It reports the server version, runtime login state and exact-role session
   count without changing either role or terminating sessions. Disposable
   PostgreSQL 16 tests cover active and already-fenced states, stale target,
   underprivileged principal and wrong CA. Provider support for the later
   fence effect remains unqualified.

   The private `norn-postgres-fence-preflight` command now makes this check
   runnable against a reviewed provider target. It requires an owner-only
   catalog JSON snapshot, its exact raw-byte SHA-256, an owner-only expected
   `TargetIdentity` JSON document, the profile and logical database ID, and
   the owner-only secret directory. For example, from `v2/api`:

   ```sh
   go run ./cmd/norn-postgres-fence-preflight \
     --catalog-file /absolute/private/catalog.json \
     --catalog-sha256 <reviewed-64-character-sha256> \
     --expected-target-file /absolute/private/target.json \
     --profile <profile-id> --logical-resource <logical-id> \
     --secrets-dir /absolute/private/database-secrets
   ```

   The JSON result reports the catalog digest, exact target, fence generation,
   declared catalog engine version, observed server version, login state and
   exact-role sessions. It refuses a different server major, changed
   snapshots, stale targets, ambiguous JSON, symlinks and non-private input
   files. It does not establish that the supplied snapshot is still the
   **active** control catalog; compare its revision and target to a fresh
   control-store readback before treating the result as provider evidence.
   It does not alter roles, terminate sessions, or qualify the later provider
   write effect. No selected-provider run has been performed yet.

   Provider qualification is still required for the first source cluster.
   [DigitalOcean's managed PostgreSQL documentation](https://docs.digitalocean.com/products/databases/postgresql/how-to/modify-user-privileges/)
   states that managed clusters do not grant superuser access and documents
   delegated `pg_read_all_stats`. It does not establish that the exact
   `CREATEROLE`, runtime-role admin option and `pg_signal_backend` shape used
   by this fixture is available on the selected cluster. Run the read-only
   principal check and a disposable writer-fence rehearsal against that
   provider/version before accepting it as a supported cutover source.
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
