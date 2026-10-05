# Fleet controller: final cross-cutting review (2026-10-04)

Scope: `feature/v3-fleet-controller`, the 5 commits on `7ea5895f..956f169b`
(WP1–WP14), reviewed as a whole against [`proposal.md`](./proposal.md),
[`plan.md`](./plan.md) §2/§5/§6, [`plan-review.md`](./plan-review.md),
[`lifecycle-contract.md`](./lifecycle-contract.md),
[`qualification-2026-10-04.md`](./qualification-2026-10-04.md) and
[ADR 0009](../adrs/0009-fleet-controller.md). Paths are relative to `v2/api/`
unless noted. No code was changed by this review.

## Verdict

**Not ready to merge as is.** The Fleet design and code are sound: no fence
bypass on a live path, no read that writes fence state, and approval reporting
that never over-claims. But the branch breaks the **whole-module** test suite
in two packages outside Fleet (`store`, `startup`). These failures do not occur
on the base. Every per-WP sweep, and the qualification run, used narrow
`-run` regexes, so none of them saw these failures. Three of the failures are
mechanical version bumps. One is a semantic conflict with H1 that needs a
decision. Once those four are resolved, the remaining items are majors and
minors that can be tracked as follow-ups.

## Test runs

- `sweep.sh`: all Fleet steps PASS. The only failures are the two known
  exclusions: the root-package Nomad/TLS skip under strict mode, and
  `controlrecovery TestCreateVerifyRestorePassiveRoundTrip`.
- Whole-module `go test -count=1 ./...` from `v2/api`, with each failure re-run
  on the pristine base (sequentially, same DBs):

| Package / test | HEAD | Base | Classification |
|---|---|---|---|
| `store` `TestControlAuthorityIsPersistedAndExpectedMatchIsEnforced` | FAIL (`schema version=49`) | PASS | **Regression.** Mechanical: the branch edited the assertion to `48`, but migration 49 also landed. |
| `store` `TestSyntheticMiniControlUpgradeAndReaderBoundary` | FAIL (`writer 31 below required 32`) | PASS | **Regression.** Semantic (H1); see B2. |
| `startup` `TestWriteContractProbe` | FAIL (contract is now writer 32 / catalog 49) | PASS | **Regression.** Mechanical expectation. |
| `startup` `TestPlatformUpgradeStartupContractProbeUsesSanitizedEnvironment` | FAIL (fixture prints 31/47) | PASS | **Regression.** Mechanical fixture. |
| `store` `TestSchemaMigratorQuiescentControlDatabaseGate` | FAIL in the whole-module run, PASS in isolation | not run in a whole-module base run | Shared-DB interference from parallel packages ("2 other client backends"); not attributed to the branch. |
| `controlrecovery` `TestCreateVerifyRestorePassiveRoundTrip` | FAIL | FAIL | Pre-existing (known exclusion). |
| `cmd/norn-control-recovery` `TestCLIEncryptedRecoveryKeysAndPassiveRestoreEndToEnd` | FAIL | FAIL | Pre-existing; same classification drift. |
| `database` `TestDumpFromOneServerRestoresIntoTheSameNamedDatabaseOnTheOther` | FAIL | FAIL | Pre-existing / environment. |

I tried to apply the three mechanical fixes (below 10 lines). The session's
permission policy blocked edits to pre-existing test assertions, so they are
listed as blockers for the implementer instead.

## Findings

### Blockers

**B1. Three mechanical expectation updates are missing outside the Fleet regexes.**
- `store/operation_acceptance_integration_test.go:499`: change `version != 48` to `49`.
- `startup/mode_test.go:67-68`: the expected `SchemaContract` should be `WriterVersion: 32, CatalogMigrationVersion: 49, CatalogMinimumWriterVersion: 32`.
- `startup/platform_upgrade_test.go:74`: the fake probe JSON needs the same 32/49/32.

These are mechanical, but the startup contract is operator-visible. Platform
upgrade compares it, so release notes should say that the startup contract is
now writer 32 / catalog 49.

**B2. `TestSyntheticMiniControlUpgradeAndReaderBoundary` asserts the opposite of H1.**
`store/mini_synthetic_upgrade_integration_test.go:159` asserts that additive
migrations after 45 keep the writer-31 rollback contract valid. Migration 48
raises `MinimumWriterVersion` to 32 by design (H1, accepted). This is the
concrete evidence that a Mini or older writer is locked out as soon as any
process applies 48, whether or not a target is ever registered. The test needs
a deliberate semantic rewrite: writer 31 remains valid through migration 47 and
is refused from 48 on. The human should confirm that rewrite explicitly,
because it modifies a pre-existing assertion. This is the one assertion change
on the branch that is not mechanical.

### Majors

**M1. The reconciler is off by default, so the success measure is opt-in.**
`fleet_reconciler_runtime.go:23` reads `NORN_FLEET_RECONCILER=true`. Without
it, no process ever derives status:
- `POST resources/{name}` creates a resource with an empty `status`;
- `SetDesired` and observation appends never recompute it;
- reads report `controller.state=unknown`, which is honest but means the
  record explains nothing.

The opt-in is not in plan §2.4 or Q6. It is documented only in
`docs/v2/operations/fleet-pilot.md:90`. Decide whether it should default to on
in the two placement processes, or record it as a decision in ADR 0009.

**M2. There is no operator runbook for the new break-glass surface.**
ADR 0009 says "the runbook must…" (H2, `adrs/0009-fleet-controller.md:150`),
but no runbook exists. Missing entries:
1. Registering a target:
   - prerequisites: every writer at ≥ 32 (H1), and every pre-fence etcd Fleet runtime stopped (H2, which the code cannot enforce);
   - choosing aliases (they are immutable);
   - what `fleet_target_registration_in_flight` means, and using abandon-plan (H7) to clear it.
2. Reading `GET targets/{id}`: Free, Active and Uncertain, and each Uncertain reason.
3. What to do on `Uncertain`:
   - `DispatchSubmissionUnresolved` and `NoLiveAttempt`: wait for the runner, or inspect GitHub;
   - `AttemptTerminalWithoutSuccess`: terminal release;
   - `AuthoritySuperseded`: re-bind by recovery, or release.
4. Terminal release versus abandon:
   - `expectedGeneration`;
   - the 30-minute `AbandonMinimumAge`;
   - both permanently abandon the plan (m16), so a new plan is required;
   - revalidation (`fleet_plan_revalidation_required`, 5-minute skew) blocks plans created before the release.
5. The ordinary-lane pre-submit defect (H4): the fence stays held and is cleared only by abandon after 30 minutes.
6. New configuration:
   - `NORN_FLEET_RECONCILER`;
   - `NORN_FLEET_GITHUB_RECOVER_WORKFLOW` (default `recover.yml`; Q11 and terminal release depend on it);
   - adding `observe` to `GitHubActionsFleetAllowedIntents` (the authority-only validator now accepts `apply,recover[,observe]`).
7. Restore:
   - the epoch advance in `RestorePassive` (`controlrecovery/restore.go:166`);
   - a failed advance leaves a non-empty target that cannot be re-restored, so a manual step is needed;
   - the etcd restore step (H5) exists only in the script.

**M3. The qualification record overstates coverage.** It reports no
whole-module run, and `docs/v3/implementation-status.md` still says that only
WP1 "has landed". Both must be updated after B1 and B2 are resolved.

### Minors

- **m1. The etcd first attempt now requires a dispatch preparation, even with an empty registry.**
  `etcdstore/v3_fleet_runner_attempt.go:231` reads the preparation
  unconditionally. Before the branch it was read only when `len(attempts) > 0`
  (`:172-178`). Any binding without a preparation key now gets
  `fleet_runner_attempt_dispatch_mismatch`. This is probably unreachable,
  because `FinishFleetGitHubDispatch` requires the preparation, but it is a
  behaviour change outside the fence and should be pinned or noted.
- **m2. PG `ReconcileFleetGitHubReservation` gained a new refusal that nothing maps or tests.**
  At `handler/fleet_github.go:241` it calls `FinishFleetGitHubDispatch`, which
  now refuses abandoned plans. That refusal surfaces as 500
  `fleet_github_receipt_failed`, not as `fleet_target_holder_abandoned` the
  way the three other call sites do through `writeFleetGitHubFinishError`. The
  route has no owning test (lifecycle-contract §1 row "github/reconcile").
- **m3. Exported unsigned bypasses on production types.**
  - `etcdstore/v3_fleet_fence_testhooks.go:101,116`: `ReleaseFleetTargetFence` and `AbandonFleetTargetPlan` skip signing and every rule.
  - `store/fleet_targets.go:86`: `(*DB).RegisterFleetTarget`.
  - `store/fleet_targets.go:348`: `(*DB).AdvanceFleetAuthorityEpoch`.
  - `store/fleet_resources.go:206`: `(*DB).SetDesiredFleetResource`, which has no CAS.

  None has a production caller (checked by grep). Each is one stray call away
  from an unsigned or uncompared mutation. Prefer build tags, or a comment and
  lint guard, as the etcd file already does.
- **m4. `LastApplied` is carried forward from the cached status.**
  `fleet/controller/derive.go:497-503` re-derives it only while the fence's
  *latest* release is `succeeded`. If a later `dispatch_not_submitted` or
  abandon release overwrites `last_release` before any reconcile observed the
  success (reconciler off, or a fast follow-up), the success is never
  recorded. This is an edge case; it is tied to M1.
- **m5. The approval policy under-reports.**
  `FleetApprovalPolicy` (`handler/fleet_resources.go:362`) never sets
  `OwnerApprovalEnvelope`, even on the external-Mac lane where it is enforced.
  This is conservative and never over-claims, but it is not exact.
- **m6. The abandoned-plan guard on legacy heartbeat and non-terminal advance is indirect.**
  The single `UPDATE`s (`store/fleet_runner_attempts.go:178`,
  `fleetRunnerAdvanceSQL`) carry only the epoch predicate. A late runner is
  stopped because `requireFleetAttempt`'s D8 write-on-read marks the expired
  attempt abandoned, and abandon requires an expired lease plus 30 minutes.
  This is correct today but fragile if D8 ever changes. The terminal advance is
  directly guarded, because a free fence refuses `DecideRelease`.
- **m7. New 500 paths on unchanged routes.**
  With an empty registry, every PG prepare, reset, execute, rerun and dispatch
  call does an extra abandoned-plan read (`refuseAbandonedFleetPlan`). A DB
  error there now returns 500 `fleet_github_receipt_failed` before any
  existing logic runs. Response codes are otherwise unchanged.
- **m8. Stale package comment.** `fleet/fleettest/doc.go:9` still says "This
  file is a placeholder". The suites have landed.
- **m9. `POST resources/{name}` is not in plan §2.5's route table.**
  It requires `platform:operate` in both the matcher and the handler, and the
  OpenAPI lists it. Record it as an additive deviation in the plan/ADR.
- **m10. The `/attempts` scope matcher is narrower than the plan wrote.**
  `handler.FleetPlanRunnerScopedPath` enumerates exact shapes; the plan
  described a simple prefix narrowing. For every *routed* path the effect is
  identical (checked against `main.go` 828-835 and 2157-2164). It is stricter
  only for unrouted shapes, which now get the default scope instead of
  deferring to the handler. This is acceptable.

## Acceptance criteria

| Item | Verdict | Evidence |
|---|---|---|
| **Change 1:** freeze the lifecycle contract | **Partially met** | `lifecycle-contract.md` maps every route, phase, credential boundary, approval rule, D1–D12, dead code, the H4 defect and the Q11 gap. ADR 0009 records the decision. However, 7 rows still name no owning regression test (`GET github`, `GET plans` direct, `github/reconcile`, and the structural etcd absences). The contract was not extended to the new fence codes and routes (this pass did not require it). |
| **Change 2:** shared lifecycle semantics (H6 narrowed) | **Met within H6** | `fleet/lifecycle` holds the pure helpers and the fence/epoch decisions, and both backends call them. `RunFleetLifecycleConformance` and `RunFleetFenceConformance` run against live PG legacy and etcd, including the three-member TLS cluster. No existing check was weakened. One-domain-service unification is deferred by the human's H6/Q9 decision. |
| **Change 3:** target execution ownership | **Met** | Acquire happens at submit (PG `MarkFleetGitHubDispatchSubmittingFenced`; etcd in the `AcceptFleetGitHubDispatch` txn). Bind covers first attempt and recovery. Release on complete is atomic with the terminal advance. Abandon refuses finish, start, recover, rerun and checkpoint. Alias resolution is strict at acquire/bind. Registry generation is compared or locked. Every live mutation path I traced is fenced when a target is registered; the only unfenced paths are the unrouted PG V3 path (Q9) and cancel / failed checkpoint (Q10). |
| **Change 4:** resource and observer | **Partially met** | Storage, bounded history, watermark ingestion, the CAS reconcile, read-time freshness, liveness and the read API are all present, and the conformance suites pass on both backends. However, status exists only when `NORN_FLEET_RECONCILER=true` (M1). "What exists" depends on observations that no shipped executor produces yet (change 5). |
| **Constraint:** no second ledger | **Met** | The resource stores desired, policy, watermarks and a derived status. Admission never reads `fleet_resources`/observations (grep: only the resource files). The fence stores no attempt ID (m18). Register, release and abandon are signed ops in the existing ledger. |
| **Constraint:** unified PG/etcd semantics (narrowed H6) | **Met within scope** | The shared suites run per backend. New intentional differences: PG has the `dispatch_not_submitted` release and Q11; etcd has neither and relies on its existing stop proof; PG release uses DB time and etcd uses process time (T2). These are documented in plan/ADR, but not yet in the lifecycle contract's D-table. |
| **Constraint:** one unresolved owner per target | **Met** | Occupancy is Active only when the fence is held and a live attempt exists; heartbeat expiry never releases. Two-plan, alias and race tests pass, including under an etcd partition. The epoch advance refuses old-epoch writers. |
| **Constraint:** credential boundaries | **Met** | No provider credentials are added. Release proof is gathered server-side through GitHub App observers (`json:"-"` evidence, re-validated in the store). Observations require a CI `fleet:operate` token with the fail-closed `observe` intent, a protected ref, the bound repository tuple and an `environment:` alias. The CI identity can never mutate targets or set desired. |
| **Constraint:** accurate approval reporting | **Met (conservative)** | Merged-plan provenance is claimed only for `github-merged-plan`. Environment reviewers and independent review are reported `not_enforced`. Operator-declared commits claim nothing. The owner envelope is under-reported (m5). |
| **Success measure** (one record explains requested / exists / running / blocking / next) | **Partially met** | The resource view has desired + history, conditions + watermarks, `status.active` with occupancy, `blocker` and `nextAction`. It holds only while the reconciler runs (M1) and only once an observe-capable executor exists. Until then, conditions are honestly `Unknown`. |

## Cross-WP traces (summary)

**PG legacy lifecycle.** The trace runs:
1. register (signed; registry `FOR UPDATE`; in-flight scan);
2. resource create, then set desired (`ResolveApprovedPlan`);
3. dispatch: `refuseAbandonedFleetPlan`, then pre-check, then reservation, then acquire (fence `FOR UPDATE` under the plan advisory lock, abandoned check, `DecideAcquire`, then `submitting`);
4. `FinishFleetGitHubDispatch` (abandoned predicate);
5. start: `bindFleetTargetFenceTx` after the runner advisory lock;
6. heartbeat and advance: epoch predicate in the single `UPDATE`;
7. checkpoint: abandoned check, then fence/epoch `FOR SHARE`, then attempt `FOR UPDATE`;
8. advance to complete: fence `FOR UPDATE`, then the `UPDATE`, then `DecideRelease(succeeded)` in one transaction;
9. reconcile (`RepeatableRead`, resource `FOR UPDATE`, no fence or attempt writes);
10. read (`DowngradeStale`).

The lock order is consistent: no cycle among acquire, bind, checkpoint,
terminal advance and release.

**etcd lifecycle.** Same shape, with ModRevision compares for the fence, the
epoch and the registry, and a `CreateRevision==0` compare for the abandoned
key on every gated txn. The race loser re-reads the fence (m10).

**Failure branches checked:**
- An ambiguous dispatch stays `submitting`/`prepared` and reads as Uncertain/`DispatchSubmissionUnresolved`. Abandon after 30 minutes, with a listing snapshot, frees the fence and permanently refuses the plan.
- Expiry gives Uncertain/`NoLiveAttempt`, and the fence is never freed.
- An epoch advance refuses heartbeat, advance and succeeded checkpoints; failed checkpoints and cancel are allowed; recovery or first attempt re-binds at Generation+1.
- Restore advances the epoch by CAS after verification.

No seam was found where two WPs each assumed the other enforced a rule.

**Reads that write.** None of the new read paths write:
- `GET targets` and `GET fence` use `FleetTargetHolderFacts`, a plain read;
- `GET resources` derives in memory.

Reconcile writes only the resource row, and only on a change. The
pre-existing PG legacy attempt GETs still write `abandoned` (D8,
intentionally unchanged).

**Empty registry.**
- No pre-existing Fleet-route assertion was changed. The pre-existing test
  files touched are `controlrecovery/registry_test.go`,
  `store/control_schema_integration_test.go` and
  `store/operation_acceptance_integration_test.go`. Their only assertion edits
  are table counts, migration counts and the writer-version constant, all
  mechanical; the other four touched files only add tests.
- Behaviour differs from the base only in m1 and m7.

## Follow-ups for the human

1. Decide B2 (rewrite the Mini writer-31 test for H1), apply the B1 bumps, then re-run the whole module.
2. Decide the reconciler default (M1).
3. Write the operator runbook (M2), including the H1 rollout warning: applying migration 48 locks out every writer below 32, Mini included, with no rollback.
4. Update `qualification-2026-10-04.md` and `implementation-status.md` (M3).
5. Gate or remove the unsigned exported store and etcd mutators (m3).
6. Map and test the abandoned refusal on `github/reconcile` (m2), and pin the etcd preparation requirement (m1).
7. Still deferred from earlier decisions:
   - end-to-end PG restore (blocked by the base `controlrecovery` drift, fixed on another branch);
   - production etcd epoch advance (H5);
   - generic step-up for release (H3);
   - executor packaging, without which observations cannot exist (change 5).
