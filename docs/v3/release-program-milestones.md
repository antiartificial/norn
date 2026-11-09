# V3 release program milestones

Status: active work map, refreshed 2026-11-09 23:42 UTC. This checklist orders the [release fitness
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

Latest gate update (2026-11-09 23:42 UTC): Norn PR #373 fixed the recovered-
forward source-lineage verifier and merged at exact source
`a4b9c3b313d65c8a054fb869b18455fde33d3a70`. Full protected-master
qualification [42721291745](https://github.com/antiartificial/norn/actions/runs/42721291745)
passed all 12 jobs. Exact-source release run
[42721807811](https://github.com/antiartificial/norn/actions/runs/42721807811)
passed authorization, pinned UI, and all four platform bundle builds; its
protected publish job is waiting for owner approval. The replacement is not
signed or published. Earlier approval was specific to the already-published
`9d9d6208` release and does not approve `a4b9c3b3`. Neither Mini nor DO was
changed by this work. The latest authenticated Mini runtime inventory remains
the 22:47 UTC readback in [launch state](LAUNCH-STATE.md): API/host `ok`, schema
51, 19 of 30 app records running and healthy, and seven queued Fleet receipts.
Refresh it before any live upgrade step. DO has not been freshly read; its last
provider/runtime evidence is dated 18:58–19:01 UTC, so use a new protected
readback before claiming current Fleet health.
All 21 checked-in TLC configurations were rerun from current master, including
the four missing-evidence workload-admission scenarios and concurrent
two-client store admission. Configured rules passed; the concurrent-admission
config intentionally disables deadlock checking for terminal stutter states.
Per-model bounds are recorded in the [statechart index](statecharts/README.md)
and the latest [launch-history entry](LAUNCH-HISTORY.md).

## Parallel workstreams

| Workstream | Current evidence | Next proof |
|---|---|---|
| Protected release qualification | Protected-master qualification [42700530242](https://github.com/antiartificial/norn/actions/runs/42700530242) passed at `e9a21d20`; immutable signed prerelease `platform-e9a21d2095d60c8a96c1440e35e045dd0b6a7637` was published and its Darwin/arm64 signature, manifest, inventory, SBOM, and source verified. PR #370's `9d9d6208` release was qualified, signed, and published after exact-SHA approval. PR #373 then merged the verifier lineage fix at `a4b9c3b3`; full protected-master qualification [42721291745](https://github.com/antiartificial/norn/actions/runs/42721291745) passed all 12 jobs. Exact-source release run [42721807811](https://github.com/antiartificial/norn/actions/runs/42721807811) built the pinned UI and all four bundles; the protected publish job is waiting for exact-SHA owner approval. | Approve and publish the exact `a4b9c3b3` release. Then import and verify it on Mini and refresh source-bound backup, shadow, and preflight evidence before recovered-forward admission. |
| Mini direct release evidence | Latest recorded authenticated inventory at 22:47 UTC reported API/host `ok` on `d4dc88e0`, schema 51, floors 5/33, host assurance succeeded, and 19 of 30 app records running and healthy. Candidate `e9a21d20` backup and private-shadow checks passed, but preflight stopped before DDL. The signed `9d9d6208` recovered-forward attempt stopped before fencing or migration because the verifier incorrectly required source equality. PR #373 fixes this through externally signed ancestry verification; its replacement release is awaiting approval. Seven stale DO dispatch receipts remain queued after lookup-only reconciliation returned `fleet_github_dispatch_binding_mismatch`; no dispatch or apply occurred. Production readiness remains separately blocked. | After replacement publication, refresh authenticated Mini source/state, backup, shadow and preflight proofs. Invoke only the exact signed candidate's `mini-recovered-forward-transition` wrapper with a fresh transition ID; preserve the seven queued receipts, then verify migration 52, API/schema, smoke, host assurance, and generation receipt. |
| Release evidence | The 18:48 UTC Mini inventory confirms API/host `ok` on signed repair release `d4dc88e`, inactive runtime fence, 19 app records running, and successful host assurance at 18:48:53 UTC. Open incident groups and snapshot-retention warnings remain for review; production readiness fails its separate production-admission contract. Nov 9 18:58 UTC DO inventory confirms nine staging nodes, both management nodes, the ingress LB, and three managed databases active/online. The 19:01 UTC public canary probe served `be5ca5d9` across two allocations on two nodes; this does not prove Norn API/control-plane health or the failed operation's terminal receipt. | Use [launch state](LAUNCH-STATE.md) for exact receipts and [launch history](LAUNCH-HISTORY.md) before repeating a read. Obtain protected control-plane runtime and receipt evidence before declaring DO fully healthy. |
| Mini Git workload admission | Nov 9 read-only inventory confirms the Mini API is healthy, its running process has a webhook secret, 12 app records have `repo.autoDeploy`, and its latest 25 webhook receipts are GitHub. Six mapped personal GitHub repositories have active push hooks to the Norn webhook hostname; endpoint-to-Mini routing and hook-to-terminal-operation outcomes remain unverified. The Mini runs `d4dc88e`, before merged exact-SHA fix `ccb49a56` and host-bound matching `765d7550`. The API uses one shared `NORN_WEBHOOK_SECRET` across configured GitHub and Gitea hooks; it does not isolate credentials per repository. Published signed release `9d9d6208` contains the recovered-forward admission and drain-path repair, but its transition attempt stopped at the separately fixed evidence-verifier lineage check. That verifier fix is in `a4b9c3b3`, whose signed release is awaiting exact-SHA approval. | After the corrected Mini transition, reconcile webhook-host ingress and app mapping, confirm each hook uses the shared secret, and rehearse one push through terminal operation and app smoke. Keep GitHub canonical; make any Norn Git copy one-way. |
| Existing DO controller | PR #946 merged the pre-effect source-object admission fix. Exact-head PR #944 and merged-main contract validation passed. Staging plan run 42555418513 proposes one private ingress firewall and no other changes; it was not applied. | Review the plan effect, then install reviewed source with timers disabled and verify it before fresh Genesis readback. |
| Fresh-Fleet bootstrap | PRs #941 (offline war-game), #940 (release intent controller), #945 (provider-scope contract), #947 (bootstrap credential lifecycle), #942 (module-owned test collection), and `norn-fleet` #948 (attested DO token-scope admission) are merged. Protected `norn-fleet` main is now `35e1c893`; the General + Stateful role pair is accepted by its topology validator, generated inventory, and Ansible/Nomad bootstrap. Three focused tests for those boundaries passed at that exact source. Existing staging remains a single `workload: app` pool; no two-pool fresh-Fleet config or provider action was added. The selected stable pool IDs are `general` and `stateful`; provider sizes, min/desired/max, cost envelope, stateful storage, and workload policy remain open. PR #948's owner-supplied token-scope evidence is bound to the token fingerprint and account but does not query DO to prove actual grants. Merged-main plan run `42693143436` proposed one management-runner-only ingress firewall with ports 18082/18083; no apply occurred. PR #837 merged at `6335c667`; no host enrollment or Genesis readback occurred. | Join persistent apply/recovery, exact state/provider identities, per-service credential lifecycle, live DO-grant review, and terminal receipt before any protected apply. The #837 enrollment lane still needs fresh owner-reviewed key/tag/host/digest inputs and a separately approved one-use dispatch. |
| Offline models | Checked-in Mini, DO release, existing Genesis/readback, fresh Fleet (including both two-pool and current 3+2+4 shapes), online upgrade, mixed-version resize at capacities 3/4, and workload admission/route/stop recovery models were rerun from current master; every configured invariant and deadlock check passed. Capacity 3 still cannot complete mixed-version resize without violating the old-cohort drain fence; capacity 4 reaches the modeled terminal receipt. | Replay remaining counterexamples against controller tests and prove each modeled boundary against implementation; see the [statechart index](statecharts/README.md). |
| Workload release | PR #302's stateless mixed-version protocol is extended by merged PRs #311, #315, #316, and #317 for stable replay, replay preflight, placement-bound overlap policy, and signed policy binding. PR #319 (`877ad4ba`) adds the opt-in staging-only authenticated producer; PR #324 (`3fcdc154`) fixes concurrent same-key store replay; PR #331 (`c58df6aa`) proves receipt reload and successor-claim recovery after rebuilding the etcd client and store. PR #333 (`443ca840`) adds a bounded TLA+ model for acceptance commit with a lost response, controller crash/restart, and durable same-key replay (115 states / 365 steps; ten invariants and deadlock check pass). PR #335 (`0eb10852`) exercises authenticated HTTP replay against real etcd after a dropped response, using a previously seeded durable acceptance. PR #337 (`15a8d2d4`) tears down and rebuilds the handler and etcd adapters before that replay. PR #339 (`0cb28c5b`) restarts the shared release reconciler around a lost route-effect response with real etcd claims/receipts and controlled fake scheduler, route, health, and drain effects; it proves same-intent receipt recovery and no duplicate route write. PR #351 (`ed296c17`) pins the exact job-driving app spec with the accepted encrypted SOPS snapshot, so worker target reconstruction does not depend on a current checkout. PR #353 (`f3b4aea8`) records the reproducible real-etcd route-response-loss/restart test; it passed against disposable etcd 3.7.3 on Nov 9. The restart test uses controlled fake scheduler, route, health, and drain effects: it does not qualify the production `etcdWorkloadReleaseExecutor`, full worker runtime, or fresh HTTP admission. The admission and release statecharts were rerun Nov 9 with all listed invariants and deadlock checks passing. No live workload release is qualified. | Prove fresh HTTP admission through real qualification and private-input persistence; prove exact source and encrypted-input recovery, full production worker/controller runtime crash and lost-response behavior, and target-only public-route evidence. Live staging canary remains gated on DO M3/M4; stateful workloads require separate writer/schema/data compatibility authorization. |

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
  single-app-pool source; keep the future two-pool contract separate.
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

### M3. Re-establish DO readiness without writes

- [ ] Recheck the exact management runner, route, provider state, and three
  control identities with fresh protected receipts. The 2026-11-09 18:58 UTC
  provider inventory found all nine staging nodes, both management nodes, the
  ingress load balancer, and three managed databases active/online. A 19:01 UTC
  public canary probe served the expected source across two allocations on two
  nodes. These reads do not establish Norn/API, etcd, Nomad/Consul, full
  workload, or terminal operation-receipt health.
- [ ] Diagnose source availability on the management host without mutation.
  Fleet PR #946 merged the pre-effect missing-source check; current protected
  main is `6335c667693d0333d4239f91edf2efb9772ab63a`, and merged-main
  contract validation
  [42555418496](https://github.com/antiartificial/norn-fleet/actions/runs/42555418496)
  passed. Installation on the management host remains unverified; install the
  exact reviewed source only after separately reviewing the provider-plan
  effect below.
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
  intent and runner authorization cannot be reused. At 19:09 UTC the protected
  `NORN_WORK_PLATFORM_CONTROLLER_BOOTSTRAP_ENABLED` setting was `false`, so
  reviewed source installation is not currently enabled.

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
  in-process tests. PR #321 verifies trusted-input resolution, protected SOPS
  capture, acceptance, and response redaction; it does not establish durable
  recovery across worker or controller restart.
- [ ] Pin and durably recover the exact app source and prove private-input
  retention/key lifecycle across worker and controller restart. The current
  producer reads configured app source and SOPS ciphertext at admission;
  merged code does not yet prove portable immutable source retrieval or a
  bounded deletion/retention contract.

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
