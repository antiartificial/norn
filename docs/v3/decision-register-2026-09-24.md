# V3 decision register — 2026-09-24

This is a reconciliation of the v3 planning ADRs and the accepted auth-aggregate decision from the durable branch. Implementation does not by itself accept a proposed ADR or qualify a milestone.

| ADR | Current disposition | Review needed before exit |
| --- | --- | --- |
| [0001: State, evidence, observability](adrs/0001-state-evidence-and-observability.md) | Proposed; archive and event foundations implemented in part | Accept retention/restore guarantees and measured byte budgets after Mini fixture and outage tests. |
| [0002: Control store and fencing](adrs/0002-control-store-and-fencing.md) | Proposed; signed PG acceptance and selected etcd adapters implemented | Accept backend contract after all consumers, external effects, and PG-free Fleet startup qualify. |
| [0003: Profiles and database bindings](adrs/0003-profiles-and-database-bindings.md) | Proposed; named PostgreSQL bindings implemented | Set first supported app/database engines and prove MySQL lifecycle and client parity before acceptance. |
| [0004: Scaling and placement](adrs/0004-scaling-and-placement.md) | Proposed; app capacity remains M4 | Decide durable desired replica intent, placement/drain/replacement precedence and measured 2→3→2 gates. |
| [0005: Upgrade compatibility](adrs/0005-upgrade-compatibility.md) | Proposed; passive candidate/schema foundations implemented | Accept rollback window, old-data compatibility and exact Mini/Fleet rehearsal evidence. |
| [0006: Migration authority](adrs/0006-migration-authority.md) | Proposed; no migration authority granted | Accept source/provenance, dry-run, cutover and rollback boundaries after migration rehearsal. |
| [0007: Auth aggregate and revocation](adrs/0007-auth-aggregate-and-revocation.md) | Accepted on durable branch; reconciled here | Qualify cross-process crash/partition and execution-boundary fencing; acceptance of invariant is not runtime sign-off. |

ADR owners and numeric acceptance budgets still need explicit review. This register prevents a code checkpoint from silently changing a proposed decision to accepted or claiming release readiness.
