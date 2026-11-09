# V3 release program milestones

Status: active work map, refreshed 2026-11-09. This checklist orders the [release fitness
and controller plan](release-fitness-and-controller-plan.md). It authorizes no
deployment, provider mutation, or credential use.

## Reuse release lessons before repeating work

Keep this milestone map and the operator runbooks procedural. Consult the
[launch state](LAUNCH-STATE.md) for current gates and
[launch history](LAUNCH-HISTORY.md) for dated observations, the
[Mini upgrade observation](m6-mini-v3-forward-upgrade-observation-2026-11-07.md)
for the direct-lane recovery sequence, the
[upgrade lessons](online-upgrade-lessons-2026-11-07.md) for platform-wide
capability gaps, and the
[M5 workload protocol](m5-workload-release-protocol-2026-11-07.md) before
repeating workload changes. Before a rerun, compare the prior attempt's exact
source, intent, state and resource identities, terminal evidence, and stated
redo conditions with current inputs. A historical pass is not current runtime
evidence; a timeout or empty readback is not proof that an external effect did
not happen.

## Parallel workstreams

| Workstream | Current evidence | Next proof |
|---|---|---|
| Release evidence | Mini serves the signed V3 RC; fresh route assurance is open. DO provider inventory is active, but runtime health is unverified. | Use [launch state](LAUNCH-STATE.md) for current receipts and [launch history](LAUNCH-HISTORY.md) before repeating a read. |
| Existing DO controller | PR #946 merged the pre-effect source-object admission fix. Exact-head PR #944 and merged-main contract validation passed. Staging plan run 42555418513 proposes one private ingress firewall and no other changes; it was not applied. | Review the plan effect, then install reviewed source with timers disabled and verify it before fresh Genesis readback. |
| Fresh-Fleet bootstrap | PRs #941, #943, and #945 provide offline topology/input, credential-provenance, and provider-scope work. Future app pools are General + Stateful. | Join persistent provider apply, exact state/provider readback, same-intent recovery, PKI/bootstrap, and terminal receipt; complete provider scope and credential lifecycle gates. |
| Offline models | Nine checked-in lane models and the additional workload capacity-3 configuration pass their listed invariants and deadlock checks. | Replay counterexamples against controller tests and prove each modeled boundary against implementation; see the [statechart index](statecharts/README.md). |
| Workload release | PR #302 contains offline stateless mixed-version controller/statechart evidence; it is not live-qualified. | Add operator admission, durable worker crash/restart proof, target-only public-route evidence, a live canary, and stateful data-compatibility proof. |

Mini is an independent node, not a Fleet member. The DO staging environment is
the only Fleet in this release work. Provider inventory, controller health,
workload health, route assurance, and release publication are separate claims.
The full dated evidence stays out of this milestone map; consult the linked
history and lane-specific records before repeating work.

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
fresh current-route confirmation before same-intent recovery. The workload
model now checks both capacity 3 (24 states / 52 steps) and capacity 4 (103
states / 275 steps). All listed invariants and deadlock checks pass in both,
but capacity 3 cannot reach target-only routing or terminal completion while
the old cohort is retained. Capacity 4 reaches the terminal receipt. This is a
progress requirement: live preflight must prove full target-cohort overlap
capacity under actual Nomad placement and resource constraints. The new
persistent-staging model passed ten invariants and the deadlock check across
102 states / 210 transitions; its reader-facing chart has 17 states / 35 arrows.
The workload route-publication model checks per-node partial publication,
same-generation recovery, inventory and health holds, independent traffic
proof, atomic active-route generation and receipt commit across 55 states and
111 transitions; eight invariants and deadlock freedom pass. These chart passes
do not prove the live controller implements the contract, finish per-file
credential lifecycle evidence, or qualify a live workload release.

Input provenance and pool semantics are maintained in the
[future two-app-pool contract](fresh-fleet-two-app-pool-contract.md) and the
open Fleet PRs #941, #943, and #945. Keep the full path inventory in its
source-derived contract rather than duplicating it here. The remaining offline
work is an integrated persistent-staging apply/recovery replay, per-service
rotation/revocation coverage, and missing/stale input holds. PR #945 cannot
attest the live DO token's scopes; obtain a current owner-reviewed scope
readback before any protected apply. Persistent `work-staging` has no
sanctioned whole-Fleet destroy path.

The helper war-game stops before provider effects, SSH, Ansible, durable
persistent-Fleet recovery, and terminal receipt. The separate exact app-03
scale replay and disposable-root retirement tests do not fill those gaps. CI
run [42470557489](https://github.com/antiartificial/norn/actions/runs/42470557489)
qualified the disposable etcd TLS/RBAC harness only. None of these results
qualifies a billable fresh-Fleet deployment.

### M3. Re-establish DO readiness without writes

- [ ] Recheck the exact management runner, route, provider state, and three
  control identities with fresh protected receipts. The latest provider-only
  inventory does not establish Norn/API, Nomad/Consul, workload, or route health.
- [ ] Diagnose source availability on the management host without mutation.
  Fleet PR #946 merged the pre-effect missing-source check, but the fix has not
  been installed there. Merged-main contract validation
  [42555418496](https://github.com/antiartificial/norn-fleet/actions/runs/42555418496)
  passed; install the exact reviewed source only after separately reviewing the
  provider-plan effect below.
- [ ] Review the staging plan from run
  [42555418513](https://github.com/antiartificial/norn-fleet/actions/runs/42555418513):
  it proposes `management_ingress_runtime[0]` (ports 18082/18083 from the
  management runner's private `/32` to ingress nodes), with 1 add, 0 change,
  and 0 destroy. A read-only provider list at 03:24 UTC found no firewall with
  the proposed `tk-staging-nyc3-management-ingress-runtime` name. Confirm the
  intended scope before a separately authorized apply; the plan run performed
  no apply.
- [ ] Install reviewed source with controller services and timers disabled.
  Verify installed source, private inputs, digests, and dormant units before
  preparing any fresh readback.
- [ ] Refresh state, route, and host-key evidence, then request a new signed
  one-use Genesis readback intent only after every preflight passes. The failed
  intent and runner authorization cannot be reused; both protected dispatch
  gates were absent at 03:06 UTC.

**Exit:** all identities and services have fresh read-only receipts. Otherwise
publish the exact hold and keep write workflows disabled. See the
[DO controller guide](https://github.com/antiartificial/norn-fleet/pull/944)
for the procedural install/readback sequence and [launch history](LAUNCH-HISTORY.md)
for the failed attempt details.

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
| M3 read-only input preparation | All offline M1/M2 work | Explicit `work` context confirmed provider resources at 02:48 UTC Nov 9; main plan run 42555418513 completed read-only with one ingress firewall addition. Runtime health, state identity, route, and host-key receipts remain unverified. No new intent until those inputs are refreshed and reviewed. |
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
