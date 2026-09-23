# ADR 0002: Domain storage contracts for Mini PG and Fleet etcd

Status: Proposed. Date: 2026-09-22. Owner: Norn; Fleet owns etcd host lifecycle.

## Context

Norn uses PostgreSQL transactions, SQL queue claims, advisory locks, uniqueness constraints and concrete store dependencies. The requested etcd deployment must preserve accepted work, authorization, execution ownership and recovery semantics.

## Recommended decision

Retain PG as the supported Mini backend and require dedicated etcd for the fresh HA Fleet release. Extract semantic interfaces for atomic acceptance/idempotency, operation claims, identity/revocation, deployments, Fleet attempts, evidence references and replay. Implement PG behind those interfaces first, with shared invariant tests, then build the etcd adapter against that contract. These are engineering milestones within v3; there is no prerequisite PostgreSQL-first Fleet release.

Keep the initial Mini profile on PostgreSQL and use three host-supervised etcd members on the Fleet control nodes. A single-member local etcd configuration may be used for development but is not an initial supported upgrade path. Membership, snapshots, TLS and recovery must not require scheduling work through Norn. Active objects are bounded; completed bulk records follow ADR 0001. Fleet must work without control PG, including historical evidence retrieval.

## Alternatives and consequences

- Keeping PG alone is the lowest-change option and remains supported for the Mini, but does not meet the initial Fleet independence from managed control PG.
- Consul KV/sessions reuse an existing service. This reduces processes but couples Norn storage growth and restoration to discovery, requires its own transaction/watch/limits analysis, and still needs a full store adapter. Prefer dedicated etcd provisionally; validate the comparison in the storage spike before final acceptance.
- Replicated SQLite requires a consensus/SQL layer and new compatibility work; copying SQLite files cannot meet the shared mutation contract. A third store adapter is out of initial scope.
- Dedicated etcd adds a third consensus group alongside Nomad and Consul. Reserve resources and test correlated failure/maintenance rather than assuming independence on shared nodes.

## Invariants and failure behavior

Accept an operation together with its idempotency reservation and audit intent atomically. Store durable operations separately from ephemeral execution leases. CAS guards revisions and uniqueness; lease deletion never deletes accepted work. Authentication and replay-sensitive checks use authoritative consistent reads.

Assign monotonic execution fences. Every mutation boundary checks ownership; a pre-call check alone cannot fence a paused process that later resumes. Downstream operations must reject stale generations, provide equivalent idempotency/concurrency enforcement, or require proof the old executor stopped before takeover. Unknown external outcomes go to reconciliation, not automatic replay.

Return outcome-unknown on ambiguous transport failures and resolve through stable operation IDs. Watch compaction requires a consistent resnapshot. Quorum loss rejects new authoritative writes; existing app traffic continues only to the extent independent Nomad/ingress/app services remain healthy. Do not fail open on auth or mutation admission.

## Migration and acceptance

Initial release requires the Mini upgrade on its existing PG and fresh Fleet bootstrap on etcd, plus independently verified backup/restore for each. Define canonical versioned export with stable IDs, signatures, indexes and authority epoch for recovery and future portability. A common format does not establish a supported cross-backend conversion.

General PG↔etcd conversion is deferred. When introduced, shadow projection cannot execute work; final switch freezes writers, drains/fences executors and verifies an import before activating one authority generation. Reacquire leases rather than restoring old ones. After target writes, returning to PG requires reverse transfer/reconciliation.

Acceptance: identical PG/etcd invariants, concurrent duplicate submission, revocation race, partition/lease loss/stale worker, quota/disk failure, watch gaps, snapshots and full restore. Prove one-node loss in a three-member group and refusal with quorum lost. No claim of exactly-once external execution without downstream support.

## Open decisions

Select supported upstream versions and limits through tests. Dedicated etcd versus Consul reuse needs a documented comparison; etcd is the requested Fleet target. Etcd cannot receive GA status until shared conformance, fresh bootstrap, membership, upgrade and full restore gates pass. Deferred cross-backend conversion is not one of those gates.
