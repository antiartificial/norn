# Norn v3 foundation implementation review

Status: staged engineering handoff, 2026-09-22. Reviewed against Norn
`7304f398c338dc5134f60c63dcec251772891cbe` and the proposed v3 planning
package. This document does not mark M0 or M1 complete, authorize a deployment,
or establish PostgreSQL/etcd parity or HA readiness.

## Recommended staging

Start with the smallest observable safety defect: make native exec sessions
owner-aware so starting a second, worker-disabled API does not terminate a
healthy session owned by the first API. Land operation claim fencing and
deployment reconciliation as a separate review unit. Extract only the
PostgreSQL adapter seam needed by those execution paths; a repository-wide
store abstraction and the etcd adapter remain later M1/M3 work.

The first two patches improve replica safety. They do not make a process-bound
PTY transferable, fence every Nomad/provider effect, or qualify active-active
API serving.

## Baseline findings

### 1. Schema startup currently terminates every running exec session

[`store.Migrate`](../../v2/api/store/postgres.go) includes an unconditional
`UPDATE exec_sessions ... WHERE status='running'`. [`main`](../../v2/api/main.go)
runs that migration before it considers the deployment-recovery,
operation-recovery, or worker skip flags. Consequently a platform candidate
started with all workers and recovery disabled can still fail a healthy exec
session owned by the serving API.

The current stream handler records no durable API owner. It changes a session
from `pending` to `running`, retains the WebSocket only in the local
[`Handler.execConns`](../../v2/api/handler/handler.go), polls the row for
revocation, and finishes by session ID alone in
[`exec_session.go`](../../v2/api/handler/exec_session.go). That is enough for
cross-replica cancellation, but not enough to distinguish a live owner from a
dead one or reject a stale owner's completion.

### 2. Deployment startup recovery is also replica-wide

[`RecoverInFlightDeployments`](../../v2/api/store/postgres.go) fails every
deployment whose status is not `deployed` or `failed`. It does not join the
deployment to its durable operation or check that operation's owner lease.
[`main`](../../v2/api/main.go) invokes it at ordinary startup before operation
recovery. A newly started replica can therefore fail a deployment still being
executed by a healthy replica. Candidate scripts avoid this only by setting a
skip environment variable; the store behavior itself is not owner-safe.

### 3. Operation claiming is atomic, but later writes are not claim-fenced

[`ClaimNextOperation`](../../v2/api/store/operations.go) correctly uses
`FOR UPDATE SKIP LOCKED`, records `locked_by`, and assigns a lease. The rest of
the lifecycle is weaker:

- lease renewal checks `locked_by` but treats a zero-row update as success;
- retry and terminal completion update by operation ID without checking owner;
- defer checks only `status='running'` and decrements `attempts`;
- the app and maintenance workers keep executing after renewal failure;
- the deploy, rollback, preflight and data-operation pipelines finish the
  operation directly, so fencing only the worker's error path would leave
  bypasses;
- `FinishOperationBySaga` is currently unused but could mutate running claimed
  work without claim identity.

A stale executor can therefore overwrite a cancellation or a newer owner's
result. `attempts` cannot safely double as a fencing generation because defer
currently decrements it and retry budget is a different concern.

### 4. The advisory app lock is useful serialization, not an effect fence

[`AcquireAppOperationLock`](../../v2/api/store/operations.go) pins a PostgreSQL
connection and serializes mutable work for one app. It prevents a healthy
second worker from entering the same app pipeline while the connection and
lock remain live. It does not stop a paused or partitioned process after its
database connection is lost, and Nomad, provider and shell commands do not
reject a Norn ownership generation. A lease or advisory-lock loss therefore
cannot retract an already issued external call.

Automatic takeover must remain limited to the existing proven-safe pre-mutation
cases. Mutable or outcome-unknown stages must stop for reconciliation until an
effect-specific fence, downstream idempotency rule, or proven-stop procedure
exists.

### 5. Migration authority is not yet versioned or single-owner

[`store.Migrate`](../../v2/api/store/postgres.go) is one large idempotent SQL
batch run by every API process. It has no schema-version ledger, minimum
readable/writable version guard, dedicated migration role, or explicit pinned
advisory lock. Worker-disabled candidates still run it. Removing the exec DML
is necessary, but it does not satisfy the M1 one-migration-owner contract.

For the first additive columns, PostgreSQL serialization and `IF NOT EXISTS`
keep the change bounded, but `ALTER TABLE` and ordinary index creation still
take locks. Measure the migration on the representative Mini fixture and keep
contraction out of the rollback window.

## Tranche 1: owner-aware exec sessions

This tranche is independently shippable as a v2 prerequisite safety patch.

### Store contract

1. Remove the startup data mutation that fails every running session.
2. Add internal ownership fields to `exec_sessions`: an API instance identity,
   an unguessable per-claim owner token, and an owner lease deadline. Keep the
   fields additive and out of the public v1 response for now.
3. Generate a unique API runtime identity using host, PID and random material;
   host and PID alone are reusable.
4. Connect with one atomic `pending` to `running` compare-and-set that records
   ownership, clears the stored command and returns the owner token.
5. Renew and normally finish only when session ID, `running` status and owner
   token all match. A zero-row mutation is ownership loss, not success.
6. Keep cancellation, device revocation and token revocation authoritative
   across replicas. They may terminalize by session identity. A later stale
   owner completion must affect zero rows and preserve that terminal reason.
7. Recover only running sessions that have a non-empty owner token and an
   expired owner lease. Do not fail legacy running rows merely because their
   new owner columns are empty. Extend hard TTL expiry to both pending and
   running rows so legacy rows eventually close conservatively.

### Handler behavior

Renew ownership in the existing revocation watcher. Close the local WebSocket
on ownership loss or renewal-store error; continuing an interactive shell after
the control store can no longer prove ownership is unsafe. Normal completion,
transport failure and expiry all use the owner token.

This closes a process-local stream on owner failure; it does not move the PTY
to another API. A client may reconnect to control state, but the command stream
itself is not resumable.

### Acceptance

- Running sessions survive a second `Migrate` and a second API's startup
  recovery while the owner lease is live.
- A wrong owner token cannot renew or finish; the correct token can.
- Revocation/cancellation racing with normal completion retains the security or
  user terminal state.
- Expired owned sessions fail with a stable owner-lost reason.
- Legacy ownerless running rows survive candidate startup and expire only at
  their hard session deadline.
- Ownership loss and store-renewal failure close the WebSocket execution path.
- Tests exercise two independent store connections against disposable
  PostgreSQL; unit tests alone cannot validate the compare-and-set boundary.

### Compatibility and rollback boundary

A new binary becomes safe when it starts beside another new binary. An old
binary still contains the unconditional startup update. Restarting or rolling
back to that old binary can terminate all running exec sessions even after the
additive owner columns exist. Release evidence must therefore choose and state
one of these boundaries:

- ship the exec fix as a prerequisite v2 patch and support rollback only to
  that patched baseline; or
- declare a bounded exec-session interruption when rollback starts an older
  binary.

Do not describe the additive schema alone as mixed-version exec safety.

## Tranche 2: fenced operation claims and owner-aware recovery

Implement this after tranche 1 acceptance because it touches overlapping
worker/pipeline ownership APIs.

### Durable claim identity

Add `operations.lock_generation BIGINT NOT NULL DEFAULT 0`. Increment it on
every successful claim and return an immutable claim containing operation ID,
owner ID and generation. Keep execution attempt count separate.

Every renewal, defer, retry and terminal completion of claimed work must match:

```text
id + status=running + locked_by + lock_generation
```

A zero-row mutation returns a typed ownership-lost result. Cancellation or a
new generation must therefore win over a stale executor.

### PostgreSQL adapter seam

Introduce a narrow worker-facing `ExecutionStore`, implemented first by
PostgreSQL, for:

- expired-operation recovery;
- claim;
- claim renewal;
- claim-aware defer/retry/finish; and
- per-app execution lock acquisition.

Keep general handlers and unrelated stores on the existing `*store.DB` during
this patch. This seam exists to run one invariant suite against execution
ownership, not to pretend the current SQL inventory is already abstracted for
etcd.

### Remove completion bypasses

Thread the immutable claim through
[`Pipeline.ExecuteOperation`](../../v2/api/pipeline/pipeline.go), data
operations, deploy, rollback and preflight. Replace every pipeline and worker
`FinishOperation` call with claim-aware completion. Remove the unused
`FinishOperationBySaga`, or restrict it so it cannot modify running claimed
work. A search/compile gate should leave no operation-ID-only terminalization
path for claimed app or maintenance work.

On any renewal error, cancel the execution context and wait for the executor to
return before releasing the PostgreSQL advisory lock. Retry or failure
recording after cancellation is still claim-aware and may legitimately return
ownership lost.

Deployment and deployment-step writes are not a second ownership system. In
this tranche they remain subordinate evidence emitted through the claimed
execution context. They must not be used to authorize takeover. Later
effect-level fencing may attach the generation to individual step/effect
records.

### Owner-aware operation and deployment recovery

Recovery may mutate only a running operation whose lease is expired (or a
legacy row with no lease under an explicit compatibility rule). Preserve the
current distinction between pre-mutation safe retry and mutable-stage manual
failure.

Remove replica-wide deployment failure from API startup. After operation
recovery, reconcile a deployment through its owning operation ID:

- preserve it when its operation has a live lease;
- preserve it when its operation was safely requeued;
- fail it and its incomplete regions when its owning operation became failed
  or canceled;
- handle a legacy orphan through an explicit, tested rule rather than treating
  every in-flight row as abandoned.

A worker/recovery-disabled candidate performs no operation or deployment
mutation. This remains candidate isolation, not a continuous-service claim.

### Acceptance

Use at least two PostgreSQL connections/stores:

1. Worker A claims an operation. Worker B's recovery leaves the operation and
   deployment unchanged while A's lease is live.
2. After lease expiry, a pre-mutation operation is safely requeued while a
   mutable-stage operation becomes manual-review failure. Only the latter's
   deployment is failed.
3. B claims the requeued operation with generation `n+1`. Every A renew,
   defer, retry and finish attempt returns ownership lost and cannot alter B's
   row or metadata.
4. Cancellation wins a race with stale success/failure completion.
5. A renewal database error cancels the executor context and cannot later be
   reported as success.
6. Concurrent workers never claim the same generation.
7. Maintenance operations use the same claim-aware terminalization while
   preserving their fail-for-review takeover policy.
8. No claimed pipeline path can call ID-only `FinishOperation` or
   `FinishOperationBySaga`.

### Explicit limitation

This tranche fences PostgreSQL control rows. It does not prove exactly-once
Nomad, provider, registry, filesystem, database-migration or shell effects.
Context cancellation is a stop request, not proof that a downstream effect did
not commit. Until those systems consume a generation or offer equivalent
idempotency, a stale/ambiguous mutable effect remains a reconciliation case.

## Follow-on gates not satisfied here

- Versioned expand/contract migrations with one migration owner, schema
  compatibility advertisement and incompatible-candidate rejection.
- Atomic operation acceptance with idempotency reservation and durable audit
  intent across every mutation entry point.
- A complete PG domain adapter and shared backend invariant suite.
- Etcd leases/CAS, authority epochs, bounded indexes, backup/restore and
  PG-independent bootstrap.
- Downstream effect fencing or proven-stop rules for each mutable stage.
- Active-active API qualification or continuous control API availability.
- M0 baseline inventory, accepted ADR dispositions, budgets and named release
  owners.

The Nomad translator already applies an InfraSpec's effective logical node pool
to service and periodic jobs. The remaining v3 scheduling gap is end-to-end
role/pool enrollment, arbitrary-role bootstrap, durable desired replica state,
capacity validation and safe drain/replacement—not total absence of
`node_pool` translation.

## Remaining effect-recovery slice: source audit

The current retry SQL in `store/operations.go` still allows `build` and `test`
as pre-mutation stages. This is not justified by their implementations:
`pipeline/test.go` runs arbitrary host shell commands and `pipeline/build.go`
can invoke `docker buildx --push`. Killing a CLI process does not prove its
descendants, BuildKit work, database transaction or provider action stopped.

Required next unit: durable effect intent keyed by operation/generation/stage/
target, immutable input digest and downstream identity; persist issuance before
the external call. Unresolved effects must gate subsequent work for that
resource, including new operations after the original is marked failed.
Unrelated resources must remain usable. Add supervised local process groups and
exit/reap evidence, but do not treat local process exit as daemon/database stop
proof. Explicit verified reconciliation must resolve the gate and permit safe
retry; permanent rejection is not the recovery implementation. Remove build/test
from unconditional crash retry until their effect-specific rules are qualified.

Tests must pause the old executor across lease loss, prevent overlapping work,
reap shell descendants, retain ambiguity after lost acknowledgments, distinguish
CLI exit from remote completion, and release the gate exactly once after valid
reconciliation. Nomad conditional submission/storage provisioning and a durable
auto-rollback outbox tied to parent completion are separate required follow-ons.
The current post-terminal publication callback does not supply that outbox.
