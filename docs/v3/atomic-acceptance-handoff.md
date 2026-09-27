# Atomic operation acceptance handoff

Status: proposed, pending implementation and verification, 2026-09-22. This is
the next bounded M1 implementation slice after operation ownership and startup
integration freeze. It does not complete M1, approve a migration, or authorize
live deployment. The complete M0–M9 goal remains in scope.

## Problem and observed entry points

The required boundary is `OperationStore.Accept` in
[planning contracts](planning-contracts.md): commit operation, idempotency
identity and audit intent together, and resolve an uncertain commit by identity.
Current execution fencing does not supply this acceptance boundary.

- `handler/control_protocol.go`, `queueMaintenanceOperation`: raw global
  idempotency keys can return an existing operation without comparing actor,
  kind, ref or payload.
- `handler/app_recovery.go` and `handler/releases.go`: request fingerprints
  exist, but lookup, insert and race reconciliation are distributed across
  handlers. `appOperationIdempotency` prefers token ID, so token rotation changes
  request identity even when the actor is unchanged.
- `handler/fleet.go`, `handler/fleet_github.go`, and release qualification create
  completed planning receipts through `InsertCompletedOperation`; these also
  require atomic acceptance and replay despite being non-executable.
- `store/operations.go`: `InsertDeploymentOperation` and
  `InsertRollbackOperation` already transactionally insert deployment, regions
  and operation. Reuse that domain behavior inside the new boundary.
- `pipeline/pipeline.go`, `Run`: still inserts those rows separately, logs
  failures, and returns a saga ID. `pipeline/preflight.go` and rollback enqueue
  entry points also need the shared acceptance contract and explicit errors.
- `handler/mutation_audit.go`: middleware independently reserves a request
  receipt before dispatch; its deferred completion signs the final HTTP outcome.
  The reservation is an unsigned `started` receipt, not a signed operation
  acceptance. It currently supplies no acceptance link through request context.

## Domain contract and transaction

Introduce domain request/result types and an `OperationStore` interface with
`Accept(ctx, acceptance)` and `Resolve(ctx, requestIdentity)`. Do not expose PG
transactions, SQL or a generic key/value abstraction through this interface.
The concrete PostgreSQL adapter can remain attached to `store.DB` initially.

Acceptance includes a prepared queued or completed operation, optional deployment
and regional intent, explicit actor and source, scoped request identity,
versioned canonical request fingerprint, and signed acceptance-intent envelope.
The result distinguishes newly accepted from replay and returns the original
operation, saga, deployment and acceptance identities. A replay must not rebuild
these identities from the current request or re-run execution.

One transaction must commit:

1. A unique request-identity record and its canonical fingerprint.
2. The operation and any associated deployment and regional rows.
3. An immutable signed acceptance intent linked to those identities and, for
   HTTP requests, the reserved request audit receipt ID.

Resolve uniqueness races inside the adapter. Same scoped identity and same
fingerprint return the original result; a different fingerprint returns a typed
conflict. Neither case inserts another deployment or acceptance intent. Validate
deployment/app/saga/payload references as one domain object. Replay requests
still undergo current authentication and authorization before result disclosure.
If the endpoint promises one active mutable operation per app, enforce that
admission policy within the transaction; the existing check-then-insert handler
test is insufficient under concurrency.

Use an additive migration following the frozen baseline, not an edit to
immutable migration 1. Prefer explicit indexed request-identity and intent
records over mutable operation metadata as the authoritative replay index.
Existing metadata remains readable for compatibility. Bound key, canonical
payload and envelope sizes before accepting work; rejection must leave no rows.

## Actor identity, fingerprint and legacy replay

Use a stable authenticated actor identifier qualified by its issuer/security
domain, plus control authority scope, operation kind/resource scope and supplied
idempotency key. Token ID and device ID are evidence about an authentication
attempt, not the stable actor identity. Renewing credentials for the same actor
must preserve replay. Do not assume `AccessPrincipal.Subject` is globally unique
without its verified namespace. For legacy shared credentials, explicitly model
the configured shared service actor; do not invent distinct human identities.
Internal producers use a named system actor and durable source-event identity.

Source audit found that `AccessPrincipal.Subject` is a display label in several
paths (token note, device name, or Cloudflare email), not a unique actor key.
The initial implementation must use verified provenance as follows:

| Existing authentication path | Stable actor boundary |
| --- | --- |
| Enrolled device | Authority-qualified device issuer plus registered device ID; credential rotation preserves it, re-enrollment does not. |
| Managed non-device token | Authority-qualified token-lineage issuer plus original JTI resolved through trusted `rotated_from` records. |
| GitHub Actions | Provider namespace plus verified owner ID, repository ID, run ID and run attempt; refresh is stable, a new attempt is distinct. |
| Cloudflare Access | Configured team namespace plus validated nonempty claims subject from authentication context, not email/display subject or an unchecked issuer claim. |
| Shared API credential | Explicit authority-qualified shared service actor; no inferred human identity. |
| Unmanaged legacy JWT | Reject acceptance if stable provenance cannot be established. |

There is no existing durable human-account mapping that justifies merging
devices or token lineages by matching display labels. The Cloudflare validator
currently does not enforce the incoming issuer claim; this mapping must not
describe that claim as verified. Authentication hardening is a separate change,
not permission for acceptance code to trust additional unvalidated input.

Canonicalization must be versioned and deterministic, reject encoding errors,
and include all accepted semantic inputs: target, kind, ref, payload, relevant
policy/qualification data and execution-affecting options. Exclude request IDs,
new operation IDs, token IDs and incidental timestamps. Preserve existing full
release-qualification fingerprint coverage. The fingerprint is a digest, not a
substitute for authorization or the signed acceptance envelope.

Legacy key lookup is an explicit compatibility path. It must verify trustworthy
actor provenance, kind/resource scope and fingerprint before returning an old
result. A raw maintenance key without sufficient actor/fingerprint evidence is
ambiguous: return a generic reconciliation/conflict response without exposing
the existing operation or creating a replacement. Never replay another actor's
result merely because a global legacy key collided. Only create an immutable
alias to a legacy operation after these checks; preserve its original identifiers
and bytes. Do not infer provenance from untrusted request metadata alone. Legacy
token-scoped keys across rotation require proven credential-to-actor lineage;
where it is unavailable, report ambiguity rather than silently duplicating work.

## Signing and request audit integration

Keep existing request-receipt canonicalization and all historical digest/signature
bytes unchanged. Add a distinct versioned acceptance-envelope canonicalization.
Sign it before the acceptance transaction commits. In production, missing key,
signing failure or intent persistence failure must prevent operation acceptance.
An unsigned intent is not an acceptable replacement for the existing signed
receipt or for this new acceptance evidence.

The immutable envelope binds at least authority scope, stable actor identity,
credential evidence where applicable, request fingerprint/version, operation ID,
kind, resource, saga/deployment identities, acceptance timestamp, request audit
receipt ID, signing schema and key ID. Normalize timestamps to stored precision
before signing and retain exact canonical bytes plus digest/signature. Use the
existing production audit signing policy and retained verification keys through
a dedicated signer boundary; do not put signing secrets in domain records.

Middleware should pass the reserved receipt identity and verified actor evidence
through request context. At acceptance time that receipt is ordinarily still
unsigned and `started`; link its ID without requiring or claiming a final
receipt digest. Deferred middleware completion subsequently signs its HTTP
outcome using the unchanged existing scheme. A crash after acceptance can thus
leave a signed acceptance and an unfinished request receipt, both truthful and
linked. Signed acceptance proves acceptance, not execution success. Replays may
have separate request-attempt receipts but retain the original acceptance intent.
Internal producers need their own signed acceptance evidence even without an
HTTP receipt; represent that absence explicitly, not with a fabricated receipt.

## Uncertain commit and compatibility boundary

Treat a lost commit acknowledgment as unknown, not as proof of rollback. Resolve
by the same scoped identity through a fresh bounded context/connection after the
request context has failed. Verify the fingerprint and signed result identity
before returning success. If resolution remains unavailable, return a typed
indeterminate outcome instructing retry with the same identity; never generate a
replacement identity. Retrying acceptance must serialize against an in-flight
original transaction and converge through the unique identity constraint.

Before requiring this boundary, raise the minimum writer contract for the new
schema so contract-aware older binaries cannot accept unfenced legacy writes.
Reader compatibility can remain where the additive schema permits. Startup-only
checks do not stop an already running old writer: upgrade activation must drain
and stop legacy writers or enforce an equivalent database write barrier before
declaring the invariant active. Pre-contract binaries ignore metadata entirely;
their exclusion must be proven by the release transition. Workers must not claim
new-format operations missing acceptance evidence; legacy queued work needs an
explicit provenance/reconciliation rule, not fabricated signed acceptance.
An additive migration alone does not provide mixed-writer safety.

## Retention and export

Keep the existing 365-day audit policy during transition. Preserve unresolved
operations, ambiguous commits, acceptance intents, replay identities and their
linked receipts irrespective of ordinary terminal-history age. Update scoped
pruning rules so request-audit cleanup cannot remove evidence still referenced
by an unresolved acceptance. Do not use cascading deletion to erase that chain.

Do not expire replay identities in this slice: deleting one would permit the
same request key to execute again. A later bounded replay horizon must be an
explicit API contract with tombstone/expired-identity behavior. Signed bytes,
key IDs, actor scope, links and replay identities belong in canonical backup and
export. Archive-based pruning awaits M2 verified archive acknowledgement; this
slice must not claim that archive workflow is implemented.

## Implementation ownership

| Review unit | Owned implementation files after freeze |
| --- | --- |
| Domain/PG acceptance | New domain types and PG acceptance files; transaction helper extraction in `store/operations.go`; additive migration registration; store invariant tests. |
| HTTP integration | `handler/control_protocol.go`, `app_recovery.go`, `releases.go`, `fleet.go`, `fleet_github.go`; audit context/signer integration and handler tests. |
| Pipeline acceptance | Enqueue portions of `pipeline/pipeline.go`, `preflight.go`, `rollback.go`; callers of legacy enqueue APIs and error propagation tests. |

Agree on types first. Do not edit overlapping operation/pipeline files while the
current ownership implementation is still changing. Enumerate all enqueue
callers, including webhook/system producers, before claiming bypass elimination.
Signed acceptance is the required deliverable; archive transport is separate.

### Verified legacy enqueue integration inventory

The following callers must change with the pipeline enqueue return contract;
fixing the versioned mutation handlers alone leaves false-success paths:

| Caller | Current behavior requiring replacement |
| --- | --- |
| `handler/deploy.go` | `Run` and `Preflight` return a saga ID without a persistence error; HTTP must acknowledge only durable acceptance. |
| `handler/deploy_group.go` | Each group member is recorded as queued after `Run`; preserve per-member errors and accepted identities without claiming whole-group atomicity. |
| `pipeline/deploy_group.go` | `RunGroup` has the same per-member false-success path; a retry must not duplicate already accepted members. |
| `handler/webhook.go` | Initial delivery becomes `deploying` after `Run`; replay becomes `replayed` after `Run`/`Preflight`. Bind acceptance to durable provider delivery/replay identity, and do not mark success on failed or indeterminate acceptance. |
| `pipeline/pipeline.go` automatic rollback | Internal rollback enqueue needs an explicit system actor, stable source identity and signed intent, with failure recorded rather than silently treated as accepted recovery. |

Webhook bookkeeping and acceptance are separate writes today. Integration must
resolve a committed acceptance after interrupted delivery-status updates, not
enqueue a replacement merely because the delivery record still looks pending.
An explicit operator replay is a new authorized action with its own durable
identity; retrying that replay request must resolve the same action. Group
members similarly need stable child identities derived from the accepted group
request and member, not freshly generated keys on every retry.

### Next producer conversion boundaries

Source review on 2026-09-22 identified these distinct transaction boundaries;
converting a final operation insert alone is not sufficient:

- App data operations can use `Accept` with atomic active-app admission. A
  handler's earlier active-operation lookup is advisory, not the admission gate.
- Rollback and release deployment must replace the whole existing deployment /
  regions / operation transaction. Preserve promotion-qualification uniqueness
  across different actors and request keys; request idempotency is not exclusive
  qualification consumption.
- Completed Fleet planning/reconciliation and release-qualification receipts
  contain generated IDs, timestamps and signatures. Resolve stable request
  identity before regenerating results, and bind immutable result evidence
  separately from stable request semantics. Rebuilding a receipt must not turn a
  valid retry into a fingerprint conflict.
- GitHub dispatch requires signed acceptance and its durable dispatch binding
  before the external call, in the same domain transaction. Retain nonce-based
  recovery after ambiguous dispatch; a post-dispatch completed receipt cannot
  provide pre-effect acceptance. Plan/kind lookup is not actor/request replay.
- Fleet runner-attempt acceptance must share the existing plan lock, attempt
  numbering, expired-attempt reconciliation and server-owned root-lineage
  transaction. Independently committing acceptance before or after it leaves an
  atomicity gap.

Legacy aliases require endpoint-specific evidence. App/release hashed keys need
verified credential lineage and the original request digest. Fleet metadata
without an attributable actor, or a GitHub `principal` that ambiguously means
token ID versus display subject, must fail non-disclosingly rather than acquire
an alias on a guessed identity.

The CLI's `Deploy`, `Preflight`, `RunDeployGroup` and webhook replay transports
also need caller-controlled retry keys when their corresponding handlers become
mandatory-key endpoints. Generate a key once per command invocation, expose it
before transport, and allow explicit reuse after an ambiguous response. Test
actual outgoing headers; server-only tests do not prove client compatibility.
Replay responses must report the accepted operation's actual lifecycle state,
not unconditionally label a previously completed operation as queued.

The same audit found missing headers in web `runtime/AppRuntime.tsx`
(`runAppAction` deploy/preflight) and `components/DeployGroupsSection.tsx`.
Their action-owned retry identity must survive ambiguous transport failure;
do not create a fresh key inside each fetch attempt. Group responses need
per-member result inspection before reporting success. Existing release and
app-recovery web flows already send keys, which does not establish these other
paths. Native NornUI release methods already accept explicit keys; its complete
v3 integration remains a separate qualification gate.

### Fleet transaction sequence from source review

Implement these as separate review units, not a blanket replacement of
`InsertCompletedOperation`:

1. **Capacity plan:** ordinary signed acceptance is sufficient. Stable input is
   normalized `PlanRequest` plus pool; inventory-derived plan IDs, digests and
   signatures are immutable output. Resolve after authorization but before
   loading current inventory. Test replay after inventory changes/removal,
   changed-input conflict, concurrent convergence and failed intent rollback.
2. **Reconciliation:** add a typed domain extension inside the acceptance
   transaction. Acquire `norn:fleet-attempt:<planID>`, read the plan, bound attempt
   and complete relevant reconciliation aggregate, validate new admission, then
   insert operation and intent. The handler's current `Limit: 100` list is not
   authoritative transition state. Existing attempt read helpers mutate expired
   leases through the pool; use transaction-local queries instead. Replay must
   precede dynamic lease/phase admission while retaining current authorization
   and CI ownership checks. Test expiry/advance replay, incompatible first
   bindings, cancellation races and rollback of all acceptance rows.
3. **Runner creation/recovery:** move predecessor cancellation into the same
   transaction as successor creation and acceptance. Preserve plan locking,
   runner uniqueness, one-live-attempt constraint, attempt numbering, root
   lineage and revision CAS. Revalidate dispatch binding/resume phase under the
   lock. Fingerprint heartbeat timeout and resume options, not just commit,
   digest and workflow URL. Failed insertion/signing must leave the predecessor
   active; concurrent recoveries must produce one authorized successor.
   Lock the dispatch row as well: `FinishFleetGitHubDispatch` currently allows
   rewriting its completed run ID/URL, so change completion to first-set or
   identical replay and test a binding change between precheck and transaction.
   Sample database wall time after acquiring locks. A canceled/expired attempt
   record and nonce continuity do not prove the previous external runner or
   provider operation stopped; preserve explicit proven-stop/outcome recovery
   requirements rather than claiming DB atomicity provides external fencing.
4. **GitHub PR/dispatch:** accept queued work before any provider call. PR
   creation currently happens before its completed receipt; replace this with
   recoverable execution using deterministic provider correlation. Dispatch
   binding (approved SHA, artifact, environment and nonce hash) belongs in the
   same transaction as acceptance; raw nonce stays private. Preserve per-plan
   serialization across provider recovery, but never hold a SQL transaction
   across HTTP calls. Scoped replay plus a domain singleton must prevent
   duplicate effects without disclosing another actor's acceptance. Test failed
   acceptance causes zero provider calls, lost responses recover the same
   PR/run, changed destructive acknowledgment conflicts, and failed completion
   persistence remains recoverable.

### Runner caller compatibility handoff

The local `norn-fleet` checkout at `56e0f7d` is clean but diverges from its
recorded upstream (`main` is four commits ahead and 141 behind), so this is an
exact local caller inventory rather than deployment or upstream evidence. Its
only runner-attempt create helper is `scripts/runner_attempt.py:start_or_resume`,
called by both `.github/workflows/apply.yml` and
`.github/workflows/recover.yml`. The shared `NornClient._call` already supports
an `idempotency_key` argument, but both create paths currently omit it and will
be rejected by the v3 required-key endpoint.

Patch that repository only from an isolated, explicitly selected revision. A
restart-stable, non-secret key such as
`fleet-runner-attempt:<planID>:<runnerAttemptID>` binds one workflow action and
remains stable across the client's existing HTTP retry loop; recovery receives
a distinct key because its GitHub run/attempt identity is distinct. Never put
the raw dispatch nonce in the key. Update `scripts/test_runner_attempt.py` to
capture and assert the outgoing key, and keep the checked-in workflow/setup
fixtures aligned. The accepted cross-repository runner identity is exactly
`github-actions:<repository>:<runID>:<runAttempt>` and the v3 handler compares
all three workload fields against verified OIDC claims; prefix aliases remain
invalid. Until that external patch is applied and tested at a known revision,
runner creation is not end-to-end compatible even though the v3 store and HTTP
acceptance path are locally verified.

### Durable event writer compatibility

The v3 PostgreSQL event writer serializes `control_events` ID allocation through
commit with an advisory lock keyed by the resolved table OID. This is a writer
protocol, not a constraint enforced by PostgreSQL: an already-running legacy
binary can still insert without the lock and make a lower sequence ID visible
after a higher cursor has advanced. Require a single-active-writer handoff (or
stop all pre-contract event writers) before enabling ordered v3 event delivery.
Metadata or a writer-version row alone does not fence an old process.

Persistent hub broadcasts are delivered only by draining committed rows after
the last contiguous cursor. A replay backlog larger than the advertised
500-event page is rejected before WebSocket upgrade with
`event_replay_too_large`; clients must perform an authenticated event-info
preflight, refresh authoritative state, and then reconnect from the captured
head. They must not reset a durable cursor merely because an arbitrary socket
error occurred. Store-less development hubs retain ephemeral direct delivery
and provide no replay guarantee.

### Verified legacy bridge design boundary

Use a separate `LegacyOperationBridge.ResolveOrAliasLegacy` seam rather than
silently falling back inside ordinary `Accept`. Its request carries the new
stable identity, current audit context, an allowlisted legacy endpoint scheme
and original input semantics. The adapter, not the caller, derives legacy keys
and verifies credential lineage; a caller-provided operation ID or `Verified`
flag is not ownership evidence.

Within one transaction, resolve trusted credential ancestry, reproduce the old
endpoint key and request digest, lock the candidate, verify its kind/resource,
source and immutable input correspondence, then insert an actor-scoped alias
and a signed bridge to the original operation. A bridge attests to provenance
verified now; it must not pretend legacy work had a new acceptance signature at
its original creation. Preserve original IDs and signed bytes. Existing audit
receipts have no durable operation link, so timestamps, paths and matching
display subjects cannot manufacture such a link.

The current identity/intent tables uniquely bind operation IDs and `Resolve`
expects a new acceptance envelope; legacy bridges require an explicit schema
and resolver extension, not reuse of those assumptions. Concurrent aliases and
lost commit acknowledgments must resolve the same bridge. An incomplete lineage
lookup is ambiguous, not permission to enqueue replacement work. Require
multi-token rotation, cross-device lineage rejection, foreign collision
non-disclosure, changed request/qualification, retained signature verification,
concurrent alias and uncertain-commit tests before enabling this compatibility
path. Raw maintenance keys and subject-only identities remain ambiguous unless
additional durable ownership evidence exists.

## Required acceptance matrix

This is the required matrix, not a completion ledger; consult
[implementation status](implementation-status.md) for locally verified subsets
and remaining integration gaps. Use independent PostgreSQL pools for concurrency and
real HTTP middleware for handler integration; unit mocks alone cannot establish
the transaction boundary.

| Case | Required evidence |
| --- | --- |
| Concurrent identical request | Two stores return one original operation/saga/deployment identity; one request identity and signed acceptance intent; one regional set. |
| Fingerprint mismatch | Same scoped key with changed kind-relevant payload/ref/qualification fails without creating extra rows. |
| Actor isolation | Same raw key from different actors never returns the other's operation; scope separation is explicit. |
| Credential rotation | Same verified actor, new token ID, same key/fingerprint replays; different actor with matching display subject in another issuer does not. |
| Legacy collision | Unattributable raw key yields non-disclosing ambiguity; verified same-actor legacy replay preserves original IDs; foreign actor cannot acquire an alias. |
| Transaction rollback | Forced intent, regional or operation insertion failure leaves no accepted operation, identity, deployment or intent. |
| Signing failure | Missing production key or signer failure prevents acceptance; committed envelope independently verifies with retained key and detects altered links/fingerprint. |
| Audit lifecycle crash | Crash after acceptance but before HTTP finish leaves valid signed acceptance linked to the unfinished receipt; existing receipt canonical bytes remain unchanged. |
| Lost commit response | Inject failure after commit but before acknowledgment; fresh Resolve and same-key retry return original result with no duplicate rows. |
| In-flight uncertain commit | Retry races original commit/rollback and converges; unavailable resolution reports indeterminate rather than false failure/success. |
| Claim visibility | A second connection cannot observe/claim the queued operation before its intent and required domain rows commit. |
| Active-app admission | Concurrent different keys obey the endpoint's promised admission policy atomically. |
| Completed planning receipt | Same acceptance/replay/audit invariants hold; receipt is terminal and never claimable. |
| Legacy pipeline errors | Failed acceptance returns an error, no false queued saga response, no orphan rows. |
| Writer compatibility | Older contract-aware writer rejected; transition fixture proves already-running/pre-contract writers cannot bypass the active invariant. |
| Retention/export | Unresolved references survive pruning; terminal retention remains scoped; original signed bytes and replay identities survive export/restore. |
| Boundary inventory | Search/compile review accounts for every operation-producing call site and removes acceptance bypasses in the selected domains. |

Passing this slice advances M1 but does not complete the full PG domain adapter.
Identity, conditional deployment/Fleet transitions, desired state, evidence/event
interfaces, canonical backup/export, and downstream-effect authority remain
separate required work with their own evidence.
