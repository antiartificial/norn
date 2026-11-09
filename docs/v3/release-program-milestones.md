# V3 release program milestones

Status: active work map, refreshed 2026-11-08. This checklist orders the [release fitness
and controller plan](release-fitness-and-controller-plan.md). It authorizes no
deployment, provider mutation, or credential use.

## Reuse release lessons before repeating work

Keep this milestone map and the operator runbooks procedural. Consult the
[launch evidence ledger](LAUNCH-STATE.md) for dated live observations, the
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

## Parallel work that can start now

| Workstream | Scope | Owner seam | Exit artifact | Status |
|---|---|---|---|---|
| Evidence ledger | Reconcile Mini completion, DO soak, current health, Genesis readback, and release receipts against source and dated docs. | Norn and norn-fleet docs plus retained run IDs. | Mini runs the current immutable V3 RC; current host/route evidence and open events need follow-up. The DO private API/runtime and protected Genesis source remain unverified. See the dated Mini readback below. | Mini runtime V3; postflight/route assurance incomplete; DO runtime unverified |
| Bootstrap input audit | Trace state, route, inventory, vars, provider scope, SSH, signed intent, runtime identity, and key flows; define the future two-app-pool input contract. | `norn-fleet` validators, workflows, Ansible, templates; [two-pool contract](fresh-fleet-two-app-pool-contract.md). | Field-level input manifest and existing-Fleet app-03 scale lost-response round trip added; a 51-pattern path inventory is checked against Ansible docs; future pool roles selected as general + stateful; IDs, sizes, count bounds, and placement remain undecided; per-file lifecycle evidence and complete fresh-Fleet workflow recovery remain. | Source audit complete; fitness partial |
| Controller and V3 API audit | Reconcile Mini direct controller, DO platform release, workload resize, online proposal, and V2 surfaces. | Norn controllers/API/tests and V3 docs. | Mini and scale test entry points mapped; Mini bounded model is in the evidence bundle; V2 semantics clarified. | Source audit complete |
| Offline models | Check existing DO platform release, Mini direct, existing-Fleet Genesis, disposable Fleet create/recovery, current staging source, future two-app-pool create/recovery, and proposed workload overlap. | TLA+ models and deterministic controller tests. | Protected `norn-fleet` main and provider inventory agree on the current 3+2+4 topology, with one app pool. The nine-node model covers that shape abstractly; the 45-state future two-pool model fixes General + Stateful roles and gates success on role-correct readiness plus stateful recovery proof. Neither is wired to the protected provider workflow or its detailed credential inputs; current staging recovery still lacks a joined code-level replay. | Nine bounded models check; topology shape reconciled; input/recovery failure coverage partial |
| Workload release protocol | Keep replica scaling, application rollout, and app-pool resize distinct; qualify stateless before stateful. | Norn app-scale and workload catalog; [M5 protocol](m5-workload-release-protocol-2026-11-07.md). | Internal stateless controller and deterministic lost-response replay are implemented in the open PR #302 candidate. Operator admission, production-store crash qualification, public-route proof, live canary, and stateful data compatibility remain open. Detailed contract, test evidence, and redo criteria are in the dated M5 protocol. |

## Mini direct readback — 2026-11-08

- `/api/health` and host status report `ok`. Mini serves V3 source
  `d4dc88e0c37003a1013caed444983b2eeb50fbcb`
  (`v3.0.0-rc.1-platform`), the current code release; `master` is one
  documentation-only commit ahead. Norn CI
  [42410071278](https://github.com/antiartificial/norn/actions/runs/42410071278)
  and immutable prerelease publication
  [42410387388](https://github.com/antiartificial/norn/actions/runs/42410387388)
  passed, with manifest, signature, SBOM, and archive assets for Darwin/Linux
  amd64/arm64.
- The latest stored host-assurance receipt is from 2026-09-06; the newest open
  `host.assurance.failed` group was seen 2026-11-08 23:17 UTC. Snapshot
  retention warnings remain. The active-event endpoint reports 25 unresolved
  incident groups (22 critical, 3 warning).
- A read-only event-reconcile dry-run at 2026-11-08 23:55 UTC (requested
  limit: 200) scanned 200 events: 54 task-restart events would be acknowledged
  by the deterministic rule; 146 need review, including 32 host-assurance
  failures, 94 failed/lost allocations, and 12 capacity warnings without
  later recovery evidence. No event was acknowledged.
- The exact installed Mini upgrade script matches the script in source
  `d4dc88e0` and excludes queued `fleet.github.apply-dispatch` receipts from
  the Mini drain. Seven such receipts remain queued with no terminal receipt;
  preserve them for Fleet reconciliation. They do not block direct Mini work.
  Running Fleet dispatches and other active Mini operations still block the
  `fail`/`wait` drain.
- Norn [PR #301](https://github.com/antiartificial/norn/pull/301) contains a
  route-authentication fix, but is not merged or deployed. Its protected
  release must be followed by fresh Mini host assurance and route inventory.

## Mini assurance recheck — 2026-11-09 00:55 UTC

- The API still reports `ok` on V3 source
  `d4dc88e0c37003a1013caed444983b2eeb50fbcb`. Consul, Nomad, Postgres, and the
  Nomad-Consul workload connector report `up`. `norn host doctor` passed its
  managed-file, LaunchAgent, runtime, and Cloudflare process/config checks.
- The latest successful `host.assure` receipt remains from 2026-09-06. The
  active-event endpoint reports 25 incident groups: 22 critical and 3 warning.
  The host-assurance failure group was last seen at 2026-11-09 00:19 UTC.
- `norn smoke platform` exits successfully and reports healthy API, drain,
  release, and event-read surfaces. It also reports recent critical and warning
  counts. Its `ok events` result does not clear those incidents or prove public
  routes.
- The authenticated event-reconcile dry-run scanned 200 events. It marked 53
  for deterministic acknowledgement and 147 for review. No events were
  acknowledged. Preserve the review set; host-assurance failures and lost
  allocations have no automatic acknowledgement rule.
- The active-operation API still lists seven queued
  `fleet.github.apply-dispatch` receipts. The direct Mini smoke reports zero
  Mini-affecting active operations because the installed script preserves and
  excludes those queued Fleet receipts. They remain Fleet work and do not
  block the Mini drain.
- The Cloudflare inventory lists 20 configured hostnames, but neither that
  inventory nor `norn host doctor` probes route reachability. PR #301 remains
  open and undeployed. After its protected signed release, run controlled host
  assurance and verify a fresh successful assurance event plus route
  inventory. Host assurance can publish or prune routes, so treat it as a live
  mutation. Do not substitute platform smoke for this gate.

## Mini read-only check — 2026-11-09 01:20 UTC

- The Mini API returned V3 source `d4dc88e0c37003a1013caed444983b2eeb50fbcb`.
  The read-only inventory reported API health and host status as `ok`, with 30
  apps and 46 services.
- `norn host doctor` passed its managed-file, LaunchAgent, runtime, and
  Cloudflare config/process checks. It does not test route reachability or
  create a host-assurance receipt. No host assurance or upgrade ran.
- The active-event endpoint returned 25 incident groups: 22 critical and 3
  warning. The host-assurance failure group was last seen at 01:20:25 UTC.
- The active-operation endpoint returned seven old queued
  `fleet.github.apply-dispatch` receipts. They remain Fleet reconciliation
  work; this readback found no Mini platform-upgrade operation.
- `norn smoke platform` exited successfully with `activeOps=0`, 10 critical,
  and 10 warning event counts. Its drain view excludes the queued Fleet
  dispatch receipts. The smoke result does not clear the 25 active incident
  groups or prove route reachability.
- Norn PR #301 remains open. A new successful host assurance and route
  inventory remain required after the protected release.

## Mini release-history recheck — 2026-11-09 01:30 UTC

- The live `/api/version` and `/api/v1/releases` endpoints both report
  `d4dc88e0c37003a1013caed444983b2eeb50fbcb` as current V3. The release list
  marks that SHA current and records its release entry at 2026-11-07 14:14
  local time.
- This confirms that Mini currently serves the V3 artifact. The release list
  does not identify a durable upgrade operation or prove the required fresh
  host-assurance and route-reachability checks. Those gates remain open.

## Mini operation-history recheck — 2026-11-09 01:33 UTC

- `norn operations --limit 100` returned no successful `platform.upgrade`
  operation for the current V3 release. The newest listed upgrade failed on
  2026-10-10, and the newest listed preflight failed on 2026-10-13.
- This is specifically an API operation-history gap; it does not mean the
  direct upgrade lacks local postflight evidence. The release script writes
  under `~/norn/release-logs`, outside the immutable release directory.
- `~/norn/release-logs/d4dc88e0c370.Xr2j8y/` contains candidate version,
  health, and schema captures plus `postflight-version.json` and
  `postflight-schema.json` dated 2026-11-08 around 09:20 UTC. The postflight
  version records the full current source SHA and exact API version; the
  schema capture reports the same version. The installed `bin/platform-upgrade`
  SHA-256 matches the script blob at source commit `d4dc88e0`.
- Together, these artifacts support that the direct script completed its
  version/schema postflight for the current Mini release. They are not a
  `platform.upgrade` API operation receipt, and they do not close the separate
  fresh host-assurance and route-reachability gates. When evidence appears
  missing, check the owning script's configured evidence path before deciding
  to repeat a live operation.

## Mini live readback — 2026-11-09 01:50 UTC

- A fresh read-only inventory reports `/api/health` and `/api/v1/host/status`
  as `ok`; `/api/version` still reports V3 source
  `d4dc88e0c37003a1013caed444983b2eeb50fbcb`. `norn host doctor` exited 0 and
  passed managed files, LaunchAgents, Consul/Nomad/Norn runtime checks, and
  Cloudflare config/process checks.
- The `latestAssurance` operation referenced by host status is
  `7d3e45e7-f550-4289-8e8c-f8258be0649b`; its operation record finished
  2026-09-06 17:23:57 -05:00. It is not fresh assurance evidence. The active
  event readback still has 25 groups (22 critical, 3 warning), so these doctor
  and health results do not close Mini host assurance or route reconciliation.
- The Mini API's configured Fleet document names `norn-disposable-nyc3` and
  declares 3 control + 2 ingress nodes. That configuration is neither proof
  that Mini belongs to a Fleet nor evidence of the DO staging Fleet's
  3+2+4 provider/runtime topology. Keep the independent Mini and DO Fleet
  evidence separate.

## Mini route-assurance fix qualification — 2026-11-09 02:08 UTC

- Norn PR #301 remains open at `fef0d6b5e2fa364c9327f733f513e5b0ec01912a`.
  Its applicable PR checks pass; targeted shell tests were also run from a
  clean worktree at that exact SHA: `test-host-assurance-route-fence.py` passed
  2 tests and `test-recovered-baseline-adopt.py` passed 10.
- The change admits only an allowlisted route-forge child through the managed
  SOPS environment while preserving the runtime-mutation fence for other
  children. This verifies the offline boundary, not live route recovery.
- A successful `norn host assure` can publish or prune routes. After this fix
  is merged and installed by the normal exact-SHA release lane, run assurance
  deliberately and verify its fresh receipt, event, and route inventory. Keep
  the Mini's current `require-signed` release policy intact.
- Cross-repository worktrees can have the same PR number. Qualify every PR
  status lookup with its repository before recording or reporting state.

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

## Offline statechart reproduction — 2026-11-09

All nine checked-in lane models were rerun with their documented configs; the
additional workload capacity-3 config was also checked. Every configured
invariant and deadlock check passed. TLC state/transition counts matched the
statechart summaries: existing DO release 299/1,036; Mini direct 82/164;
existing-Fleet Genesis 69/107; workload overlap capacity 3 24/52 and capacity
4 103/275; fresh DO create 308/813; future two-pool Fleet 45/98; nine-node
staging recovery 102/210; workload stop recovery 10/15; route publication 55/111.
Rendered charts and reports were written under `/tmp` with prefix
`norn-v3-statechart-` and suffix `20261109`; the checked-in specs, configs, and
views remain the durable reproduction source. These bounded runs verify the
models as written, not the controller implementation, live credentials,
providers, hosts, or availability.

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

Fresh-Fleet input inventory from the source audit:

| Domain | Existing producer/check | Remaining offline work |
|---|---|---|
| Provider and state | Operator supplies DO token and Spaces credentials. Apply rechecks active account UUID and exact state bucket/key/region/endpoint/versioning. Fleet PR #945 adds a source-derived, offline DO scope contract across persistent Terraform roots, direct API readers, and `DO_WORK_TOKEN` workflows; its CI passes, but it cannot inspect the live token's grants. | Before protected apply, require a current owner-reviewed scope readback from the DO control panel; retain only the nonsecret scope list, reviewer, and date. Add missing/stale-evidence holds and rotation coverage. Never include token values in receipts. |
| Plan authority | Protected plan run/artifact digest, Norn capacity plan UUID, one-use nonce, destructive acknowledgement; apply recreates plan and checks it before provider effects. | Exercise expiry, replay, changed artifact, stale state, lost apply response, and exact plan binding. |
| Network/SSH | Registered SSH fingerprint, protected runner private-key path, admin `/32`s, node host keys from first boot. | Synthetic absent/substituted key and firewall inputs; host-key freshness remains live read-only proof. |
| Current staging topology | Protected `norn-fleet` main declares 3 control + 2 ingress + 4 app Droplets (9 total), one app pool labeled `workload: app`. Provider inventory through explicit `work` context matches counts, tags, and sizes. Managed PostgreSQL and MySQL are separate database services. | Fresh protected state, node enrollment, control-plane, workload placement, and route health remain unverified. The nine-node model matches the count shape but does not prove workflow implementation or exact state recovery. |
| Future fresh-Fleet application pools | User selected two app pools for future new Fleets. Current DO staging remains one app pool and will not be migrated as part of this contract. | [Contract fields and recovery requirements](fresh-fleet-two-app-pool-contract.md) are recorded. Roles are general + stateful; stable IDs, sizes, min/desired/max, cost, placement, and workload/recovery semantics remain open before changing provider source. |
| Node/bootstrap identity | Protected runner OIDC and writer fence; reconciliation derives inventory from provider outputs and invokes Ansible. PKI is generated per cluster after create. | `norn-fleet` PR #941 joins synthetic provider outputs→inventory→local PKI and checks exact count, restricted modes, secret-free evidence, and repeat digests. It stops before SSH/Ansible; protected interrupted/repeated reconciliation remains open. |
| Service credentials | Many initial ACL, gossip, etcd, Norn, observability, registry, TLS, and optional Tailscale values are supplied through protected owner files. Nomad creates the scoped Norn identity; the initial Norn etcd token expires after 72h. | Draft `norn-fleet` PR #943 maps all 51 bootstrap paths across PKI, fixed Consul ACL, gossip, management authority, etcd identity, core Norn API/authority/signing inputs, ingress TLS, and conditional canary/registry/Grafana credential flows; it also records three controller-only CA keys and the generated Nomad Norn identity. PR #945 covers source-derived DO scope requirements offline only. A signed exact-accessor ensure/revoke path exists for the metrics token. Many fixed/service credentials still lack complete rotation/revocation procedures; the observed DO scope grants and provider-side revocation remain unverified. |

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

On 2026-11-08, a fresh explicit `doctl --context work` readback again showed
all nine NYC3 staging Fleet droplets active (3 control, 2 ingress, 4
app), the `tk-staging-nyc3-ingress` load balancer active, and the staging
management authority PostgreSQL plus application PostgreSQL and MySQL
databases online. The two management droplets were also active. This is
provider metadata only; it does not verify Nomad, Consul, Norn/API, workload,
or public/private route health.

The protected readback failure was still only reported as a generic source
capability hold. Fleet PR #940 now adds a local, credential-free diagnostic
that distinguishes a missing capability commit, missing target commit,
non-ancestor target, and missing target-tree markers. Its six focused tests
pass locally. Protected validation run `42488580934` passed its full contract
workflow, including plan/reconciliation and executor convergence/recovery; the
diagnostic is neither installed nor run against the management host. Fresh
protected state/route/host-key evidence and a new one-use signed intent remain
required before another readback.

On 2026-11-08, all nine committed Norn V3 statecharts were regenerated from
the current checkout into `/tmp/norn-v3-statecharts-current`; TLC reported
the committed state/transition counts and passed every listed invariant and
deadlock check. These remain bounded models, not proofs of provider, host,
credential-scope, or application behavior. The 51-path credential lifecycle
manifest still records unresolved provider scope and several service
rotation/revocation procedures as admission holds.

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
