# V3 release gate status — 2026-09-26

This is a dated review ledger, not a release acceptance. Recheck PRs, Mini,
Fleet, provider state, and the exact candidate before acting. The authoritative
exit criteria are in [execution milestones](execution-milestones.md).

**Signed milestone gates: 0/10 (0%).** This is a gate fraction, not a percent
of implementation effort or a forecast. Source and local-runtime checkpoints
are substantial but cannot sign a broader gate.

| Gate | Current disposition | Next decisive evidence |
| --- | --- | --- |
| M0 contracts and Mini baseline | Open. Mini source-copy and size/growth observations exist. Synthetic Postgres.app PITR and Mini-to-Mac logical restore fixtures passed, but live WAL archiving remains off; the 15-minute control RPO and 30-minute RTO are unqualified. | Owner chooses the [control recovery target](m0-mini-control-recovery-decision.md), off-host destination and retention; prove protected clean-host restore and measured end-to-end time. |
| M1 shared control behavior | Open. Durable acceptance, claims, schema and passive startup have local tests. Reviewed PostgreSQL migrations have an opt-in private supervised effect; enabled supervised mode rejects unreviewed commands. A standalone expired claim with a source checkpoint and durable effect can requeue for observation. A guarded expired deployment migration can requeue with source/build checkpoints, completed snapshot step, original effect and no later mutable step; replay verifies and reuses the original target-bound dump. Legacy or unproven migrations remain manual recovery. | Qualify the [private migration effect contract](m1-supervised-app-migration-contract.md) on protected Linux runtime and process crashes, including original-effect replay and snapshot publication, then finish other external-effect reconciliation and Mini compatibility/rollback checks. |
| M2 profiles, databases and retention | Open. Local PostgreSQL/MySQL, WordPress and S3-emulator rehearsals passed. | Prove real-provider retention and separate-node restore, managed MySQL behavior, complete archive/accounting and profile parity. |
| M3 etcd and fresh Fleet | Open. Disposable three-member and host-unit rehearsals exist; the normal etcd router remains narrow. | Protected separate-host empty-Fleet bootstrap, quorum/fault/restore/soak, no control-PG dependency, and hosted Fleet contract check. |
| M4 capacity and placement | Open. Scale intent/visibility, a disposable claimed PostgreSQL→Nomad scale, active-deployment deferral, a local loaded drain safety check, and a private signed etcd-to-Nomad deployment rehearsal through terminal replay exist. A source audit of Fleet PR #176 identified Traefik/Consul as ingress but no consumer of Norn's desired traffic-weight tag. | Complete the [etcd app admission and execution sequence](m4-etcd-app-admission-sequence-2026-09-26.md), including an applied and observed ingress revision before recording active weight; then exact release-version placement and capacity proof on protected Fleet hosts, loaded 2→3→2 nodes, safe migration, worker acknowledgment continuity, and failed-drain retirement block. |
| M5 Mini upgrade rehearsal | Open. Private source-copy schema/passive checks passed. | Production-key protected backup and private restore, exact signed candidate, isolated upgrade/rollback with unchanged jobs, routes and identities. |
| M6 running upgrades and app databases | Open. Foundations only. | V3 A→B rolling rehearsal and fenced app database cutover/recovery for supported PG/MySQL paths. |
| M7 representative app mobility | Open. A [read-only Mini candidate review](m7-mini-app-candidate-review-2026-09-26.md) identifies real database/file/workload shapes, but no app or destination is selected. | One database-backed Mini app and its data/files/work move to independent Fleet with traffic and rollback proof. |
| M8 release qualification | Open. No signed release candidate. | Version/client matrix, fault/soak/growth evidence, operator runbooks and signed artifacts for the qualified scope. |
| M9 controlled adoption | Open. No v3 Mini/Fleet deployment. | Separately verify Mini upgrade, empty Fleet launch and selected app migration after M8. |

## Review branches checked on 2026-09-26

- Draft Norn [PR #76](https://github.com/antiartificial/norn/pull/76):
  last reviewed implementation head `baa9ec3dbbe9531e6b597e3fc3db3d5e9410b0ed`; all nine
  checks passed in [run 36295224334](https://github.com/antiartificial/norn/actions/runs/36295224334),
  including a secretless full release-bundle build, ephemeral signing, import,
  and installed-release verification. This is not a production signing run.
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
  The [Mini Trove/bookmark refresh](m0-mini-trove-bookmark-workload-check-2026-09-26.md)
  separately records a completed PM sync and daily capture, a successful
  bookmark-session read, and $10.00 remaining X API credit at its check time.
- Draft Fleet [PR #176](https://github.com/antiartificial/norn-fleet/pull/176):
  `6267655052b209b22dc8b3421cb9339f797af9bc`; the hosted `contract`
  job failed before runner assignment because of the GitHub account
  billing/spending-limit condition. The repository currently has no generic
  ephemeral CI fallback runner registered. The local three-client Nomad 2.0.7
  rehearsal verified loaded 2→3→2 placement and exact-node drain predicates;
  all clients ran on one Mac, so it does not substitute for hosted contract
  CI or a protected, separate-host Fleet.

At 2026-09-27 07:52 UTC, the exact Fleet PR #176 head
`6267655052b209b22dc8b3421cb9339f797af9bc` passed a local rerun of
the workflow's pinned actionlint check, 1,410 Python contract tests (two
documented skips), validation of all four Fleet documents, OpenTofu recursive
formatting, Ansible syntax, and both disposable-root OpenTofu 1.12.6
init/validate checks. The local Ansible version was 2.21.3 rather than the
workflow's 2.18.3; the ordinary roots' OpenTofu 1.10.6 validation was not
rerun. The hosted `contract` job at this head still has no assigned runner
and no executed steps, so the required hosted check remains failed. Local
checks do not replace that check or protected separate-host Fleet evidence.

An exact-head rerun on 2026-09-27 (run `36285145209`, attempt 3) again
terminated before runner assignment with zero steps. Its fresh GitHub check
annotation cites failed account payments or an Actions spending limit. The
Norn repository is public, while Fleet and Like Trove are private; Norn's
passing hosted runs therefore do not establish runner availability for those
two companion repositories. The account owner must clear the private-repo
Actions billing condition, then rerun the exact Fleet and Like Trove heads.

## Protected-master integration audit — 2026-09-27 04:49 UTC

PR #76 targets `feature/norn-v3-planning-handoff`, not protected `master`.
At its reviewed head, the V3 branch is 757 commits ahead of the common base
with `master`; `master` has 39 commits absent from V3. A read-only Git merge-tree
simulation reported **126 file conflicts** (82 content, 44 add/add), including
CI, the signed-release workflow and helpers, API handlers and stores, HA lab,
and UI. PR #76 itself changes 556 files relative to its feature-branch base.
Retargeting that PR directly to `master` would therefore not produce a
reviewable or verified release candidate.

The protected `master` branch requires six legacy CI contexts, including
macOS API and CLI tests, the Fleet pilot workload and OpenAPI contract. The
current V3 feature-branch CI emits different context names and runs its main
API/CLI checks on Linux. A master integration must preserve the master-only
workloads and verify the required checks alongside the V3-specific tests.

GitHub reports `master` branch protection and immutable releases enabled.
The `platform-release` environment has branch policy, a required reviewer,
and the expected names for its signing key, immutability-check token, and
public-key variable. Their values and operational validity were not read or
tested. The publisher must still be dispatched from protected `master` for an
exact commit already merged there; the draft PR head cannot be the signed
production candidate.

## Protected-master integration candidate — 2026-09-27

Draft [PR #77](https://github.com/antiartificial/norn/pull/77) targets
protected `master`. Its integration code checkpoint
`300e3a3d817f8642f25b4636921c1ccb703b38ba` combines the V3 source with
the current master-only release and Fleet behavior. That checkpoint passed
all reported PR checks in the [legacy master run
36303333777](https://github.com/antiartificial/norn/actions/runs/36303333777)
and [V3 run
36303333772](https://github.com/antiartificial/norn/actions/runs/36303333772),
including API/CLI, web, OpenAPI, Fleet pilot, workflow lint, production etcd
TLS/RBAC, Mini synthetic schema, and release-bundle rehearsal. A full API
suite also passed locally against disposable etcd at this head. These are
integration checks; the PR remains draft and no protected-master merge,
signed publication, live Mini upgrade, or Fleet launch occurred. Signed
milestone gates remain **0/10**.

The CI repair exposed a real source integration defect: the normal etcd Fleet
runtime omitted the configured GitHub App environment. The candidate now
passes that binding through and proves its process path. The Apple connector
validates unsupported specifications before checking the host OS, allowing
the same contract tests to run on Linux while still refusing a valid Apple
workload off macOS.

At 2026-09-27 07:39 UTC, the exact PR candidate
`b8e87439d76f80afd4c293bbcb4bd77b6b58cc8c`, built with the release
version flag, passed `mini-private-copy-rehearsal` on Mini. The source
`norn_v2` connection was forced read-only for a private dump; every migration
and passive-startup check used a disposable socket-only PostgreSQL 17.7
cluster. All 28 original tables and 256,533 rows preserved their primary-key
and full-row fingerprints across migrations 1–44; a second migration pass and
passive health passed. The live Nomad base-job identities/versions and
cloudflared configuration matched before and after. Disposable candidate and
database state were removed. This is a source-copy compatibility check, not a
protected production-key backup, off-host restore, signed release rehearsal,
rollback, or M5 sign-off. A separate read-only control-database check at
07:37 UTC measured 251,595,923 bytes and still found `archive_mode=off` and
`archive_timeout=0`, so the draft 15-minute backup-only RPO remains unproved.

At this PR head, the complete API package suite also passed locally with
disposable PostgreSQL 16 and etcd available together. Both fixtures were
stopped and removed afterward. This exercises the combined test environment;
it does not prove live concurrency, off-host restore, or a milestone gate.

The later [schema-45 private-copy rehearsal](m5-mini-private-copy-schema45-2026-09-27.md)
passed on Mini at candidate `b24723f5946df97ffa2401832d7a860784c36684`
with 257,489 original rows preserved, a second migration pass, passive
startup, and unchanged base job/cloudflared fingerprints. It remains a
source-copy compatibility checkpoint, not a protected restore or M5 gate.

At 2026-09-27, the current draft PR #77 head
`1722ba427b9bcf08df317d29f5caae4ab0cdabbb` passed all 15 reported
checks. [Norn CI run 36304890397](https://github.com/antiartificial/norn/actions/runs/36304890397)
ran the complete API suite on Linux with disposable PostgreSQL 16 and etcd
together, plus the production etcd TLS/RBAC, Mini schema, web, CLI, workflow,
and release-bundle checks. [Repository CI run 36304890452](https://github.com/antiartificial/norn/actions/runs/36304890452)
passed the protected-master legacy contexts. A PostgreSQL timestamp assertion
was corrected to compare against the database-persisted precision before
this exact-head run. GitHub reports the draft PR mergeable; no protected merge,
production signing, Mini upgrade, or Fleet launch has occurred. This is the
integration candidate's CI baseline, not M0–M9 sign-off.

## Shortest release path from here

1. Decide Mini's control-store RPO and off-host retention, then qualify its
   protected backup and clean-host restore. Preserve the one-hour freshness
   rule of the separate legacy-upgrade proof.
2. Finish M1 effect reconciliation and M2 real-provider, cross-node database
   and archive proof. Clear Fleet CI runner admission, then qualify an exact
   protected candidate on separate hosts with three etcd voters and app pools.
   Record all pins.
3. Review the integrated candidate in draft PR #77, including the protected
   release and Fleet boundaries, then merge only through protected `master`.
   After the exact reviewed candidate is merged, publish and verify its signed
   immutable bundle with the protected release environment. Preserve the
   legacy macOS and V3-specific checks on the merge candidate.
4. Run the loaded M4 placement/drain and isolated M5 Mini upgrade/rollback
   rehearsals. Complete M6–M7 for the supported app database and migration
   scope; sign M8 before the separately approved M9 adoption steps.
