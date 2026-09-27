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
| M3 etcd and fresh Fleet | Open. Disposable three-member and host-unit rehearsals exist; the normal etcd router remains narrow. | Protected separate-host empty-Fleet bootstrap, quorum/fault/restore/soak, no control-PG dependency, and hosted Fleet contract check. |
| M4 capacity and placement | Open. Scale intent/visibility, a disposable claimed PostgreSQL→Nomad scale, active-deployment deferral, a local loaded drain safety check, and a private signed etcd-to-Nomad deployment rehearsal through terminal replay exist. | Complete the [etcd app admission and execution sequence](m4-etcd-app-admission-sequence-2026-09-26.md), including effective ingress/traffic observation before recording active weight; then exact release-version placement and capacity proof on protected Fleet hosts, loaded 2→3→2 nodes, safe migration, worker acknowledgment continuity, and failed-drain retirement block. |
| M5 Mini upgrade rehearsal | Open. Private source-copy schema/passive checks passed. | Production-key protected backup and private restore, exact signed candidate, isolated upgrade/rollback with unchanged jobs, routes and identities. |
| M6 running upgrades and app databases | Open. Foundations only. | V3 A→B rolling rehearsal and fenced app database cutover/recovery for supported PG/MySQL paths. |
| M7 representative app mobility | Open. A [read-only Mini candidate review](m7-mini-app-candidate-review-2026-09-26.md) identifies real database/file/workload shapes, but no app or destination is selected. | One database-backed Mini app and its data/files/work move to independent Fleet with traffic and rollback proof. |
| M8 release qualification | Open. No signed release candidate. | Version/client matrix, fault/soak/growth evidence, operator runbooks and signed artifacts for the qualified scope. |
| M9 controlled adoption | Open. No v3 Mini/Fleet deployment. | Separately verify Mini upgrade, empty Fleet launch and selected app migration after M8. |

## Review branches checked on 2026-09-26

- Draft Norn [PR #76](https://github.com/antiartificial/norn/pull/76):
  last reviewed implementation head `37472689e7ede4a73a68559d101630d4654e449b`; all eight
  checks passed in [run 36291945901](https://github.com/antiartificial/norn/actions/runs/36291945901).
  The disposable Nomad 2.0.7/PostgreSQL 16 rehearsal proved that an active
  deployment defers a claimed scale without reserving an effect, then the
  same claim completes and persists replica intent after deployment success.
  A race between preflight and launch can still leave an ambiguous effect for
  operator reconciliation. This is source and local-runtime validation, not
  protected Fleet qualification or milestone sign-off. Subsequent private
  etcd app acceptance, terminal results and effect reservation passed
  disposable real-etcd tests. The private worker's guarded Nomad registration,
  source-digest/no-diff readback, exact allocation health, effect completion,
  and claim-fenced terminal projection passed a combined disposable etcd and
  Nomad 2.0.7 rehearsal. Terminal writes now refuse unresolved app effects.
  The test constructs the region's active weight; actual ingress activation,
  normal worker dispatch, and the ordinary etcd Fleet route remain open.
- Draft Fleet [PR #176](https://github.com/antiartificial/norn-fleet/pull/176):
  `6267655052b209b22dc8b3421cb9339f797af9bc`; the hosted `contract`
  job failed before runner assignment because of the GitHub account
  billing/spending-limit condition. The repository currently has no generic
  ephemeral CI fallback runner registered. The local three-client Nomad 2.0.7
  rehearsal verified loaded 2→3→2 placement and exact-node drain predicates;
  all clients ran on one Mac, so it does not substitute for hosted contract
  CI or a protected, separate-host Fleet.

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
