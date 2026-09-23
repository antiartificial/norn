# Next substantive batch: M2 database consumers and retention

Queue after execution-recovery corrections are independently accepted. Do not start concurrently with that batch. The complete v3 roadmap remains intact; this document selects the next implementation sequence, not a smaller definition of completion.

## First vertical integration: database target consistency

Read ADR 0003, planning-contracts.md, database-binding-handoff.md and the accepted database package. Connect the resolver to actual application consumers, not another standalone abstraction. Present explicit versioned application/profile syntax before changing an existing Fleet v1 parser; retain security NORN_PROFILE independently. No implicit new fields in Fleet v1.

Implement one private connection-material adapter and pass the resolved target through runtime rendering, migrations, snapshot, restore, and health probes. Persist the complete expected target tuple at acceptance; execution re-resolves and rejects a changed generation. Persist catalog retirement history atomically with catalog revisions so recreation cannot erase accepted-work fencing. Credential-only rotation must not repoint the target. Remove ambient libpq fallback for named bindings; the control DSN is never an application default.

Snapshots need target-bound namespaces and metadata plus an explicit legacy Mini mapping, not database-name-only lookups. Cross-target restore needs explicit intent. Preserve original Mini behavior through a configured compatibility adapter without silently switching its database or route. Keep command-line arguments and diagnostics free of DSNs/secrets.

Acceptance must exercise two isolated PostgreSQL servers containing the same database name: runtime, migration, backup, restore, and health select the same declared target. Include stale accepted generation, rotated credentials, named/legacy ambiguity, secret canaries, and negative restore mappings. Use existing disposable infrastructure or explicitly scoped local instances; no production access or cloud provisioning. MySQL remains a required engine in the full roadmap; add its distinct adapter and real-engine tests when a scoped local runtime is available, never substitute PostgreSQL tests as proof.

## Next vertical integration: bounded logs and evidence history

Read ADR 0001 and retention-implementation-handoff.md. Application stdout already streams from Nomad; implement allocation/task-aware rotation, bounded spool and authorized historical reads separately from signed control evidence archival.

For control evidence, implement immutable storage plus durable archive intent/outbox, read-back verification, exact original signatures, archive-aware authorized queries, restore/reindex and retention holds. Mini needs private atomic filesystem publication; Fleet needs conditional immutable object publication with restricted credentials. Finish with verified pruning and measured bounded hot-state growth, not shadow-only upload. Late saga events, rollback references, replay identities, Fleet lineage and unresolved effects must survive. Outages retain required evidence or reject new work at the explicit capacity boundary; no silent loss.

Keep each vertical slice reviewable with source and runtime tests. Update capability reporting to distinguish implemented, unqualified and unsupported paths. No deployment, commits/pushes, external-repository changes, privileged containers or paid resources without a separately authorized scope.
