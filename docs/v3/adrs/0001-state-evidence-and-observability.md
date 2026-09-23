# ADR 0001: Separate control state, evidence and observability

Status: Accepted. Date: 2026-09-22. Accepted: 2026-09-23. Owner: Norn. Parent: [roadmap](../architecture-roadmap.md).

## Disposition (2026-09-23)

Acceptance covers the architecture: the three separate storage contracts (authoritative control state, durable evidence archive, diagnostic logs), the outbox-with-verified-watermark archive protocol, and the fail-closed evidence-reserve rule. The storage separation is validated by implementation — the control-store boundaries (operations, deployments, events, Fleet attempts, mutation-audit) are already extracted behind interfaces and archive-eligible bulk is distinguished from authoritative state. Collector/query-backend selection, byte budgets and organization-specific retention windows remain open and are gated on P0 measurement, not on this review; they do not hold the architecture.

## Context

Application stdout/stderr already streams from Nomad. PostgreSQL also holds authoritative records and accumulating event/history payloads. Removing records indiscriminately would break replay, audit verification and recovery.

## Recommended decision

Define three storage contracts: authoritative control state, durable evidence archive, and diagnostic logs. Keep active state and compact recovery pointers in PG initially and optionally etcd later. Store completed bulky evidence in immutable, checksummed archive objects. Query indexes are rebuildable. Logs use bounded local rotation and one collector per node, with optional centralized querying.

Local archives use an atomic filesystem adapter with export support. Fleet archives use object storage with independent credentials, retention and recovery. Reserve separate buckets/policies for Terraform state, audit evidence, database backup and diagnostic logs. This limits accidental shared retention or deletion policies.

Keep the existing 365-day audit policy during migration. Proposed diagnostic age targets are seven days locally and 30 days in Fleet, subject to hard byte budgets; report truncation. Resolve payload-specific replay and idempotency windows in the state inventory before pruning.

## Alternatives and consequences

- Everything in PG is initially simple but couples telemetry growth to control recovery and prevents a small etcd model.
- Everything in object storage lacks the coordination and transactional admission needed by active control work.
- Retaining mandatory history PG alongside etcd keeps a database dependency; optional indexes are acceptable only if authoritative records can be recovered without them.
- Loki with an object backend is a candidate, not a selected dependency. Compare it with direct archival plus limited query and a managed log service in a bounded spike.

## Invariants and failure behavior

Archive through an outbox persisted atomically with the eligible state transition. Verify object identity, checksum and original signed bytes before recording an archive watermark and deleting eligible payloads. Preserve open incidents, nonterminal operations, security replay protection and recovery references. Deleting evidence requires a separate retention policy from diagnostic rotation.

Collector failure must not block app serving. Bound spool space and surface diagnostic loss. Archive failure retains pending authoritative evidence; exhausted evidence reserve refuses new audited mutations rather than discarding receipts. Historical reads enforce the same tenant/app permissions as live reads. Stable event cursors return a resync requirement when their replay window expires.

## Migration and acceptance

Measure current sizes/rates; add archive references compatibly; run upload/verify in shadow mode; compare retrieval; enable bounded pruning last. Rollback disables pruning and keeps archive reads supported. Never require reconstructing signed payloads from reformatted JSON.

Acceptance: collector outage/restart, disk pressure, duplicate upload, object corruption, archive outage, expired cursor and unauthorized query tests. Restore control state plus archive and verify receipt signatures. Show bounded hot-state growth under a representative workload.

## Open decisions

Collector/query backend selection, byte budgets and organization-specific retention remain proposed until measured. The storage separation is the recommended architecture.
