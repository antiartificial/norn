# M1 control-boundary audit

Audit point: `integration/v3-m1-control-audit` at `07ea7d4` on 2026-09-23.

This is a source inventory, not milestone qualification. It identifies control
consumers that still require PostgreSQL's concrete `*store.DB` and mutation
paths that launch an external effect without first crossing the canonical
signed `store.OperationStore` acceptance and generation-fenced
`store.ExecutionStore` boundary.

## Boundary already established

- `store.OperationStore` is the only signed work-acceptance boundary.
- `store.ExecutionStore` requires an immutable operation claim containing the
  owner and generation before a worker may defer, retry, finish, or launch an
  effect.
- `store.AuthStore` keeps credential/device revocation and exec-session
  cancellation in one backend transaction.
- The deploy pipeline accepts work through `OperationStore`, and its state
  carries an `OperationClaim`. Build/test effects can use the fenced effect
  supervisor. These are the reference shape for conversions below.

## P0: direct destructive or externally visible effects

These HTTP handlers perform the effect inline. They do not create signed work,
do not acquire a generation-fenced claim, and cannot distinguish a retry after
an ambiguous response from a new request.

| Producer | Direct effect | Required conversion |
| --- | --- | --- |
| `handler/apps.go:198-239` | Restart or scale a Nomad job | Accept `app.restart` / `app.scale`, reserve the exact Nomad effect under a claim, execute in a worker, and return the durable operation. |
| `handler/canary.go:31-57` | Promote a Nomad deployment | Accept a promotion bound to app, region, and current Nomad deployment identity; fence the promotion effect and persist its result. |
| `handler/cron.go` | Trigger or resubmit periodic jobs and then separately update cron state | Pause now has a signed, claim-fenced effect path. Trigger, resume, and schedule update remain inline. Make each remaining desired state and operation one atomic aggregate; execute from a claim and reconcile ambiguous Nomad responses. [A live force check](m1-cron-force-idempotency-qualification-2026-09-24.md) shows that reusing Nomad's generic idempotency token does not deduplicate a forced run. |
| `handler/forge.go:35-139` | Rewrite cloudflared configuration and restart the service | Queue a host mutation whose effect records the prior config digest, intended config digest, restart result, and rollback boundary. |
| `handler/snapshots.go:300-319` | Run `pg_restore --clean` in the API process | Route compatibility restore through the existing durable recovery operation, snapshot identity checks, pre-restore safety snapshot, and fenced database effect. |
| `handler/ops_contextdb.go:287-369` | POST a feedback rollback to ContextDB | Accept an operation keyed by namespace/event/mode and use a durable HTTP effect with a remote idempotency identity and stored receipt. |
| `handler/function.go` | Copy a database variable, submit a Nomad batch job, and delete the variable asynchronously | Queue one operation; bind variable ownership and job identity to its claim; recover completion and cleanup after API restart. The [private request-material handoff](m1-function-invocation-handoff.md) defines the encrypted body and key-recovery gate. |
| `handler/wake_gateway.go:196-225` | Scale a service to one from a request helper | Model wake as an idempotent capacity intent or a fenced effect; concurrent replicas currently have only process-local locking. |

The Fleet GitHub bridge is closer to the target: it serializes by plan and
records a signed terminal receipt. Its external action still precedes the
signed receipt, so its completion gate is deterministic remote recovery plus a
durable effect reservation created before dispatch. The terminal receipt also
needs an archive subject instead of relying on an empty saga ID.

## P1: concrete PostgreSQL consumer roots

The following production objects retain `*store.DB` and therefore prevent a
backend-selected control plane even where their individual methods are reads:

| Consumer root | Current concrete responsibilities | Interface boundary to introduce |
| --- | --- | --- |
| `handler.Handler` | Auth, operations, deployments, Fleet attempts, cron, events, notifications, recovery, readiness, metrics, and database catalogs | Inject small aggregate interfaces. Keep SQL error translation inside the PostgreSQL adapter; handlers must not inspect `db.Pool` or `pgx` errors. |
| `pipeline.Pipeline` | Deployment state, checkpoints, database catalogs, operation lookup, and region transitions | Split accepted deployment catalog, checkpoint store, deployment projection, and database-catalog activation from execution. |
| `beacon.Service` / `beacon.Notifier` | Incident/event history, dedupe, auto-ack, and notification channels | Define one Beacon aggregate plus notification read model; preserve dedupe and correlated auto-ack atomicity. |
| `retention.Archiver` / `retention.HistoryStore` | Evidence outbox, archive acknowledgement, pruning, holds, and read-through index | Define an evidence source/outbox interface whose claim and acknowledgement semantics stay transactional. |
| API runtime helpers | Evidence policy, database target catalog, build-effect configuration | Consume the same narrow stores selected before any backend connection. |

Startup also wires PostgreSQL directly into recovery, hub, Beacon, saga,
pipeline, handler, evidence retention, and database catalog construction in
`v2/api/main.go`. Moving only workers or `OperationStore` to etcd would create a
split authority and is not an acceptable intermediate runtime mode.

## Conversion order

1. Queue restart, scale, canary promotion, and cron mutations through signed
   acceptance. These are small Nomad operations and exercise the complete
   accept/claim/effect/recovery contract without entangling deploy assembly.
2. Extract handler auth to `AuthStore`, operation reads to an operation
   projection interface, and exec sessions to the existing auth aggregate.
   Remove handler checks of `db.Pool` and translate not-found/conflict errors at
   the store boundary.
3. Extract pipeline checkpoint, deployment, and database-catalog interfaces.
   Require an effect reservation before every Nomad, migration, build/push,
   cloudflared, and restore mutation.
4. Extract Beacon and event-stream stores, then evidence outbox/read-through.
   Preserve multi-record invariants as aggregate methods rather than composing
   several narrow calls in handlers.
5. Construct a single backend bundle before connecting to PostgreSQL or etcd
   and inject every consumer from it. Reject configurations where aggregates
   resolve to different authorities.

## Required M1 qualification

- Run the shared acceptance, auth, claim, checkpoint, and effect suites against
  each backend with two API/worker processes.
- Kill a worker before launch, after launch, and after remote success but before
  acknowledgement; prove each effect is either safely resumed, reconciled, or
  stopped for operator review.
- Race duplicate HTTP requests, token/device revocation against exec connect,
  claim expiry against finish/retry, and two app mutations against the same
  app lock.
- Load pre-v3 operations and sessions and prove their compatibility behavior is
  explicit and bounded.
- Demonstrate that every mutating route either returns a signed durable
  operation or is documented as a deliberately synchronous, non-durable
  compatibility surface with no external effect.

Until these checks pass, M1 has useful canonical interfaces and PostgreSQL
behavior but is not complete.

## Current delta — 2026-09-25

The table above is the `07ea7d4` audit inventory, not the current route
map. Restart, scale, canary promotion, cron pause/resume/trigger/schedule,
and wake now have signed claimed paths. Cron trigger's ambiguous Force result
stops behind a one-shot effect reservation and has a positive-evaluation
operator correction; an unknowable result stays held. The normal and
lost-response schedule updates passed disposable Nomad/PostgreSQL tests.

The `/invoke` route no longer calls legacy `Handler.InvokeFunction`. It
selects the signed private-invocation admission/claimed worker only when the
complete PostgreSQL runtime is configured, otherwise returns an explicit
503. Historical function executions remain readable. The claimed function
path passed literal worker-kill/two-successor recovery against disposable
Nomad 2.0.7 and PostgreSQL 17.7 on 2026-09-25. Archive-cleanup and private
Mini migration-23 rollback qualification remain before it is a release-ready
capability. Etcd function parity is absent.

`Forge`/teardown/toggle endpoint now accept signed `app.cloudflared-mutate`
operations and execute through a host-bound, claim-fenced effect. The local
receipt permits exact recovery after a confirmed restart; a crash between
config write or restart and the receipt remains held for operator review.
The [host-effect record](m1-cloudflared-host-effects-2026-09-25.md) names the
remaining real-host and two-process gates. The ContextDB feedback rollback route now returns 501
without contacting its remote service. The P1 PostgreSQL aggregate roots
remain a barrier to a full etcd Fleet API. M1 is still open, including literal
process crashes and two-replica races for the newly converted effects.

The legacy `RestoreSnapshot` handler now queues `app.snapshot-restore` through
the same signed durable operation as the versioned restore route; the original
`pg_restore`-inline row above is historical. Snapshot import now accepts a
signed `app.snapshot-import` operation for both target-bound and legacy
snapshots; the worker owns the download and publication. The target-bound
path retains manifest, target, and digest checks. A lost worker claim is a
one-attempt failure requiring operator inspection of any published dump or
sidecar before resubmission. This still needs the two-process crash gate.
Both target-aware and legacy flat snapshot export HTTP routes now pin a
filename and queue a signed `app.snapshot-export` operation. The worker uses
an operation-specific remote key, create-only writes, and remote readback of
the dump before publishing its manifest. Legacy imports of these new keys
verify the manifest and dump digest. A lost claim remains a one-attempt
failure for inspection. Automatic predeploy export now uses the deployment's
signed claim and the same create-only, read-back-verified publication path;
an unverified configured export fails the snapshot step before migration or
job submission. The snapshot creation itself and export are still effects
inside the deploy step without a separate effect reservation. A crash during
that step can leave a partial remote key or a local snapshot. Named-target
predeploy snapshots now use the accepted operation ID and start time for one
replay name, reuse only a dump with matching target provenance, and pin the
claimed export manifest timestamp to the same operation. Local PostgreSQL
tests verified that a second execution reuses the snapshot and remote pair.
The pinned path now refuses a conflicting target dump, orphan sidecar, or
missing operation start time instead of advancing to another filename.
Legacy claimed exports now pin their manifest timestamp to the accepted
operation, so an exact remote replay verifies the same bytes. Legacy
predeploy snapshots also pin one operation name but refuse to reuse an
existing unbound dump; that case requires operator inspection. A process crash
between the external effect and durable step completion motivated the durable
intent and recovery work below. Both claimed export routes
recheck the live operation lease immediately before remote publication, after
any predeploy dump work; this closes the known expired-before-upload case but
does not fence expiry during an upload. The create-only S3
behavior has passed the local emulator, including multipart completion, but
hosted-provider qualification remains open. M1 export is not yet qualified.
The predeploy path now checks the live operation claim again after the remote
dump and manifest have been verified, before deploy may advance to migration
or job submission. The claimed predeploy object adapter also checks the live
claim immediately before each remote write. An isolated PostgreSQL test
expires the claim after either remote write. After the dump write, the step
refuses to publish the completion manifest; after the manifest write, its
final claim check refuses to advance. Both cases leave remote objects for
inspection. This narrows stale publication but does not erase remote objects.
The pure create-only publisher now has a real subprocess crash test: the child
exits immediately after writing the dump, leaving no completion manifest; a
new call rejects changed remote bytes without publishing a manifest, then
verifies the pinned dump and publishes the matching manifest. This proves
the remote pair's replay behavior only; it does not test claim recovery in two
API processes or qualify hosted object storage.
Migration 39 now stores an immutable per-operation, per-object export intent
with bucket, key, dump digest and size, and manifest digest. The manual and
predeploy claimed export paths commit it under the live PostgreSQL claim
before the first remote write, then mark it published only after both remote
objects are read back. An integration fixture checks the `prepared` row at
the write boundary; a store test proves exact adoption after claim turnover
and rejects changed content. Expired standalone `app.snapshot-export` operations
with a prepared or published intent now receive a bounded successor claim;
unreserved exports retain the one-attempt inspection outcome. A literal child
process reserves the intent, writes only the dump and exits; the successor
claims the original operation, verifies the existing dump, publishes the
manifest, records the receipt and finishes the original operation. This proves
the PostgreSQL recovery transition with a local file-backed object store.
The complete API worker/deploy-step process crash path and provider-backed
create-only behavior still needed qualification at this checkpoint.
The standalone legacy `app.snapshot-export` pipeline route also passed a
control-PostgreSQL integration test: its first claimed execution left only
the remote dump and a prepared intent; recovery requeued the operation, and
`Pipeline.ExecuteOperation` on a successor claim verified that dump, added the
manifest, recorded the receipt and terminalized the same operation. The
full `OperationWorker` process and deployment snapshot step were separate
qualification work at this checkpoint. A literal worker subprocess test now
exits immediately after its create-only dump write. A second worker runs
normal expiry recovery, claims the same signed `app.snapshot-export` operation
as attempt 2, verifies the existing remote dump, publishes the manifest, and
records both a published intent and a succeeded operation. This uses a local
file-backed object store and disposable PostgreSQL. Deployment snapshot-step
recovery and hosted provider behavior remain open.
An expired deploy may now be requeued within its existing attempt budget only
when its snapshot step is still running, an immutable export intent exists,
source and content-addressed build checkpoints are present, and no other
mutable deployment step has started. Prebuilt and bound images now record the
same build checkpoint as a built image. A store test keeps deploys that reached
migration, lack an export intent or checkpoints, or name a mutable image in
manual review. A disposable two-target PostgreSQL test exercises a successor
deploy claim through `snapshotTarget`: it reuses the operation-pinned local
snapshot and verifies the remote dump before publishing the manifest and
receipt. A second test enters full `Pipeline.run`, simulates a process stop
after the dump write, and replays clone, admission, build, test and snapshot
under a successor claim. The source checkpoint remains unchanged and the
snapshot step completes; the test deliberately stops before migration.
Replay through later deployment steps and a literal deploy worker process
crash remain unqualified.
