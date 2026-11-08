# V3 release program milestones

Status: active work map, refreshed 2026-11-08. This checklist orders the [release fitness
and controller plan](release-fitness-and-controller-plan.md). It authorizes no
deployment, provider mutation, or credential use.

## Parallel work that can start now

| Workstream | Scope | Owner seam | Exit artifact | Status |
|---|---|---|---|---|
| Evidence ledger | Reconcile Mini completion, DO soak, current health, Genesis readback, and release receipts against source and dated docs. | Norn and norn-fleet docs plus retained run IDs. | Independent Mini currently serves signed V3 RC `d4dc88e0c37003a1013caed444983b2eeb50fbcb`; smoke and host doctor passed. Route assurance is stale/failed, with 7 queued Fleet-dispatch operations and 25 open incident groups still requiring separate follow-up. A route-authentication fix is proposed in open PR #301; its API/CLI/UI/OpenAPI and related CI checks pass, but it has not been merged, deployed to Mini, or verified with live `norn host assure` and route inventory. Live DO provider inventory was read through explicit `work` context, but private API/runtime health and protected Genesis source availability remain unverified. | Source audit refreshed; Mini app/route health is not fully clean; DO provider inventory refreshed |
| Bootstrap input audit | Trace state, route, inventory, vars, provider scope, SSH, signed intent, runtime identity, and key flows; define the future two-app-pool input contract. | `norn-fleet` validators, workflows, Ansible, templates; [two-pool contract](fresh-fleet-two-app-pool-contract.md). | Field-level input manifest and existing-Fleet app-03 scale lost-response round trip added; a 51-pattern path inventory is checked against Ansible docs; future pool roles selected as general + stateful; IDs, sizes, count bounds, and placement remain undecided; per-file lifecycle evidence and complete fresh-Fleet workflow recovery remain. | Source audit complete; fitness partial |
| Controller and V3 API audit | Reconcile Mini direct controller, DO platform release, workload resize, online proposal, and V2 surfaces. | Norn controllers/API/tests and V3 docs. | Mini and scale test entry points mapped; Mini bounded model is in the evidence bundle; V2 semantics clarified. | Source audit complete |
| Offline models | Check existing DO platform release, Mini direct, existing-Fleet Genesis, disposable Fleet create/recovery, current staging source, future two-app-pool create/recovery, and proposed workload overlap. | TLA+ models and deterministic controller tests. | Protected `norn-fleet` main and provider inventory agree on the current 3+2+4 topology, with one app pool. The nine-node model covers that shape abstractly; the 45-state future two-pool model fixes General + Stateful roles and gates success on role-correct readiness plus stateful recovery proof. Neither is wired to the protected provider workflow or its detailed credential inputs; current staging recovery still lacks a joined code-level replay. | Nine bounded models check; topology shape reconciled; input/recovery failure coverage partial |
| Workload release protocol | Separate replica scaling, application rollout, and app-pool resize; select stateless then stateful qualification candidates. | Norn app-scale and workload catalog; [M5 protocol](m5-workload-release-protocol-2026-11-07.md). | Sequence names `norn-fleet-canary` then `hello-norn-mysql-managed`; stateless intent pins both generations' job/source/spec/image identities, public traffic-probe path/body digest, and weighted TLS routes. Receipt adapters provide claim-fenced CAS; etcd lease-expiry recovery requeues the same release. The stop executor requires exact target-health and mixed-route proof. | Offline contract, durable receipt CAS, refreshable target-health step, old-stop executor and fresh drain readback, prerequisite guard, scale-target reader, route-step intent/authority, independent route/traffic proof, atomic active-route/receipt promotion, and claim-bound publish/prove/commit entry point are implemented. A signed `app.workload-release` worker now verifies acceptance under the app lock and reconciles health → weighted route steps → durable old-stop/readback → final route → terminal receipt; deploy and workload route writers share a serialized executor. Tests cover total-health hold, stable stop marker across claim renewal, lost-response stop recovery, exact drain receipt, final-route receipt, terminal completion, and same-step route replay after lost response. A driver-level fixture now loses the response after receipt initialization, health readback, each route step, stop, drain receipt, and completion, then re-enters the same reconciler to a single-stop terminal receipt. A production-store worker fixture, process/lease crash matrix, and live qualification remain open. |

These workstreams are independent until their findings need to merge into the
lane manifest. The immediate parallel wave is M1/M2 offline contract and
recovery work, workload protocol refinement, and V2 caller migration planning.
M3 is a separate read-only preparation lane. M4 and live M5 are serialized
behind M3 and must not run concurrently against the DO testbed. Keep edits in
separate plan/evidence files unless a shared implementation change is
coordinated.

The Mini direct controller has behavioral tests but no Norn-repository TLA+
model; its bounded model is retained in the statechart bundle. The existing
Fleet release statechart does not model Genesis or cloud provisioning. The
Protected `norn-fleet` main at
`4451767624b12cbc41cc32e160a10d4b46006e0f` declares 3 control + 2 ingress + 4
app Droplets, and explicit-context provider inventory agrees. The older local
`norn-fleet` checkout at `cc2be083a00d6afd3964655519dc2abb580150b6` declared
only control and ingress; the [topology audit](staging-topology-source-audit-2026-11-08.md)
records why that five-node view is stale. Current staging has one app pool;
the separate future fresh-Fleet contract is two pools, General + Stateful.
[norn-fleet PR #941](https://github.com/antiartificial/norn-fleet/pull/941)
contains the credential-free nine-node input slice and an eight-of-nine
partial-output hold. Its documented command passes 81 tests locally, and
protected CI run `42477659325` passed. The PR remains candidate
evidence, not merged coverage. It does not constitute a joined persistent
provider apply/recovery fixture.

On 2026-11-08, the current local `norn-fleet` checkout passed 15 targeted
disposable input/checkpoint tests and its full Python suite: 1,830 tests passed
with four skips. Those tests verify disposable topology and helper contracts;
they do not execute protected provider workflows or qualify persistent staging
recovery. The five-node disposable-profile adoption test joins database
adoption to provider-state verification and a terminal receipt, but it is not
the persistent staging lane. The app-03 production-handler fake-provider test
proves same-intent recovery of that exact existing-Fleet scale effect only.

The full current staging bootstrap sequence remains unjoined across reviewed
plan, provider apply, fresh state/provider identity, same-intent recovery,
inventory/PKI, bootstrap, and terminal receipt. Per-service credential
provenance and lifecycle evidence and DO-token permission-scope admission also
remain open. Persistent work-staging has no sanctioned whole-Fleet destroy.
Genesis installs a dormant controller on an existing Fleet; it does not
provision provider resources. V2 cleanup still distinguishes older unversioned
handlers, the `v2/api` Go package path, and the separate `/effect-grants/v2`
protocol. Do not delete code or tests based on names alone.

The documented fresh-Fleet war-game command was run against `norn-fleet` on
2026-11-08: **81 tests passed** locally in 31 seconds. The command lists
checkpoint coverage explicitly, and the recovery and adoption tests import
fixture modules instead of test classes. PR #941 carries that war-game;
its protected validation run `42477659325` passed. A separate PR,
[norn-fleet PR #942](https://github.com/antiartificial/norn-fleet/pull/942),
changes workflow collection to classes defined by each test module. A local
comparison preserves all 4,236 distinct test IDs and removes 248 repeated
executions. Its plan/reconciliation and executor suites passed in CI; the
first full run failed later when the DigitalOcean provider checksum request
timed out. Full validation rerun `42477537463` passed. The separate
disposable-retirement suite's 84-test run remains unverified. These checks do not execute GitHub
Actions or prove protected runner, environment, artifact, provider, or teardown
behavior.

## Ordered gates

### M0. Freeze the present-tense evidence

- [x] Reconcile dated Mini and DO claims. Preserve earlier failures as history.
- [x] Correct stale statements, including the post-merge Genesis readback status
  and the conflict between the completed soak record and older “no soak” text.
- [x] Set the DO operational status from fresh read-only evidence or mark it
  held with exact failing probes.

**Exit:** one current evidence ledger; no health claim relies only on an old
soak, retained provider state, or a merge.

### M1. Close lane and input contracts

- [x] Keep Mini direct, existing DO platform release, fresh DO bootstrap,
  workload resize, and future online Norn release separate.
- [ ] List every supplied and generated input, its producer, transformations,
  freshness, exact consumer, scope, lifecycle, and recovery semantics.
- [ ] Define one stable intent and receipt schema per lane.

**Exit:** every workflow input is accounted for and unknown inputs hold before
effects. Fresh-Fleet provider creation is not conflated with Genesis.

### M2. Build deterministic offline fitness

- [ ] Review and merge the credential-free `staging/nyc3` input vertical slice
  in `norn-fleet` PR #941. It calls production
  plan-manifest, account/backend, topology, inventory, recovery-guard, and
  local PKI functions with synthetic inputs; substitution and eight-of-nine
  output hold before bootstrap. Its documented 81-test command passes
  locally, and protected CI run `42477659325` passed. This helper-level slice stops
  before provider effects, SSH,
  Ansible, durable staging recovery, or terminal receipt. Protected helper
  tests separately cover controller-install pins and three-control SSH.
- [x] Exercise production durable `converge`/`recover` handlers after a lost
  response for the existing Fleet's exact app-03 scale effect.
- [ ] Join persistent staging provider apply/recovery handlers, durable
  same-intent receipt, exact state/provider identity, and post-recovery
  bootstrap into one synthetic workflow. Bind it to the current nine-node
  single-app-pool source; keep the future two-pool contract separate.
- [x] Exercise the separate disposable-root saved-plan retirement and
  final-zero recovery contracts; these tests do not authorize teardown of
  persistent work-staging and the lane is not live-qualified.
- [x] Exercise synthetic secret materialization, private-file modes,
  grant-scoped cleanup/rotation, and secret-safe preflight receipts.
- [x] Join mirror, scoped grant use, grant cleanup after interruption, retry,
  and secret-safe preflight receipt in one synthetic credential-lifecycle
  fixture. Service-specific token rotation/revocation and per-file lifecycle
  evidence remain open.
- [ ] Add synthetic valid fixtures for all other lane validators and
  serializers without production credentials.
- [ ] Cover all-lane absent, malformed, stale, mismatched, substituted, replayed,
  expired, unsafe-mode, symlink, partial, crash, and lost-response cases.
- [x] Model Mini direct writer fencing, existing-Fleet Genesis activation,
  existing-Fleet all-stop release, and proposed workload mixed versions in
  separate bounded statecharts.
- [x] Model fresh Fleet create/partial-retire from the existing provider
  workflow inputs and effects. The Genesis model explicitly blocks that
  request and performs no provider effect.
- [x] Model a 3+2+4 fresh-Fleet create shape, lost response, exact
  state/provider readback, same-intent forward recovery, and full-inventory
  prerequisite for PKI/configuration. Pool counts now match current
  `staging/nyc3`, but the model is not wired to its parser, workflow, or exact
  inputs; token-scope admission is modeled but not implemented.
- [x] Model future fresh-Fleet bootstrap with two independent app pools,
  lost-create-response readback, same-intent recovery, full-inventory gating,
  and terminal receipt. Pool identities are abstract and no counts or capacity bounds are
  adopted; see [the two-pool model](statecharts/fresh-fleet-two-app-pools/README.md)
  and [input contract](fresh-fleet-two-app-pool-contract.md).
- [ ] Model a future online Norn upgrade after its compatibility and fencing
  contract is reviewed.
- [ ] Replay every counterexample against controller tests. Record model
  omissions and the live proof needed for each.

**Exit:** each lane has a synthetic success case, a failure matrix, invariant
results, and secret-safe output checks. No provider or SSH call occurs.

Nine bounded statecharts and their specs, configs, views, reports, and TLC
output are checked into [the statechart bundle](statecharts/README.md). The
workload chart models the offline protocol; its active-route generation and
receipt successor now advance atomically, while the runtime controller is not
yet wired to it. The stateless intent/receipt contract is implemented and the
Nomad allocation readback now binds through a validated receipt projection.
The chart models route-proof-loss hold and fresh same-generation route confirmation; fallback
to an older generation requires a separate intent. It also models loss of one
canary allocation while the mixed route is serving; the resulting hold requires
fresh current-route confirmation before same-intent recovery. The workload TLC
run has 102 distinct states / 279 steps and no invariant violations. The new
persistent-staging model passed ten invariants and the deadlock check across
102 states / 210 transitions; its reader-facing chart has 17 states / 35 arrows.
The workload route-publication model checks per-node partial publication,
same-generation recovery, inventory and health holds, independent traffic
proof, atomic active-route generation and receipt commit across 55 states and
111 transitions; eight invariants and deadlock freedom pass. These chart passes
do not prove the live controller implements the contract, finish per-file
credential lifecycle evidence, or qualify a live workload release.

Fresh-Fleet input inventory from the source audit:

| Domain | Existing producer/check | Remaining offline work |
|---|---|---|
| Provider and state | Operator supplies DO token and Spaces credentials. Apply rechecks active account UUID and exact state bucket/key/region/endpoint/versioning; DO token scope is not checked. | Define and review required per-operation scopes, bind nonsecret owner-approved scope evidence to setup/rotation, and add policy tests. Never include token values in receipts. |
| Plan authority | Protected plan run/artifact digest, Norn capacity plan UUID, one-use nonce, destructive acknowledgement; apply recreates plan and checks it before provider effects. | Exercise expiry, replay, changed artifact, stale state, lost apply response, and exact plan binding. |
| Network/SSH | Registered SSH fingerprint, protected runner private-key path, admin `/32`s, node host keys from first boot. | Synthetic absent/substituted key and firewall inputs; host-key freshness remains live read-only proof. |
| Current staging topology | Protected `norn-fleet` main declares 3 control + 2 ingress + 4 app Droplets (9 total), one app pool labeled `workload: app`. Provider inventory through explicit `work` context matches counts, tags, and sizes. Managed PostgreSQL and MySQL are separate database services. | Fresh protected state, node enrollment, control-plane, workload placement, and route health remain unverified. The nine-node model matches the count shape but does not prove workflow implementation or exact state recovery. |
| Future fresh-Fleet application pools | User selected two app pools for future new Fleets. Current DO staging remains one app pool and will not be migrated as part of this contract. | [Contract fields and recovery requirements](fresh-fleet-two-app-pool-contract.md) are recorded. Roles are general + stateful; stable IDs, sizes, min/desired/max, cost, placement, and workload/recovery semantics remain open before changing provider source. |
| Node/bootstrap identity | Protected runner OIDC and writer fence; reconciliation derives inventory from provider outputs and invokes Ansible. PKI is generated per cluster after create. | `norn-fleet` PR #941 joins synthetic provider outputs→inventory→local PKI and checks exact count, restricted modes, secret-free evidence, and repeat digests. It stops before SSH/Ansible; protected interrupted/repeated reconciliation remains open. |
| Service credentials | Many initial ACL, gossip, etcd, Norn, observability, registry, TLS, and optional Tailscale values are supplied through protected owner files. Nomad creates the scoped Norn identity; the initial Norn etcd token expires after 72h. | Draft `norn-fleet` PR #943 maps all 51 bootstrap paths across PKI, fixed Consul ACL, gossip, management authority, etcd identity, core Norn API/authority/signing inputs, ingress TLS, and conditional canary/registry/Grafana credential flows; it also records three controller-only CA keys and the generated Nomad Norn identity. A signed exact-accessor ensure/revoke path exists for the metrics token. Many fixed/service credentials still lack complete rotation/revocation procedures, and DigitalOcean token scope plus provider-side revocation remain unverified. |

The helper-level war-game in `norn-fleet` PR #941 covers plan binding, provider/backend
checks, topology/inventory/PKI transformations, partial-inventory hold, local
PKI generation/idempotency, and secret redaction. A separate production-handler
fake-provider round trip covers lost response, same-intent recovery, and
terminal receipt for the existing Fleet's exact app-03 scale effect; it is not
fresh-Fleet creation. Protected helper tests additionally cover exact control
inventory/SSH continuity and read-only controller-install admission against
pinned inputs. The war-game does not execute protected apply, durable
persistent-Fleet recovery, Ansible, or terminal receipt. Separate component tests cover owner-only secret materialization,
rotation/revoke and preflight receipt redaction. The documented disposable
retirement suites are also reported as passing 84 tests on 2026-11-08, but
their run chronology and source record remain unverified. They cover exact
saved-plan retirement and recovery only for
disposable roots. A complete synthetic replay
of the protected workflow for the adopted staging topology remains outside the harness. The
credential fixture joins executor mirror/grant/retry behavior but does not
prove each service's own rotation/revocation procedure. Close those gaps before
creating another billable Fleet. Do not add a destroy path to persistent
work-staging to satisfy this gate.

CI run [42470557489](https://github.com/antiartificial/norn/actions/runs/42470557489)
passed the pinned etcd 3.5.17 TLS/RBAC qualification with isolated-member
partition/rejoin, member loss, quorum refusal, authenticated snapshot,
corruption rejection, restore, and Fleet conformance suites. This validates the
disposable control-store test harness; it does not close the provider,
credential-provenance, or integrated Fleet bootstrap gaps above.

### M3. Re-establish DO readiness without writes

- [ ] Prove pinned management route and protected runner availability.
- [ ] Read current state and exact three-control identities, API health,
  capabilities, etcd quorum/alarms, storage headroom, and host keys.
- [ ] Replace the Genesis readback workflow's Oct 21 state and route pins with
  reviewed fresh provider-state, route, and host-key receipts.
- [ ] Resolve the unclassified etcd health failure and prior API timeouts with
  secret-safe diagnostics.
- [ ] Run fresh Genesis readback with a one-use intent only after its exact
  source fix and runner preflight are verified.
- [ ] Before another readback, prove that both the pinned capability commit and
  signed target commit exist in the management host's protected Norn repository.

**Exit:** all identities and services have fresh read-only receipts. Otherwise
publish a specific hold and keep all write workflows disabled.

Current blockers: the local `doctl` default `norn-pilot` context belongs to a
different account from work-staging. A separate, read-only query through
explicit context `work` on 2026-11-08 09:36 UTC confirmed nine active NYC3
Droplets (3 control, 2 ingress, 4 app), the active ingress load balancer, and
online managed PostgreSQL/MySQL resources; those provider resources match
`norn-fleet` staging source `4451767624b12cbc41cc32e160a10d4b46006e0f`.
This confirms provider inventory/status metadata only, not Nomad/Consul, node
enrollment, workload placement, or Norn/API/route health. The post-fix
protected readback `42418784411` passed its pinned input checks and private
transport, then failed because the protected management source repository
lacks the capability commit or signed target commit. The exact missing object
is unknown. That run used Oct 21 state and route files, so fresh protected
readback and service health remain unconfirmed. A new readback needs reviewed
fresh state/route/host-key evidence and a fresh signed one-use intent after the
source-object issue is diagnosed.

The local Norn repository contains both pinned source commits (`86ad17b` and
`9e2e33`) and confirms the capability commit is an ancestor of the signed
target. The current target tree contains both the advertised
`fleet-platform-release-intent-v1` capability and the Genesis import verifier.
This narrows the question to protected management-host object availability or
repository selection; it does not prove either object exists in that bare
repository. Keep the host-side diagnosis read-only.

The local Fleet controller now has separate synthetic tests for either missing
commit and reports the exact missing object. This is not yet merged or
installed, and no source objects have been added to the protected host.

On 2026-11-08, a fresh local `doctl account get` check matched the default
account prefix (`05fe610a…`) and did not match the pinned work account prefix
(`17f40050…`). The read-only provider inventory above used explicit context
`work`, avoiding that default mismatch. An older retained note's 02:59 UTC
timestamp still lacks a reconciled source record. M3 remains held pending
fresh protected state/route/host-key receipts and source-object diagnosis.
Current DO control-plane and application health are therefore unverified.

### M4. Qualify the existing DO platform controller

- [ ] Re-run local controller and offline crash/replay fitness at the exact
  reviewed commit.
- [ ] Use the existing all-stop, forward-only Norn release lane on DO only
  after M3 passes.
- [ ] Independently verify every control, singleton API, etcd, capabilities,
  durable pins, terminal receipt, and one-use runner retirement.
- [ ] Prove same-target recovery and prove unknown bytes/host identity hold.

**Exit:** protected release and independent postflight receipts agree. Do not
claim mixed Norn control versions.

### M5. Qualify workload resize and application overlap

- [x] Separate replica scaling, application-version rollout, and app-pool
  resizing; select the stateless canary and stateful follow-up from the actual
  workload catalog. See the [M5 workload protocol](m5-workload-release-protocol-2026-11-07.md).
- [ ] Keep Norn fixed. Choose one stateless web and one worker or stateful
  workload with explicit data and writer contracts.
- [ ] Test replica resize separately from node-pool resize.
- [ ] Exercise canary, mixed application versions, drain, route failure, node
  loss, and rollback boundary under synthetic load first.
- [ ] Move the same declared workload class to DO only after M3 and M4 pass.

**Exit:** data, routes, acknowledgements, and desired/observed capacity remain
correct through restart, drain, failure, and retry.

### M6. Decide on online Norn platform upgrades

- [ ] Keep all-stop as default while writing a compatibility and writer-epoch
  matrix for any online candidate.
- [ ] Model API ownership, etcd quorum, mixed readers/writers, lease/fence,
  partitions, controller loss, and post-write recovery.
- [ ] Qualify the protocol on DO with measured API outage, write pause, and
  workload interruption.

**Exit:** enable an online lane only if the model and live fault exercise pass;
otherwise retain the all-stop contract.

### M7. Remove V2 runtime surfaces after adoption

- [x] Inventory older unversioned handlers, adapters, flags, docs, clients, and
  tests. The [surface-retirement report](v2-surface-retirement-inventory-2026-11-07.md)
  confirms `v2/api` and `v2/cli` are current V3 build module paths, some CLI/UI
  callers still use unversioned `/api` routes, and a protected `norn-fleet`
  runner still calls `/effect-grants/v2`.
- [x] Move the CLI and UI app-list reads from `/api/apps` to the documented
  `/api/v1/apps` route. Both routes currently share the same handler; retain
  the legacy alias and compatibility tests while external usage and remaining
  `/api/apps/{id}/...` callers are unresolved.
- [x] Move operation history and platform release-history reads to their
  existing V1 routes in the CLI and UI. Preserve the unversioned aliases and
  compatibility behavior; platform rollback remains unversioned until its
  different V1 mutation contract is migrated explicitly.
- [x] Move the CLI app-detail and snapshot-inventory reads to the equivalent
  scope-checked V1 routes; retain snapshot restore/retention aliases pending
  explicit parity for named-database selection and durable-operation inputs.
- [ ] Move callers to V3 before deleting V2-only code and low-value tests.
- [ ] Retain only the compatibility fixtures and migration code needed to
  upgrade the supported V2 baseline safely.

**Exit:** all supported callers use replacement contracts, removed surfaces
have no callers, and V2-to-V3 migration and recovery fixtures remain and pass.

## Dependencies and concurrency

The initial source/evidence audits are complete. These work items can proceed
in parallel now; their dependencies identify where later integration or live
work must wait:

| Work item | Can run alongside | Dependency / hold |
|---|---|---|
| Fresh-Fleet topology and integrated replay | Five-node disposable profile and credential fixture | Current DO source/provider topology is 3+2+4 with one app pool. The 3+2+4 model captures that shape but not the workflow implementation; join protected apply/recovery, exact state/provider identities, and post-recovery bootstrap. Keep the future two-pool General + Stateful default separate. |
| Credential lifecycle follow-up | Fresh-Fleet integrated replay | Joined executor fixture covers mirror, scoped grant, interruption cleanup, retry, and redaction; reconcile exact per-service rotation/expiry/revocation and path-level owner/mode evidence. |
| M3 read-only input preparation | All offline M1/M2 work | The default `norn-pilot` doctl context is the wrong account; explicit `work` context re-confirmed all nine tagged droplets active and three Norn-related managed databases online at 12:41 UTC Nov 8. Protected state identity, route, and host-key pins remain stale, and the private API health readback is not current. No new intent until those source objects and route inputs are refreshed and reviewed. |
| M5 workload contract and model replay | M2 harness and workload protocol | Offline compatibility work can continue; live qualification waits for M3 and M4 and must serialize on the DO testbed. |
| V2 caller migration plan | Offline controller and docs work | Map each caller to its replacement contract; retain fixtures until supported V2 baseline migration and recovery are covered. |

M4 release and M5 workload live qualification share the DO testbed and must
not overlap. M6 depends on M4 and a stable workload/control compatibility
matrix. V2 removal follows Mini/DO adoption gates. Any provider create/apply,
Fleet release, or workload mutation must serialize against one reviewed state
snapshot, immutable intent, and exact plan. Offline tasks can continue without
consuming protected runs.

## Program completion

Close the program only when every supported lane has a complete input contract,
offline success and failure coverage, live readiness evidence before effects,
an end-to-end controller receipt, independent postflight, and a tested recovery
path. Each unresolved limitation must be named in its runbook. The operator
should make one decision at a `READY`/`BLOCKED` boundary and one explicit
approval for irreversible data or provider changes, while the controller owns
staging, dispatch, reconciliation, and same-intent recovery.
