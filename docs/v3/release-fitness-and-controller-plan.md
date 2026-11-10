# V3 release fitness and controller plan

Status: proposed execution order, reconciled against repository, provider-source,
and authenticated Mini evidence on 2026-11-10 06:30 UTC. This plan authorizes no
deployment or provider mutation.

The goal is to release Norn V3 through repeatable controllers with a small,
reviewable operator decision, complete input accounting, deterministic offline
recovery tests, and clear holds when evidence is missing. The Mini remains an
independent node. The DigitalOcean work Fleet remains the only Fleet and is a
lower-resource staging testbed.

## How to use this plan

Run the steps in order. A completed local model does not count as a live pass.
Each step ends with an evidence artifact and a gate. Keep `VERIFIED`, `NOT
VERIFIED`, and `INCONCLUSIVE` distinct. Never turn missing or stale evidence
into a pass. Do not dispatch a write workflow until its preceding gate passes.

## Current evidence and scope

| Area | Reconciled status | What remains unproven |
|---|---|---|
| Mini direct V3 | The independent Mini runs candidate A4 `a4b9c3b3…` with signed controller release `5bb691de…`. The existing generation `mini-v3-20261110T0021Z-a4b9c3b3` reached `candidate-promoted`; the normal API launcher and host-agent are restored. Authenticated platform smoke and host status pass. | Full workload health is not established: stopped/unhealthy apps, unknown background services, and active incidents remain. This does not qualify online mixed-version upgrades or a production release. See the [current Mini recovery record](LAUNCH-STATE.md#mini-direct-v3-generation-finalized-2026-11-10-0630-utc). |
| DO staging Fleet | Historical evidence includes the completed 72h35m soak and provider inventory of the nine-node staging topology. The latest DO evidence recorded in the launch ledger is a plan-only run and a protected state readback hold; neither changed infrastructure. | Current DO Norn/API, etcd, Nomad/Consul, route, and workload health remain unverified. The latest readback is not bound to current state and its enable gate is absent. Historical provider state and soak are not runtime proof. See [current DO staging evidence](LAUNCH-STATE.md#current-do-staging-fleet-evidence-2026-11-10-0323-utc). |
| DO Genesis/readback | The protected path remains fail-closed. Readback run [42119779657](https://github.com/antiartificial/norn-fleet/actions/runs/42119779657) fetched current protected backend state but failed because its bytes differed from the owner-reviewed digest. The latest Mini-reported Fleet API view contains only configured topology and historical read-only plans, not current provider or node health. | Refresh and bind exact state digest, lineage/serial, route, and host-key receipts; restore the protected readback enablement; then obtain current read-only evidence under a fresh one-use intent. Do not treat a successful connection to pinned addresses as current provider identity. |
| Fresh DO Fleet | The last recorded protected source check supports General and Stateful as future fresh-Fleet app-pool roles (`general`, `stateful`). Current DO staging remains one app pool; no fresh two-pool provider profile is configured. | Sizing, capacity, cost, placement, stateful recovery, exact provider identity, integrated persistent apply/recovery, bootstrap, and terminal receipt remain open. The owner-attested scope contract is offline evidence and does not prove actual grants on the runner token. Per-service credential lifecycle is incomplete; persistent staging has no whole-Fleet destroy lane. Genesis installs on existing infrastructure and does not create provider resources. |
| DO Norn release | The protected release runbook defines an all-stop, forward-only platform release path with exact artifacts, state, host keys, and a one-use runner. The latest recorded release run was cancelled before executing release steps. | Current health, fresh Genesis readback, a current end-to-end release receipt, and mixed Norn control-version operation remain unqualified. |
| Workload changes | Replica scale 2→3→2, app-node drain, and the 72h35m soak are historical evidence. Admission replay, successor-claim recovery, accepted-source/SOPS pinning, and receipt-bound encrypted-input cleanup have offline and real-etcd coverage at separate boundaries. All 27 checked-in statechart configurations passed their configured checks on 2026-11-10, including the Mini prior-controller handoff model. | Fresh authenticated HTTP admission joined to real etcd, real signed qualification and active-route lineage is open, as are full worker/controller process recovery, delayed production-driver behavior, live Nomad/ingress qualification, and mixed-version rollout on DO. Stateful writer/schema/data compatibility remains separate. |

The M0 evidence ledger is reconciled in [LAUNCH-STATE.md](LAUNCH-STATE.md).
Mini API health, generation finalization, workload health, DO provider
inventory, and DO runtime health are separate claims. Historical soak and
provider inventory do not establish present service health. Earlier records
remain as dated history; M0 completion means evidence is classified accurately,
not that the DO Fleet is currently healthy.

## Step 1. Reconcile the evidence ledger — M0 complete

Inspect the exact signed release, controller, recovery, state-readback, soak,
and workflow receipts in Norn and norn-fleet. Confirm whether the post-merge
Genesis readback actually ran. Refresh only protected read-only evidence for
the DO management route, three control APIs, etcd quorum, Norn capabilities,
and service health. Do not run an apply, restart, or release.

Record one row per claim with repository and commit, workflow/run ID, intent or
receipt digest, target hosts, timestamp, observed result, and confidence. Reconcile
the PR #939 status in `LAUNCH-STATE.md` with the merge present in norn-fleet.
Preserve the known timeouts and unclassified etcd failure as open findings until
a newer read-only diagnostic resolves them.

The latest post-fix readback is now known: run `42418784411` passed protected
input checks and private transport, then held because a required Norn source
commit is absent from the management host's protected repository. The error
does not identify whether the capability commit or target commit is missing.

**Gate:** the DO fleet is either `healthy and current` with fresh read-only
receipts or `held` with exact failing probes and no mutation. The soak is shown
as completed qualification evidence, not as present-tense health.

Current state: the soak is complete and explicit-context provider inventory
matches current main topology, but protected state identity and DO API/runtime
health are unconfirmed. The local `norn-pilot` `doctl` context belongs to a
different account; read-only inventory used explicit `work` context. Post-fix
Genesis readback is held on source-object availability. The latest run used
Oct 21 state/route files and failed read-only before any Genesis effect.

The local Fleet source now distinguishes “target commit absent” from
“capability commit absent” in this pre-effect check, with synthetic regression
coverage. That diagnostic change has not been installed on the management host
or merged and does not supply either missing object.

## Step 2. Freeze lane contracts and release boundaries

Maintain five separately named lanes:

1. **Mini direct:** independent single-node V3 upgrade on existing PostgreSQL.
   Its successful Nov 7 transition is the worked reference. New Mini releases
   use backup, private restore, exact signed candidate, writer proof, controller
   journal, preservation, postflight, and repair receipts.
2. **Existing DO platform release:** update Norn API/UI on the existing three
   controls. Keep the all-stop maintenance window and forward-only same-target
   recovery. Do not mix control binaries.
3. **Fresh DO Fleet bootstrap:** create or recreate provider resources, three
   controls, two default application pools, management services, keys, and
   runtime identities. Keep this separate from Genesis and routine release.
4. **Workload resize/release:** add or remove application capacity and roll
   workload versions using workload-specific compatibility, data, readiness,
   drain, and route rules. Do not use workload overlap as evidence for mixed
   Norn control versions.
5. **Online Norn control upgrade:** a future lane. It remains disabled until
   compatibility, leader fencing, quorum, writer epochs, interruption budgets,
   and crash recovery pass offline and on the DO testbed.

The fresh-Fleet path already has a provider plan/apply sequence. Audit and
compose its existing plan artifact/hash, durable capacity intent, one-use
dispatch nonce, runner, lock, provider account preflight, SSH inputs, OIDC and
writer-fence dependencies into one explicit operator-visible readiness
contract. Do not describe it as absent, and do not conflate it with Genesis.

For each lane, publish a typed intent schema and an input manifest. Each field
must name its producer, scope, freshness rule, transformation, consumer, secret
classification, rotation rule, redacted evidence, and recovery behavior. Reuse
account or network authority only with explicit scope. Never reuse host keys,
executor tokens, or runtime service credentials across independent Norns.

The source audit of the existing DO fresh-Fleet workflow found this concrete
input chain:

| Input/identity | Current source and transformation | Open fitness item |
|---|---|---|
| Approved plan and dispatch | Successful protected plan run/artifact SHA, Norn capacity plan UUID, one-use dispatch nonce, and destructive acknowledgement are rebound before apply. | Join into one typed intent/receipt; model replay, expired nonce, changed artifact, and lost apply response. |
| Provider/state authority | Supplied `DO_WORK_TOKEN`; supplied Spaces key ID/secret and backend coordinates. Preflight verifies active account UUID and exact versioned `norn-work-fleet-state` bucket/key/region/endpoint. `norn-fleet` PR #945 merged the offline provider-scope contract at `87cf8d8`; PR #941's war-game exercises synthetic scope admission. | Confirm actual DO token scopes against the documented least-privilege contract before a protected apply; provider-side token revocation and integrated apply evidence remain open. |
| SSH/firewall | Registered DO public-key fingerprint, protected runner private-key path, admin IPv4 `/32`s, SSH user, and per-run `known_hosts`. | Manifest provenance and host-key freshness/rotation receipt; assert no private key enters Terraform state/artifacts. |
| Topology | Protected `staging/nyc3` source at `4451767624b12cbc41cc32e160a10d4b46006e0f` declares 3 control + 2 ingress + 4 app Droplets (9 total); explicit-context provider inventory matches. Managed PostgreSQL/MySQL are separate services. | [norn-fleet PR #941](https://github.com/antiartificial/norn-fleet/pull/941) adds a war-game that calls production plan/account/backend/topology/inventory/recovery-guard/local-PKI functions with synthetic data and tests an eight-of-nine hold. Its exact command passes 81 tests locally; protected validation run `42477659325` passed. It does not prove protected apply/recovery, durable same-intent state/provider identity, or terminal receipt. Disposable-root saved-plan retirement is tested separately; persistent work-staging has no sanctioned whole-Fleet destroy lane. |
| Apply authorization | Protected runner, Norn API/OIDC audience, short-lived run-scoped token, writer-fence DSN, one-use plan/nonce. | Lost runner, token expiry, and durable same-intent recovery cases in integrated harness. |
| Host configuration | Post-create reconciliation reads provider outputs, builds inventory, validates owner-only bootstrap vars/SSH material, generates cluster PKI, then runs checked-in Ansible. `norn-fleet` PR #947 merged the rebased bootstrap credential lifecycle, sourced input manifests, and local preflight at `ef846e4`; it covers bootstrap inputs, generated PKI, and secret lifecycle checks. | The merged source mapping and local preflight do not prove protected host configuration, provider apply, or Genesis readback. Many service credentials still lack complete rotation/revocation ceremonies; confirm actual DO token scope and provider-side revocation separately. |
| Generated PKI/runtime | `prepare_pki.py` creates cluster-scoped Consul/Nomad/etcd CAs and node leaves, validates existing files, and refuses replacement; validity is 10 years for CAs and 825 days for leaves with a seven-day safety horizon. Nomad creates the scoped Norn token and controller journaling records its replacement; the create request sets no TTL. Norn's initial etcd credential expires after 72 hours. | Certificate rotation/revocation is not implemented; accepted Nomad token revocation requires a separate bounded procedure. Never reuse cluster/node identity across independent Norns. |
| Supplied service secrets | Protected directory supplies ACL/bootstrap tokens, gossip keys, etcd/Norn passwords, API/authority, observability/audit, registry/TLS, and optional per-node Tailscale keys. Ansible uses no-log/private inputs for sensitive values. PR #947's merged source-backed manifests and synthetic lifecycle fixture improve traceability and local preflight coverage. | The fixture does not contact real providers, SSH, or Ansible. Remaining service-specific rotation/revocation ceremonies, test mode/ownership and missing/expired-input cases across the full workflow, and secret-safe terminal receipts need integrated qualification. |

Existing focused tests cover account preflight, PKI generation, release
contract validation, Fleet hooks, and Consul/Nomad/etcd helpers. The
credential-free merged vertical slice in PR #941 exercises real plan
binding, account/backend checks, provider-output inventory, partial-inventory
hold, local PKI generation/idempotency, and synthetic output validation. It
has a specific eight-of-nine partial-output case. It passed locally as 81
unique tests after imported fixture `TestCase`s were changed to module
references and checkpoint tests were listed explicitly; protected validation
run `42477659325` passed before PR #941 merged as `09f05c6`. PR #947 later
merged the rebased bootstrap credential lifecycle and source-backed manifests
as `ef846e4`. These helper-level fixtures do not
establish protected apply or an integrated durable input-to-recovery fixture.
A separate
round-trip test invokes the production durable `converge` and `recover` handlers
with the existing local fake Norn/provider fixture for the existing Fleet's
exact app-03 scale effect: fake apply commits then loses its response, and
same-intent recovery reconciles to a terminal receipt without duplicating that
effect. It is not a nine-node fresh-Fleet create test. A separate synthetic
credential fixture joins source mirroring, scoped grant use, interruption
cleanup, retry, and secret-safe preflight attestation; service-specific token
rotation/revocation is still unproven. None of these fixtures contacts real
providers, SSH, or Ansible. The full GitHub create workflow, durable
partial-create recovery for the adopted topology, exact provider/state postflight, and
protected runner behavior remain uncovered. Extend those workflow seams
before live provisioning.

The setup utility previously omitted `NORN_WORK_FENCE_WRITER_DSN` even though
the staging apply workflow consumes it. Setup now configures and checks this as
an apply-only `work-staging` secret; the plan and production environments do
not receive it. Focused setup workflow tests cover that boundary.

**Gate:** no workflow name or runbook describes an existing-Fleet Genesis
install as fresh cloud provisioning. Each lane has a complete list of required
and generated inputs, and unknown inputs fail closed before effects.

## Step 3. Complete the credential-free input and transformation war-game

Extend the existing bootstrap inventory into executable temporary-directory
fixtures. Reuse real validators and serializers where possible. Use synthetic
keys and tokens only. Never print values or copy production secrets into test
fixtures.

Cover these chains end to end:

- DigitalOcean project/token scope, protected Terraform state and backend
  credentials, quotas, region/image availability, SSH bootstrap identity, and
  exact resource plan for fresh-Fleet creation.
- Owner-signed intent, public-key pin, exact source/archive, route/state/host
  key receipts, Ansible inventory and vars, controller install, disabled systemd
  units, and first activation authorization.
- Per-host and per-deployment CSPRNG credential generation, one-way delivery,
  destination owner/mode, process use, nonsecret receipt, rotation, and revoke.
- Norn release signatures, publisher run metadata, old/new artifact digests,
  runner identity, operation journal, API receipt, and controller/helper version.
- Application image/config, schema epoch, database bindings, replica/capacity
  intent, route and drain receipts, plus data-safe rollback boundary.

Include missing, stale, malformed, wrong-scope, substituted, replayed, expired,
permission-broad, symlinked, partial, and interrupted inputs. Assert that a
blocked plan creates no provider request or remote effect. Assert that reports
contain only paths, roles, digests, fingerprints, and failure reasons, never
credential bytes. Make randomness deterministic only in tests; production
generation must use the target operating system CSPRNG.

**Gate:** each lane has a green synthetic happy path and red cases for every
required input and trust boundary. The report distinguishes “input is valid”
from “live credential or route was exercised.”

## Step 4. Model controller state and recovery offline

Keep one bounded model per lane. Five are now checked into [the statechart
bundle](statecharts/README.md): existing DO all-stop release, Mini direct
migration/writer fencing, Genesis install/activation on existing controls,
fresh-Fleet create/partial recovery, and proposed workload mixed versions.
Model online Norn upgrade only after its compatibility and fencing contract is
reviewed. Keep these as separate state spaces.

For each model, name invariants before checking transitions. At minimum prove:

- no external effect without a fresh, exact signed intent and matching input
  digests;
- one owner and one live executor per intent, with durable idempotency across
  controller restarts and lost responses;
- no destructive step while state, host identity, quorum, writer inventory, or
  target bytes are unknown;
- no old Norn binary starts after target writes or target service promotion;
- no fleet resource is deleted until its exact identity is reconciled and
  dependent workloads are absent or moved;
- workload scale-down waits for target health, acknowledged drains, and durable
  data/route evidence;
- every crash point either resumes the exact intent, safely reconciles an
  already-completed effect, or remains held for a named operator action.

For each TLC counterexample, replay its steps against a real unit/integration
test before changing the model. Keep the Mermaid chart, TLA+ source, config,
report, and model command together. State the omissions, such as provider
eventual consistency, host process semantics, timing, and real key custody.

**Gate:** all named invariants hold for bounded models, all injected crash and
replay cases pass against controller code, and every model omission has an
explicit live qualification or accepted limitation.

## Step 5. Clear DO staging read-only blockers

Once the input war-game passes, run only authenticated read-only checks through
the pinned management host. First produce fresh protected provider-state,
route, and host-key receipts, then prove that their roster matches the three
reachable controls. The current Genesis readback workflow hardcodes the Oct 21
state and route input paths, so update its reviewed pins before another run.
Then resolve private API timeouts and the unclassified etcdctl health result.
Verify exact control identities, singleton API, etcd quorum and alarms,
release/capability identity, executor token validity, storage headroom, route,
runner labels, and protected file presence.

For Genesis specifically, prove that both `platform_controller_capability_commit`
and the signed intent's `newSourceSha` exist in the protected management Git
repository before dispatching another readback. The current install playbook
checks the repository path and ownership, while Genesis checks commit objects
only during remote readback. Add a read-only object-presence preflight and
synthetic missing-object cases so this failure is caught before another
protected run.

For every blocker, retain a safe diagnostic receipt before a repair. Record the
failure point, invariant, root cause, least-risk repair, rollback boundary, and
offline regression. Do not use a release run to diagnose infrastructure.

**Gate:** all controls and the private API pass current health and identity
readbacks, Genesis readback passes, and the protected runner input manifest
matches the reviewed digests. Otherwise stay held.

## Step 6. Qualify the existing DO Norn release controller

Use a signed candidate that includes the Genesis intent/import capability. Run
the offline controller tests first, then a protected dry readback, then one
reviewed all-stop Norn release on the staging Fleet. Preserve a measurable
maintenance window and verify exact API/UI source on every control, one serving
API, etcd health, capability endpoint, durable pins, runner retirement, and a
signed terminal receipt. Exercise crashes before and after every remote effect
using the deterministic harness before any retry.

Repeat recovery with the same target and prove idempotent convergence. Inject
unknown source bytes and stale host keys and prove the controller holds before
effects. Do not claim mixed-control-version support.

**Gate:** the protected release receipt and independent postflight agree on
target, state, hosts, health, singleton service, durable pins, and runner
retirement. A successful earlier soak remains separate evidence; perform a new
soak only if a release-relevant component changed or the existing soak does not
cover the candidate.

## Step 7. Qualify workload resizing and mixed application versions

The source-backed candidate order and separate offline/live qualification
contracts are recorded in the [M5 workload protocol](m5-workload-release-protocol-2026-11-07.md).

Keep the Norn control release fixed. Select one stateless web workload and one
stateful or worker workload. Record their actual database, queue, file, schema,
and route contracts. Add capacity before placement changes. Use an additive
schema change and explicit writer epoch where old and new app versions overlap.
Canary, verify data and request invariants, drain with acknowledgements, observe
the mixed-version period, retire old allocations, and shrink capacity last.

Inject allocation loss, node loss, stale placement, duplicate retry, failed
drain, database cutover interruption, and route publication failure. Prove that
controller retries converge and that rollback stops at the documented data
compatibility boundary. Test replica resize separately from node-pool resize.

**Gate:** the target app remains correct through mixed versions, restart, drain,
and resize; no writer or allocation is lost; scale intent and observed count
agree; recovery evidence is durable. Only then offer online workload resizing
for that declared workload class.

## Step 8. Decide whether to build online Norn upgrades

Keep the existing all-stop DO lane as the supported default. Use the online
proposal only after an explicit compatibility matrix shows the old and new
APIs can share the expanded store. Define minimum reader/writer epochs, active
member fencing, one-member promotion, etcd quorum checks, lease expiry, API
interruption budget, client reconnect behavior, and a point after which recovery
is forward-only.

Model those transitions and inject leader loss, partition, API response loss,
slow member catch-up, incompatible writer, lease expiry, and controller crash.
Then qualify one member at a time on DO with an agreed request/load profile.
If any old writer can accept incompatible data, retain all-stop and mark online
upgrade unsupported.

**Gate:** compatibility invariants and recovery pass both the model and the
staging fault exercise, with separately measured API availability, write pause,
and workload interruption. “Online” is not a label for a successful rolling
restart.

## Step 9. Remove V2-only surfaces after V3 adoption gates pass

The source-backed [V2 surface retirement inventory](v2-surface-retirement-inventory-2026-11-07.md)
found no general `/api/v2` namespace. The V3 build still uses `v2/api` and
`v2/cli` module paths; checked-in CLI/UI clients still call unversioned `/api`
routes, and a protected `norn-fleet` runner still calls
`/api/v1/.../effect-grants/v2`. Inventory older handlers, adapters, flags,
docs, CI jobs, fixtures, and tests in the V3 branch. Migrate supported callers
before removing an older handler or V2-only runtime implementation. Keep
compatibility code only where needed to import or upgrade existing V2 Mini
data, and label it as migration support. Do not remove the effect-grant route
or its read/recovery behavior until the protected caller has a qualified
replacement and saved V2 grant records have passed their recovery window.
Preserve migration fixtures and forward-recovery tests needed to prove safe
V2-to-V3 adoption.

**Gate:** each removed route or protocol has no supported callers, replacement
V3 contracts and behavior tests pass, migration fixtures still prove safe
adoption, and the release matrix passes from the supported V2 baseline and
current V3 source. Do not use package or test names as a deletion rule.

## Definition of done

The system is ready for hands-free V3 operation when all of these predicates
hold:

1. Every supported lane has an explicit input manifest, owner, freshness rule,
   credential lifecycle, side-effect list, recovery table, and tested controller
   intent schema.
2. Offline war-games cover successful, invalid, stale, replayed, interrupted,
   and ambiguous cases without reading live credentials or contacting providers.
3. State models pass their invariants and model counterexamples are replayed
   against tests. Model omissions have named live evidence.
4. Live staging readbacks prove current identities, health, route, quorum,
   source, and runner state before a write workflow is enabled.
5. Each released controller path has a protected end-to-end receipt, independent
   postflight, same-intent recovery proof, and a documented operator action for
   states it cannot safely resolve itself.
6. One operator action starts each ready workflow. The controller performs
   staging, dispatch, monitoring, reconciliation, and recovery automatically.
   The operator sees one `READY` or `BLOCKED` result with evidence and a concrete
   resolution, plus an explicit approval for irreversible infrastructure or
   data transitions.
7. Runbooks and status pages agree with the latest receipts. V2-only runtime
   support is removed only after V3 migration and adoption gates pass.

## Priority and sequencing

Do Step 1 first because status drift can make later evidence misleading. Do
Steps 2–4 before any expensive provider or host transition. Then clear the
read-only DO blockers in Step 5. Qualify the existing DO release in Step 6,
workload resizing in Step 7, and only then decide whether online Norn upgrades
earn implementation. Perform the V2 cleanup in Step 9 after the Mini direct and
DO staging V3 adoption gates are both recorded.

The most useful immediate deliverable is not another deployment. It is a
current DO health and Genesis readback receipt, followed by a synthetic input
and crash matrix that demonstrates which exact inputs and recovery steps still
need work.
