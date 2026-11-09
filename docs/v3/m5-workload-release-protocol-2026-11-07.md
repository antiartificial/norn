# M5 workload resize and mixed-version protocol

> **Current delta (2026-11-09):** The authenticated, staging-only workload
> admission API merged as Norn PR #319 (`877ad4ba`), and PR #324
> (`3fcdc154`) fixes concurrent same-key replay at the etcd store boundary.
> PR #331 (`c58df6aa`) reconstructs the etcd client and operation store after
> claim loss, reloads the durable receipt, and advances it under a successor
> claim. PR #333 (`443ca840`) extends the bounded admission replay model to
> cover a durable acceptance whose response is lost, controller crash/restart,
> and same-key replay after restart (115 states / 365 steps; ten invariants and
> deadlock checking pass). The model abstracts the implementation. Together
> these provide store-level restart evidence and offline protocol evidence.
> PR #335 (`0eb10852`) exercises authenticated HTTP replay against real etcd
> after dropping a response. It seeds the prior signed acceptance through the
> real store, then verifies same-ID replay and changed-selection conflict.
> PR #337 (`15a8d2d4`) closes and rebuilds the HTTP handler and etcd adapters
> before retrying. This proves API-boundary restart recovery for replay, not
> fresh HTTP admission or workload worker/controller restart.
> Two independent store clients now converge on one fresh acceptance and one
> replay; its bounded model records the pre-fix counterexample. A separate
> handler test and statechart force two initial replay misses and prove one
> fresh response plus one replay through a fake atomic store. The older status
> text below predates that producer and should be read as implementation
> history.
> The API remains opt-in, undeployed, and unqualified against live or
> disposable Nomad/ingress drivers. Exact source recovery, private-input
> retention/deletion semantics, and full recovery qualification remain open.

Status of the pre-#319 implementation snapshot: the durable etcd worker,
signed-acceptance verification, claim-bound route writer, old-stop recovery,
and same-intent controller replay are wired as offline implementation seams.
At that snapshot there was no authenticated operator/API path. PR #319 adds
that path, but production-driver crash qualification and live qualification
remain open. The private-input key inventory is checked at startup when its
dedicated capability is enabled; that capability remains disabled by default.
This note authorizes no live workload, route, or provider change.

## Current verification — 2026-11-09

Norn PR [#311](https://github.com/antiartificial/norn/pull/311) merged at
`4c90aecfc47d4687cc3f39d433d8d5d182d61a7d`. It adds durable replay keyed by
a server-computed digest of stable request selection (qualification ID and
target replica count). The store resolves the existing signed acceptance
before validating the current plan, qualification trust/expiry, source
projection, serving-lineage observer, or encrypted input snapshot. A focused
loopback-etcd integration test accepts once, removes the current source
projection, retries with a new proposed operation ID and changed plan and
SOPS bytes, and verifies the original accepted operation returns without a
fresh lineage read. Changed qualification or replica selection conflicts.
This is a store-boundary guarantee. PR #319 adds an opt-in staging API that
computes the stable request digest and submits it through private acceptance;
PR #321 adds deterministic HTTP-handler coverage for fresh admission and exact
same-key retry before mutable reads. PR #324 adds simultaneous same-key
coverage across two independent etcd clients and records the repaired race in a
separate statechart. A deterministic HTTP test now forces two handlers to miss
the initial replay lookup, then checks one fresh response and one replay with
the same operation ID. That HTTP test uses a fake atomic store; an
implementation-backed HTTP-to-etcd concurrency test, full worker/controller
restart and lost-response recovery, and production-driver qualification remain
open.

### HTTP-to-etcd fixture boundary (2026-11-09)

A local disposable-etcd integration harness was explored and rejected before
being kept: a synthetic already-succeeded `app.deploy` cannot be admitted as
the qualification source. The real store requires a signed Fleet target,
exclusive app admission, a completed signed deployment with complete region
results, and an active route whose initial-route intent and reservation match.
The ordinary terminal deployment helper is package-private to `etcdstore`
tests; the public initial-route completion path requires deployment-bound
ingress proof. Directly writing a successful-looking operation or route would
weaken the evidence this test is meant to provide.

The next implementation-backed test must establish its qualified source via
the real accepted deployment and route-completion path (or first extract a
shared fixture seam that still drives that path), then run the production HTTP
handler and real `V3OperationStore` against disposable etcd. It may fake the
Nomad lineage observer, but it must not fake qualification lookup, target or
policy reads, serving-route resolution, private-input persistence, or atomic
acceptance. The abandoned harness was removed; no runtime or release behavior
was changed by this investigation.

Exact-head PR #311 checks passed for API Go tests, Fleet pilot workload,
durable replicas on three Nomad clients, app catalog, CLI, OpenAPI, platform
release qualification, workflow lint, Web UI, and Socket security. Path-filtered
checks were skipped. This CI evidence validates code and disposable fixtures;
it does not qualify the deployed DO Fleet, Mini, or any live workload release.

The offline statechart in `docs/v3/statecharts/workload-release-admission/`
checked 458 states and 1,663 transitions, with 18 safety invariants and
deadlock checking passing. The expanded model separates the active-route
predecessor from independently observed serving lineage, and includes a crash
after stage-effect reservation but before the create marker. It remains a
protocol model, not proof of the runtime driver.

The claimed worker stages exact operation-bound SOPS ciphertext, marks the
one-use create attempt, uses create-only Nomad registration, ignores the
registration response, and binds only exact target readback. Missing or
ambiguous readback remains pending and cannot trigger a second registration.
The runtime key preflight, deterministic worker tests, and focused etcd
successor-recovery tests are offline evidence. Delayed Nomad acceptance,
production ACL behavior, key-retention lifecycle, and version-pinned
production-driver qualification remain open.

The V3 store resolves a qualification by exact ID from its signed operation
record, verifies the trusted staging signature and expiry, and requires a
matching completed staging deployment and region results. Both that lookup and
private-input acceptance have passed against disposable local etcd. PR #319's
API admission producer calls this resolver and private acceptance; PR #321
exercises that path with a fake store and Nomad-lineage observer. PR #324 proves
same-key concurrency at the store boundary. PR #331 reconstructs the client and
store to verify durable receipt reload and successor-claim recovery. PR #333's
TLA+ model checks the protocol path from a lost acceptance response through
controller stop/restart to durable same-key replay, but it does not run the
HTTP handler or runtime controller. The HTTP concurrency test exercises two
real handler calls through a fake atomic store. PR #335 separately exercises
the authenticated handler's replay branch against real etcd after a simulated
lost response. It seeds the acceptance through the real store. PR #337 then
closes and rebuilds that handler and the etcd adapters before the retry. Fresh
HTTP admission, full workload worker/controller restart and lost-response
behavior, and production driver behavior remain unqualified.

PR #337's exact-head CI passed API Go tests, Fleet pilot workload checks, M4
durable-replica qualification, app catalog and OpenAPI contracts, platform
release qualification, CLI tests, workflow lint, Web UI checks, and Socket
security. The focused integration test also passed against a local etcd
instance with
`NORN_TEST_ETCD_ENDPOINTS=http://127.0.0.1:23791 go test . -run '^TestEtcdWorkloadReleaseAdmissionHTTPReplaysDurableAcceptanceAfterRestart$' -count=1`.
These results qualify the replay branch across API handler and adapter
reconstruction. They do not qualify the runtime worker/controller, live DO
workloads, or fresh admission.

## Release-readiness milestones and order

The current code and evidence support a staged plan, not a claim that V3 is
ready to update either environment. Keep the independent Mini and the DO
staging Fleet as separate release lanes. The DO provider inventory is not
evidence that Nomad, Consul, Norn, application routes, or workloads are healthy.

| Milestone | Status | Work | Exit evidence | Can run alongside |
| --- | --- | --- | --- | --- |
| M5-A: offline protocol | Partial: merged PR #311 now adds exact stable-request replay before mutable reads; expanded bounded statechart (458 states/1,663 transitions, 18 invariants plus deadlock), fake-driver lost-response matrix, pure plan/binding resolver, claim-fenced write-once etcd binding, one-use attempt marker, and stage-worker wiring pass locally. Offline tests cover secret-stage failure before marker/create, conservative hold while an ambiguous create remains absent, and exact-target adoption when it becomes visible without resubmission. Four independent missing-admission-evidence scenarios now reject. Production-driver failure matrix remains open. | Keep the bounded admission/recovery statechart and Go contract fixtures aligned; enumerate crashes at every durable stage/create/bind/route/stop boundary. | Reproducible model report and deterministic fixtures prove exact replay, pre-attempt reservation recovery, exact-target adoption after response loss, no traffic before binding, no old stop before full target health plus target-only route proof, and hold on absent or mismatched target. | M5-B, input-provenance inventory, M5-C design |
| M5-B: immutable input contract | Partial: purpose-separated encryption, signed-plan-bound SOPS snapshot capture, atomic etcd acceptance/input persistence, opt-in retained-key preflight, signed qualification lookup, and server-side Nomad lineage are implemented. PR #319 resolves an operator-selected qualification ID and checks configured app-source spec digest, but automatic candidate discovery is not provided; durable source retrieval after worker restart and private-envelope retention/deletion semantics remain open. | Make exact app source durably retrievable; define private envelope availability, rotation, retention and deletion; preserve qualification expiry and reject caller-supplied scheduler observations. | Deterministic tests bind app, trusted signer, validity window, completed staging deployment, source SHA, exact content-addressed artifact, and attestation subject digest; missing or rotated inputs fail closed across restart. | M5-A, M5-C design |
| M5-C: atomic release admission | Implemented, not deployed: PR #319 adds an opt-in staging-only managed-operator endpoint. It accepts only qualification ID and target replica count, resolves exact replay before mutable reads, derives serving lineage/current Fleet target/approved policy/source/SOPS snapshot, and calls private acceptance with the Nomad observer. PR #321 covers fresh admission, exact retry, and CI-principal rejection with deterministic fakes. PR #324 covers simultaneous same-key acceptance across two independent etcd clients. The handler concurrency test forces two initial replay misses and checks one fresh result plus one same-ID replay using a fake atomic store. PR #335 covers authenticated same-key replay through the real handler and etcd after a dropped response, with the prior acceptance seeded through the real store. PR #337 rebuilds the handler and etcd adapters before replay. | Prove fresh admission through the handler and real etcd with real qualification and private-input persistence. Then qualify workload worker/controller restart and production-driver recovery. Install only after DO M3/M4 and exact-source/private-input recovery design pass. | Store concurrency, fake-store handler concurrency, and real-etcd handler replay across handler/adapter reconstruction now pass at their separate boundaries. Fresh handler acceptance, full runtime recovery, and production-driver qualification remain required. | M5-A/B |
| M5-D: private stage and binding | Partial: signed-plan-bound stage effect, shared app gate, one-use attempt marker, successor-claim recovery, exact database-free job builder, operation-bound SOPS input decryption, private Nomad secret staging, create-only CAS, exact readback, and strict-intent downstream wiring implemented locally. Retained input-key preflight now checks V3 etcd before its deploy worker starts. Offline orchestration tests prove secret-stage failure, claim loss after secret staging, and marker transaction failure all prevent create; absent readback holds, and a later exact readback is adopted without a second registration. Full API Go suite passes. Delayed-request/production-driver cases are not yet qualified. | Extend deterministic worker fault cases for every stage/create/readback/bind boundary, then run the same protocol against a version-pinned disposable Nomad/ingress stack with delayed acceptance and lost responses. | Fault matrix proves exact-target adoption after lost response or durable conservative hold, never a duplicate create; no route changes during stage. | M5-E test harness |
| M5-E: production-driver qualification | Not run. | Exercise the complete controller with disposable Nomad/ingress, real configured ACL policy, delayed requests, response loss, controller restart, and route/readback faults. | Version-pinned evidence for stage, route, health, drain, same-intent recovery, and service probes; no unresolved effects. | M5-D implementation |
| M5-F: DO staging workload release | Partial: historical 2→3→2 scale, sequential drain, and canary probes passed; the 2026-11-08 public route serves source `be5ca5d…` across two reported allocations. The durable second-release operation receipt has not been re-read, and mixed-version recovery is not qualified. | Reconcile the failed operation against current DO Norn/Nomad receipts; after M5-A–E, run fixed-image resize and mixed-version recovery through the release-owned admission protocol. | Correlated signed operation, exact image/route/drain receipts, healthy probes, no stranded allocations, and recovery evidence. Stateful MySQL compatibility remains a separate later gate. | M5-A–E completion and receipt readback |
| M5-G: independent Mini V3 upgrade | Complete for the 2026-11-07 direct V3 adoption; the 2026-11-09 read-only Mini refresh confirms API/host `ok` on signed repair release `d4dc88e…`. The latest assurance receipt is still dated Sep 6, and 25 incidents remain. | Retain the signed adoption/controller-repair receipts and repeat candidate-specific backup, compatibility, and postflight checks for every future Mini release. Keep Mini independent of Fleet. | Signed target `bba290d…`, controller journal and preservation receipt, schema-51 postflight, and current API/host readback on signed repair source `d4dc88e…`. This does not establish uninterrupted service, fresh route assurance, or rollback across future incompatible schema changes. | Documentation; M6 low-interruption/rollback work |

Remaining M5-A counterexample replay and M5-B source/private-input recovery are
the next offline priorities. PR #319 exposes an operator action, so keep its
runtime gate disabled until those recovery contracts and M5-D/E are proven.
M5-F is the first workload qualification. M5-G records the completed independent Mini adoption; it does
not qualify DO workloads. Run the DO runtime/capacity gate before M5-F and
refresh Mini preflight before any later Mini release.

The fresh-Fleet bootstrap lane is separate from upgrading the existing DO
staging Fleet. Its future two application pools default to **General** and
**Stateful**; do not resize or reshape current staging to implement that
default. Bootstrap still needs deterministic credential/input war-gaming and
an idempotent controller recovery record before any provider mutation.

## V2 compatibility and test retirement

The current versioned HTTP contract is `/api/v1`; `v2/api` is the Go source
path, not evidence of a separate `/api/v2` API. A few `v2` test names refer to
wire-schema generations rather than the retired server contract. The runtime
still retains an unset-database-profile fallback for historical PostgreSQL
deployments, and legacy migration/recovery fixtures still protect the Mini
transition. No test is safe to delete based on its name alone. Retire tests
with the legacy runtime paths they cover after the Mini PostgreSQL/runtime
compatibility gate is explicitly closed; keep recovery and fail-closed
security tests until then.

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
seams. PR #319 now adds an opt-in authenticated staging API that creates an
accepted release operation. It is not deployed or live-qualified. A
production-store worker crash matrix remains open.

The etcd claim verifier now reloads the signed `app.workload-release`
acceptance, requires its exact `app/<name>` identity, checks the running claim
and app lock, and binds the intent to the current Fleet target generation and
datacenter placement. Local etcd integration tests pass for the valid signed
intent and fail closed on changed target, payload drift, or released lock. This
verifies admission provenance only; it does not reserve or publish route steps.

The admission design now also defines a scheduler-free
`WorkloadReleasePlan` that resolves only after a create-only Nomad registration
has been observed. The plan-to-binding resolver and etcd write-once binding
store are implemented locally: binding requires the signed plan, live operation
claim, app lock, and unchanged Fleet target generation; replay of the same
binding is idempotent, while a conflicting binding or stale claim is rejected.
The plan resolver and write-once binding were part of PR #302. Subsequent
worker, private-input, and recovery wiring merged in later PRs, including the
operator admission route in PR #319. Exact source and private-input recovery
contracts and live driver qualification remain open.

The stage effect has a signed-plan validator and reserves through the shared
app mutation gate. The claimed worker now rebuilds the target from the exact
local InfraSpec, opens only the operation-bound encrypted managed inputs,
stages those inputs, writes a claim-fenced one-use attempt marker, and issues
create-only Nomad CAS. It ignores the write response and binds only after exact
source/no-diff Nomad readback. A missing or ambiguous target returns pending;
the worker does not retry registration. Existing health, route, drain, and
stop readers resolve the durable binding back into the strict intent. Binding,
successful effect completion, and app-gate release commit in one etcd
transaction. Focused etcdstore, startup, worker, secrets, and API package tests
pass locally. Full repository verification and delayed-request/production
driver qualification remain open.
Qualification provenance has a store-level check against the completed V3
deployment record, described below. PR #319 resolves an operator-selected
qualification ID; automatic candidate discovery is not provided. Exact source
retrieval and private-input retention semantics remain open.

Private workload-release acceptance now requires a trusted signed staging
qualification and corroborates it against the V3 deployment store: the
qualification's deployment ID must resolve through the signed acceptance
index to a succeeded `app.deploy` operation, with matching staging app,
source revision, immutable image, spec digest, and completed region results.
A valid signed receipt without that completed V3 source is refused. PR #319
connects this resolver to the opt-in staging HTTP admission route. The
operator selects the qualification ID; candidate discovery remains explicit.

Private workload-release acceptance now has a separate etcd input record. Its
purpose-separated envelope binds the exact encrypted SOPS bytes to authority,
operation, app, and plan digest; the acceptance, private record, one-active-app
fence, and current control-owned Fleet target revision commit atomically. A
retry opens the retained envelope and constant-time compares the submitted
snapshot before returning the original acceptance. Restore can enumerate all
required envelope key IDs. Admission also requires a current control-owned
active route and matching reservation; the signed plan's old deployment must
match that route, and its first route generation must be the immediate
successor. Both etcd revisions are compared in the acceptance transaction, so
a route handoff racing acceptance cannot leave a stale release queued. This
PR #319 mounts this primitive behind the staging-only authenticated
admission API and connects it to the shared target job builder; deployment and
live qualification remain open.

The API can now opt into retained-key startup preflight with
`NORN_WORKLOAD_RELEASE_INPUTS_ENABLED=true`,
`NORN_WORKLOAD_RELEASE_INPUT_CURRENT_KEY_ID`, and
`NORN_WORKLOAD_RELEASE_INPUT_KEYS` (a JSON object mapping key IDs to base64
32-byte keys). This ring is separate from Function V3 and audit signing keys.
When enabled, startup reads all retained workload-release envelope key IDs and
fails before workers/HTTP serving if the store cannot be read, the store does
not expose the key inventory, or any historical key is absent. Turning this
capability on does not enable an admission endpoint or release worker. Key
rotation/removal still needs an explicit retention and deletion policy; do not
remove a key until all retained records that need it are retired or rewrapped.

## Qualification order

### A. Offline contract first

Build a deterministic test fixture using synthetic images, payloads, operation IDs,
schema epochs, and route receipts. No real Fleet credentials, provider calls,
SSH, public route mutation, or production data should be available to it.

The proposed release-owned staging and recovery path has a bounded TLA+ model
in [`statecharts/workload-release-admission`](statecharts/workload-release-admission/README.md).
It checks the attempt-marker crash gap, delayed create, exact target binding,
route progression, and the target-only proof required before old-job stop.
The model does not prove that the admission or staging code exists. It also
does not replace disposable Nomad and ingress qualification.

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
`draining` receipt proving full target health and the final target-only route
generation while the old allocations are still running before it reserves or
launches the old-job stop. Mixed-route proof alone is not sufficient. See
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
verifies the signed acceptance under the app lock. The current API and CLI
entry points do not create an accepted operation. Operators cannot use this
worker path to start a release.

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
completion, then re-enters the same reconciler. Merged PR #339 additionally
rebuilds the etcd client and operation store after a lost route-effect response,
recovers the operation under a successor claim, and runs the production shared
`reconcileWorkloadRelease` lifecycle to completion. Its scheduler, route,
health, and drain effects are controlled fakes; it does not exercise the
production `etcdWorkloadReleaseExecutor`, Nomad/ingress adapters, fresh HTTP
admission, or full worker-runtime wiring. Those production-driver and live
qualification gates remain open.

The receipt lifecycle refreshes target allocation health in warming, routing,
and draining phases. Total target health loss after traffic begins records a
terminal held receipt. Committing the final target-only route while all old
allocations are still running transitions into draining. Before any old-job
stop request, the controller requires full target health and target-only route
proof for the exact committed generation. Its stable stop marker survives
claim renewal. After the durable stop result, a fresh Nomad readback binds the
exact stopped revision and every pinned allocation into the receipt; terminal
completion requires the target-only route, full target health, and the verified
old-allocation drain. The lost-response integration fixture covers stop recovery,
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
