# M5 workload resize and mixed-version protocol

Status: the durable etcd worker, signed-acceptance verification, claim-bound
route writer, old-stop recovery, and same-intent controller replay are wired
as offline implementation seams. There is still no operator/API path to admit
an `app.workload-release` operation. A production-driver crash matrix and
live qualification remain open as of 2026-11-08.
This note authorizes no live workload, route, or provider change.

## Keep three changes separate

1. **Replica resize:** change one workload's durable desired count while keeping
   its image and configuration fixed. This exercises Norn `app.scale` and
   scheduler placement.
2. **Application release:** run old and new application allocations together
   under a workload-specific compatibility contract, then move route traffic
   and drain the old generation. This is not the same as changing replica
   count.
3. **App-pool resize:** add or retire Fleet nodes through a reviewed provider
   capacity plan. Adding nodes does not guarantee existing allocations move.

Keep the Norn control binaries fixed for all M5 work. The DO platform lane's
all-stop protocol is independent from application overlap.

## Source findings

The repository already has durable Norn app-scale effects and tests for exact
job targeting, ambiguous counts, operation-claim generation, and recovery after
Nomad accepts a scale but persistence of the desired count fails. The etcd
integration suite also covers a lost Nomad response and same-effect recovery.
These prove scale-operation behavior; they do not prove mixed application
versions or data compatibility. See `v2/api/pipeline/scale.go`,
`v2/api/pipeline/scale_test.go`, and
`v2/api/etcd_scale_http_integration_test.go`.

There are two useful staging candidates:

- **First: `norn-fleet-canary`**, a database-free web service on the `app` pool.
  Its catalog declares two to four replicas, distinct-host placement, readiness
  and liveness checks, bounded rollout/drain settings, and a restricted
  `/route-proof` and `/version` route. It is the safer first target for testing
  route health, canary readiness, mixed application image versions, drain, and
  same-intent recovery. Its release note explicitly says route health, two
  healthy replicas, and 2→3→2 scale remain separate qualification evidence.
  Sources: `v2/infra/fleet-staging/canary/infraspec.yaml` and
  `v2/infra/fleet-staging/canary/RELEASE.md`.
- **Second: `hello-norn-mysql-managed`**, which exercises shared managed-MySQL
  data, separate migration/runtime identities, signed writes, durable
  readback, and the protected route. It is a better stateful compatibility
  test after the stateless path passes. Its catalog says it remains inactive
  until its protected release gate and routes/variables are verified. The
  serving direct Nomad job does not prove Norn-managed release or scaling.
  Sources: `v2/infra/fleet-staging/hello-norn-mysql/infraspec.yaml` and
  `v2/infra/fleet-staging/README.md`.

The mixed-version statechart in
`docs/v3/statecharts/workload-mixed-version/` is a proposed transition contract,
not a description of a shipped generic mixed-version release controller. It
requires additive schema expansion, compatible old/new writers during overlap,
healthy target capacity before each old allocation drains, fresh route proof,
and same-intent recovery. Proposed route policy is forward-only: loss of route
proof holds drains and schema contraction; recovery resumes only after a fresh
readback confirms the exact currently published generation. Automatic fallback
to an older generation is not modeled. Switching back requires a separately
admitted intent and fresh health and data-compatibility proof.

An offline contract slice now exists in
`v2/api/pipeline/workload_release_contract.go`. It binds immutable workload,
deployment, old/target image, revision, and allocation identities to a stable
intent digest. The intent also pins the exact public traffic-probe path and
canonical response-body SHA-256 required for independent route proof; the path
must be one of its admitted public paths. Its verified-progress receipts
enforce the route generation and
monotone old-only → mixed → target-only progression; target health and exact
route proof precede an old-allocation drain. Each intent step now renders
through the production TLS weighted-route renderer, and receipt validation
checks its digest against the canonical rendered route bytes. Duplicate or unknown drains,
mutable target tags, backward/non-total traffic weights, missing mixed steps,
unbound/unhealthy target identities, excess surge, skipped route generations,
and unsupported terminal receipts hold. Target observations bind exact
allocation IDs, distinct node IDs, job ID, namespace, task group, pinned Nomad
job version and modify index, target image digest, and health in one record.
The read-only Nomad adapter now derives this projection from exact job and
allocation readback; offline API fixtures cover healthy, pending, and changed
identity/image cases. A reasoned held receipt can capture
total target health loss while preserving the last route readback; it cannot
advance or drain and is terminal for that intent. A pure successor validator
permits only one forward route step and preserves drain acknowledgements
through holds. Canonical receipt digests are order-stable and sensitive to
progress changes. PostgreSQL and etcd stores now provide claim-fenced receipt
compare-and-swap; the pipeline repository validates each transition before
persistence. The immutable intent pins the old deployment's Nomad job ID,
version, modify index, job digest, spec digest, image, and allocation IDs, and
the target deployment, Nomad job, source job digest, spec digest,
content-addressed image, operation, and execution identities. A read-only
Nomad observer verifies target source/no-diff plan, exact job revision, and
allocation health. The target-health controller writes that projection into
the warming receipt under the current operation claim; it does not publish a
route or advance traffic. A separate Nomad observer checks the exact post-stop job
version/modify-index receipt, deployment/spec/job-digest provenance, old task
image digest, and terminal state of every pinned allocation; unnamed live
allocations or missing/substituted IDs hold. A guarded Nomad stop CAS stamps a
unique supervisor execution ID and immutable intent digest onto the stopped job revision.
Marker-based readback can rediscover the exact post-stop version/index after a
lost write response, without repeating the stop. The pipeline now has a
durable effect executor that binds the operation claim, app/region resource
gate, one-use Nomad stop, marker readback, and terminal-allocation proof. An
end-to-end HTTP fixture commits the stop then drops the response; same-effect
recovery verifies the marker and completes with one registration. The receipt
stores and stop executor are connected to claim-bound progress and stop
recovery. The etcd runtime verifies signed acceptance and dispatches the
internal worker; route generation reservation, per-node publication,
independent readback, and atomic promotion are also implemented as controller
seams. The external API/CLI path that creates an accepted release operation is
still absent, so these slices do not yet form an operator-facing release
action. A production-store worker crash matrix and live qualification also
remain open.

The etcd claim verifier now reloads the signed `app.workload-release`
acceptance, requires its exact `app/<name>` identity, checks the running claim
and app lock, and binds the intent to the current Fleet target generation and
datacenter placement. Local etcd integration tests pass for the valid signed
intent and fail closed on changed target, payload drift, or released lock. This
verifies admission provenance only; it does not reserve or publish route steps.

## Qualification order

### A. Offline contract first

Build a deterministic harness using synthetic images, payloads, operation IDs,
schema epochs, and route receipts. No real Fleet credentials, provider calls,
SSH, public route mutation, or production data should be available to it.

The stateless canary contract should assert:

- exact old/new image digests, source revisions, app/job identity, region,
  namespace, and signed intent remain bound across admission and recovery;
- a target allocation is healthy on a distinct eligible app node before it
  receives traffic or an old allocation is drained;
- failed readiness, insufficient surge capacity, lost route proof, stale
  allocation inventory, changed target digest, expired/replayed intent, or
  lost controller response causes a hold or same-intent reconciliation, never
  an unreviewed second target;
- each drained allocation is acknowledged once; route receipts identify the
  actual target generation; the terminal receipt matches desired and observed
  counts;
- crash injection after every durable checkpoint either resumes the same
  target safely or stays held without retiring the serving generation.

Add a separate `app.scale` fixture for 2→3→2 with the image/config pinned.
Assert accepted desired count survives API/worker restart and later redeploy,
one incomplete drain blocks contraction, and same-intent recovery does not
launch a duplicate Nomad scale effect. Do not use this fixture as evidence of
mixed-version compatibility.

For the MySQL phase, add two synthetic application generations that both read
the expanded schema, gate writes by a declared writer epoch, and preserve an
acknowledged record across target failure, an explicitly admitted route switch,
restart, and recovery. Contracting schema is a later intent after exact
old-writer absence; image rollback alone cannot undo data writes. Use disposable
synthetic data and verify its stable record ID/hash without logging credentials
or response bodies.

### B. Live work Fleet gates

Only after the DO read-only readiness milestone and platform release gate pass:

1. Read capacity, eligible app nodes, allocations, route target, pending work,
   and exact current image/operation receipts. Verify there is one extra
   schedulable app node for a two-replica, distinct-host canary; otherwise
   separately approve capacity first.
2. Use the existing `norn-fleet-canary` with a protected immutable candidate
   digest. Record continuous route probes, target allocation health, operation
   and effect receipts, errors/latency, drain acknowledgements, and exact
   allocation IDs throughout rollout.
3. Run fixed-image 2→3→2 replica scale separately. After that passes, run
   old/new application-version overlap and prove the target route before
   draining any old allocation. Restore the original desired count and confirm
   no allocation is stranded.
4. Advance to `hello-norn-mysql-managed` only after its separate protected
   release gate, route, runtime variables, and database identities are
   verified. Capture acknowledged writes before, during, and after overlap;
   verify the same records remain readable and no old writer survives the
   schema-contract step.
5. Keep app-pool node addition/removal in its own reviewed capacity plan.
   During contraction, prove workload drain and exact node identity before
   deletion; do not assume node addition rebalances existing allocations.

Record test start/end, source and image digests, workload identity, pool
inventory, baseline and peak allocation counts, requests/errors/latency,
acknowledged-write checks, operation/effect IDs, route/drain receipts, recovery
checkpoint, and any service interruption. A deployment being `healthy` in
Nomad alone does not prove public-route availability or data correctness.

## M5 exit criteria

The old-generation stop/recovery slice now has a bounded statechart and a
deterministic Go lost-response fixture. The model checks one stop write per
intent, marker recovery without a second stop, and completion only after the
pinned old allocations are terminal. The executor requires a validated
`draining` receipt proving full target health and the exact mixed-route
generation before it reserves or launches the old-job stop. See
[`statecharts/workload-stop-recovery`](statecharts/workload-stop-recovery/README.md).
The receipt repository now loads integrity-checked bytes from claim-fenced
PostgreSQL or etcd CAS stores and validates each successor before storage. The
etcd adapter survives owner-lease expiry and advances the receipt under a new
claim generation for the same operation; expired `app.workload-release`
operations now enter same-intent recovery instead of terminal manual recovery.
The stop executor loads the receipt itself and checks that it belongs to the
same operation. A target-health controller also verifies the active claim and
warming phase, reads the exact pinned target job through Nomad, and persists
pending or healthy allocation evidence without mutating Nomad or ingress.
The etcd runtime registers the worker for `app.workload-release` and
verifies the signed acceptance under the app lock. The current API/CLI surface
does not create such an accepted operation, so this remains an internal worker
seam, not an operator-facing release action.

The scale-target reader now resolves completed workload lineage through its
signed acceptance and complete receipt. It also requires the shared route
reservation to match the active pointer, so an in-progress workload route
transition blocks a scale target until the active route and receipt are
reconciled. Local etcd-backed tests cover a valid completed release, mismatched
route generation, changed Fleet target placement, and a conflicting shared
reservation. This is read-side fencing only; it does not publish a route.

The etcd route writer now persists an immutable per-step intent and shared
reservation under the signed claim, app lock, accepted payload, active-route
predecessor, target placement, and current ingress-inventory fences. Its
per-node authorization rechecks those bindings and releases route bytes only
to a node in that inventory. The active pointer remains unchanged while a
step is being published, so scale readers fail closed. A same-intent retry can
continue after partial node publication without pretending every node remains
on the predecessor. After node publication, a commit adapter independently
checks route readback, exact ingress endpoints, and the public traffic probe,
then advances the active pointer and receipt in one etcd transaction. Its
same-intent recovery requires fresh proof and accepts only the exact committed
generation; node publication retries are idempotent at that generation.
Local etcd tests cover intent creation, partial-publication retry, listed-node
authorization, wrong-node denial, atomic promotion, lost-commit-response
recovery, same-generation publication retry, and a failed public probe leaving
durable state unchanged. `PublishProveCommitClaimedWorkloadReleaseRouteStep`
now provides one claim-bound entry point for intent, per-node publisher
fanout, independent proof, and atomic promotion. The etcd worker now claims
signed `app.workload-release` operations, verifies acceptance under the app
lock, and reconciles health, route steps, old-stop/readback, final route, and
terminal receipt. Deploy and workload route writers share a serialized
executor because they bind the same private authority listener. Owner-only
transport configuration is reused. The controller lifecycle has a deterministic driver-level fixture that injects
lost responses after initialization, health, each route step, stop, drain, and
completion, then re-enters the same reconciler. A production-store worker
fixture using the real etcd, Nomad, and ingress adapters to inject process and
lease failures remains open; no live qualification has run.

The receipt lifecycle refreshes target allocation health in warming, routing,
and draining phases. Total target health loss after traffic begins records a
terminal held receipt. Committing the mixed route transitions into draining,
which makes the exact old-stop executor reachable only after mixed-route
traffic proof. Its stable stop marker survives claim renewal. After the durable
stop result, a fresh Nomad readback binds the exact stopped revision and every
pinned allocation into the receipt; the final route can advance only after that
drain proof, and terminal completion requires the target-only route and full
target health. The lost-response integration fixture covers stop recovery,
exact drain receipt, final-route receipt, and terminal completion. Route-intent
integration covers replay after a lost commit response, and the top-level
fixture covers eight durable response-loss boundaries with exactly one
old-stop write. The full repository test run exposed migration inventories
that must be updated alongside any new durable table: startup contract,
control-recovery inspection, and retention classification now include
workload-release receipt migration 52. After extracting the driver seam,
`NORN_TEST_ETCD_ENDPOINTS=http://127.0.0.1:23795 go test ./... -count=1`
passed on 2026-11-08 against a local etcd instance, which was stopped after
the run.

The separate [route-publication statechart](statecharts/workload-route-publication/README.md)
checks 55 states and 111 transitions across eight safety invariants and
deadlock freedom. It models partial per-node publication and an atomic etcd
transaction that advances the active route and receipt together. A lost
response after commit requires fresh same-intent route and traffic
revalidation before the old stop. Route-step authority, durable intent, independent traffic proof, and atomic
active-route/receipt promotion now exist as controller seams. Internal worker
dispatch and driver-level lost-response replay are implemented. Operator/API
admission, a production-driver crash matrix, and live qualification remain
open.

- Fixed-image replica scaling, application-version overlap, and node-pool
  resizing have separate intent and receipt types.
- Offline success, invalid-input, capacity, route-loss, crash, replay, and
  recovery cases pass with no provider or public-route mutation.
- Each old-allocation drain acknowledgement is derived from exact Nomad
  readback of the pinned old job revision and the named allocation reaching a
  terminal state; stale, substituted, or still-running allocations hold.
- The stateless canary maintains its agreed service budget and exact route
  generation through scale, rollout, drain, restart, and same-intent recovery.
- The stateful candidate preserves every acknowledged write across overlap and
  recovery, and schema contraction waits for verified old-writer absence.
- Current DO read-only readiness and the DO platform release gate are fresh
  before any live workload qualification; otherwise M5 stays offline-only.

Until those gates pass, mixed application versions remain a modeled and
proposed protocol. Existing app-scale and Fleet capacity mechanisms are useful
building blocks but do not by themselves qualify that protocol.
