# Review of the Fleet controller implementation plan (changes 1–4)

Reviewer: skeptical senior review of [`plan.md`](./plan.md) against
[`proposal.md`](./proposal.md) and the code at `7ea5895f`
(`feature/v3-fleet-controller` worktree). Code paths are relative to `v2/api/`
unless they start with `docs/` or `v2/`.

`go build -buildvcs=false ./...` succeeds on the base commit. No integration
suites were run for this review. Every finding below comes from reading the code.

**Verdict:** the plan is not ready to implement as written. Its current-state
map is mostly accurate: almost every cited symbol, line number, migration number
(48 is the next free number; 47 is `database-cutover-evidence-references`) and
test name exists. The fence design, however, has three blockers:

- some states can never be released;
- the PG release proof cannot be computed;
- the "shared suite" would qualify an unrouted PG code path instead of the live one.

There are also several major correctness and process gaps.

---

## Blockers

### B1. Some "outcome uncertain" states have no release path

**Evidence**

- The plan's explicit release requires a bound run that GitHub observes as `completed` (plan.md:278).
- `RecoverBoundPlan` turns *absence* into `ErrDispatchAmbiguous` by design (`githubapp/client.go:563-589`).
- `ReconcileDispatch` says "An absent result stays ambiguous" (`githubapp/reconcile.go:111-129`).
- So a PG dispatch left in `submitting` with `run_id=0` (`store/fleet_github_dispatches.go:108-111`) can never bind a run when GitHub never created one.
- The same applies to an etcd preparation that has no binding (`etcdstore/v3_fleet_github_dispatch.go:125-131`). The etcd path has no reset or delete at all.
- "Release: never submitted" (plan.md:277) only covers the client-proven pre-submit case.
- Result: the target stays occupied forever, and only manual DB/etcd surgery can free it. That is exactly the deadlock the proposal forbids in spirit. The proposal says only "heartbeat expiry alone cannot release"; it does not demand permanent lockout.

**Recommended plan change**

1. Add an audited **abandon** transition for the holder dispatch, recorded as a signed operation in the *existing* `operations` ledger (for example `fleet.target.fence-release`).
2. Require `admin`, plus step-up where the backend supports it.
3. Allow it only after a minimum age since `submission_started_at` (or the preparation's `CreatedAt`).
4. Store a GitHub listing snapshot as evidence.
5. Make the abandon atomically and permanently refuse the following for that plan and nonce:
   - `FinishFleetGitHubDispatch`;
   - first-attempt admission;
   - recovery;
   - rerun.
6. Result: a run that appears later cannot register an attempt. The runner's attempt registration is the real execution-boundary fence, so this is safe without proof of absence.
7. Specify this transition for both backends in section 2.2.

### B2. PG explicit release cannot use `ObserveApplyRunAttempt`, and recovery runs are not observable

**Evidence: the raw nonce is unavailable on PG**

- `observeApplyRun` requires the **raw** nonce (`dispatchNonceRe.MatchString(nonce)`, `githubapp/reconcile.go:157`).
- It also verifies the run's `dispatch_nonce` input (`githubapp/reconcile.go:144`).
- PG stores only the hash. The comment is at `store/fleet_github_dispatches.go:11-13`, migration 44 runs `DROP COLUMN IF EXISTS dispatch_nonce` (`store/master_protected_pilot_migration.go:33`), and the rerun comment says "Norn never persists it" (`handler/fleet_github.go:290`).
- `ObserveApplyRunAttempt` exists only on the etcd handler's observer interface (`handler/fleet_etcd_attempts.go:36`).
- plan.md:636 says release "reuses `fleetApplyRunObserver.ObserveApplyRunAttempt`". That works only on etcd.

**Evidence: recovery runs cannot be observed**

- Recovery executors run in a **different** GitHub run (`currentRun == binding.RunID` is rejected at `handler/fleet_runner.go:629`) with intent `recover`.
- `observeApplyRun` verifies apply-workflow inputs, so it cannot observe a recover run at all.
- plan.md:278 checks "`binding.RunID` and the latest attempt's run attempt". That conflates two different runs.
- If a recovery attempt is the latest holder, the check either:
  - cannot pass, which is another deadlock; or
  - checks the wrong run, which is unsafe because a live recovery executor would be ignored.

**Recommended plan change**

- Add a WP (or extend WP6) with new `githubapp` methods:
  - an apply-run observer keyed by nonce **hash**, reusing `verifyApplyRunByNonceHash` (`githubapp/client.go:816`);
  - a recover-run observer keyed by `(runID, runAttempt)` parsed from each attempt's `RunnerAttemptID`, which verifies workflow path, actor, repository and plan input.
- Release must prove terminal status for **every distinct run ID that ever hosted an attempt**, plus the bound apply run. It must not check only `binding.RunID`.
- Add the observer to the PG `Handler`'s `fleetGitHub` interface.

### B3. The shared behavioral suite targets the unrouted PG path, not the live one

**Evidence**

- The live PG routes are the legacy handlers (`main.go:832-837`, `main.go:2149-2154`).
- The signed PG V3 handlers (`handler/fleet_attempts.go:49-225`) are referenced only from tests (`handler/acceptance_integration_test.go:1117`).
- WP3 wires the PG suite with profile `PostgresV3` (plan.md:543).
- The harness signature `AdmitAttempt(store.OperationAcceptance)` (plan.md:542) cannot drive legacy `CreateFleetRunnerAttempt(*model.FleetRunnerAttempt)` (`store/fleet_runner_attempts.go:16`).
- Change 2's acceptance criterion, "PG and etcd pass the same behavioral suite", would therefore be met by dead code. The live PG path would get only hand-written fence tests.
- WP5a also spends effort fencing that unrouted path (`acceptFleetRunnerAttempt`, `UpdateFleetRunnerAttempt`; plan.md:591-592).

**Recommended plan change**

- Make the conformance harness operate at the **handler/HTTP boundary**: start/heartbeat/advance/cancel/checkpoint requests with a CI principal. Implement it once for PG legacy and once for etcd.
- Alternatively, give it a legacy adapter that branches only on `Profile`.
- Run it on `PostgresLegacy` and `EtcdV3`.
- Drop PG V3 from WP5a unless Q9 decides to route it.
- Keep the existing V3 tests as they are.

---

## Majors

### M1. The "registry empty" decision and alias resolution sit outside the admission snapshot

**Evidence**

- T7 covers only the fence and the epoch (plan.md:213).
- The rule "if the registry is empty, behave as today" (plan.md:262-263) is a separate read.
- A plan can pass the empty check concurrently with a registration, while another plan then acquires the new fence.
- Registering a target while a plan is in flight is unaddressed. Its dispatch is already `submitting` or `dispatched`, but it holds no fence. A second plan then acquires the free fence: two executors on one target.
- The in-flight plan's next attempt bind (requires "held by this plan", plan.md:274) is also refused, which strands it.

**Recommended plan change**

- Add a registry generation row or key:
  - PG: a singleton row read `FOR SHARE` in admission and `FOR UPDATE` in registration;
  - etcd: one key, compared on ModRevision in every admission txn.
- Make aliases immutable in this pass. In etcd, compare the alias key ModRevision.
- Registration must either refuse while any plan whose cluster or environment resolves to the target has a non-terminal dispatch or attempt, or adopt that plan as the holder in the same transaction.
- Add a conformance case `RegisterWhileInFlight`.

### M2. Mixed-version writers bypass the fence

**Evidence**

- plan.md:833 keeps migration 48/49 `MinimumWriterVersion` equal to migration 47 "so mixed-version Mini nodes are unaffected".
- An older binary writing `fleet_github_dispatches` and `fleet_runner_attempts` has no fence code. It bypasses the fence entirely.
- The migrator already has a writer-floor mechanism (`store/schema_migrations.go:488-494`).

**Recommended plan change**

- Either raise `ControlSchemaWriterVersion` and migration 48's `MinimumWriterVersion` so that old writers are excluded once the fence tables exist,
- or refuse target registration until the schema status shows no writer below the fence-aware version.
- Document the choice in the ADR.

### M3. `Active` occupancy is too permissive

**Evidence**

- `FenceOutcome` marks `Uncertain` only in three cases (plan.md:280-285):
  - the dispatch is `submitting`;
  - the latest attempt is terminal but not succeeded;
  - the epoch is old.
- A held fence whose dispatch is `dispatched` but has **no attempt** therefore stays `Active` (`await_runner`) forever. This happens when the runner died before registering, or when the workflow failed early.
- That contradicts "stale evidence → Unknown". On PG it is also unreleasable (B2).

**Recommended plan change**

- Define `Active` ⇔ held **and** a live, unexpired attempt exists.
- Every other held state is `Uncertain`.
- This is simpler and strictly more conservative.

### M4. Revalidation only looks at successes

**Evidence**

- `fleet_plan_revalidation_required` compares against `LastSucceeded.CompletedAt` (plan.md:273, plan.md:276).
- After an explicit release of a failed or partial apply, an older plan is admitted against a baseline that has changed.
- The proposal says the second plan "must be revalidated before execution". That covers any intervening execution, not just a success.

**Recommended plan change**

- Record `last_released_at` (DB clock) and the release reason.
- Refuse plans whose `StartedAt` is before any release other than `dispatch_not_submitted`.
- Treat timestamps within a clock-skew bound as "refuse".

### M5. The rerun path is under-specified and becomes unfenced after release

**Evidence**

- WP5a says `RerunFleetGitHubApply` gets a "same-plan check only" (plan.md:589).
- The rerun is allowed when no attempts exist and the run concluded failure (`handler/fleet_github.go:640-669`). Those are exactly the conditions under which explicit release is allowed.
- After a release, a rerun would execute with the fence `free`.

**Recommended plan change**

- The rerun must require the fence to be `held` by the same plan and nonce at the current epoch, atomically with `MarkFleetGitHubDispatchRerunSubmitting`.
- Explicit release must terminally abandon the holder dispatch (see B1).

### M6. The "never submitted" release relies on a code path that is currently a no-op

**Evidence**

- In `DispatchFleetGitHubApply`, `MarkFleetGitHubDispatchSubmitting` runs at `handler/fleet_github.go:857`.
- The `ErrDispatchPreSubmit` branch then calls `DeletePreparedFleetGitHubDispatch` (`handler/fleet_github.go:867-869`).
- That delete only matches `dispatch_state='prepared'` (`store/fleet_github_dispatches.go:129`), so it deletes nothing. The row stays `submitting`, and the next call goes to `RecoverBoundPlan`, which stays ambiguous.
- No test covers this path. The only test call is the direct store call at `store/fleet_github_dispatches_integration_test.go:118`.
- `TestDispatchPreSubmitFailureReleasesFence` (plan.md:609) therefore either fails or silently requires a behavior change.

**Recommended plan change**

- Record this as a pre-existing defect in WP1.
- Decide explicitly whether to switch the ordinary lane to `ResetFleetGitHubDispatchPreSubmit` (a behavior change to flag).
- Only then count it as a release path.

### M7. PG lock-order inversion between checkpoint admission and legacy advance

**Evidence**

- Checkpoint admission takes the V3 advisory key `norn:fleet-attempt:` (`hashtextextended`), then locks the attempt row `FOR UPDATE` (`store/fleet_reconciliation_acceptance.go:88,112`).
- Legacy create and recover use a *different* key, `hashtext('norn.fleet-runner:'…)` (`store/fleet_runner_attempts.go:28,328`).
- Legacy heartbeat and advance take no lock at all; each is a single `UPDATE` (`store/fleet_runner_attempts.go:115-167`).
- If WP5a adds "fence row lock" to advance (fence, then attempt) and an epoch check to checkpoint admission (attempt, then fence), the two paths deadlock. PG aborts one with 40P01, which surfaces as a 500.
- plan.md:832 states only "plan lock, then fence row", and the plan locks are not even shared between these paths.

**Recommended plan change**

- Use one global order: fence row before attempt rows, in every path, including `enforceFleetReconciliationAdmission`.
- Use `FOR SHARE` for epoch checks and `FOR UPDATE` only for acquire/bind/release.
- For legacy heartbeat and advance, an `AND EXISTS (SELECT … fleet_target_fences f JOIN fleet_authority a …)` predicate inside the existing single `UPDATE` avoids new locking entirely.

### M8. WP2 is over-abstracted and contradicts the plan's own "D1–D12 unchanged" rule

**Evidence**

- T1 and T3 (plan.md:203-213) require a transactional snapshot and one atomic commit.
- But D9 (non-atomic PG V3 evidence check, `handler/fleet_attempts.go:213,402`) is preserved.
- Legacy start reads the binding and attempts outside any transaction (`handler/fleet_runner.go:91-111`) and inserts in a separate transaction (`store/fleet_runner_attempts.go:23-55`).
- Legacy heartbeat and advance are lock-free single statements.
- `DecideAdmission(Snapshot…)`, `DecideHeartbeat`, `DecideAdvance` and `DecideCancel` over three paths with different request schemas (D1), replay rules (D12), expiry semantics (D8) and recovery rules (D5–D7) either:
  - run on a non-transactional snapshot for legacy, violating T1; or
  - force a restructuring of legacy, which is a behavior change.
- The profile struct mostly re-encodes the `if backend == …` branches.
- WP2 is ~700 lines across ~10 files, including signed-envelope code.

**Recommended plan change**

- Limit WP2 to pure, zero-behavior-change helpers:
  - phase lists and next phase;
  - the drain predicate (see m5);
  - lineage;
  - expiry projection;
  - stop-proof validation;
  - the etcd checkpoint attempt check.
- Put the *new* fence and epoch decisions (`FenceOutcome`, acquire/bind/release rules) in `fleet/lifecycle`. This is where a shared contract adds value.
- Drop the `Decide*` admission/heartbeat/advance/cancel functions.
- Restate T1–T7 as obligations of the new fence/epoch checks only.
- Split WP2 into WP2a (helpers, ≤300 lines) and fence decisions inside WP4.

### M9. The status CAS does not cover the inputs, and freshness is evaluated only at write time

**Evidence**

- The precondition is `{Revision, DesiredGeneration, AuthorityEpoch}` (plan.md:399-408).
- Observation append bumps a counter but not necessarily `revision` (plan.md:350).
- `GetFleetResource` and `ReconcileInputs` are separate reads (plan.md:419-421).
- A slow, single reconciler can therefore commit status derived from inputs read *before* a newer failure observation or fence change. Two controllers are not needed to trigger this.
- Conditions are frozen at `EvaluatedAt`. If the reconciler stops, a `True` readiness stays `True` forever, which violates proposal line 55 ("stale evidence produces Unknown") across a controller crash.
- The plan also says "write only if the status changed or `EvaluatedAt` is older than `RescanInterval`" (plan.md:421). Whether staleness is re-derived after a crash then depends entirely on the rescan running.

**Recommended plan change**

- PG: read the resource `FOR UPDATE`, read the inputs, derive (pure) and write, all in one transaction.
- etcd: compare the ModRevision of every input key (fence, holder dispatch/preparation, attempts) and an observation watermark in the status txn.
- Or add `InputsWatermark` (fence revision plus max observation sequence) to `StatusPrecondition`.
- Have the read API downgrade any condition whose `ObservedAt + freshness < now` to `Unknown/ObservationStale` at read time.

### M10. Observation pruning can resurrect older observations

**Evidence**

- `Applied` is computed against "the latest *applied* observation for the same source" (plan.md:352).
- Pruning removes the oldest 100+ by sequence (plan.md:355).
- If one noisy source floods the table, another source's latest applied row (for example a failure) is pruned.
- A later-arriving observation that is older than the pruned failure but newer than anything remaining is then marked `Applied=true` and clears the failure. That violates the matrix item "old observations cannot clear newer failures".

**Recommended plan change**

- Persist a per-source watermark `{observedAt, sequence}` on the resource row (or etcd value), updated atomically with the append.
- Pruning must never delete the latest applied row of any source.
- Add the conformance case `PruneNeverResurrectsOlderObservation`.

### M11. The `LastAppliedGeneration` linkage is undefined in practice

**Evidence**

- `LastApplied` comes "from `fence.LastSucceeded` + `DesiredHistory` commit match" (plan.md:322).
- The applied commit is the dispatch `ApprovedHeadSHA`, the merge commit resolved by `resolveApprovedPlan` (`githubapp/client.go:605`).
- The desired `CommitSHA` is whatever the operator PUT, for example a later `main` commit.
- `DesiredHistory` is capped at 20.
- A successful deployment can therefore show `DesiredNotApplied` forever. That fails Change 4's acceptance "accurate status across successful deployment".
- The plan's `CapacityPlan.SourceDigest` is the *pre-change* inventory digest (`fleet/schema.go:123`), so it cannot be used to match either.

**Recommended plan change**

Before WP8, define the linkage explicitly. Options:

- `PUT desired` must name a commit equal to the `ApprovedHeadSHA` of a merged plan;
- record the inventory digest at desired-put time and compare on success;
- or verify ancestry through the GitHub App.

Make `TestDeriveSuccessfulDeployment` use a realistic case where the merge SHA differs from a later desired commit.

### M12. Same-plan concurrent executors remain possible on the live PG path

**Evidence**

- Legacy recovery cancels a *live* source in the DB, with the message "superseded by verified recovery workflow", without any proof that the external executor stopped (`store/fleet_runner_attempts.go:341-351`, D6).
- The fence bind on recovery then simply re-points `HolderAttemptID` (plan.md:274).
- The old run can still be mutating.
- The proposal's invariant is "one unresolved mutation owner per infrastructure target". The plan's fence enforces it across plans only.

**Recommended plan change**

- Raise this as an explicit open question (Q11).
- Recommendation: once a target is registered, refuse legacy recovery of an unexpired source, or require the etcd-style predecessor-stop proof. That proof depends on the nonce-hash observer from B2.
- Either way, the contract document must say this gap exists.

### M13. A first attempt under a superseded epoch is stranded

**Evidence**

- The fence is acquired at dispatch under epoch N. If the epoch advances before the runner registers, the first attempt bind requires `AuthorityEpoch == current`, so it is refused (plan.md:274).
- Re-binding is defined only for *recovery*.
- Recovery requires a predecessor attempt on every path:
  - `handler/fleet_runner.go:135-138`;
  - `store/fleet_runner_attempt_acceptance.go:279-282`;
  - `etcdstore/v3_fleet_runner_attempt.go:176-179`.
- The plan is then stuck. On PG it is also unreleasable (B2).

**Recommended plan change**

- Let first-attempt admission re-bind (generation+1, new epoch) when the holder plan and nonce match, exactly as recovery does.
- Add `EpochAdvanceBeforeFirstAttemptRebinds`.

### M14. Work-package parallelism errors

The dependency graph (plan.md:474-488) allows conflicting parallel work.

| Parallel pair | Conflict |
|---|---|
| **WP3 ∥ WP4** | WP4 wires `RunFleetTargetConformance` "in both conformance test files" (plan.md:574), and WP3 creates those files. Both also add to `fleet/lifecycle`. |
| **WP5a ∥ WP5b** | WP5b wires `store/storetest/fleet_fence.go`, which WP5a creates (plan.md:596, 623). Both must implement fence decisions in `fleet/lifecycle`, and "fence logic arrives in WP5" (plan.md:520). |
| **WP6 ∥ WP9** | WP9 depends only on WP5a/b, WP7 and WP8. Both WP6 and WP9 edit `main.go` and `etcd_fleet_runtime.go`. WP9 edits `handler/fleet_github.go` and `etcd_fleet_github_dispatch.go`, which WP5a/b also touch. |
| **WP1 ∥ WP2** | WP1's contract document cites function names that WP2 deletes. WP1's missing regression tests are the safety net WP2 needs, so they must land **first**. |

**Recommended plan change**

1. WP1-tests.
2. WP2a (helpers).
3. WP4 (fence types, decisions, storage, suite skeleton).
4. WP5-suite (shared fence suite file).
5. WP5a ∥ WP5b, each touching only its own backend.
6. WP6, then WP9, then WP10, serialized on `main.go` and `etcd_fleet_runtime.go`.
7. WP7 ∥ WP8 can run alongside WP5.

### M15. Test commands can pass silently

**Evidence**

- Suites "wired" as subtests of `TestFleetLifecycleConformance{Postgres,Etcd}` are verified with `-run 'FleetResource'`, `-run 'FleetReconciler'` and `-run 'FleetFence'` (plan.md:611, 627, 667-668, 706-707).
- Those patterns match no top-level test, so the command exits 0 with `[no tests to run]`. Verified: `go test ./handler -run TestRequiresDrain` prints `ok … [no tests to run]`.
- The three-member script exports only `NORN_TEST_ETCD_TLS_*` (`v2/scripts/test-etcd-three-member-fleet:113-119`) and checks only exit codes.
- Any suite that gates on `NORN_TEST_ETCD_ENDPOINTS` would SKIP, and the script would still print PASS.
- The §4 pass criterion "grep SKIP" is advisory, and the §4 handler command lacks `-v`.

**Recommended plan change**

- Give each suite its own top-level test: `TestFleet{Lifecycle,Target,Fence,Resource,Reconciler}Conformance{Postgres,Etcd}`.
- Add `NORN_TEST_REQUIRE_INTEGRATION=1`, which turns these Skips into `t.Fatal`.
- Make the script set it and fail on `--- SKIP` or `no tests to run`.

### M16. The authority-restore qualification is not faithful

**Evidence: etcd**

- The plan copies the prefix to a new prefix (plan.md:739).
- But the acceptance index stores **absolute keys including the prefix**: `OpPut(operationAcceptanceIndexKey(id), key)` (`etcdstore/v3_fleet_runner_attempt.go:236`), which is read back at `:446-454`.
- A copied prefix therefore resolves receipts from the *old* prefix.

**Evidence: PG**

- `CREATE DATABASE … TEMPLATE` fails while the test's own pool is connected to the source database.

**Additional gap**

- Q4 leaves restore unwired, so "authority restore invalidates old ownership" is only simulated by calling the method directly.

**Recommended plan change**

- Use the existing real snapshot restore stage of the three-member script (`v2/scripts/test-etcd-three-member-fleet:192-211`) to run the restore case against restored data.
- For PG, use `pg_dump`/`pg_restore`, or close the pool before `TEMPLATE`.
- Decide Q4 as below.

### M17. Route-authorization hazards in the new paths

**Evidence**

- `controlScopeForRequest` defers *all* non-GET `/api/v1/fleet/` paths containing `/attempts` to the handler (`main.go:1835-1838`).
- A resource named `attempts-prod` therefore makes `PUT /api/v1/fleet/resources/attempts-prod/desired` skip middleware scope enforcement.
- The authority-only router's CORS allows only GET/POST/DELETE/OPTIONS (`main.go:2125`), but the plan adds PUT routes.

**Recommended plan change**

- Every new handler calls `requireControlScope` / `requireFleetOperateScope` itself, as the existing Fleet handlers do (`handler/fleet.go:156`).
- Optionally narrow the matcher to `/api/v1/fleet/plans/`. That only narrows existing behavior.
- Add PUT to CORS, or use POST.

---

## Minors

### m1. Factual slips in section 1

- 1.4 credits PG legacy with `validateFleetRunnerAttemptLineage`. Legacy runs **no** lineage validation; `CreateFleetRunnerAttempt` only derives the root (`store/fleet_runner_attempts.go:31-38`). The function is V3-only (`store/fleet_runner_attempt_acceptance.go:276`).
- The PG dispatch states are `prepared → submitting → dispatched`, plus `rerun_submitting`. There is no `bound` state (`store/fleet_github_dispatches.go:69-111`).
- `/github/reconcile` exists only in the general router (`main.go:842`). It is absent from `fleetAuthorityOnlyRouterWithHandler` (`main.go:2155-2161`).
- The PG V3 handlers are also exercised by `handler/fleet_attempts_test.go`, not only by `acceptance_integration_test.go`. Minor.

### m2. Dead legacy store functions

`RetryFleetRunnerAttempt`, `FailFleetRunnerAttempt` and `InsertFleetReconciliation` (`store/fleet_runner_attempts.go:169-310`) have no non-test callers. The contract should list them as dead and must not fence them.

### m3. Do not touch signed fingerprint material

`FleetReconciliationAdmission` and `FleetRunnerAttemptAdmission` are part of the request fingerprint (`store/operation_acceptance.go:583`). Fence and epoch facts must be read in-tx and must never be added to these structs. Otherwise replay of in-flight identities breaks. Add this to the WP2/WP5 conventions.

### m4. Stop-proof clock

The existing stop-proof check uses the process clock (`time.Since`, `store/fleet_runner_attempt_acceptance.go:113-120`). The new `ValidatePredecessorStop(…, now)` must be called with `time.Now()`, not the DB or etcd clock, to stay behavior-preserving.

### m5. The three drain predicates are not identical

- The handler copy handles int, int64 and float64 (`handler/fleet.go:630-650`).
- The store copy handles int, int64 and float64 (`store/fleet_reconciliation_acceptance.go:244-255`).
- The etcd copy handles float64 and `json.Number` only (`etcdstore/v3_fleet_runner_attempt.go:520-528`).
- `TestRequiresDrainMatchesAllLegacyCopies` cannot pass as named.
- Document the superset as a deliberate change for etcd int-typed payloads, or keep per-backend number decoding.

### m6. The `root:` alias kind is never resolved

The resolution steps (plan.md:232-235) never use `root:`. Either resolve the configured root (`FleetGitHubConfigPath`) or drop the kind.

### m7. Canonicalization rules

Lowercasing `StateBackend` merges case-distinct S3 keys and TFC workspaces. This errs in the safe direction (over-exclusion), but URI canonicalization (trailing slashes, query strings) is unspecified. Specify it and test it.

### m8. etcd epoch compare

The plan compares the epoch with `Compare(Value(...))` on JSON. Prefer a ModRevision compare, or a decimal-string value.

### m9. Do not compare resource keys in execution txns

Execution txns must not compare resource or observation keys (T1 lists "resource", plan.md:205). Otherwise reconciler writes, which heartbeats themselves trigger via `Notify`, contend with runner heartbeats.

### m10. Losing the etcd acquire race gives the wrong code

The loser of an `AcceptFleetGitHubDispatch` race returns the generic "reservation already exists or fleet plan changed" (`etcdstore/v3_fleet_github_dispatch.go:133-136`). That maps to `fleet_github_dispatch_preparation_unavailable` (`etcd_fleet_github_dispatch.go:132`). WP5b must re-read the fence to return `fleet_target_execution_occupied`.

### m11. PG fence acquisition happens after the signed reservation

PG fence acquisition happens *after* `ensureFleetGitHubReservation` (`handler/fleet_github.go:850-860`, `:541-551`). A 409 "occupied" therefore leaves a queued `fleet.github.apply-dispatch` reservation behind. Add an occupancy pre-check before reserving; the authoritative check stays at `MarkFleetGitHubDispatchSubmittingFenced`.

### m12. Notify deduplication

Notify deduplication must coalesce only *pending* names. A notification that arrives while the same name is being reconciled must re-enqueue it.

### m13. Redundant precondition field

`StatusPrecondition.DesiredGeneration` is redundant, because a desired PUT already bumps `Revision`.

### m14. Naming

- `fleet_authority` vs ADR 0002's control authority epoch (`docs/v3/architecture-roadmap.md:115,212`): name it `fleet_authority_epoch`, or define it as the generic epoch so a second epoch concept does not appear later.
- `/api/v1/fleet/targets` vs the existing `/api/v1/apps/{id}/fleet-target`: the "`platform:operate` precedent" (plan.md:440) is the *app placement* target (`etcd_fleet_runtime.go:150`), which is an unrelated concept.

### m15. §4 snippets

- The etcd image digest is elided (`a055da83...`). Use the full `sha256:a055da833a7c013b836ed0822e8ec1f99b059658be255ad8d0fcd31b635ae3d6`.
- The PG URL is not percent-encoded, unlike the referenced script (`v2/scripts/test-m4-capacity-local.sh:136-145`).

### m16. Explicit release semantics

State that explicit release permanently abandons the holder plan: recovery, rerun and first attempts are refused afterwards.

### m17. Use the existing ledger for audited state changes

Record target registration and fence release as signed operations in the existing `operations` ledger, rather than only as a `release_evidence` column. Etcd has no `MutationAuditMiddleware`.

### m18. Drop `HolderAttemptID`

`HolderAttemptID` on the fence mirrors the attempts table and can diverge from it. Derive it instead, and keep the fence a pure lock: `{generation, state, holder plan/nonce, epoch, last release}`.

## Second-ledger check

No new operation ledger is introduced. That much passes.

There is still a risk. The fence's holder attempt, `LastSucceeded`, `release_evidence` and the stored `Status.Active` refs all carry mutable copies of execution state.

- Keep the fence as a lock (m18).
- Keep `Status` as a derived cache that admission never reads.
- Put audit facts in the existing operations ledger (m17).

## Scope that can be cut

| Item | Reason to cut |
|---|---|
| WP2 `Decide*` functions | See M8. |
| PG V3 fence integration | Unrouted; see B3. Unless Q9 routes it. |
| `verifiedAt` / `verified_observation_sequence` | No observer until change 5. Keep the `TargetIdentityMismatch` status condition only. |
| `NextAction` values `dispatch_plan` and `review_plan` | They need a plan↔desired linkage that does not exist (M11). |
| `root:` alias kind | Never resolved (m6). |
| Fleet suites in the partition stage of the three-member script | Run lifecycle, fence and resource suites in the healthy and post-restore stages. Run only the fence race and the existing process test in the partition stage. |

## Qualification matrix gaps

| Matrix item | Gap |
|---|---|
| Checkpoint lost / crash after checkpoint | Covered only on PG V3 (unrouted) and etcd (B3). |
| Desired revision changes during execution | Only a pure `DeriveStatus` test exists. Add a resource-conformance integration case: PUT desired during an active attempt, then assert the attempt, fence and dispatch bindings are byte-identical and the status is `ExecutionInProgress`. |
| Authority restore | Only simulated (M16). |
| GitHub dispatch ambiguous | Covered only for occupancy. Nothing proves it can be **resolved** (B1). |
| Real three-member etcd | Can silently SKIP (M15). |

---

## Open questions Q1–Q10: recommended answers

**Q1. Fence rollout rule.**

- Accept opt-in by data, but only with:
  - a registry generation that admission checks or locks;
  - a registration-time refusal or adoption of in-flight plans (M1);
  - the writer-version floor (M2).
- Without those, opt-in-by-data opens exactly the race it is meant to close.

**Q2. Revalidation rule.**

- Use "plan `StartedAt` is before the target's last release of any reason except `dispatch_not_submitted`" (M4), with a skew margin.
- Do not use the inventory-digest comparison. The server's checkout can lag, and `SourceDigest` is the pre-change digest.

**Q3. Observation identity and verification.**

- Confirm fail-closed `observe` intent on a protected ref.
- Also bind the reporter's repository and environment to the target's aliases.
- Confirm that verification does not gate admission. Drop the `verifiedAt` fields until change 5.

**Q4. Epoch advance trigger.**

- Yes, minimally in scope. The roadmap requires re-acquiring execution leases under a new epoch after restore (`docs/v3/architecture-roadmap.md:115`).
- Wire it into the PG restore activation point (`controlrecovery/restore.go`) and into the etcd post-restore stage of the three-member script and its runbook.
- Otherwise mark the matrix item as only "simulated".

**Q5. Freshness window.**

- Keep a 15-minute constant for now, since there is no observer cadence until change 5.
- Evaluate freshness at read time too (M9), so the value matters less.
- Revisit when the read-only executor lands.

**Q6. Where the reconciler runs.**

- Confirm the authority-only PG process and the etcd runtime.
- But do **not** register resource routes on the general Mini router either. The general router already serves Fleet routes (`main.go:824-845`), so there they would serve status that is never reconciled.
- Rule: routes only where the reconciler runs.

**Q7. Authorization for registration and release.**

- Use the same scope on both backends.
- Registration: `platform:operate`.
- Explicit or break-glass release: `admin`, plus step-up on PG, recorded as a signed operation.
- `api:write` is too weak for overriding a safety state.

**Q8. Desired-revision acceptance.**

- Acceptable for the first release only if:
  - the record is labeled `verification: "operator-declared"`;
  - the commit must match a merged plan's `ApprovedHeadSHA`, or be verified through the GitHub App when one is configured;
  - the `ApprovalPolicy` and status never imply protected-main provenance for it.

**Q9. Legacy versus V3 on PG.**

- Agree: migrating PG to V3 is out of scope.
- Consequently, fence and suite work must target **PG legacy**, and PG V3 fencing should be dropped (B3).
- Keep D10 recorded and unchanged.

**Q10. Epoch effect on in-flight runners.**

- Confirm: heartbeat, advance and success checkpoints are refused; cancel is allowed.
- Additionally allow *failed*-status checkpoints, which are evidence-only and safe.
- Allow first-attempt re-bind (M13), so an epoch advance never strands a dispatched plan.
