# ADR 0005: Independent upgrades and bounded compatibility

Status: proposed, 2026-09-22. Planning only; no upgrade authorized.

## Context

The [v3 roadmap](../architecture-roadmap.md#p4--running-upgrades-and-database-availability) separates application serving, control API availability, database writes and background work. Current single-active API deployment and startup-wide exec recovery do not establish continuous control API availability. Coupling a Norn release to datastore or scheduler upgrades would multiply failure modes.

## Recommended decision

Publish an explicit compatibility matrix for Norn server/client/runner releases, persisted schema/object formats and supported PostgreSQL, etcd, Nomad and Consul versions. Product and document versions remain independent. Capability negotiation gates optional features; unsupported combinations fail before accepting mutations.

Require two initial release paths: upgrade the existing v2 Mini to v3 on its current PostgreSQL and runtime, and bootstrap fresh HA DigitalOcean Fleet directly into v3 with three control-node etcd members. Neither path requires PG↔etcd conversion; that capability is deferred under [ADR 0006](0006-migration-authority.md). A Norn release does not implicitly upgrade application schemas, database engines or consensus services.

Use additive expand/contract schema changes with one migration owner. Allow mixed binaries only within a tested version window, advertise minimum readable/writable versions, and defer destructive contraction until older writers and the rollback window have been retired. Qualify owner-aware session recovery and controlled single-active API handoff before promising control API availability; active-active serving needs a separate concurrency decision.

Use health-gated, drained app rollouts and one-member-at-a-time consensus upgrades with catch-up checks. Preserve workload serving during control maintenance wherever the data path remains independent.

## Alternatives and tradeoffs

- A coordinated stop-and-upgrade is simpler and remains acceptable for the single-host local profile, with an explicit outage.
- Immediate active-active API deployment could reduce handoff gaps but requires stronger ownership, recovery and auth concurrency qualification.
- Automatic dependency upgrades reduce operator steps but conceal compatibility and rollback boundaries; keep them independently planned.

## Invariants and failure behavior

- Starting a candidate cannot invalidate another owner's healthy session or take over an unfenced operation.
- An incompatible candidate stays out of service and cannot mutate state.
- A failed readiness/catch-up gate stops progression; do not take a second quorum member down.
- Binary rollback is supported only while persisted data remains compatible. Restore is a separate operation with a separate loss boundary.
- Database promotion may interrupt connections and leave transaction outcomes ambiguous. Retry only where application semantics make it safe; reconnect behavior alone does not prove exactly-once writes.

## Migration

Publish the minimum v2 upgrade baseline and export format; ship a prerequisite v2 patch only if the compatibility audit shows it is necessary. Rehearse with a passive copy whose workers, schedules and external mutations are disabled. Verify restore, deploy compatible v3 candidates against existing PG, verify clients/runners and enable capabilities incrementally. Preserve Nomad jobs, Consul identities, routes, credentials and application data; control upgrade must not trigger a blanket workload redeployment. Schedule contraction separately after the mixed-version window. Upgrade external dependencies in independent tested windows using the procedure for the selected versions.

## Acceptance evidence

Test the complete Mini v2→v3 upgrade and rollback rehearsal, fresh Fleet bootstrap, successive v3 upgrades, mixed versions, incompatible-startup rejection, expand/contract and supported rollback, long-running operation handoff, failed candidate isolation, consensus member replacement and database promotion under workload. Attach request/error/latency measurements and work-recovery results. Local downtime, Fleet API handoff, app serving and database-write budgets must be reported separately. Actual Mini upgrade is a separately scheduled live operation after rehearsal; planning artifacts do not authorize it.

## Unresolved decisions

Set the supported mixed-version window, single-active handoff versus active-active scope, schema contraction delay and numerical availability budgets. Confirm whether long-lived streams reconnect or drain to completion. Continuous control API service remains a qualification target until these tests establish it.
