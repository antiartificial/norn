# Norn v3 architecture and delivery roadmap

Status: proposed implementation plan, 2026-09-22. No infrastructure changes or migrations authorized by this document. Baseline inspected: Norn `7304f39`, norn-fleet `56e0f7d`, including the current working trees. Existing local changes were preserved.

## Planning boundary and release scope

This roadmap is the review artifact before implementation. Current authorization covers planning and documentation only. No application changes, provider provisioning, production measurements, migrations, or deployments are part of this planning pass.

Norn v3 has two initial release gates: upgrade the existing Mini from v2 to v3 on PostgreSQL while preserving its workloads, and bootstrap a fresh HA DigitalOcean Fleet directly on three-member etcd. Both use the same control contracts. PostgreSQL remains supported for the Mini; an upgrade does not force a backend switch or workload relocation. The target release includes independent observability retention, named database bindings, persistent app scaling, explicit placement and coordinated upgrades/application migrations. General PG↔etcd conversion is deferred.

Product, wire API, Fleet document, InfraSpec, object format and database schema versions are independent. Preserve supported `fleet/v1` documents; introduce a new document version only for incompatible semantics. Advertise optional capabilities instead of assuming a v3 client implies every backend/provider feature exists. A roadmap under `docs/v3` does not require duplicating the source tree into `v3/`.

Out of scope for initial v3: Kubernetes replacement, generic database operator support, CockroachDB production qualification, active-active multi-region writes, automatic cloud scaling without policy/review, and universal zero-interruption database upgrades.

## Decisions to settle before implementation

| Decision | Recommended starting position | Evidence/review needed |
| --- | --- | --- |
| v3 backend support | PG for Mini and etcd for fresh Fleet are initial release requirements | Shared invariant suite and full Fleet restore without PG |
| Local consolidation | One local PG initially; same logical resource identities as Fleet | Mac/Linux runtime and restart profile; optional single-member etcd later |
| Fleet control storage | Three host-managed etcd members; no control PG requirement | Quorum, fencing, snapshots, disk behavior and recovery qualification |
| API concurrency | Qualify single-active handoff first; active-active is a separate decision | Session ownership, queue recovery and fencing analysis; choose before availability promise |
| Logs/archive | Bounded node buffers; collector and independent archive/query backend | Compare operational overhead and budget; preserve auditable records independently of diagnostics |
| Retention | Proposed 7-day local/30-day Fleet diagnostics, existing 365-day audit policy | Organization needs, byte budgets, replay windows and archive retrieval tests |
| Database engine scope | PG control/apps plus MySQL binding for WordPress; provider-specific migration capabilities | Engine/version support matrix and backup/restore/reconnect tests |
| Service availability | App traffic, control API, database writes and background jobs have separate targets | Agree acceptable interruption/data-loss budgets before paid rehearsal |
| Fleet role model | Explicit roles independent of pool names; distinct worker/app placement | Schema proposal and executor/inventory validation parity |

These are recommendations, not silently selected production defaults. Hardware sizes, retention bytes and downtime budgets require measurement. Scope approval should settle product choices; bounded engineering spikes can settle collector choice and exact schema syntax.

The store ADR must compare a dedicated etcd group with reuse of existing Consul primitives before final selection, including transaction limits, watches, isolation and coupled failure risk. The requested etcd path remains the proposed target. Set numerical RPO/RTO, control mutation pause, API failover, request-error, log-spool/loss and restore budgets from measurements; every release gate must attach its measured result artifact. Include sustained load and disk-growth evidence in etcd GA qualification.

## Release milestones and dependencies

| Milestone | Deliverable | Dependency | Release gate |
| --- | --- | --- | --- |
| M0 | Contracts and Mini baseline | Planning review | Decisions, measurements and fixture plan |
| M1 | Shared semantics and PG adapter | M0 | Correct ownership and backward-compatible state |
| M2 | Profiles, bindings and retention | M1 | Shared local/Fleet contracts and archive proof |
| M3 | Etcd and fresh HA bootstrap | M1; M2 archive contract | Quorum/fencing and PG-free restore |
| M4 | Fleet scaling and replacement | M1; M2 profiles | Placement and 2→3→2 under load |
| M5 | Mini upgrade rehearsal | M1–M2 | v2→v3 on PG with workload identities preserved |
| M6 | Running upgrades and app DB lifecycle | M2–M4 | v3→v3 and app DB cutover/recovery |
| M7 | First Mini→Fleet app migration rehearsal | M4–M6 | Data, traffic and worker ownership reconciled |
| M8 | Release qualification | M3–M7 | Both release paths, client parity and fault evidence |
| M9 | Controlled adoption | M8; named rollout scope | Mini, Fleet and app cutovers verified separately |

The [execution milestones](execution-milestones.md) define review units, dependencies and rollback boundaries. These are internal engineering stages of one v3 release, not mandatory sequential production releases. Etcd is an initial HA release gate; failure to qualify it blocks that claim. General backend conversion is not a dependency of fresh Fleet bootstrap or the Mini PG upgrade.

Estimate implementation effort only after M0's inventories and spikes. Track effort separately from CI/review/provider wait time and production maintenance windows.

## Version transition and client compatibility

1. Identify the actual supported Mini source version. Publish a targeted v2 prerequisite only if direct v3 upgrade cannot be made safe; do not require an intermediate release without evidence.
2. Back up data plus independent secrets/signing keys; rehearse restore. Install v3 against the existing PG backend using additive schema changes.
3. Keep old/new binary coexistence bounded to an explicitly tested version window. Delay destructive schema contraction until that window closes. Record a minimum readable/writable schema version and refuse incompatible startup.
4. Verify CLI, web, NornUI, agents and protected runners negotiate new capabilities; test older clients for preserved behavior or explicit unsupported-operation errors.
5. Enable resource bindings/retention/scaling capabilities incrementally. Preserve existing job IDs, endpoint identities, operation IDs, credentials and signed evidence or provide explicit mappings.
6. Perform an optional backend migration later as a separate operation. Before target writes, use the rehearsed rollback; after target writes, reconcile/export back rather than point at stale PG.

Release rollback is possible only while the persisted schema remains compatible. Database restore and runtime binary rollback are distinct operations with different data-loss boundaries.

Mini and Fleet remain independent control-plane identities. Move selected applications with explicit identity/provenance mappings, database/secrets bindings and data/file transfer, then cut traffic after fencing source writers and proving readiness. Do not clone the Mini control database into the live Fleet. Qualify the supported PG, etcd, Nomad and Consul versions alongside the Norn/client compatibility matrix.

## Planning artifacts and repository ownership

The [planning package](README.md) links the proposed ADRs and [planning contracts](planning-contracts.md). They turn the decisions below into reviewable records; their proposed status does not assert implementation or qualification.

Before implementation, complete ADRs for state/evidence separation, store semantics/fencing, profiles/resource bindings, desired scaling/placement, upgrade compatibility and migration authority. Each ADR must state alternatives, decision, invariants, failure behavior, migration and acceptance criteria.

Norn owns domain interfaces, API/CLI/UI behavior, stores and qualification tests. norn-fleet owns provider resources, inventory/bootstrap, node generations and protected runners. NornUI owns capability-aware native presentation and migration status. Cross-repository changes require a tested compatibility matrix and ordered release steps; no repository should emit intent another cannot execute.

The detailed P0–P5 work packages below define the implementation content of these milestones.

## Outcomes and decisions

Support one application/control contract across a consolidated Mini using PostgreSQL and a fresh distributed Fleet using etcd. Move bulky history into independent storage and keep application databases independent of the control backend.

Three distinct durability requirements must remain explicit:

- Control correctness: authentication/revocation, accepted operations, idempotency, execution ownership, deployment and Fleet checkpoints.
- Evidence: durable signed receipts and recoverable historical records, including original bytes needed for signature verification.
- Observability: container output, diagnostic logs, request logs, and aggregate metrics with independent retention and loss budgets.

Do not delete an operational record merely because it resembles a log. Do not retain PostgreSQL as a mandatory historical store in the final etcd profile: archived evidence must be retrievable from durable object storage; optional query indexes must be rebuildable.

## Current implementation and gaps

| Area | Existing evidence | Required change |
| --- | --- | --- |
| Container output | `api/handler/logs.go` and `api/nomad/logs.go` stream Nomad allocation output without storing it in PostgreSQL | Explicit allocation/task selection, aggregation, node rotation, collector and historical query contract |
| Control records | `api/store/postgres.go` mixes state, event replay, history, payloads and evidence | Inventory sizes/growth; classify retention and dependencies before pruning |
| Execution coordination | `api/store/operations.go` uses session advisory locks and SQL claims | Backend-independent atomic claims, fencing, idempotency and recovery contracts |
| Database health | `api/store/database_recovery.go` inspects PostgreSQL archive settings and replication views | Managed-provider evidence adapter and future etcd-specific readiness |
| App placement | `api/nomad/translator.go` sets job pool and counts, but no explicit host spread | Actual pool enrollment, per-process placement design, required anti-affinity and drain capacity proof |
| Fleet bootstrap | Fleet `ansible/templates/nomad.hcl.j2` enables clients only for ingress; module selects literal `ingress` key | Role-based inventory/bootstrap/LB targeting for arbitrary pool names |
| Node replacement | Fleet runbook documents bounded contraction, but same-address replacement fails closed | Generation-based add/join/verify/drain/retire executor |
| API availability | Fleet `ansible/templates/norn.service.j2` wraps the API in `consul lock -n=1`; only one API is active | Preserve single-active fencing initially; qualify handoff or explicitly implement active-active serving |
| Startup recovery | `api/store/postgres.go` marks all running exec sessions failed at startup | Owner/lease-aware recovery so starting a replica does not invalidate healthy sessions |
| Desired replica counts | `api/handler/apps.go` scales Nomad imperatively; deployment regenerates InfraSpec counts | Persist desired scale and prove redeploy preserves it |
| App databases | InfraSpec PostgreSQL declaration contains only a database name | Named database services and consistent endpoint resolution for app, migration, backup and restore |

All Norn paths above are relative to `v2/`. This is source assessment, not proof of the proposed Fleet being deployed. A three-node PostgreSQL cluster solely for small control state is optional, not a requirement. No measured control DB size has been established here.

## Deployment profiles

| Profile | Authoritative control store | Evidence/history | Logs | Availability |
| --- | --- | --- | --- | --- |
| Local initially | One local PostgreSQL instance, dedicated control database/role; app DBs may share the instance | Local archive with export option | Bounded local files and live Nomad reads | Single-host downtime accepted |
| Fleet initial v3 | Three etcd members, one per control node, isolated resources and disks/directories | Object archive; rebuildable index | Independent log backend | Majority quorum; one control-node failure tolerated after qualification |
| Local etcd, later option | One etcd member, same logical contracts as Fleet | Local archive; optional object export | Same local logging path | Explicitly non-HA; not an initial gate |

Local mode consolidates processes and storage but preserves logical names, credentials, schema, ownership and backup contracts. It must never silently claim Fleet availability. etcd is an additional consensus group alongside Nomad and Consul; qualify resource contention and independent restore before adopting it.

Run etcd under host supervision outside the app scheduler so restoring the control store does not depend on scheduling a job through that same store. Restore durable operation records, but reacquire execution leases under a new authority epoch; imported historical leases must not confer execution ownership.

## P0 — Measure and establish storage contracts

Read-only measurements: database bytes and top tables/indexes, row counts/age, daily growth, connection peaks across all API/agent processes, event rates, largest payloads, and restore duration. Measure a representative workload; service count alone cannot determine sizing.

Build a retention inventory for every table and payload. Separate active operations, unexpired idempotency/replay protection, open incidents, device revocations, current deployment/rollback pointers and Fleet recovery lineage from discardable diagnostics. Define legal product retention independently of log retention.

Size the retained Mini PG and independent app database services from measured memory, connections and storage. Small managed control PG remains a future deployment option, not a required Fleet resource. Use direct connections or proven session pooling for current session advisory locks; transaction pooling is incompatible with those locks. Managed application database health needs provider-aware evidence where internal archive/replication views are unavailable.

Exit: baseline report, connection budget, restore measurement, per-record retention and ownership map. No speculative exact footprint or downtime promise.

## P1 — Independent log and history retention

1. Add an explicit per-task Nomad log rotation budget and host journal limits, with an aggregate node disk budget.
2. Build allocation/task/region-aware live log selection and continuation cursors. Preserve source identity through aggregation; redact secrets and enforce app authorization on live and historical reads.
3. Add one collector per node (host service or Nomad system job) with a bounded disk spool, retries, rate limits, and visible dropped-record counters. Select the collector/backend after a local and Fleet compatibility spike; a Loki/object-storage design is a candidate, not an existing integration.
4. Propose diagnostic targets of 7 days local and 30 days searchable Fleet logs, tunable per organization and constrained by hard byte limits. Disk caps take precedence over diagnostic age targets, with visible truncation/loss reporting. Reserve authoritative audit/evidence policy separately; preserve today's 365-day audit default during transition.
5. Archive large terminal event bodies and webhook payloads only after their replay/recovery window. Keep summaries and immutable archive references in the control store. Use a durable outbox, checksummed uploads and verified archive acknowledgement before pruning. Preserve schema versions, signatures, IDs and referential closure.
6. Bound UI event replay and return an explicit expired-cursor/resync response after compaction. Keep open incidents and unresolved operations available until resolution.

Observability loss policy can allow dropping diagnostics under disk pressure. Accepted mutation receipts and security/recovery evidence cannot use that policy. Archive failure must retain pending evidence and alert; if its reserved capacity is exhausted, refuse new audited mutations rather than silently lose evidence.

Exit: collector restart, backend outage, node loss, archive outage and cursor-expiry tests; archived receipt verification and retrieval; measured bounded control-store growth under sustained event traffic.

## P2 — Retained Mini PG and named application database services

Initial release scope retains Mini control PG in place and qualifies application DB migrations. The control-cluster replacement procedure below is a later capability; it is not needed for fresh etcd Fleet bootstrap.

Introduce versioned database-service intent in the appropriate infrastructure contract and backend-neutral references in InfraSpec. Proposed concepts (not valid v1 YAML yet): service ID, purpose (`control` or `application`), engine/version, provider/resource reference, topology, backup policy, secret references and connection generation.

Provider provisioning stays with the protected infrastructure runner. Norn resolves connection references without exposing credentials. Resolution must be shared by deployed apps, migrations, pg_dump/restore, probes and recovery jobs. Include multiple databases/roles, extension requirements, pools, per-app budgets and revocation/rotation.

Ordinary WordPress needs MySQL/MariaDB, not PostgreSQL. Provide a separate engine capability and migration/backup adapter; do not label WordPress PostgreSQL-compatible by default. Treat CockroachDB as a separate engine with its own compatibility tests.

Control DB migration is an externally supervised operation: its coordinator cannot depend exclusively on the database being taken offline. Persist a signed migration manifest/checkpoints outside both candidate stores, including source/target identity, authority generation and recovery instructions.

For small control PG: provision target, prepare roles/extensions/TLS/networking, rehearse restore, drain operations, suspend every writer (API, agents, webhooks, watchers and scheduled jobs), fence source writes, final dump/restore, verify counts/checksums/constraints/sequences, update all connection consumers, start one candidate with mutations disabled, verify auth and audit, activate one authoritative target generation, then resume remaining replicas/workers. Preserve signing keys outside the DB. Never run APIs against independently writable old and new copies.

Before target writes, rollback can return to the fenced source after coordination. After target writes, source is stale: rollback requires reverse migration/reconciliation or accepting explicit data loss; changing the DSN back is not a rollback plan. Retain the source until the acceptance window ends.

Application DB moves use the same principles per consistency group. Stop web writers, workers, cron and integrations at cutover. A rolling deployment pointing half the writers at each database is unsafe. An online path requires proven source/target replication support, DDL freeze, sequence/large-object/role/extension handling, final write fence and catch-up, pool reconnection, and a tested rollback boundary. DigitalOcean currently does not offer its built-in continuous migration for managed-PG-to-managed-PG transfers; select dump/restore or separately qualify another replication method.

Exit: restored control identities/receipts work, old writers are rejected, all consumers use the new generation, app migrations/backups target the intended service, and an interrupted migration is recoverable without the source control API.

## P3 — Fleet capacity and rolling availability

Support explicit pool roles independent of names (`control-nyc3`, `app-nyc3`, `db-nyc3`). Align Go/schema/client validation, provider tags, inventory, Ansible roles, actual Nomad node pools and LB membership. A `database` pool remains VM capacity until an engine operator implements its lifecycle. Permit an explicit ingress role on separate nodes or a deliberately combined app/ingress profile.

Expose process replica counts, required distinct-host placement, resources and graceful drain. Add per-process pool placement using separate jobs where necessary: Nomad job-level pool assignment cannot express different pools for two task groups. Version this translation carefully to preserve job identity and migration behavior.

Extra app nodes add schedulable capacity and failure headroom. They do not automatically add application replicas or rebalance existing allocations. Model VM scaling and process scaling as separate actions and provide a reviewed rebalance/drain path.

Persist process scaling in authoritative desired state so subsequent deployments do not reset it. Replace the bootstrap's control/ingress-only topology assertion as part of the same role-mapping change, rather than accepting a schema that the executor cannot configure.

For equal nodes, an initial N-1 resource budget is `(node_count - 1) * allocatable_per_node`, with extra rollout reserve. Also simulate actual placement, fragmentation, per-pool constraints, singleton tasks and volumes. Strict one-per-host placement can keep a replica pending after failure if no spare eligible host exists; surface that tradeoff rather than silently co-locating replicas.

Prioritize web/API replicas separately from deferrable Trinity/Pipeline workers. Stateless replicas require external sessions and durable uploads; WordPress uploads/plugins and stateful local volumes need a storage/deployment policy before safe horizontal scaling. Workers need idempotent work and queue ownership semantics.

Implement node generations: create spare capacity, join, prove placement and endpoint health, drain old nodes, retire exact old identities. Check provider quotas and transient surge separately from steady-state pool max. Hold control/etcd membership quorum through changes. Start with reviewed scaling; autonomous VM scaling remains a later policy-controlled capability.

Exit: scale 2→3→2 under traffic, grow replica count independently, fail one node, replace a node and drain workers; verify placement, user requests and no lost acknowledged work.

## P4 — Running upgrades and database availability

| Upgrade | Mechanism | Realistic availability contract |
| --- | --- | --- |
| Stateless app | Health-gated rolling/canary, surge, graceful shutdown, backward-compatible schema | Target zero failed synthetic requests during planned rollout; test long connections |
| Norn API | Independent replica readiness/traffic drain, worker ownership transfer, signed releases | App traffic continues; control API remains available once Fleet rollout is qualified |
| Control schema | Versioned expand/contract migrations serialized by a migration owner | Old/new binaries coexist during supported window; destructive change delayed |
| Nomad/Consul/etcd or OS | One member/node at a time, quorum and catch-up checks | Continue while quorum and capacity hold; avoid stacking simultaneous failures |
| Control backend switch | Rehearsed export/import with authority fence | Short planned control mutation pause; app traffic continues independently |
| App DB major version/cluster move | Rehearsed migration, final writer fence, reconnect/retry | Bounded write interruption by default; zero visible errors only after client tests |

Database HA still permits brief connection/transaction failures at promotion. Applications must reconnect and retry only safe/idempotent operations, including ambiguous commits. No blanket zero-downtime or zero-data-loss claim: define RPO/RTO and observed user error targets per lane.

A Norn release must not automatically upgrade PostgreSQL, etcd, Nomad, Consul or app schemas. Serialize incompatible changes. Preserve independent upgrade windows for control and application DBs.

First fix startup-wide exec-session invalidation and qualify controlled single-active API handoff. Active-active serving is a separate decision requiring worker/recovery/auth concurrency tests; changing the Consul service lock alone is insufficient. Continuous API availability is an acceptance target, not the present Fleet guarantee.

Exit: mixed-version API test, backward-compatible migration/rollback, long-running operation handoff, DB promotion during traffic, and failed candidate isolation. Report timings and errors from rehearsal.

## P5 — Backend abstraction and etcd qualification

Create domain interfaces for atomic operation acceptance/idempotency, claims, deployment state, Fleet attempts, identity/revocation, evidence references and event cursors. Hide direct SQL access behind the PG adapter first. Specify transactional invariants before implementing a KV adapter; do not implement a generic SQL-to-key/value translator.

Use etcd compare-and-swap transactions for object revisions and atomic acceptance with idempotency/audit intent. Persistent operation records must not expire with executor leases. Leases apply to execution ownership only. A lease expiry does not stop an old process from mutating Nomad or a provider: enforce monotonic fencing generations at the execution boundary. Where an external system cannot enforce fencing, reject automatic takeover until the previous executor is proven stopped or its result reconciled.

Use bounded objects, explicit indexes and pagination. Handle watch interruption/compaction through resync; never infer a failed transaction solely from a lost response. Preserve authentication revocation, replay tombstones, retry lineage and receipt verification across stores. Map backend-neutral event cursors with an authority epoch; do not expose SQL sequence numbers as interchangeable with etcd revisions.

Before migration, add equivalent backend-specific admission checks: PG backup/HA proof or etcd quorum, disk latency, space alarms, snapshots, restore and fencing proof. Configure peer/client TLS, least-privilege credentials, snapshot encryption/export, compaction/defragmentation and supported one-member-at-a-time upgrade procedures. Bootstrap and disaster recovery must operate without a functioning Norn queue.

Etcd implementation first qualifies a fresh Fleet and independent backup/restore. The cross-backend migration stages below are deferred follow-on work, not initial release gates:

1. PG adapter passes invariant tests with unchanged behavior.
2. etcd adapter passes the same suite plus quorum loss, partitions, lease loss, stale executor, duplicate delivery, full disk/quota and snapshot-restore cases.
3. Export canonical objects/evidence references to a passive target; compare shadow reads. PG remains sole authority; avoid ad hoc dual writes.
4. Rehearse local migration and three-node Fleet migration. Freeze admission and all writers, drain/fence workers, export final state, verify, activate a new authority epoch, then resume one executor before widening.
5. Keep old store fenced. Test recovery after each cutover checkpoint. If target has accepted writes, reverse export/reconciliation is required before returning to PG.
6. Retire mandatory control PG only after operation/auth/audit/history and restore tests pass without it. Optional search databases must remain disposable/rebuildable.

Exit: all acknowledged control mutations survive the defined failure model; no duplicate external action from stale ownership; no silent loss of identity/evidence; full recovery without control PG.

## Technical workstreams and review units

1. P0 measurement/report and retention inventory.
2. P1 live-log selection/rotation, then collector/archive/outbox and retention jobs.
3. P2 database references/provider evidence, then control migration coordinator and app adapters.
4. P3 role/pool mapping and placement, then node generations/scaling/drain proof.
5. P4 rolling platform upgrades and DB migration rehearsal; run alongside P3 once prerequisites land.
6. P5 PG abstraction and etcd prototype; promote only after shared conformance and destructive-failure tests.

P0–P5 are technical workstreams; execution order and initial versus deferred scope are defined by M0–M9 in [execution milestones](execution-milestones.md). Each unit should ship with API/CLI/web/native capability negotiation where it changes user-visible contracts, migration/rollback notes, and specific acceptance evidence. Planning does not require a live Fleet or authorize creating paid resources.

## External references checked 2026-09-22

- [Kubernetes logging architecture](https://kubernetes.io/docs/concepts/cluster-administration/logging/): node-local logs and separate central backend.
- [etcd transactions and leases](https://etcd.io/docs/v3.6/learning/api/): atomic comparisons and lease expiry semantics.
- [etcd upgrade procedure](https://etcd.io/docs/v3.6/upgrades/upgrade_3_6/): version-specific snapshot and member upgrade workflow; select the guide matching the deployed versions.
- [DigitalOcean PostgreSQL migration](https://docs.digitalocean.com/products/databases/postgresql/how-to/migrate/): current continuous-migration restrictions and dump/restore alternative.
- [DigitalOcean PostgreSQL major upgrades](https://docs.digitalocean.com/products/databases/postgresql/how-to/upgrade-version/): compatibility checks and downgrade/PITR limitations.
- [WordPress requirements](https://wordpress.org/about/requirements/): MySQL/MariaDB application database requirement.
