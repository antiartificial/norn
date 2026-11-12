# V3 release program milestones

Status: refreshed 2026-11-12 04:39 UTC. The existing DO staging Fleet's V3
platform release is deployed through the controller (2026-11-11). The latest
provider/runtime observation recorded here remains 04:02 UTC; this refresh
adds the merged held-operation recovery model and does not claim a new live
probe. The first controller-admitted workload is not proven; its date is
uncommitted and has no calendar forecast. Do not present the platform
deployment date as the workload or overall V3 launch date. This map orders
the [release fitness and controller plan](release-fitness-and-controller-plan.md);
live actions remain subject to protected workflow, intent, and state gates.

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

## Current gate snapshot (live evidence last observed 2026-11-12 04:02 UTC)

- **DO platform:** signed release `fa301fa5f881b847a26f61a501092f9a9dd090fd`
  is running after the controller-mediated all-stop upgrade. Postflight proved
  one healthy private API backed by etcd and signed API/UI pins on all three
  controls. Promotion interrupted service; online upgrade is not qualified.
- **DO staging observation:** all nine tagged Fleet Droplets were active at
  04:02 UTC. Public `/version` returned HTTP 200 in 0.231 seconds with
  allocation `c776c80f-6ba8-0992-a552-bfc21c441f8a` and version
  `af0f054358d9b9c7b68ea25132b52ee5eb099e12`. The public-pilot verifier passed
  12/12 reads at 02:40:57 UTC and observed two distinct allocation IDs. This
  proves the existing public MySQL pilot responds through its route; it does
  not establish allocation-to-current-host binding, controller admission, or
  continuous health.
- **First workload:** a 04:02 name-only audit found all 18 readback-specific
  environment binding names missing from GitHub Actions repository-level and
  `work-staging` configuration; no values were read. No protected readback ran,
  and no controller-admitted canary has been
  observed. The next gates
  are reviewed protected inputs and credential provenance, signed read-only
  workload/ACL/network readback, terminal-artifact replay after controller
  restart, full-executor recovery through real Nomad/ingress, admission review,
  and a responding stateless canary with durable same-intent receipts. See
  [current launch state](LAUNCH-STATE.md) for evidence and
  [launch history](LAUNCH-HISTORY.md) for dated records.
- **Managed MySQL candidate:** its InfraSpec declares
  `norn-fleet-mysql.tkmkt-dev.com`; protected catalog promotion
  ([42312866244](https://github.com/antiartificial/norn-fleet/actions/runs/42312866244))
  and three-control readback
  ([42341905690](https://github.com/antiartificial/norn-fleet/actions/runs/42341905690))
  succeeded, and DNS resolves to `159.89.243.220`. A 03:41 TLS probe
  returns the Traefik default certificate. The managed release toggle is
  `false`, with no protected release dispatch or Norn-owned A/B deployment.
  This stateful candidate is a separate follow-up and does not qualify the
  first stateless controller-admitted workload.
- **Mini:** independent direct lane; its owner-defined success bar is met
  (signed V3 active, API healthy, and `vigil-gateway` responded). Mixed health
  and queued-upgrade recovery remain separate follow-ups. Mini is not a Fleet
  member and has no Fleet resize lane.
- **Follow-on Fleet work:** workload resize/mixed-version rollout and online
  platform upgrade remain open. Fresh-Fleet Genesis/bootstrap is a separate
  lane. None has a committed launch date.

- **Offline recovery modeling:** merged PR #433 adds a bounded M5 model for
  abandoning a permanently held workload operation, retaining its private
  inputs until a terminal abandonment receipt, and retiring decryption keys
  only when no retained operation depends on them. The baseline and three
  unsafe mutants were checked; this is policy/model evidence, not an
  implemented controller path or live recovery qualification.

The `work` DigitalOcean context is TurnKey Marketing; `norn-pilot` is personal.
The DO staging Fleet is the only Fleet in this release program. Provider power,
API health, workload response, route assurance, and release publication are
separate claims.

## Parallel workstreams

| Workstream | Current evidence | Next proof |
|---|---|---|
| Protected platform release | Source `fa301fa5` passed all 12 qualification jobs in run [42963040596](https://github.com/antiartificial/norn/actions/runs/42963040596); protected run [42963531936](https://github.com/antiartificial/norn/actions/runs/42963531936) published the immutable signed release with UI and four OS/architecture bundles. | Keep exact-SHA qualification and publication separate from controller deployment for each release. |
| Mini direct | Successful under the owner-stated bar: signed `34178b8b` is active at schema 52, API health returned HTTP 200, and the running Norn-managed `vigil-gateway` allocation returned HTTP 200 from `/health`. Other app health remains mixed. | Keep queued-upgrade recovery and Git-driven workload admission as separate follow-ups. Do not include Fleet workload resizing in the Mini lane. |
| Existing DO Fleet | Signed source `fa301fa5` is deployed through the controller. Postflight verified one healthy private API with etcd backend and signed API/UI pins on all three controls. At 04:02 UTC Nov 12, nine tagged Fleet Droplets were active and public `/version` returned HTTP 200 in 0.231 seconds with allocation `c776c80f-6ba8-0992-a552-bfc21c441f8a`. The public-pilot verifier passed 12/12 reads at 02:40:57 UTC and observed two allocation IDs; this proves public response, not controller admission or app-host binding. A 04:02 name-only audit found all 18 readback-specific environment binding names missing from GitHub Actions repository-level and `work-staging` configuration; no values were read. No protected readback or controller-admitted canary has run. | Review and bind protected readback inputs and credential provenance; run signed workload/ACL/network readback; prove terminal-artifact replay and full-executor recovery; review admission and launch a responding stateless canary. Then qualify mixed-version resize and online upgrade. Fresh-Fleet Genesis/bootstrap remains separate. |
| Fresh-Fleet bootstrap | General and Stateful app-pool roles are supported by topology validation, generated inventory, and bootstrap. No two-pool profile, sizes, capacity bounds, or cost envelope is selected; current staging remains 3+2+4 with one app pool. | Complete synthetic parser/serializer and persistent apply/recovery coverage; settle SKU, region, capacity, cost, placement, and stateful recovery before proposing a profile. |
| DO workload resize/release | The authenticated staging-only producer is opt-in and not deployed. PR #351 pins the exact job-driving app spec and encrypted SOPS snapshot; completed releases remove encrypted inputs with a receipt-bound tombstone, while pending or held releases retain them. PR #419 adds a real-etcd composition test spanning durable stage reservation, one-use create authorization, delayed target binding, successor claim, and route-loss recovery through terminal receipt across two etcd client restarts; its Nomad and route effects are simulated. Merged PR [#421](https://github.com/antiartificial/norn/pull/421) (commit `b357f6a`) adds a disposable loopback qualification that drives the production target-stage helper against real Nomad, drops the successful create response, hides the first readback, then proves the successor binds Nomad's actual initial job version 0 and nonzero modify index without a second registration. It fixes missing deployment/spec provenance metadata in the target builder. Exact-head CI passed, including API Go tests, Fleet pilot workload, and three-node durable replica qualification. Full private-input/executor recovery, Nomad health and ingress qualification, fresh signed HTTP admission, and DO workload admission/resize remain open. Mixed-version models pass at capacities 3 and 4; capacity 3 holds, and capacity 4 reaches the modeled terminal receipt. | Join HTTP admission to real etcd, signed qualification, completed staging deployment, and control-owned active-route lineage. Extend disposable driver qualification through private-input open/stage, target health, route, and old-cohort stop; then qualify a database-free canary and mixed-version resize on DO under controller gates. Keep stateful compatibility separate. This resize lane applies to the DO staging Fleet, not the Mini. |
| Offline state models | All 30 checked-in configurations were rerun on Nov 10 with each config mapped to its owning TLA spec. Twenty-eight passed; the two intentionally buggy Mini descendant configs each reproduced the expected three-transition later-upgrade hold. The corrected Mini config passed five invariants and deadlock. Models cover Mini, DO release/readback, fresh Fleet, online upgrade, admission, routing, recovery, and resize. | Replay implementation-facing boundaries against controller tests. Statechart results do not prove runtime refinement or service health. |

Mini is an independent node, not a Fleet member. DO staging is the only Fleet
in this release program. Replica and app-pool resizing belong to the DO
staging Fleet. Mini workload admission is a separate Git-to-Norn path; mixed
health and the unqualified webhook path do not negate the direct deployment
success criterion.
Provider inventory, controller health, workload health, route assurance, and
release publication are separate claims. Keep this map procedural; use the
linked launch records for the dated detail.

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

- [x] Review and merge the credential-free `staging/nyc3` input vertical slice
  in `norn-fleet` PR #941. It calls production
  plan-manifest, account/backend, topology, inventory, recovery-guard, and
  local PKI functions with synthetic inputs; substitution and eight-of-nine
  output hold before bootstrap. The documented 12-module command was rerun
  against `norn-fleet` main `6335c667` on 2026-11-09 and passed 84 tests in
  33 seconds. Protected CI run `42477659325` also passed. The local run emitted
  three `find: -user: norn-phase: no such user` diagnostics from the macOS
  fixture host, which lacks the Linux service account; these did not fail the
  suite, but remain an environment-specific cleanup diagnostic to remove or
  explain in the fixture. This helper-level slice stops
  before provider effects, SSH,
  Ansible, durable staging recovery, or terminal receipt. Protected helper
  tests separately cover controller-install pins and three-control SSH.
- [x] Exercise production durable `converge`/`recover` handlers after a lost
  response for the existing Fleet's exact app-03 scale effect.
- [ ] Join persistent staging provider apply/recovery handlers, durable
  same-intent receipt, exact state/provider identity, and post-recovery
  bootstrap into one synthetic workflow. Bind it to the current nine-node
  control/ingress/app single-app-pool source (the provider inventory also has
  two management nodes); keep the future two-pool contract separate.
- [x] Exercise the separate disposable-root saved-plan retirement and
  final-zero recovery contracts; these tests do not authorize teardown of
  persistent work-staging and the lane is not live-qualified.
- [x] Exercise synthetic secret materialization, private-file modes,
  grant-scoped cleanup/rotation, and secret-safe preflight receipts.
- [x] Join mirror, scoped grant use, grant cleanup after interruption, retry,
  and secret-safe preflight receipt in one synthetic credential-lifecycle
  fixture. Merged `norn-fleet` [PR #947](https://github.com/antiartificial/norn-fleet/pull/947)
  now source-maps producer, consumer, scope, and lifecycle fields for all 51
  listed bootstrap credential paths. This inventory is not proof of an
  end-to-end transformation or recoverable lifecycle: service-specific
  rotation/revocation and transition procedures remain open.
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
  inputs. `norn-fleet` PR #948 now implements an owner-attested token-scope
  gate and verifies it offline; live DO grant evidence still requires owner
  review on the exact credential used by the protected runner.
- [x] Model stable workload-release request replay before mutable qualification,
  source, plan, scheduler-lineage, or encrypted-input reads. Merged PR #311's
  etcd-backed regression test replays the accepted operation after removing
  source evidence and changing the current plan and encrypted snapshot; changed
  request selection conflicts. PR #319 supplies the opt-in API producer and
  PR #321 exercises it with deterministic handler fakes; this is not live
  controller qualification.
- [x] Reproduce and fix the concurrent same-key app-fence race using two
  independent etcd clients. PR #324's integration test requires one fresh
  acceptance and one replay; its TLA+ model preserves the five-step pre-fix
  counterexample and checks 14 fixed states / 18 transitions. A separate HTTP
  interleaving model and deterministic handler test force two initial replay
  misses and return one fresh result plus one same-ID replay through a fake
  atomic store. PR #335 exercises authenticated replay through the real HTTP
  handler and etcd after a dropped response, using a seeded acceptance. Fresh
  HTTP admission and runtime restart/response-loss qualification remain open.
- [x] Reconstruct the etcd client and operation store after claim loss, reload
  the durable workload receipt, and recover the same intent under a successor
  claim. PR #331's full `etcdstore` suite and exact-head CI passed. This proves
  store-level process-restart recovery only.
- [x] Extend the admission replay statechart with durable acceptance followed
  by a lost response, controller crash/restart without local acceptance
  memory, and exact same-key replay. PR #333's bounded model checked 115 states
  and 365 steps; all ten invariants and deadlock checking passed. This is
  protocol evidence only; it does not exercise the HTTP handler, etcd adapter,
  or runtime worker/controller restart.
- [x] Exercise authenticated HTTP replay against real etcd after dropping the
  response. PR #335 seeds the prior signed durable acceptance through the real
  store, then retries through the production handler and verifies same-ID
  replay and changed-selection conflict. PR #337 closes the handler and etcd
  client, rebuilds both adapters and the handler, then verifies the same replay.
  These tests do not exercise fresh handler admission or workload
  worker/controller runtime restart. On 2026-11-09, the five focused tests for
  signed qualification lookup, atomic private-input acceptance/replay, receipt
  recovery, and this seeded-acceptance HTTP replay passed against disposable
  loopback etcd at `127.0.0.1:12379` (`go test ./... -run
  'Test(ResolveWorkloadReleaseQualificationRequiresSignedCompletedSource|V3PrivateWorkloadReleaseAcceptanceIsAtomicReplaySafeAndKeyRecoverable|V3WorkloadReleaseStagedTargetBindingIsClaimFencedAndWriteOnce|V3WorkloadReleaseReceiptCASFencesAndRecoversEtcd|EtcdWorkloadReleaseAdmissionHTTPReplaysDurableAcceptanceAfterRestart)$'
  -count=1`). This refreshes component evidence; it still does not join fresh
  HTTP admission to those real-etcd prerequisites.
  At 16:22 UTC, seven of these existing integration tests were rerun against a
  newly started disposable local etcd 3.7.3 on macOS: the authenticated seeded-
  acceptance HTTP replay and reconciler lost-route-response restart tests, plus
  signed qualification, atomic private-input, staged-target binding, receipt
  CAS, and route-intent recovery. All passed. This is a repeat of the same
  component boundary; it does not close fresh HTTP admission or qualify a live
  Nomad/ingress effect.
- [x] Exercise ordered HTTP rejection when qualification, Fleet target, approved
  policy, active serving route, Nomad serving lineage, or encrypted input is
  missing. The deterministic handler tests verify conflict responses and no
  acceptance write; they do not join fresh admission to real etcd qualification
  and private-input persistence.
- [x] Recover the production shared workload-release reconciler across a
  controller-runtime restart after route publication loses its response. PR
  #339 persists operation receipts and claim recovery in real etcd, rebuilds
  the client and operation store, and adopts the already-published route step
  under a successor claim without a duplicate route write. Scheduler, route,
  health, and drain effects are controlled fakes; this does not exercise the
  production etcd workload executor or fresh HTTP admission. The focused test
  passed against disposable local etcd.
- Fresh HTTP admission has two distinct real-etcd building blocks, not one
  joined proof: handler tests use mutable-evidence fakes, while the store
  integration test calls `AcceptPrivateWorkloadRelease` directly with a real
  signed qualification and encrypted-input envelope. The production handler
  path also requires a completed signed staging deployment and a control-owned
  active route whose route intent, reservation, target, and deployment receipts
  agree; private acceptance rechecks that predecessor atomically. The joined
  fixture must create or faithfully replay those records through the owned
  deployment/route completion path, keep production etcd ownership checks in
  place, and use deterministic adapters only at external Nomad/ingress
  boundaries. Then send the authenticated HTTP request and inspect the durable
  acceptance and envelope after adapter restart. Do not write an arbitrary
  active-route marker: that bypasses the ownership proof under test.
- [ ] Prove fresh HTTP admission with real qualification and private-input
  persistence, then exercise the production workload executor and worker
  runtime across process/lease loss and external-effect response loss.
- [x] Model future fresh-Fleet bootstrap with two independent app pools,
  lost-create-response readback, same-intent recovery, full-inventory gating,
  and terminal receipt. Pool identities are abstract and no counts or capacity bounds are
  adopted; see [the two-pool model](statecharts/fresh-fleet-two-app-pools/README.md)
  and [input contract](fresh-fleet-two-app-pool-contract.md).
- [x] Bound a proposed online Norn platform-upgrade sequence with additive
  schema expansion, passive candidate checks, fenced traffic/writer handoff,
  same-intent crash recovery, and delayed schema contraction. TLC checked 97
  states / 251 steps; all 12 invariants and deadlock checking passed. This
  records protocol assumptions only; the compatibility/fencing contract is
  not yet reviewed or implemented. See the [online-upgrade model](statecharts/online-norn-upgrade/README.md).
- [ ] Review the binary/schema compatibility and writer-fencing contract, map
  each modeled transition to an idempotent controller phase, and replay those
  transitions against implementation tests before claiming online-upgrade support.
- [ ] Replay every counterexample against controller tests. Record model
  omissions and the live proof needed for each.

**Exit:** each lane has a synthetic success case, a failure matrix, invariant
results, and secret-safe output checks. No provider or SSH call occurs.

Reader-facing lane statecharts and their specs, configs, views, reports,
and TLC output are indexed in [the statechart bundle](statecharts/README.md);
focused admission and source-gate models are linked from their parent lanes. The
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
[future two-app-pool contract](fresh-fleet-two-app-pool-contract.md), merged
PRs #941 and #947. Keep the full path inventory in its
source-derived contract rather than duplicating it here. The remaining offline
work is an integrated persistent-staging apply/recovery replay, per-service
rotation/revocation coverage, and missing/stale input holds. Merged PR #945
cannot attest the live DO token's scopes; obtain a current owner-reviewed
scope readback before any protected apply. Persistent `work-staging` has no
sanctioned whole-Fleet destroy path.

The helper war-game stops before provider effects, SSH, Ansible, durable
persistent-Fleet recovery, and terminal receipt. The separate exact app-03
scale replay and disposable-root retirement tests do not fill those gaps. CI
run [42470557489](https://github.com/antiartificial/norn/actions/runs/42470557489)
qualified the disposable etcd TLS/RBAC harness only. None of these results
qualifies a billable fresh-Fleet deployment.

### M3. Re-establish DO controller readiness and prove live state

- [x] Refresh provider inventory and protected state handoff. The 04:49 UTC
  read-only `doctl --context work` inventory found all eleven staging Droplets
  active (two management, three control, two ingress, four app). Release-state
  readback
  [42964119843](https://github.com/antiartificial/norn-fleet/actions/runs/42964119843)
  passed at lineage `9201526d-4ce8-c70b-6a70-7e94a68dc580`, serial 23, and
  checked all three control identities.
- [x] Complete the protected Genesis journal readback. Run
  [42898054171](https://github.com/antiartificial/norn-fleet/actions/runs/42898054171)
  verified the signed Genesis intent and dormant journal. The units/timers
  remain stopped; this is not Genesis activation or a fresh-Fleet deployment.
- [x] Install and recover the controller path. Fleet PRs #966, #968, #969, and
  #979–#981 are merged. Gate-install
  [42951258208](https://github.com/antiartificial/norn-fleet/actions/runs/42951258208)
  installed the reviewed modules; the Linux gate remained disabled, the
  one-use runner retired, controller journals cleared, and the queue contained
  no eligible intent. The latest release controller then completed run
  [42964798605](https://github.com/antiartificial/norn-fleet/actions/runs/42964798605)
  with a terminal-and-runner-retired receipt.

**Exit for existing-Fleet controller readiness: passed.** A new-Fleet Genesis
deployment remains an independent future lane and is not a prerequisite for
updating the existing DO staging Fleet. See the
[DO controller guide](https://github.com/antiartificial/norn-fleet/pull/944)
and [launch history](LAUNCH-HISTORY.md) for recovery lessons.

### M4. Qualify the existing DO platform controller

- [x] Complete the exact-source signed platform release. Source `fa301fa5`
  passed all 12 qualification jobs and immutable publication; see runs
  [42963040596](https://github.com/antiartificial/norn/actions/runs/42963040596)
  and [42963531936](https://github.com/antiartificial/norn/actions/runs/42963531936).
- [x] Use the existing all-stop, forward-only Norn release lane on the existing
  DO Fleet. Controller run
  [42964798605](https://github.com/antiartificial/norn-fleet/actions/runs/42964798605)
  promoted `fa301fa5` from `34178b8b` after fresh protected state readback.
- [x] Verify target release, exactly one healthy private API with etcd backend,
  durable signed API/UI pins on all three controls, and terminal runner
  retirement. The all-stop transition interrupted service.
- [ ] Prove same-target recovery and prove unknown bytes/host identity hold.

**Existing-Fleet V3 platform deployment: complete.** M4's stronger retry and
unknown-identity recovery cases remain open. Do not claim online mixed Norn
control versions or workload readiness from this platform postflight alone.

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
- [x] Add a separate CAS-protected, operator-approved stateless overlap
  policy bound to the current Fleet placement generation. It records the
  approver and time, replica bounds, surge limit, and monotonic route steps;
  it is absent by default.
- [x] Bind the approval ID and canonical policy digest into the signed
  workload release plan. Private acceptance checks replica/surge/route bounds
  against the current policy and compares its etcd revision in the acceptance
  transaction; exact replay still resolves before mutable policy reads.
- [x] Expose an opt-in staging-only authenticated admission producer. PR #319
  accepts a signed qualification ID and target replica count, resolves exact
  replay before mutable reads, then derives policy, Fleet target, serving
  route, Nomad lineage, app source and encrypted SOPS snapshot server-side.
- [x] Cover fresh handler admission and exact-replay ordering in deterministic
  tests. PR #321 verifies trusted-input resolution, protected SOPS capture,
  acceptance, and response redaction. PRs #335 and #337 cover same-key replay
  through real etcd after response loss and handler/adapter reconstruction;
  they do not establish full executor/worker process recovery.
- [x] Pin the exact job-driving app spec and encrypted SOPS snapshot in the
  accepted private-input envelope. PR #351 adds worker reconstruction without
  rereading the current checkout and real-etcd persistence/open coverage.
- [x] Delete encrypted inputs only after a validated complete receipt, using a
  claim/app-lock-fenced transaction with a digest-only tombstone; retain
  pending and held inputs for recovery.
- [ ] Join fresh authenticated HTTP admission to real etcd with a real signed
  qualification, completed staging deployment, control-owned active-route
  lineage, and durable private-input readback. Existing tests cover these
  boundaries separately, not in one fresh acceptance path.
- [ ] Exercise the production executor and worker across process/lease loss
  and lost external-effect responses. The policy for a permanently held
  operation is now modeled in the
  [workload abandonment statechart](statecharts/workload-release-abandonment/README.md):
  abandonment requires a terminal receipt, retained private inputs remain
  available until that receipt, and a decryption key cannot be retired while
  any retained operation depends on it. Baseline and three unsafe-mutant
  checks pass. Implement the policy in the controller and exercise it across
  process/lease loss before closing this gate; the model alone does not prove
  runtime behavior.

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
| Fresh-Fleet topology and integrated replay | Five-node disposable profile and credential fixture | General + Stateful role validation/inventory/Nomad bootstrap now exist in protected `norn-fleet` main; current DO remains 3+2+4 with one app pool. The two-pool model captures the workflow contract but not provider handlers or a fresh profile; join protected apply/recovery, exact state/provider identities, and post-recovery bootstrap. |
| Credential lifecycle follow-up | Fresh-Fleet integrated replay | Joined executor fixture covers mirror, scoped grant, interruption cleanup, retry, and redaction; reconcile exact per-service rotation/expiry/revocation and path-level owner/mode evidence. |
| M3 DO controller and Genesis readback | All offline M1/M2 work | The `work`-context inventory at 19:42 UTC found all eleven DO staging Droplets active; state readback #42871361099 matched serial 23. Protected gate install #42861703620 and dormant Genesis bootstrap #42881334515 succeeded; Genesis units remain stopped and bootstrap is disabled. Readback #42883390770 failed before SSH because original private vars pinned the previous source. Fleet PR #968 derives vars from the digest-pinned template and exact controller source; its rebased exact-head contract is running. After it merges, create a fresh signed readback intent, verify Genesis, then prove services and workload health. |
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
