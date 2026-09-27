# V3 release gate status — 2026-09-26

This is a dated review ledger, not a release acceptance. Recheck PRs, Mini,
Fleet, provider state, and the exact candidate before acting. The authoritative
exit criteria are in [execution milestones](execution-milestones.md).

**Signed milestone gates: 0/10 (0%).** This is a gate fraction, not a percent
of implementation effort or a forecast. Source and local-runtime checkpoints
are substantial but cannot sign a broader gate.

| Gate | Current disposition | Next decisive evidence |
| --- | --- | --- |
| M0 contracts and Mini baseline | Open. Mini source-copy and size/growth observations exist; the 15-minute control RPO and 30-minute RTO are unqualified. | Owner chooses the [control recovery target](m0-mini-control-recovery-decision.md), off-host destination and retention; prove protected clean-host restore and measured end-to-end time. |
| M1 shared control behavior | Open. Durable acceptance, claims, schema and passive startup have local tests. | Finish external-effect fencing/reconciliation coverage and Mini runtime compatibility/rollback checks. |
| M2 profiles, databases and retention | Open. Local PostgreSQL/MySQL, WordPress and S3-emulator rehearsals passed. | Prove real-provider retention and separate-node restore, managed MySQL behavior, complete archive/accounting and profile parity. |
| M3 etcd and fresh Fleet | Open. Disposable three-member and host-unit rehearsals exist. | Protected separate-host bootstrap, quorum/fault/restore/soak, no control-PG dependency, and hosted Fleet contract check. |
| M4 capacity and placement | Open. Scale intent/visibility and a local loaded drain safety check exist. | Exact release-version placement and capacity proof; loaded 2→3→2 nodes, safe migration and failed-drain retirement block. |
| M5 Mini upgrade rehearsal | Open. Private source-copy schema/passive checks passed. | Production-key protected backup and private restore, exact signed candidate, isolated upgrade/rollback with unchanged jobs, routes and identities. |
| M6 running upgrades and app databases | Open. Foundations only. | V3 A→B rolling rehearsal and fenced app database cutover/recovery for supported PG/MySQL paths. |
| M7 representative app mobility | Open. Contract only. | One database-backed Mini app and its data/files/work move to independent Fleet with traffic and rollback proof. |
| M8 release qualification | Open. No signed release candidate. | Version/client matrix, fault/soak/growth evidence, operator runbooks and signed artifacts for the qualified scope. |
| M9 controlled adoption | Open. No v3 Mini/Fleet deployment. | Separately verify Mini upgrade, empty Fleet launch and selected app migration after M8. |

## Current review branches

- Draft Norn [PR #76](https://github.com/antiartificial/norn/pull/76):
  `39d79861c71f73b15e3007c6fa05b639c79304ef`; all eight checks passed
  in [run 36284522950](https://github.com/antiartificial/norn/actions/runs/36284522950).
  This is source validation, not Mini promotion or milestone sign-off.
- Draft Fleet [PR #176](https://github.com/antiartificial/norn-fleet/pull/176):
  `8f5760fcb8b645f19a6e67caff8438f48e9daa28`; the hosted `contract`
  job failed before runner assignment because of the GitHub account
  billing/spending-limit condition. The repository currently has no generic
  ephemeral CI fallback runner registered. Local contract tests and Nomad
  drain checks do not substitute for that check or a protected Fleet.

## Shortest release path from here

1. Decide Mini's control-store RPO and off-host retention, then qualify its
   protected backup and clean-host restore. Preserve the one-hour freshness
   rule of the separate legacy-upgrade proof.
2. Finish M1 effect reconciliation and M2 real-provider, cross-node database
   and archive proof. Clear Fleet CI runner admission, then qualify an exact
   protected candidate on separate hosts with three etcd voters and app pools.
   Record all pins.
3. Run the loaded M4 placement/drain and isolated M5 Mini upgrade/rollback
   rehearsals. Complete M6–M7 for the supported app database and migration
   scope; sign M8 before the separately approved M9 adoption steps.
