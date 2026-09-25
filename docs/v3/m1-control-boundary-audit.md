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
