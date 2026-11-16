# Operations and evidence

Norn records application and platform work as operations with explicit progress
and outcomes. An accepted request, a live process, and a completed operation
are different states.

| Work | Expected evidence |
| --- | --- |
| Preflight | Candidate source, input validation, build/test result, terminal outcome |
| Application deploy or rollback | Operation identity, deployment stages, serving allocation and route/version proof |
| Snapshot, restore, or migration | Exact app/database binding, selected snapshot or migration, safety/recovery evidence |
| Platform upgrade | Signed source/artifact and host identity, drain/compatibility checks, observation and terminal receipt |
| Infrastructure change | Reviewed desired source, provider/state identity, plan, authorized executor and provider readback |

## The store depends on the release strategy

The self-hosted strategy uses PostgreSQL control state and local host maintenance
integration. The tested Fleet strategy uses etcd control state and an external
management/runner path. Application databases are separate from those control
stores. PostgreSQL queue implementation details are not a description of every
Fleet operation.

## Recovery preserves effect identity

A controller persists enough intent and progress to inspect the operation after
restart. If an external service accepted a request but its response was lost,
read back the same effect before deciding whether another action is safe.
Unknown effects hold for resolution rather than receiving an automatic repeat.

Read-only work and some explicitly replay-safe transitions can retry. A mutable
stage does not become replay-safe because its process exited. Inspect the
specific controller's journal and recovery contract.

## Observation checks the user-visible result

Beacon events, logs, allocation health, and operation receipts answer different
questions. Combine them with a request to the intended application's endpoint.
For platform changes, observe workload continuity and API ownership throughout
the accepted window, with an explicit outage and sample-gap policy.

[Operator workflow](workflow.md), [controller recovery](recovery.md), and
[Fleet operations](fleet.md) describe the corresponding procedures.
