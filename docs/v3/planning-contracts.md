# Norn v3 planning contracts

Status: proposed, 2026-09-22. This is a planning artifact, not an implemented API, supported configuration or authorization to provision/migrate infrastructure. Read alongside the [architecture roadmap](architecture-roadmap.md). Repository owners below identify delivery responsibility; named maintainers must be assigned before implementation starts.

The initial release has two required paths: existing Mini v2→v3 on its retained PostgreSQL/runtime/workloads, and fresh DigitalOcean HA Fleet on three control-node etcd members. Both use the same domain contracts. General PG↔etcd control conversion, optional Fleet PG and local single-member etcd are deferred support lanes. The [execution milestones](execution-milestones.md) govern sequencing and exit evidence; the W packages below group implementation responsibilities rather than require separate releases.

## State inventory and retention boundary

Inventory source: the 24 control tables declared by [`Migrate`](../../v2/api/store/postgres.go) in the current working tree. HA-lab application tables are not control tables. Classification below is proposed policy; existing deletion behavior must be audited before changing it. A table name alone never proves a record is disposable.

| Existing tables | Authoritative state to retain | Archive/retention handling proposed |
| --- | --- | --- |
| `operations` | Accepted work, outcome, idempotency identity, retries, active execution ownership and recovery references | Keep active/unresolved operations and unexpired deduplication records online; archive terminal payloads only after retry, replay and rollback windows close. Retain a compact authoritative result/tombstone for the promised idempotency window. |
| `deployments`, `deployment_regions`, `deployment_steps` | Current deployment, regional routing progress, resumable steps and rollback candidates | Archive completed historical graphs together. Never let parent deletion cascade away active regional state. Preserve image/source identity and evidence closure. |
| `fleet_runner_attempts`, `fleet_github_dispatches` | Current attempt, retry/root lineage, dispatch identity, approval binding and replay protection | Preserve unresolved lineage and nonce protection; archive terminal evidence only after recovery/replay windows close. Sensitive dispatch values require restricted access. |
| `cron_states` | Current desired schedule and paused state | Keep authoritative until resource deletion; record changes in independent audit evidence. |
| `func_executions` | Running execution and unresolved outcome | Archive completed history after reconciliation; diagnostics and duration statistics may have shorter policies than proof of accepted work. |
| `saga_events` | Events needed to understand/resume an unresolved saga | Separate recovery-relevant records from diagnostic messages during call-site audit; archive terminal saga history with its operation/deployment. |
| `control_events` | Bounded client replay stream, not a replacement for domain state | Compact only after a declared replay window; expired cursors require explicit resync. Archive only records that also serve evidence requirements. |
| `beacon_events` | Open actionable incidents, acknowledgement and snooze state | Preserve unresolved incidents; archive resolved history under incident policy. Do not classify all beacon bodies as logs. |
| `webhook_deliveries` | Delivery identity, deduplication and pending dispatch/retry state | Archive payloads after handling/replay windows; keep deduplication summaries for the promised retry horizon. Redact or encrypt sensitive payloads. |
| `notification_channels` | Current configuration and access to channel credentials | Keep authoritative configuration; migrate credential values to secret references. Never include raw tokens in ordinary evidence archives. |
| `access_grants`, `access_devices`, `access_tokens` | Live grants, identity, token lineage and revocations | Expiry alone does not permit deleting records still needed by sessions or revocation checks. Keep tombstones through maximum credential validity/skew; archive security evidence under its own policy. |
| `github_actions_assertion_uses`, `access_enrollments`, `step_up_challenges` | Single-use/replay protection, enrollment and challenge state | Keep through validity plus clock/replay margin and dependent sessions; then purge secrets/hashes as appropriate while retaining necessary security evidence. |
| `exec_sessions` | Active owner, authorization linkage and terminal result | Recover by owner/lease rather than invalidating every running session on startup; archive terminal metadata with authorization evidence. Command data requires restricted access. |
| `mutation_audit_events`, `mutation_audit_incidents` | Incomplete receipts, incident links and durable verification evidence | Preserve the existing 365-day audit policy during transition; allow archived storage only with verified retrieval/signature bytes and complete references. Unresolved evidence is never age-pruned. |
| `recovery_drills` | Active drill and latest qualifying proof required by readiness | Archive old completed drills; preserve the proof used for a current readiness decision and its provenance. |
| `access_observation_buckets` | Aggregated observations used by any current decision | Candidate for independent metrics retention after auditing readers. Export or summarize before pruning where a release decision references it. |

The SQL inventory is only one part of migration scope. P0 must also inventory InfraSpec/Git identities, secret and signing-key stores, certificates, Nomad/Consul identities, object archives and provider state. These cannot be reconstructed merely by exporting SQL. New v3 desired-scale objects, database bindings, archive outbox and authority epochs also need ownership and retention definitions before implementation.

Application stdout/stderr already follows the Nomad log path rather than this database. The work is to bound and collect those logs and separate bulky control history safely. Proposed diagnostic age targets are seven days local and thirty days Fleet, subject to byte caps. These do not shorten audit/security/recovery retention.

Archive transaction: commit durable archive intent with the domain change; upload immutable bytes; verify checksum and retrievability; commit acknowledgement/reference; prune eligible source payload only afterward. Repeat safely after any crash. Search indexes are rebuildable. When evidence capacity is exhausted, reject new audited mutations rather than discard accepted evidence; diagnostic collectors may drop with explicit counters.

## Conceptual interfaces and resources

These names describe required behavior, not final Go signatures, endpoint paths or valid YAML. Avoid introducing a generic SQL/KV abstraction that conceals domain invariants.

| Proposed boundary | Required contract |
| --- | --- |
| `OperationStore.Accept` | Atomically accept operation, idempotency identity and audit/outbox intent; a repeated request returns the original result identity. Resolve an uncertain commit by identity lookup. |
| `ExecutionStore.Claim/Complete` | Compare revision and authority epoch; return a monotonically fenced owner; retain durable work after lease expiry. Stale owners cannot complete or initiate a new external mutation. |
| `DeploymentStore` / `FleetAttemptStore` | Conditional transitions, bounded indexes, preserved lineage, paginated query and referential closure. |
| `IdentityStore` | Atomic enrollment/challenge consumption, revocation and assertion replay protection; fail closed when authority cannot be established. |
| `DesiredStateStore` | Persist process scale and placement independent of a single deploy; define precedence between a new spec and an explicit scale override. |
| `EvidenceStore` | Immutable manifest/reference, verified archive acknowledgement, authorized retrieval and independent verification without control PG. |
| `EventStream` | Opaque epoch-scoped cursor, bounded replay, explicit compaction/resync and no implied equivalence between PG sequences and etcd revisions. |
| `DatabaseResolver` | Resolve one logical service/binding and connection generation consistently for app, worker, migration, backup, restore and probe. Credentials remain secret references. |
| `MigrationJournal` | Externally recoverable signed checkpoints, source/target identities, fencing proof and authority generation; usable while both candidate control APIs are offline. |

Proposed resources:

- `DatabaseService`: stable ID, purpose, engine/version, provider reference, topology, backup policy and supported migration capabilities. Control/app purpose does not imply separate hardware locally.
- `DatabaseBinding`: service ID, database/role, secret reference, TLS requirements, consistency group and connection generation. MySQL/MariaDB gets an explicit engine adapter for WordPress; PostgreSQL compatibility must not be assumed.
- `DeploymentProfile`: local/Fleet topology, logical storage bindings, logging/archive policy and declared availability class. Local consolidation preserves identities and ownership while making its single-host failure domain visible.
- `ProcessPlacement`: replicas, resources, pool, required/preferred host separation, shutdown/drain policy and scale-override precedence.
- `NodeGeneration`: exact provider/node identity, replacement predecessor, readiness proof and drain/retirement checkpoint. Surge allowance is separate from steady-state maximum.
- `ControlAuthority`: backend identity, epoch and migration state. Restored historical leases never grant execution rights.

API proposals: capability discovery for each optional feature; separate process-scale and node-pool-scale operations; migration plan/validate/status/checkpoint views; database-binding inspection with credentials omitted; live/historical log queries with allocation/task identity; archive receipt retrieval; and explicit cursor-expired responses. Mutations require idempotency keys and return durable operation IDs. Destructive retirement remains a separately reviewed action. UI/API acceptance must reject unsupported combinations before runner dispatch.

An etcd adapter must enforce bounded object/transaction sizes and explicit indexes. If an external API cannot enforce the fencing token, execution takeover requires proof the prior executor stopped or reconciliation of its action; a lease timeout alone is insufficient.

## Compatibility matrix to qualify

No row below is a claim of present support. Pin exact Norn, Fleet runner, CLI/web/NornUI, PostgreSQL, etcd, Nomad and Consul versions in each release evidence manifest.

| Source → target | Required behavior / gate |
| --- | --- |
| Existing v2 → minimum upgrade baseline | Audit whether a prerequisite patch is necessary; preserve endpoints, credentials, IDs and current workloads. Publish the minimum baseline without assuming an intermediate release is always required. |
| Baseline v2 + PG → v3 + same PG | Additive schemas; explicit minimum readable/writable versions; tested old/new binary coexistence window; rollback before contraction. |
| Fresh DO Fleet → v3 with three control-node etcd members | Initial GA gate: independent bootstrap, shared storage invariants, membership, quorum failure, backup/restore and successive v3 upgrades without control PG. |
| One Mini application → Fleet application | Initial GA rehearsal: preserve/map app identity, rebind dependencies, transfer data/files/secrets, fence jobs and writers, verify readiness, then switch traffic. Mini control store stays in place. |
| v3 PG → v3 etcd | Deferred: separate fenced migration; complete object/evidence/auth comparison; independent keys; final target authority activation. Not required for initial GA. |
| v3 etcd → v3 PG | Deferred: canonical export/import and reconciliation after target writes; never point back at a stale source. |
| Optional Fleet control PG / local single-member etcd | Deferred qualification; no supported topology implied by sharing an adapter or export format. |
| Old clients → v3 API | Preserve supported reads/mutations or return actionable unsupported capability; no silent interpretation of new placement/migration intent. |
| New clients → older API/runner | Hide/disable unavailable actions; never emit a plan the deployed runner cannot execute. |
| Fleet v1 / existing InfraSpec → v3 | Continue documented semantics; version incompatible fields independently of product version. Proposed resources above are not valid v1 fields. |
| PG or app DB engine major upgrade | Pin supported source/target versions/extensions, provider capabilities and all consumers; test reconnect and rollback boundary per engine. |

## Measurement and qualification plan

All numbers below are draft engineering acceptance targets for discussion, not existing SLAs, measured capabilities or promises. P0 must record observed baselines and either validate or revise each target explicitly before implementation scope is accepted. Timing includes the user-visible interval, not just the database command duration.

| Lane | Draft target | Measurement / failure experiment | Owner |
| --- | --- | --- | --- |
| Control state | Zero lost acknowledged mutations and zero duplicate external effects within the qualified single-node-failure model | Record operation IDs and external effects across crash, uncertain response, lease loss, partition and stale-worker replay | Norn |
| Planned stateless rollout | Zero failed synthetic requests over a 30-minute test at representative load; p99 latency ≤2× baseline | Roll API/web replicas and drain a node; exercise persistent connections separately | Norn + norn-fleet |
| Control API handoff | ≤30 seconds of mutation unavailability, no lost accepted work | Kill active API during queued and running work; measure admission and recovery separately | Norn + norn-fleet |
| Mini v2→v3 control upgrade | Draft ≤5 minutes mutation pause for a declared ≤1 GiB fixture; existing app traffic continues where runtime independence permits | Passive-copy rehearsal, compatible schema changes, controlled API switch and rollback; verify existing jobs/routes/data without blanket redeploy | Norn + Mini operator |
| Online-qualified app DB cutover | ≤60 seconds write interruption and zero acknowledged-write loss during planned cutover | Workload with web/worker/cron writers, final catch-up, sequence/role checks and pool reconnect | Norn + app owners |
| Disaster restore | Control RTO ≤30 minutes; draft backup-only RPO ≤15 minutes, reported separately from HA failover | Restore onto clean hosts without the old control API; recover secrets/evidence and reacquire ownership | norn-fleet + Norn |
| Log buffering | Survive a 30-minute backend outage without loss at measured normal ingest; enforce a draft 2 GiB/node spool cap | Disconnect backend, restart collector, exhaust spool, compare sequence counts and dropped counters | norn-fleet + Norn |
| Evidence archive | Zero pruning before verified acknowledgement; retrieve/verify archived receipt within 60 seconds in rehearsal | Crash before/after upload and acknowledgement; deny archive access; corrupt a fixture; rebuild index | Norn |
| Fleet capacity | 2→3→2 nodes and one-node loss with no lost acknowledged work; critical placement fits N-1 plus declared rollout reserve | Placement simulation and runtime traffic/queue measurements; include fragmentation, host separation and volumes | norn-fleet + Norn |
| Etcd sustained operation | 72-hour representative soak plus 10× burst; steady logical dataset fits ≤50% of configured quota after compaction | Measure database growth, object sizes, disk latency, watch lag and defrag; inject quorum loss/full disk and restore snapshot | Norn + norn-fleet |

The app owners must distinguish retryable reads, idempotent writes and ambiguous commits; an arbitrary retry cannot satisfy a no-duplicate-effects target. Singleton/stateful apps need their own availability exception or architecture work. Node loss can destroy unshipped diagnostic logs; report that separately from collector/backend-outage loss. Disaster RPO depends on backup cadence and storage failure scope; it is not the same as failover RPO.

P0 captures table/index bytes, row ages/counts, largest payloads, daily growth, event rates, connection peaks, WAL growth, restore time, CPU/memory/IOPS, archive volume and log ingest distribution. Use sanitized representative fixtures before any live measurement. Measure the retained Mini control PG from memory/connections/latency as well as bytes; current session advisory locks require direct connections or verified session pooling. Fleet measurements size etcd memory/disk/quota and backup capacity independently of managed application databases.

Each result bundle contains source commits, versions, topology/resources, workload/fixture identity, test start/end, request counts/errors/latency, operation/effect reconciliation, storage measurements and recovery checkpoints. A failing target produces a scope/design decision, never a silently weakened success label.

## Work packages and dependencies

| Package | Deliverable | Dependency | Accountable repository / collaborators |
| --- | --- | --- | --- |
| W0 | Approve ADRs; assign maintainers and budgets; complete call-site/retention audit and baseline template | Roadmap + this inventory | Norn; norn-fleet and NornUI review |
| W1 | PG domain interfaces, invariant suite, owner-aware session recovery and canonical export format | W0 | Norn |
| W2 | Node/task rotation, collector spike, historical authorization/query and archive outbox | W0; W1 for atomic archive intent | norn-fleet for host collectors; Norn for API/archive; NornUI for presentation |
| W3 | Database resources/resolver, local/Fleet profiles and provider readiness evidence | W0 | Norn contracts; norn-fleet provider/bootstrap; NornUI bindings/status |
| W4 | Durable process scale, explicit roles/pools/placement and capacity planning | W0; W1 for desired state | Norn + norn-fleet; NornUI scale/placement |
| W5 | Node generations, drain/replacement and control upgrade handoff | W4 + owner/fencing contracts from W1 | norn-fleet executor; Norn coordination |
| W6 | Externally recoverable application DB upgrade/cutover and one Mini-to-Fleet app migration | W1 + W3; archive/key recovery contract from W2 | norn-fleet supervisor; Norn adapters; app owners reconnect validation |
| W7 | Etcd adapter, fresh three-member Fleet bootstrap, membership/backup lifecycle and full PG-free recovery | W1 fencing/authority contracts; W2 archive retrieval for final qualification; independent of W6 conversion tooling | Norn adapter; norn-fleet lifecycle |
| W8 | Mini v2→v3 same-PG upgrade/rollback rehearsal, fresh Fleet and successive v3 upgrades, client parity and measured fault qualification | W2–W7 for final release sign-off; Mini rehearsals begin after W1–W3 and do not wait for Fleet node replacement | Norn release owner; norn-fleet/NornUI/app owners sign off their lanes |

W2, W3 and W4 can proceed independently after their prerequisites. W7 adapter/bootstrap work starts after the W1 contracts stabilize, alongside database migration and Mini upgrade work; it does not wait for a PG-first Fleet release or general control conversion. Initial GA requires both Mini PG and Fleet etcd evidence from W8. General PG↔etcd conversion has no initial work-package dependency and requires a future scoped plan. No implementation estimate, live Mini upgrade or paid rehearsal is approved by these packages. M0 closes when ADR dispositions, named owners, unresolved bounded spikes and approved numerical budgets are recorded.
