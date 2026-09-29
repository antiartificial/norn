# Norn v3 planning package

**Current release work:** start with the [2026-09-29 release handoff](session-resume-2026-09-29.md)
for the latest checked-in state, open gates, and next sequence. It is the only
handoff in this tree; Git history retains earlier session and topic handoffs.

The architecture below began as a proposal on 2026-09-22. Use the current
handoff and execution milestones to distinguish later implementation from
release qualification and live operations.

An Architecture Decision Record (ADR) captures a consequential choice, its alternatives, rationale, consequences and validation. `Proposed` means ready for review; `Accepted` means the choice has been agreed, not that it is implemented. Later changes supersede records rather than erase the rationale.

## Read in this order

1. [Architecture roadmap](architecture-roadmap.md): release scope, milestones and dependencies.
2. [ADR 0001 — State, evidence and observability](adrs/0001-state-evidence-and-observability.md).
3. [ADR 0002 — Control stores and fencing](adrs/0002-control-store-and-fencing.md).
4. [ADR 0003 — Profiles and database bindings](adrs/0003-profiles-and-database-bindings.md).
5. [ADR 0004 — Scaling and placement](adrs/0004-scaling-and-placement.md).
6. [ADR 0005 — Upgrade compatibility](adrs/0005-upgrade-compatibility.md).
7. [ADR 0006 — Migration authority](adrs/0006-migration-authority.md).
8. [Planning contracts](planning-contracts.md): state inventory, interface/resource proposals, compatibility, measurement and acceptance plan.
9. [Execution milestones](execution-milestones.md): delivery sequence, parallel work, exit gates and release evidence.
10. [Implementation status](implementation-status.md): branch-specific local progress and qualification boundary.
11. [Foundation review](foundation-review.md): source-backed safety findings, bounded implementation slices and review acceptance tests.

The older session and topic handoffs are available in Git history. Use the
current release handoff above for next actions and the status and review
documents for implementation details.

## Recommended decisions for review

- Require both initial release paths: existing Mini v2/PG → v3 on the same PG/runtime, and fresh DigitalOcean HA Fleet v3 on three control-node etcd members.
- Keep three logical storage concerns: control correctness, evidence archive, diagnostics. Etcd must not retain a mandatory historical PG dependency.
- Consolidate physical services locally while maintaining the same logical identities and explicit non-HA status.
- Preserve PostgreSQL as the supported Mini control backend. Fleet uses etcd independently from application DBs; optional Fleet control PG is not an initial GA gate.
- Make desired replica count durable and distinct from VM count; require explicit placement and remaining capacity before drain.
- Qualify single-active API handoff before promising continuous API service; test ownership and schema compatibility before wider concurrency.
- Use a writer-fenced, externally recoverable migration coordinator; never treat switching a connection string back as sufficient rollback after new writes.
- Rehearse one actual Mini-to-Fleet application migration, including its data, files, jobs and traffic. General PG↔etcd control conversion is deferred and does not block either initial installation path.

## Remaining product choices

Architecture can be reviewed now. The following still need an agreed product policy or measured engineering result:

| Choice | Proposed default | How to close |
| --- | --- | --- |
| Availability and recovery budgets | Separate app/control/database/job budgets; draft targets in planning contracts | Confirm acceptable interruptions, then benchmark and qualify |
| Logs | Seven-day local / 30-day Fleet diagnostic targets under hard disk caps | Confirm retention/cost needs; collector/backend spike |
| Etcd support | Required for initial HA Fleet GA; PG required for the Mini upgrade | Shared invariants plus isolated partition, restore and fresh-bootstrap evidence; no general backend-conversion gate |
| API serving model | Single-active handoff first | Decide whether measured handoff meets the required API budget |
| Initial app DB scope | PG plus MySQL for WordPress | Confirm engine workflows included at GA; defer Cockroach |

The user has separately authorized staged local implementation. See implementation status for evidence; this does not implicitly accept every proposed ADR or numerical budget. Resolve the decisions required by each slice before implementing it. The initial backward-compatible startup-safety slice does not select a new backend or qualify a release. Update milestone status based on evidence rather than the existence of these documents; live deployment remains separately scoped.
