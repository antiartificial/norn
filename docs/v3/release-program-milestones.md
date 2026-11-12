# V3 release program milestones

Status: refreshed 2026-11-12 15:24 UTC (latest DO runtime observation: 15:24 UTC). The existing DO staging Fleet's V3
platform release is deployed through the controller (2026-11-11). A read-only
`doctl --context work` check at 15:24 found the expected work account active
and all eleven Norn staging Droplets active: two management, three control,
two ingress, and four app nodes. Platform `/version` returned HTTP 200 in
0.247 seconds with allocation `de4645bf-25c0-93bd-124b-5e7f9c8cda05` and
version `af0f054358d9b9c7b68ea25132b52ee5eb099e12`. At 15:24, canary
`/route-proof` returned HTTP 200 in 0.245 seconds with `norn-fleet-canary`; `/version` returned HTTP 200 in 0.159 seconds with source
`be5ca5d969ccf3509e62ce04a833516bb5c204db`, node `515ddcea44b8`, and
allocation `6b4b9efa-ca9a-2dc6-3e0b-d7cc3e27bcf8`; its node and allocation
match the 14:53 and 15:17 responses. The platform allocation varied at 14:53,
returned to its 14:29 value by 15:17, and remained the same at 15:24. These point-in-time
responses prove platform and workload endpoints responded; they do not prove
V3 signed admission, allocation-to-current-host binding, or continuous health.
The full API module suite
passed locally at 11:27 UTC, including Fresh-Fleet route and admission-handler
checks, and again at 12:12 UTC after terminal receipt and target-handoff changes.
A focused real-etcd test passed at 12:10 UTC, including partial-inventory
refusal, lost write responses, exact replay, ordinary target-fence readback,
and create-fence retirement. The updated authority statechart passed nine
invariants and deadlock checking across 19 states/21 steps at 12:11 UTC. These
are local code/model checks, not provider workflow or protected CI qualification.
After those checks, Fresh-Fleet effect-grant issue/consume/status routes were
added behind the existing default-off gates. At 12:24, focused route tests,
three focused real-etcd durability tests, and the full API module suite passed.
The checks cover exact issue recovery after a lost response, one-use consume,
no successful grant-record replay after an ambiguous consume, runner-bound
digest/status readback without re-serving the grant record, and the existing
terminal/fence handoff. The protected Fleet worktree's focused apply-graph and
Fresh-Fleet war-game rerun passed 11 tests at 12:30 UTC; its staging create
remainder remains held by the effect guard and its Norn attempt authority is a
local fake. Norn PR [#437](https://github.com/antiartificial/norn/pull/437)
merged as `f41f70cc` at 12:41 UTC. Protected run
[43219500086](https://github.com/antiartificial/norn/actions/runs/43219500086)
passed API Go tests, OpenAPI, UI, workflow lint, platform gate, catalog, and
Fleet pilot workload checks; companion run
[43219500056](https://github.com/antiartificial/norn/actions/runs/43219500056)
passed three-client durable-replica qualification. The API routes remain
default-off and have not been published or deployed. Provider-executor
integration, a protected Fresh-Fleet workflow, and any provider effect remain
open. No DO change was made. No Fresh-Fleet API release was published or deployed.
The recorded 15-module `norn-fleet` workload-readback suite passed 146 tests
at 11:28 UTC on then-current `main` `a2262959`. The repository's names-only GitHub Actions
environment audit at 15:24 UTC, against the current `norn-fleet` main workflow
at `867d545b`, found 18 required
variable names missing, including the enable flag. Five shared
variables and three state-backend secret names are present; the audit read no
values and did not validate file paths or contents. The [staging input runbook](https://github.com/antiartificial/norn-fleet/blob/main/docs/runbooks/work-staging-norn-release.md)
confirms the missing ACL issuer files need dedicated Nomad/Consul management
credentials: Nomad provisioning requires a `management`
token, and the Consul accessor pin does not prove reduced issuer scope. These
are privileged inputs requiring owner review of grant, lifetime, rotation, and
revocation before the protected run. The current provenance validator checks
UTC timestamp format and digest bindings, but it does not enforce review
freshness or inspect the cited grant evidence. Review the current issuer
authority at preflight and refresh the owner evidence and manifest after any
credential rotation or scope change. The 15:24 UTC workflow-history query found no protected readback run; the only
recorded run is failed candidate-branch push
[43117232561](https://github.com/antiartificial/norn-fleet/actions/runs/43117232561)
with zero jobs. Fleet issuer-review model PR [#1024](https://github.com/antiartificial/norn-fleet/pull/1024)
merged as `3a7bbd3b` at 14:49 UTC after contract and Socket checks passed.
Fleet runbook PR [#1025](https://github.com/antiartificial/norn-fleet/pull/1025)
merged as `867d545b` at 15:17 UTC after its contract and Socket checks passed.
Norn milestone PRs #443, #444, and #445 merged as `f6a49993`, `e2946c5e`, and
`f9341711`. These document status and do not qualify the protected readback.
The offline readback model was rerun at 11:34
on matching `norn-fleet` main: 70 states/120 steps. The provenance model
explored 16 states/16 steps, and eight unsafe provenance mutants failed at
their expected invariants. These model checks do not qualify runtime.
The V3-admission-qualified workload gate remains open; its date is
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

## Current gate snapshot (status refreshed 2026-11-12 15:24 UTC; provider inventory, platform and canary endpoints, names-only environment audit, and workflow history refreshed 15:24 UTC; offline model and exact-head CI evidence is listed under each workstream; Norn milestone PR #445 merged as `f9341711` at 15:01 UTC; Fleet input-audit PR #1025 passed contract and Socket checks and merged as `867d545b` at 15:17 UTC; no release publication or deployment)

- **DO platform:** signed release `fa301fa5f881b847a26f61a501092f9a9dd090fd`
  is running after the controller-mediated all-stop upgrade. Postflight proved
  one healthy private API backed by etcd and signed API/UI pins on all three
  controls. Promotion interrupted service; online upgrade is not qualified.
- **DO staging observation:** the explicit `work` account was active (UUID
  prefix `17f40050`). At 15:24 UTC all eleven Norn staging Droplets were
  active (two management, three control, two ingress, four app). Platform
  `/version` returned HTTP 200 in 0.247 seconds with allocation
  `de4645bf-25c0-93bd-124b-5e7f9c8cda05` and version
  `af0f054358d9b9c7b68ea25132b52ee5eb099e12`. The public canary's `/route-proof`
  returned HTTP 200 in 0.245 seconds (`norn-fleet-canary`); `/version` returned
  HTTP 200 in 0.159 seconds with source `be5ca5d969ccf3509e62ce04a833516bb5c204db`,
  node `515ddcea44b8`, and allocation `6b4b9efa-ca9a-2dc6-3e0b-d7cc3e27bcf8`.
  The platform allocation varied at 14:53, returned to its 14:29 value by 15:17,
  and remained the same at 15:24. These point-in-time
  responses do not prove allocation-to-current-host binding, V3 signed
  admission, or continuous control-plane health. The public-pilot verifier's
  12/12 reads at 02:40:57 UTC remain the latest recorded MySQL pilot check.
- **V3 workload admission:** the 15:24 UTC names-only audit against
  `norn-fleet` main `867d545b` found 18 required environment variable names
  missing, including `NORN_WORKLOAD_READBACK_ENABLED` and input file paths.
  Five shared variables and three state-backend secret names are present; the
  audit read no values and did not validate file paths or contents. The recorded
  15-module readback suite passed 146 tests on then-current main `a2262959`; fake-backed tests do not
  qualify protected credentials or the runner. The 15:24 workflow-history
  query found no protected readback run; the only run is failed candidate push
  [43117232561](https://github.com/antiartificial/norn-fleet/actions/runs/43117232561)
  with zero jobs. The [staging input runbook](https://github.com/antiartificial/norn-fleet/blob/main/docs/runbooks/work-staging-norn-release.md)
  records the issuer constraint: Nomad requires a `management` token, while a
  Consul accessor pin does not prove reduced issuer scope. The provenance
  validator checks timestamp format and digest binding, not review freshness or
  cited grant contents. Review current issuer authority at preflight; refresh
  evidence after rotation or scope change. The first durable V3 admission
  receipt remains absent. Next gates are protected-input and credential
  provenance review, signed workload/ACL/network readback, terminal-artifact
  replay after controller restart, full-executor recovery through real
  Nomad/ingress, admission review, and same-intent receipts binding the
  responding canary. See [current launch state](LAUNCH-STATE.md) and
  [launch history](LAUNCH-HISTORY.md).
- **Managed MySQL candidate:** its InfraSpec declares
  `norn-fleet-mysql.tkmkt-dev.com`; protected catalog promotion
  ([42312866244](https://github.com/antiartificial/norn-fleet/actions/runs/42312866244))
  and three-control readback
  ([42341905690](https://github.com/antiartificial/norn-fleet/actions/runs/42341905690))
  succeeded, and DNS resolves to `159.89.243.220`. At the last recorded check
  (03:41 UTC), TLS returned the Traefik default certificate, the managed
  release toggle was `false`, and no protected release dispatch or Norn-owned
  A/B deployment had been recorded.
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

- **Issuer review model:** Norn milestone PR [#443](https://github.com/antiartificial/norn/pull/443)
  passed all applicable required checks at exact head `7290d72a` and merged as
  `f6a49993` at 14:47 UTC; it documents the existing privileged Nomad/Consul
  issuer and freshness gap. Fleet model PR [#1024](https://github.com/antiartificial/norn-fleet/pull/1024)
  adds offline checks for exact current issuer review, issuer rotation, and
  Nomad token class; its contract and Socket checks passed, and it merged as
  `3a7bbd3b` at 14:49 UTC. The local 91-test readback suite and 4-test binding
  audit passed. The TLA baseline checked 11 states/14 steps; the unverified
  Consul-scope and stale-review mutants violated the expected safeguards, while
  the wrong Nomad token class held before signing. These checks expose the
  existing validator freshness gap; they do not change live staging or close
  protected readback, credential review, or admission gates.

## Parallel workstreams

| Workstream | Current evidence | Next proof |
|---|---|---|
| Protected platform release | Signed source `fa301fa5f881b847a26f61a501092f9a9dd090fd` passed the 12-job qualification and was published, then deployed to the DO Fleet through the all-stop controller upgrade. Postflight proved the private etcd-backed API and signed API/UI pins on all three controls. | Keep exact-source qualification, publication, and controller promotion distinct. Online upgrade remains unqualified because the completed promotion interrupted service. |
| Mini direct | Independent Mini met the owner-defined bar on 2026-11-10: signed V3 active, API healthy, and `vigil-gateway` returned HTTP 200. Other workload health and queued-upgrade recovery are separate follow-ups. | Revisit queued-upgrade recovery and Git-driven admission separately. The Mini is not a Fleet member and has no Fleet resize lane. |
| Existing DO Fleet and workload admission | At 15:24 UTC all eleven Norn-tagged Droplets were active; platform `/version`, canary `/route-proof`, and canary `/version` returned HTTP 200. Platform allocation varied at 14:53 and returned to its 14:29 value by 15:17, then stayed stable through 15:24; public responses do not prove host lineage. The names-only audit against `norn-fleet` main `867d545b` found 18 readback bindings missing; five shared variables and three state secrets are present. It read no values or files. Protected workflow history still contains only failed candidate push `43117232561` with zero jobs. PR #1024's issuer model and PR #1025's runbook audit merged; the first durable V3 admission receipt is still absent. | Provision and verify the exact protected files and owner-reviewed ACL/provider provenance. Run signed readback, retrieve terminal evidence after restart, qualify full-executor recovery through Nomad/ingress, and bind the responding canary to durable same-intent admission receipts. The first controller-admitted workload remains unscheduled. |
| Fresh-Fleet bootstrap | General + Stateful application-pool roles are supported in topology validation, inventory, and bootstrap. The nine-node offline model passed 10 invariants across 39 states/58 steps; the Norn authority model passed nine invariants and deadlock across 19 states/21 steps. Norn PR #437 merged effect-grant/status routes behind default-off gates. Fleet PR #1024 added issuer-review mutants. No Fresh-Fleet provider apply or protected creation workflow has run. | Connect the reviewed one-use grant to the provider executor and protected workflow; qualify partial inventory, ambiguous effects, controller/runner loss, exact provider identity, post-recovery bootstrap, and future General + Stateful sizing/stateful recovery. |
| DO workload resize/release | The staging-only producer remains opt-in and undeployed. PR #421 qualified the production target-stage helper against disposable real Nomad with dropped create response and hidden first readback; it proved the successor binds actual job version 0 and modify index without duplicate registration. Mixed-version models hold at capacity 3 and reach terminal receipt at capacity 4. Full private-input/executor recovery, health/ingress, fresh signed admission, and live DO resize remain open. | Join admission to real etcd and signed qualification; extend disposable driver proof through private-input staging, target health, route, and old-cohort stop. Then qualify a stateless canary and mixed-version resize on DO. Keep stateful compatibility separate. |
| Controller recovery and offline models | Merged PR #433 models held-operation abandonment, retaining private inputs through terminal receipt, and key retirement only after retained operations no longer depend on them. Baseline and unsafe mutants were checked. This is policy/model evidence, not a deployed controller path. | Prove implementation refinement, terminal-artifact replay after restart, full-executor recovery, and online platform upgrade. Preserve the all-stop lane as the currently qualified option. |

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
- [x] Model the nine-node control/ingress/app create and recovery contract,
  including exact plan/state lineage binding, same-intent recovery, a provider/
  state write mismatch hold, bootstrap ordering, and terminal receipt. The
  companion statechart passed 10 invariants and deadlock across 39 states and
  58 steps on 2026-11-12. This is offline protocol evidence only.
- [ ] Join persistent staging provider apply/recovery handlers, durable
  same-intent receipt, exact state/provider identity readback, and post-recovery
  bootstrap into implementation-facing tests. The current model uses the
  nine-node control/ingress/app source; the provider inventory also has two
  management nodes. Keep the future two-pool contract separate.
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

- [x] Refresh provider inventory and protected state handoff. The latest
  read-only `doctl --context work` inventory at 15:24 UTC found all eleven
  staging Droplets active (two management, three control, two ingress, four
  app); platform `/version` and canary `/route-proof` and `/version` returned
  HTTP 200 at 15:24 UTC, as recorded above. The platform allocation changed
  from its 14:29 value at 14:53, returned by 15:17, and remained steady at
  15:24. The protected readback evidence
  remains the 11:17 UTC provider refresh and release-state readback
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
| M3 DO controller and Genesis readback | All offline M1/M2 work | Existing-Fleet controller readiness and dormant Genesis journal readback are complete; Fresh-Fleet activation remains separate and unapproved. Current provider and endpoint reads at 15:24 found all eleven staging Droplets active and platform/canary endpoints responding. The platform allocation varied at 14:53 and was steady from 15:17–15:24; host lineage remains unproved. The 15:24 names-only audit against main `867d545b` found 18 required workload-readback bindings missing. Nomad/Consul issuer scope and review freshness remain owner gates. Do not dispatch until inputs, provenance, signed intent, and bounded ACL token issuance are reviewed. |
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
