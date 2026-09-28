# Norn v3 execution milestones

Status: integration under review in draft PR #77 on `codex/v3-master-integration`, 2026-09-27. Scope incorporates the Mini/Fleet clarification. Protected Fleet provisioning and live cutovers have not started. Detailed ADRs remain proposed where decisions are unresolved.

## Release contract

Initial v3 must pass both paths:

1. Existing single-machine Mini: v2/PostgreSQL → v3/PostgreSQL, preserving running workloads and application data.
2. Empty DigitalOcean HA Fleet: fresh v3 with three etcd members on the control nodes and independent application databases.

These are engineering milestones within one v3 release, not mandatory intermediate production releases. No general PG↔etcd control conversion is required to complete either path. Keep that as a later capability. A targeted v2 prerequisite release is needed only if the inspected Mini version cannot safely upgrade directly.

Application migration is separate from platform upgrade. The first release qualifies moving a representative Mini application to Fleet; moving every app is not a release gate. Keep the Mini and Fleet as independent control-plane identities. Transfer app definitions/data and selected provenance with an explicit mapping; do not clone the entire Mini control database into the live Fleet or run both as the same authority.

## Milestone order

| ID | Outcome | Depends on | Accountable repositories | Exit evidence |
| --- | --- | --- | --- | --- |
| M0 | Freeze initial contracts and establish Mini baseline | Planning review | Norn, Fleet, NornUI | Reviewed decision register, source/runtime inventory, fixture and budget plan |
| M1 | Shared control semantics and working PG adapter | M0 | Norn | Store invariant tests, ownership fixes, old-data compatibility |
| M2 | Shared profiles, database bindings and bounded history | M1 | Norn, Fleet, NornUI | Local profile integration, binding resolution and archive/replay tests |
| M3 | Etcd-backed control plane and fresh HA bootstrap | M1; M2 archive contract | Norn, Fleet | Three-member bootstrap/restore, authority/fencing and quorum tests |
| M4 | Fleet app pools, persistent scaling and replacement | M1; M2 profiles | Norn, Fleet, NornUI | Verified placement, 2→3→2 nodes, replica growth and safe drain |
| M5 | Rehearsed Mini v2→v3 upgrade on PG | M1–M2 | Norn, NornUI | Isolated upgrade/rollback from representative Mini state; jobs and routes unchanged |
| M6 | Running upgrades and app database lifecycle | M2–M4 | Norn, Fleet, app owners | v3→v3 rolling rehearsal; app DB upgrade/cutover and interrupted recovery |
| M7 | First Mini→Fleet application migration rehearsal | M4–M6 | Norn, Fleet, selected app repo | Data/work reconciliation, traffic cutover, rollback/recovery boundary |
| M8 | v3 release qualification | M3–M7 | All release owners | Signed candidate, client parity, version matrix, soak/fault results and operator runbooks |
| M9 | Controlled adoption | M8; explicit rollout scope | Operators + release owners | Mini upgrade, clean Fleet deployment and selected app migrations independently verified |

After M1, profile/archive, etcd and scheduling work can overlap at agreed interfaces. M5 proceeds without a live DO Fleet. Provider-backed tests follow local/Linux isolated qualification; their budget and exact targets are established before provisioning. M8 qualifies the package; M9 is actual deployment, not an automatic side effect of completion.

## M0 — Decisions, baseline and fixtures

- Record deployed Mini binary/source/schema versions, supervisor configuration, app specs, enabled/disabled state, actual job IDs/counts, routes/ports, secrets references and volume/database ownership. Prior conversation snapshots are not sufficient.
- Measure PG table/index sizes, growth and connection use; classify all observed control tables and non-SQL stores. Record archive, encryption/signing keys and restore requirements without printing credentials.
- Build a sanitized representative fixture for routine CI. Separately plan a private restore rehearsal for fidelity, with denied production network egress and all workers/webhooks/cron/provider mutation disabled.
- Resolve scale precedence: proposed rule is a revisioned desired-scale object seeded by import, with explicit scale changes retained across code deploys. Applying a changed declared scale is an explicit configuration operation with conflict detection, not an implicit deploy reset.
- Decide API handoff contract, pool/ingress roles, resource schema ownership, first app engine support, diagnostic budget and log backend spike. Draft numerical targets in planning-contracts are not yet accepted SLAs.
- Assign named owners and PR boundaries. Re-estimate at M1/M3 based on actual work; no calendar commitment before the baseline and spikes.

Gate: review-ready contracts and measurable targets, verified restoration strategy, no implicit ownership ambiguity. Scope decisions can be accepted independently of unmeasured targets.

## M1 — Shared control behavior

Suggested review units:

1. Domain interfaces and a PG adapter preserving observable behavior; inventory and eliminate bypassing direct SQL at control boundaries.
2. Atomic acceptance/idempotency/audit intent and explicit execution ownership/fencing contract.
3. Owner-aware exec-session and operation recovery, including candidate startup with workers disabled.
4. Versioned additive schema migrations with one migration owner, compatibility guard and rollback window.
5. Canonical backup/export format for inspection and disaster recovery; stable identifiers and original receipt/signature bytes.

Gate: concurrent request/recovery tests, ambiguous response reconciliation, no global failure of healthy sessions on candidate startup. Any downstream action that cannot be fenced has a defined reconciliation or proven-stop rule.

Rollback: retain old readable schema; do not contract fields during the Mini upgrade window. Export format does not imply a supported cross-backend converter.

## M2 — Profiles, database references and retention

Suggested review units:

1. Local-PG and Fleet-etcd profiles with common logical resource names and independent control-plane identities.
2. Database service/binding resolver shared by apps, workers, migrations, backups, restore and probes. Preserve legacy Mini connection resolution through an explicit compatibility adapter.
3. Allocation/task-aware logs, node rotation and bounded collector spool; authenticated historical queries.
4. Evidence archive/outbox and verified pruning; event replay expiry/resync; local filesystem and Fleet object adapters.
5. Capability/status UI exposing current profile/backend, desired vs observed scale, binding generation, archive health and unsupported operations.

Gate: an unchanged imported Mini app resolves to its existing DB and route; a new Fleet app resolves independently; no secrets leak in exported topology; evidence survives archive/index recovery. Ordinary WordPress uses a MySQL adapter. CockroachDB is deferred.

Rollback: disable collection/pruning independently; retain compatibility reads for archived records. Binding changes never silently rewrite live application connections.

## M3 — Etcd and fresh Fleet

Suggested review units:

1. Etcd adapter with shared invariant suite, bounded keys/indexes, lease-independent operation records and revisioned event cursors.
2. Host-supervised member bootstrap, TLS, membership changes, snapshots, compaction, monitoring and offline restore.
3. Provider-independent readiness for control backend; remove mandatory SQL probes from etcd deployments without weakening admission.
4. Fresh Fleet workflow creates three control members, independent discovery/scheduler groups and evidence/log destinations. Bootstrap identity/keys without depending on a functioning Norn queue.

Gate: fresh bootstrap and full restore without control PG; one member lost; quorum lost; stale executor; corrupted snapshot; disk/quota alarm; archive outage. Verify no accidental default/local PG dependency anywhere, including host agents and tools.

Rollback: disposable rehearsal topology can be recreated from supported backups. Production member changes require quorum-safe recovery; binary downgrade support is version-specific. Do not replace all control voters together for a generic blue/green VM policy.

## M4 — App capacity and placement

Suggested review units:

1. Role-based pool schema, inventory/bootstrap/LB wiring and actual Nomad pool enrollment, including arbitrary names.
2. Durable replica intent and drift visibility; scale/config precedence; explicit per-process placement and identity mapping where jobs split.
3. Required/preferred host spreading, resources, graceful drain, web/worker priorities and stateful-storage constraints.
4. Generation-based surge/join/readiness/drain/retire executor with exact node identities and interruption recovery.

Gate: VM growth independently from replicas; redeploy preserves desired scale; placement matches policy; 2→3→2 under load; failed drain blocks retirement; N-1 and rollout reserve fit actual allocations. Worker drain preserves acknowledged work. For routed services, bind the deployment's signed InfraSpec and accepted region to a server-selected Fleet plan and durable prior route, persist a generation-fenced route intent, then prove every ingress node and the public endpoint before recording an active traffic weight. A desired weight in Nomad or Consul metadata is insufficient. Node addition alone is not a rebalance guarantee.

Rollback: retain prior node generation until replacement readiness; restore desired revisions through reviewed operations. Restore old job identity only through a declared mapping/traffic handoff.

## M5 — Mini upgrade rehearsal

Use a copied control DB and private copies of specs/state. The rehearsal must not hold usable production mutation credentials or network access to production services. An isolated Nomad/Consul environment proves translation compatibility; read-only identity comparisons capture the live workload preservation requirement.

Rehearse backup → drain Norn operations → install candidate → additive migration → health/auth/history validation → release switch → worker resumption → smoke. Upgrade API/agent consumers compatibly. Keep existing application PG, app data, Nomad allocations and routes in place for the real upgrade; no fleet adoption or app redeploy is implied.

Gate: original IDs/credentials/receipts survive; no duplicated periodic work; new deployment and recovery operate correctly on fixtures; old release can return while schema remains compatible. Record control interruption separately from application request availability. A single-host reboot still interrupts that host's workloads.

Output: exact supported source version, signed target candidate, commands/runbook, backup manifest and rollback conditions. Live Mini cutover is M9.

## M6 — Running upgrades and app databases

Qualify Norn v3 version A→B under traffic, rather than only first installation. Prove candidate isolation, session/worker ownership and compatible old/new schemas. Upgrade Nomad, Consul, etcd and OS independently, one voter/node at a time with readiness checks. Do not label single-active handoff as continuous API service unless tests meet the agreed target.

Define app consistency groups and a migration journal outside the DB being changed. Provision target, validate engine/extensions/roles/networking, rehearse restore, fence all web/worker/cron writers, perform final synchronization, update consumer generation, reconnect, verify, then retain source for the acceptance window. DO managed-PG-to-managed-PG online migration requires a separately qualified path; dump/restore is the baseline.

Gate: PG and selected MySQL adapter backup/restore; planned engine upgrade or cluster replacement; ambiguous commit/retry handling; process crashes at every cutover checkpoint; no old writers after activation. After target writes, return requires reverse reconciliation, not merely old credentials.

Control-PG replacement on the Mini and general PG↔etcd conversion may follow later; app database cutovers are required here.

## M7 — Application mobility

Select one representative low-risk database-backed application with explicit data/file/queue ownership and synthetic traffic. Include web and worker behavior in the fixture; prove other storage patterns separately before offering migration support for them.

Export definition plus selected provenance, map its identity into the independent Fleet, provision target dependencies and copy app data/files. Keep target workers/schedules disabled until cutover. For final activation, quiesce/fence source writers, synchronize, enable one target ownership generation, verify data/work, then switch traffic and remaining consumers. Delayed DNS propagation means the source must remain safely read-only/proxying/maintenance-mode as designed; DNS alone is not a write fence.

Gate: data and acknowledged work reconcile; auth/URLs/files function; no duplicate schedules; source rollback before new writes and recovery after new writes are rehearsed. Preserve unrelated Mini workloads. Record which application data/engine classes are actually supported.

## M8–M9 — Release and adoption

Release evidence includes exact Norn/Fleet/NornUI/runner and upstream versions, workload fixtures, topology and resource reservations, request/error/latency results, operation/effect reconciliation, retention growth and restore records. Etcd soak and failure qualification are initial HA release gates. Any deferred capability is explicitly absent/experimental in negotiation.

Adoption has three independent steps: upgrade Mini on PG; deploy empty DO Fleet on etcd; move selected apps. Schedule them separately. A failed Fleet launch must not force a Mini rollback; a successful Mini upgrade must not be mistaken for HA qualification.

General cross-backend conversion, local single-member etcd, Fleet-PG qualification, autonomous cloud scaling, multi-region HA and additional database engines remain later milestones unless scope is explicitly expanded.

## Execution status

As of 2026-09-28, **0 of 10 M0–M9 gates are signed (0%)**. This is the release
gate completion ratio, not an estimate of code completed. The integration is
under review in draft PR #77; passing local tests and CI do not sign a gate.

For planning, the estimated implementation progress on 2026-09-28 is below.
These are judgment estimates rounded to 5%, based on the breadth of working
implementation and evidence relative to each exit. They are not release
sign-offs, schedule forecasts, or a substitute for the gate evidence below.
The simple, equally weighted mean is about **32%**; later Fleet and adoption
milestones may require disproportionate time.

| Milestone | Estimated work complete | Main remaining boundary |
| --- | ---: | --- |
| M0 | 60% | Off-host recovery target and protected restore |
| M1 | 65% | Protected runtime and remaining effect recovery proof |
| M2 | 55% | Real-provider retention and separate-node restore |
| M3 | 50% | Protected multi-host bootstrap, faults and soak |
| M4 | 40% | Normal app execution, ingress authority and loaded Fleet proof |
| M5 | 25% | Representative Mini upgrade and rollback |
| M6 | 10% | Rolling upgrade and app database cutover rehearsals |
| M7 | 5% | Complete app mobility and traffic rollback rehearsal |
| M8 | 10% | Signed candidate and release qualification |
| M9 | 0% | Controlled adoption after qualification |

| Gate | Current disposition | Evidence and next qualification boundary |
| --- | --- | --- |
| M0 | Open | Mini baseline and private restore rehearsals exist; off-host backup RPO, destination, retention and owner decisions remain unresolved. |
| M1 | Open | Control ownership, acceptance, effect and recovery slices have local tests; complete cross-process and downstream-effect qualification remains. |
| M2 | Open | Profile, database binding, MySQL and archive slices have local evidence; unchanged imported Mini behavior and complete retention/recovery gate remain. |
| M3 | Open | [Three-member TLS/RBAC etcd catalog rehearsal](m3-etcd-catalog-three-member-qualification-2026-09-27.md) passed locally, including quorum loss and full restore. Host-supervised Fleet bootstrap/repair, multi-host faults, alarms, rotation, upgrades and soak remain. |
| M4 | Open | [Private app admission](m4-etcd-app-admission-sequence-2026-09-26.md), the [normal release admission contract](m4-normal-release-admission-contract-2026-09-27.md), a [two-ingress local weighted-route and rollback fixture](m4-local-traefik-weighted-route-fixture-2026-09-27.md), and the [ingress publisher contract](m4-ingress-node-publisher-contract.md) exist. A first-route transaction reserves generation one against the signed deployment, pinned InfraSpec, control-owned Fleet target and active ingress inventory. A non-CI platform operator can now configure that target with revision fencing and a validated Fleet document; the create/read/stale-replace path passed disposable-etcd tests. A disposable-etcd test carries the route intent through a claimed private mTLS listener to a local node file and rejects later authorization after inventory replacement. The private terminal transaction now accepts positive active weight only with a stored, signed-source traffic proof, completed Nomad health effect and revision comparisons for route, acceptance, target and Fleet inventory; a synthetic disposable-etcd test covers success, invalid public proof and inventory replacement. The private acceptance-to-Nomad test passed against disposable local etcd and Docker-capable Nomad on 2026-09-27, including real healthy allocation readback and refusal to complete positive traffic without ingress proof. An opt-in normal etcd worker composes the claimed job and route steps. An independently opt-in staging release route now derives its signed `app.deploy` aggregate from verified CI identity, artifact admission, checked-out InfraSpec, control-owned Fleet target and active database catalog; a disposable-etcd HTTP test covers acceptance, scoped status, exact replay after token rotation, target replacement refusal during active work, and a claimed signed Nomad job plan. A separate opt-in test carried that normal HTTP acceptance through a claimed operation, managed input staging, a real healthy allocation on disposable local etcd/Consul/Nomad, and retained the nonterminal ingress gate; a successor claim reused the submitted job without a second Nomad registration. Its signature and vulnerability verifier hooks were synthetic. Signed artifact verification and authenticated ingress publication/traffic through the normal executor remain unqualified. An optional real public-registry digest check passed locally; signature and vulnerability policy were synthetic. An optional two-Traefik disposable rehearsal routes the accepted release's healthy Nomad allocation through both local ingresses, rejects partial publication and withdrawal, and observes HTTPS 404 after full withdrawal; it does not use protected publisher authority or a public load balancer. This is local control-path evidence, not a protected public traffic proof. Fleet draft [PR #177](https://github.com/antiartificial/norn-fleet/pull/177) stages a disabled publisher service with a separate account; 1,414 local Python tests, Ansible syntax, schema validation, action lint, and all six pinned OpenTofu validation roots passed ([receipt](m4-fleet-pr177-local-validation-2026-09-27.md)). Its hosted contract job failed before any runner steps: GitHub annotated the job with an account billing/spending-limit block. The temporary Linux X64 self-hosted CI fallback is not currently configured or registered, so this PR has local validation but no hosted contract receipt. Fleet now permits a reviewed third staging ingress client while holding desired capacity at two. Its isolated empty-state OpenTofu comparison adds only `ingress-03` for the proposed three-node shape; a local hook test emits all three private members with a matching snapshot digest, and Norn parses that exact membership while rejecting the prior two-node digest. These are configuration and local contract proofs, not a protected capacity plan or live 2→3→2 rehearsal ([Fleet readiness review](https://github.com/antiartificial/norn-fleet/blob/codex/fleet-ingress-readback/docs/runbooks/staging-v3-protected-rehearsal-readiness-2026-09-28.md)). Reviewed deployment admission, complete protected-node publication, public load-balancer proof, partial-publish recovery, placement, scaling and drain under load remain. |
| M5 | Open | The [latest private Mini copy](m5-mini-private-copy-schema47-2026-09-28.md) preserved 28 original tables and 261,547 rows through schema 47 and passed passive startup without changing live jobs or routes. The CI upgrade fixture boots a passive candidate for schema-safe preflight, imports and verifies an ephemeral signed release before preflight, rejects an incomplete fetched release, and checks promotion-lock contention. Protected production-key backup/restore, the maintenance transition, representative current-state upgrade and rollback with a production-signed candidate remain. |
| M6 | Open | The [application database cutover plan](m6-app-database-cutover-implementation-plan.md) defines the first PG path, authority generations and crash checkpoints. Private PG/etcd journals have disposable CAS, replay, active-resource uniqueness, migration and recovery-inspection evidence. Both backends require a signed exact-intent operation and live claim for private journal prepare/advance; etcd compares the fenced app lock. Claimed preparation binds the active catalog revision and exact source/target logical bindings, and claimed quiesce/final-sync writes refuse catalog drift. Claimed activation now refuses a well-formed reference without independently verified external proof and a generation-bound consumer switch; disposable PostgreSQL and etcd tests confirmed the journal stays at final-sync. Generic `database.cutover` success is refused. A catalog-bound private PG role fence passed disposable existing-session termination, exact-role readback, retry and verify-full TLS refusal with a wrong CA. The read-only preflight checks delegated authority, declared/observed server major, runtime login state, exact-role sessions and current database client sessions by role. A [read-only Nomad inventory](m6-m7-first-app-shape-review-2026-09-28.md) now reports regional jobs and live allocations from the stored app spec, including old desired-stop writers, but marks external writers unverified and promotion unready. A [local fixture transfer](m6-local-mobility-transfer-2026-09-28.md) passed a baseline restore, final full replacement and exact row/job/tick/file comparison between two disposable databases. The [first provider-pair review](m6-first-provider-pair-review-2026-09-28.md) recommends a separate managed PostgreSQL 17 application target for a Mini-hosted synthetic fixture; no provider or live app was selected or provisioned. External receipts, complete writer inventory, provider privileges, coordinator, selected-provider transfer lane, consumer switch, running upgrade, and PG/MySQL cutover/recovery rehearsals remain. |
| M7 | Open | First complete Mini-to-Fleet application mobility rehearsal remains. |
| M8 | Open | Candidate qualification, version matrix, soak/fault evidence and runbooks remain. |
| M9 | Open | Mini upgrade, clean Fleet deployment and selected app migrations require separate controlled adoption. |

The [2026-09-28 read-only Mini refresh](m0-mini-baseline-refresh-2026-09-28.md)
still identifies release `a5da8ef15d12e9eca7561e90b90d96f6dc652a21`,
an empty active operation queue at collection time, and PostgreSQL WAL
archiving off. It does not alter the M0 or M5 estimates or sign either gate.
The [latest candidate private Mini copy](m5-mini-private-copy-schema47-2026-09-28.md)
passed schema 47 and passive startup against 28 original tables and 261,547
rows, with unchanged live job and route-config fingerprints. Protected backup,
off-host restore and the maintenance transition remain M5 work.

The [read-only first-app shape review](m6-m7-first-app-shape-review-2026-09-28.md)
found a PostgreSQL 17 source-major observation for `turnkey-offer-intake`,
but did not select a live app or complete its writer inventory. The
deploy-disabled synthetic mobility fixture remains the first M6/M7 rehearsal.

The opt-in normal HTTP-to-Nomad local rehearsal has a
[repeatable disposable runner](m4-http-to-nomad-local-qualification.md).
First-route admission now rejects an InfraSpec without the signed endpoint
probe needed to complete its traffic proof.
The claimed executor checks active ingress inventory and its exact node
identity map before resolving databases or changing Nomad; the durable route
intent revalidates that inventory after job health.
An opt-in 2026-09-28 disposable Linux rehearsal now carries a signed-source
first-route intent from etcd through the real mTLS authority and two private
publisher handlers. It observes one route file after a refused second node,
retains the running operation, retries both nodes, reads back both route files,
and creates the fixture's durable traffic proof only after publication. Partial
publication and a failed public probe leave no proof or terminal deployment.
The traffic observation is synthetic; this does not qualify effective Traefik state,
the public load balancer, or a protected Fleet deployment.
Fleet PR #177 has a [local validation record](m4-fleet-pr177-local-validation-2026-09-27.md)
for 1,414 Python tests and Ansible syntax; GitHub's contract job remains
unstarted because of account billing or a spending limit.

[Implementation status](implementation-status.md) and [resume handoff](RESUME.md)
retain the earlier bounded implementation checkpoints. This table governs only
the formal milestone percentage; each gate needs its own full-scope evidence
and owner sign-off before its disposition changes.
