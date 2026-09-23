# Norn v3 execution milestones

Status: planning baseline, 2026-09-22. Scope incorporates the user's Mini/Fleet clarification. Implementation, provisioning and live cutovers have not started. Detailed ADRs remain proposed where decisions are unresolved.

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
- Measure PG table/index sizes, growth and connection use; classify all 24 tables and non-SQL stores. Record archive, encryption/signing keys and restore requirements without printing credentials.
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

Gate: VM growth independently from replicas; redeploy preserves desired scale; placement matches policy; 2→3→2 under load; failed drain blocks retirement; N-1 and rollout reserve fit actual allocations. Worker drain preserves acknowledged work. Node addition alone is not a rebalance guarantee.

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

All M0–M9 implementation gates are pending. Documents establish the plan, not test results. Immediate next work after planning is M0 baseline/decision closure and then M1 review units; runtime mutations require their own named execution scope.
