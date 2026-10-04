# Fleet controller implementation plan (changes 1–4), revision 2

Status: revised draft. It addresses the review in [`plan-review.md`](./plan-review.md);
see §6 for the disposition of each finding.
Input: [`proposal.md`](./proposal.md).
Branch: `feature/v3-fleet-controller` (from `codex/v3-m5-forward-recovery` @ `7ea5895f`).
Go module root: `v2/api` (module `norn/v2/api`). Paths are relative to `v2/api/`
unless they start with `docs/` or `v2/`.

**Scope.** This pass covers changes 1–4 only: Norn Go code, storage, HTTP and tests.
Executor packaging, workflow migration, CLI/UI and the read-only executor
profile are out of scope.

**Decisions.** Section 5 lists every open question and its default; all were
accepted by the human on 2026-10-04. No implementing agent may change a default.

**Revision 2 direction:**
- Fence work and the shared suite target the **live PG legacy path** and etcd. The unrouted PG V3 path is not fenced.
- WP2 is limited to pure, zero-behavior-change helpers.
- All new shared logic is limited to the fence and epoch.
- Parallel WPs never touch the same file.
- Every test command fails on zero matched tests or on any skip.

---

## 1. Current-state map (summary of the Change 1 content)

### 1.1 Three attempt implementations

| Path | Router | Handlers | Store | Record |
|---|---|---|---|---|
| **PG legacy (LIVE)** | general router `main.go:824-843`; authority-only allowlist `fleetAuthorityOnlyRouterWithHandler` (`main.go:2149-2154`) | `handler/fleet_runner.go`: `StartFleetRunnerAttempt`, `Heartbeat…`, `Advance…`, `Cancel…`, `Get…`, `List…` | `store/fleet_runner_attempts.go`: `CreateFleetRunnerAttempt`, `HeartbeatFleetRunnerAttempt`, `AdvanceFleetRunnerAttempt`, `CancelFleetRunnerAttempt`, `RecoverFleetRunnerAttempt`, `AbandonStaleFleetRunnerAttempts` | `model.FleetRunnerAttempt` |
| **PG signed V3 (UNROUTED)** | none; exercised by `handler/fleet_attempts_test.go` and `handler/acceptance_integration_test.go` | `handler/fleet_attempts.go` `*SignedFleetRunnerAttempt` | `store/fleet_runner_attempt_acceptance.go` `acceptFleetRunnerAttempt`; `store/fleet_attempts.go` `UpdateFleetRunnerAttempt` | `fleet.RunnerAttempt` |
| **etcd signed V3 (LIVE on etcd)** | `runEtcdFleetRuntime` (`etcd_fleet_runtime.go:51`; runner routes `:163-176`, only when the GitHub App is configured) | `handler/fleet_etcd_attempts.go` `EtcdFleetRunnerHandler` | `etcdstore/v3_fleet_runner_attempt.go` `acceptFleetRunnerAttempt` (:97), `UpdateFleetRunnerAttempt` (:318) | `fleet.RunnerAttempt` |

**Shared table.** Both PG paths share `fleet_runner_attempts` (`store/postgres.go:284`
and migration 44). `idx_fleet_runner_attempt_live_plan` allows one live attempt per plan.

**Shared checkpoint path on PG.** The live PG checkpoint route
`RecordFleetReconciliation` (`handler/fleet.go:382`) goes through signed acceptance:
`enforceFleetReconciliationAdmission`, `store/fleet_reconciliation_acceptance.go:83`.
It locks with the V3 key `norn:fleet-attempt:` (`hashtextextended`) and reads the
attempt with the V3 scanner.

**Dead legacy code.** `RetryFleetRunnerAttempt`, `FailFleetRunnerAttempt` and
`InsertFleetReconciliation` (`store/fleet_runner_attempts.go:169-310`) have no
non-test callers. The plan records them as dead and does not fence them.

### 1.2 Routes

| Route (`/api/v1/fleet/...`) | PG | etcd |
|---|---|---|
| `POST validate`, `GET github` | `ValidateFleetDocument`, `FleetGitHubStatus` | absent |
| `GET node-pools` | `FleetInventory` | `etcdFleetInventory` |
| `POST node-pools/{pool}/plan` | `PlanFleetCapacity` (`handler/fleet.go:155`), signed `fleet.capacity-plan` | `etcdFleetPlan` |
| `GET plans` | `ListFleetPlans` | `etcdFleetPlans` |
| `GET/POST plans/{id}/reconciliations` | `ListFleetReconciliations` / `RecordFleetReconciliation` | `.ListReconciliations` / `.Reconcile` → `acceptFleetReconciliation` |
| `GET/POST plans/{id}/attempts[...]` | legacy handlers (1.1) | `EtcdFleetRunnerHandler` |
| `POST plans/{id}/github/pull-request` | `CreateFleetGitHubPullRequest` | `etcdFleetGitHubPullRequest` |
| `POST plans/{id}/github/dispatch` | `DispatchFleetGitHubApply` (`handler/fleet_github.go:722`) | `etcdFleetGitHubDispatch` → `acceptEtcdFleetGitHubDispatch` |
| `POST plans/{id}/github/{prepare,prepare/reset,execute,rerun}` | external-Mac lane only (rerun requires `approval_envelope_sha256<>''`, `store/fleet_github_dispatches.go:88`) | absent |
| `POST plans/{id}/github/reconcile` | `ReconcileFleetGitHubReservation`, **general router only** (`main.go:842`); absent from the authority-only allowlist | absent |

**Authorization.**
- `controlScopeForRequest` (`main.go:1804`) defers every non-GET `/api/v1/fleet/` path that contains `/attempts` or ends in `/reconciliations` to the handler (`main.go:1835-1838`).
- Handlers require exact `fleet:operate` plus a bound GitHub Actions identity (`requireFleetOperateScope`, `handler/handler.go:317`; `fleetRunnerPrincipalOwnsAttempt`, `handler/fleet_runner.go:715`).
- etcd enforces scopes per route through `etcdManagedTokenAuth`.
- The authority-only router's CORS allows only GET, POST, DELETE and OPTIONS (`main.go:2125`).

### 1.3 Phases

- **Canonical list.** `fleetReconciliationPhases` (`handler/fleet.go:35`) has 9 phases. Its etcd copy is `fleetValidPhase` (`etcdstore/v3_fleet_runner_attempt.go:505`).
- **PG legacy, no drain.** `fleetRunnerPhases(false)` (`handler/fleet_runner.go:759`) has 6 phases. It starts at `infrastructure_applied` and has no `old_nodes_drained`.
- **V3 advance.** `nextFleetReconciliationPhase` (`handler/fleet_attempts.go:394`) walks all 9 phases.
- **Initial phase.** PG V3 uses `provider_applying`, or `prechange_verified` when the plan drains. etcd always uses `prechange_verified` (`fleetInitialPhase`).
- **Checkpoint ordering.** It is shared through `store.ValidateFleetReconciliationAdmissionTransition` (`store/fleet_reconciliation_acceptance.go:192`).
- **Drain predicate.** There are three copies with **different number decoding**:
  - `handler/fleet.go:630-650` (int, int64, float64);
  - `store/fleet_reconciliation_acceptance.go:234-255` (int, int64, float64);
  - `etcdstore/v3_fleet_runner_attempt.go:513-528` (float64, `json.Number`).

### 1.4 Records and concurrency primitives

| Concept | PG | etcd |
|---|---|---|
| Capacity plan | `operations` kind `fleet.capacity-plan`; payload `fleet.CapacityPlan` (`fleet/schema.go`, which includes `cluster`; `SourceDigest` is the *pre-change* inventory digest) | `/v3/operations/<id>` |
| Dispatch | `fleet_github_dispatches`. States `prepared → submitting → dispatched`, plus `rerun_submitting` (`store/fleet_github_dispatches.go:69-121`). **Nonce hash only**; migration 44 drops the raw nonce (`store/master_protected_pilot_migration.go:33`). | `/v3/fleet-github-dispatch-preparations/<plan>` holds the **raw nonce** and its hash. `/v3/fleet-runner-dispatches/<plan>` holds the binding. Completion is signed. No reset or delete path exists. |
| Attempt serialization | Legacy create/recover: app lock `fleet-github-dispatch:<plan>`, plus `pg_advisory_xact_lock(hashtext('norn.fleet-runner:'…))`. Legacy heartbeat/advance/cancel: **single lock-free `UPDATE`s**. Checkpoint admission: `hashtextextended('norn:fleet-attempt:'…)` then attempt `FOR UPDATE`. | Per-plan CAS key `/v3/fleet-runner-plan-state/<plan>` |
| Receipts | `operations` kind `fleet.reconciliation`, signed | `/v3/fleet-reconciliations/<plan>/<op>` plus signed acceptance |
| Expiry | Legacy **writes** `abandoned` on read (`abandonStaleFleetRunnerAttempt`). V3 projects it. | Projects it (`fleetProjectExpiredAttempt`) |
| Recovery | Legacy: unlimited. It cancels a *live* source ("superseded by verified recovery workflow", `store/fleet_runner_attempts.go:341-351`) **without stop proof**, but must match `authorityConsumption`. | One successor only. It requires a server-observed `PredecessorStop`; the predecessor becomes `failed`. |
| Lineage validation | Legacy: **none**; it only derives the root (`store/fleet_runner_attempts.go:31-38`). V3: `validateFleetRunnerAttemptLineage`. | `fleetValidateLineage` |
| Advance evidence gate | Legacy: atomic SQL `EXISTS` on a succeeded checkpoint | `hasSignedFleetReconciliationEvidence`, CAS on plan-state |
| Global fences | `runtime_mutation_fence` (app runtime only) | `/v3/fleet-active-ingress-cluster-epoch/<cluster>` (ingress CAS key, not an authority epoch) |
| Authority | static `NORN_CONTROL_AUTHORITY` UUID in acceptance identities | same, checked only in `normalize`/`Resolve` |
| Schema writer floor | `SchemaMigration.MinimumWriterVersion`, enforced by the migrator (`store/schema_migrations.go:488-494`); current `ControlSchemaWriterVersion = SnapshotExportIntentWriterVersion = 31` | **none** |

**Missing concepts.** No authority epoch and no target identity exist in Go code.
`fleet.Document` has `cluster.{name,provider,region}` and `metadata.{repository,environment}` only.

**GitHub observation:**
- `ObserveApplyRunAttempt`/`observeApplyRun` (`githubapp/reconcile.go:156-198`) need the **raw** nonce. They exist only on the etcd handler's observer interface (`handler/fleet_etcd_attempts.go:36`).
- `RecoverBoundPlan` (`githubapp/client.go:563`) works by nonce hash. It reports *absence* as ambiguous by design.
- `verifyApplyRunByNonceHash` (`githubapp/client.go:816`) exists but is private.
- There is no observer for recover runs.

### 1.5 Credential and approval facts

Norn holds GitHub App credentials and **no provider credentials**. Runners use
GitHub OIDC to obtain a `fleet:operate` token (`CIIdentity`).

**Approval checks.** The following are enforced (`githubapp/client.go` `resolveApprovedPlan`, ~:600):
- the PR is merged;
- the plan workflow succeeded on the default branch at the merge SHA (`ApprovedHeadSHA`);
- the OIDC claim `RefProtected` is set;
- the environment lane matches;
- destructive plans carry `allowDestructive`;
- on the external-Mac lane only, an owner-signed approval envelope digest is bound.

**Not enforced:** GitHub Environment reviewers and independent review.

### 1.6 PG-versus-etcd differences (preserved; none is changed by this plan)

| # | Behavior | PG legacy (live) | PG V3 (unrouted) | etcd V3 |
|---|---|---|---|---|
| D1 | Start request | `RunnerAttemptStartRequest` | `RunnerAttemptCreateRequest` | same as PG V3 |
| D2 | Signed attempt creation | no | yes | yes |
| D3 | Initial phase (no drain) | `infrastructure_applied` | `provider_applying` | `prechange_verified` |
| D4 | Non-drain phases | 6 | 9 | 9 |
| D5 | Recovery limit | unlimited | unlimited | 1 |
| D6 | Predecessor stop proof | none (see Q11) | none | required |
| D7 | Predecessor becomes | `canceled` | `canceled`/`abandoned` | `failed` |
| D8 | Expiry | written on read | projected | projected |
| D9 | Advance evidence | atomic SQL | non-atomic handler read | atomic CAS |
| D10 | Raw dispatch nonce at rest | never | never | yes |
| D11 | Rerun, prepare/execute, reconcile routes | yes | n/a | no |
| D12 | Heartbeat replay | same sequence + `revision-1` returns current | strict | strict |

**Pre-existing defect (recorded, not fixed; see H4).** In
`DispatchFleetGitHubApply`, `MarkFleetGitHubDispatchSubmitting` runs at :857. The
`ErrDispatchPreSubmit` branch then calls `DeletePreparedFleetGitHubDispatch`
(:867-869). That delete matches only `dispatch_state='prepared'`
(`store/fleet_github_dispatches.go:129`), so it is a no-op. The row stays
`submitting`, and later calls stay ambiguous.

---

## 2. Design decisions

### 2.1 What is shared (scope reduced per M8)

New package **`fleet/lifecycle`**. It is pure Go and imports only `fleet`, `model` and stdlib.

**(a) Zero-behavior-change helpers (WP2).** Each backend keeps its own read,
commit, request schema and profile-specific branches (D1–D12).

```go
func Phases(profile Profile, requiresDrain bool) []string   // Profile = PostgresLegacy | V3
func NextPhase(profile Profile, current string, requiresDrain bool) (next string, terminal bool, err error)
func ValidPhase(phase string) bool
func RequiresDrain(action string, currentDesired, proposedDesired float64) bool // callers keep their own number decoding (m5)
func ValidateLineage(attempts []fleet.RunnerAttempt) error  // order-insensitive
func ProjectExpiry(a fleet.RunnerAttempt, now time.Time) fleet.RunnerAttempt
func ValidStopEvidence(e StopEvidence, predecessorID, sourceRunID string, now time.Time) bool // callers pass time.Now() (m4)
type StopEvidence = /* moved definition; store.FleetRunnerPredecessorStopEvidence becomes an alias */
```

**(b) New fence and epoch logic (WP3).** Both backends call the same pure decision functions:

```go
type TargetIdentity struct{ Provider, ProviderAccount, StateBackend string }
func CanonicalTarget(in TargetIdentity) (TargetIdentity, string /*targetID*/, error)
type FenceFacts struct {
    TargetID string; Generation int64; Held bool; HolderPlanID, HolderNonceSHA256 string
    AuthorityEpoch int64; LastRelease *Release; Revision int64
}
type Release struct{ PlanID, Reason string; At time.Time } // reasons: succeeded | dispatch_not_submitted | released_terminal | abandoned
type HolderFacts struct {
    DispatchState string // PG state, or etcd "prepared"|"bound"
    DispatchCreatedAt, SubmissionStartedAt time.Time
    Attempts []fleet.RunnerAttempt // lifecycle.FromLegacy for PG legacy
    Abandoned bool
}
type Occupancy string // "Free" | "Active" | "Uncertain"
func Outcome(f FenceFacts, h HolderFacts, currentEpoch int64, now time.Time) (Occupancy, string /*reason*/)
func DecideAcquire(f FenceFacts, planID, nonceSHA256 string, planStartedAt time.Time, currentEpoch int64) (FenceFacts, error)
func DecideBind(f FenceFacts, planID, nonceSHA256 string, currentEpoch int64) (FenceFacts, error)  // first attempt AND recovery
func DecideEvidenceWrite(f FenceFacts, planID string, currentEpoch int64, kind EvidenceKind) error // heartbeat|advance|success-checkpoint|failed-checkpoint|cancel
func DecideRelease(f FenceFacts, h HolderFacts, mode ReleaseMode, proof *TerminalProof, now time.Time) (FenceFacts, error)
const AbandonMinimumAge = 30 * time.Minute  // H5, accepted 2026-10-04
const RevalidationSkew  = 5 * time.Minute
```

**Transactional obligations.** These apply only to fence and epoch checks:
- **T1.** Fence, epoch and registry facts are read in the same transaction (PG) or compared in the same txn (etcd) as the mutation they gate.
- **T2.** Time comes from PG `clock_timestamp()` after locks, or from the etcd process clock. Stop proof keeps the process clock.
- **T3.** The fence mutation commits atomically with the dispatch, attempt or receipt write it accompanies.
- **T4.** A lost commit is indeterminate. It is never retried as a new decision.
- **T5.** Reads never write fence state. Occupancy is derived.
- **T6.** Replay of an accepted identity returns before any fence check. Existing replay paths come first.
- **T7.** Execution txns never read or compare resource or observation keys (m9).
- **T8.** Fence and epoch facts are **never** added to `FleetReconciliationAdmission`, `FleetRunnerAttemptAdmission` or any other fingerprinted struct (m3).

### 2.2 Target identity, registry and mutation fence

**Identity.**

`CanonicalTarget` rules:
- `Provider` and `ProviderAccount` are lowercased and trimmed.
- `StateBackend` must be an absolute URI. The scheme and host are lowercased. The path keeps its case; a trailing `/` is removed.
- Query, fragment and userinfo are rejected (m7).

The target ID is `tgt_` + hex(sha256(canonical JSON)). It is derived by the server, and any supplied ID must match it.

**Aliases.**
- There are two kinds, `cluster:<name>` and `environment:<lane>`. The `root:` kind is dropped (m6).
- Aliases are **immutable** in this pass.
- Resolution for a plan:
  1. Resolve `cluster:` + `plan.Payload["cluster"]`.
  2. If an `environment:` alias exists for the dispatch lane (PG `binding.FleetEnvironment`, etcd `preparation.FleetEnvironment`), it must name the same target. Otherwise `fleet_target_alias_conflict`.

**Verification.**
- Registration records a declared identity. `verifiedAt` is cut until change 5.
- A provider observation reporting a different account or state backend makes `ProviderStateKnown=False/TargetIdentityMismatch`.
- Verification does not gate admission (Q3).

**Registry generation (M1).**
- A registry singleton holds a `generation`.
  - PG: the singleton row is read `FOR SHARE` in every fence-relevant admission and `FOR UPDATE` in registration.
  - etcd: the registry key's ModRevision is compared in every fence-relevant admission txn, and the registration txn bumps it.
- If the registry is empty, every admission behaves exactly as today. If it is non-empty and the plan's cluster resolves to no target, admission is refused with `fleet_target_unregistered` (Q1).
- Registration refuses (`fleet_target_registration_in_flight`) while any plan whose cluster or environment would resolve to the new target is in flight. In flight means a dispatch in `submitting`, `dispatched` or `rerun_submitting` (PG), or a preparation or binding (etcd), whose latest attempt is not `succeeded` and which is not abandoned.
  - PG: the in-flight scan runs inside the registration transaction.
  - etcd: the registration txn compares `ModRevision(prefix).WithPrefix()` of the preparations and bindings prefixes against the read revision.

**Storage.**
- PG migration 48, `fleet-targets-and-authority-epoch`:
  - `fleet_target_registry(singleton BOOL PK CHECK(singleton), generation BIGINT NOT NULL)`, seeded `(true,0)`.
  - `fleet_targets(target_id PK, provider, provider_account, state_backend, created_at, registration_operation_id)`.
  - `fleet_target_aliases(alias PK, target_id FK)`.
  - `fleet_target_fences(target_id PK FK, generation BIGINT, held BOOL, holder_plan_id, holder_nonce_sha256, authority_epoch BIGINT, last_release_plan_id, last_release_reason, last_release_at, revision BIGINT)`.
  - `fleet_target_abandoned_plans(plan_id PK, nonce_sha256, target_id, operation_id, abandoned_at)`.
  - `fleet_authority_epoch(singleton BOOL PK CHECK(singleton), epoch BIGINT NOT NULL CHECK(epoch>0), activated_at, reason)`, seeded `(true,1,now(),'initial')` (m14).
  - Migration 48 sets `MinimumWriterVersion = FleetTargetFenceWriterVersion = 32`, and `ControlSchemaWriterVersion` becomes 32 (M2; see H1).
- etcd keys under `s.prefix`:
  - `/v3/fleet-target-registry`
  - `/v3/fleet-targets/<id>`
  - `/v3/fleet-target-aliases/<sha256(alias)>`, created with `CreateRevision==0`
  - `/v3/fleet-target-fences/<id>`
  - `/v3/fleet-target-abandoned/<planID>`
  - `/v3/fleet-authority-epoch`, whose value is a decimal string; txns compare its **ModRevision** (m8)
- etcd has no writer floor (H2).
- New PG tables are added to `controlrecovery/registry.go` and `retention/inventory.go`.

**The fence is a lock, not a ledger (m18).** It stores no attempt ID. The holder
attempts are derived from `fleet_runner_attempts` or the etcd attempt keys for
`holder_plan_id`. `Status.Active` (2.3) is a derived cache that admission never reads.

**Lock order (PG; M7).** Every path follows this order:
1. plan advisory lock (where the path already takes one);
2. `fleet_target_registry`;
3. `fleet_authority_epoch`;
4. `fleet_target_fences`;
5. `fleet_github_dispatches`;
6. `fleet_runner_attempts`.

Rows 2–3 are `FOR SHARE`, except in registration and epoch advance. Row 4 is
`FOR UPDATE` only for acquire, bind, release and abandon; otherwise it is `FOR SHARE`.

**Legacy single-statement updates.** Heartbeat and advance (`store/fleet_runner_attempts.go:115-167`)
take **no new lock**. Each gains an `AND NOT EXISTS (… fence of this plan's target
with authority_epoch <> (SELECT epoch FROM fleet_authority_epoch))` predicate in
the existing `UPDATE`.

**Fence transitions:**

| Transition | Where (atomic with) | Rule |
|---|---|---|
| **Acquire** | PG: new `MarkFleetGitHubDispatchSubmittingFenced` replaces `MarkFleetGitHubDispatchSubmitting` at `handler/fleet_github.go:548` (execute) and `:857` (dispatch). etcd: inside the `AcceptFleetGitHubDispatch` txn. | The fence must be free, or held by the same plan and nonce. Otherwise `fleet_target_execution_occupied`. **Revalidation (M4):** refuse with `fleet_plan_revalidation_required` if `last_release_reason <> 'dispatch_not_submitted'` and `plan.StartedAt < last_release_at + RevalidationSkew`. **Pre-check (m11):** a non-locking occupancy read runs *before* `ensureFleetGitHubReservation`, so a refused request leaves no queued reservation. The locked check stays authoritative. **etcd race loser (m10):** re-read the fence and return `fleet_target_execution_occupied` instead of `fleet_github_dispatch_preparation_unavailable`. |
| **Rerun (M5)** | PG: new `MarkFleetGitHubDispatchRerunSubmittingFenced` replaces the call at `handler/fleet_github.go:~670` | The fence must be held by the same plan and nonce at the current epoch, and the plan must not be abandoned. |
| **Bind** (first attempt **and** recovery; M13) | PG legacy `CreateFleetRunnerAttempt` / `RecoverFleetRunnerAttempt` (fence `FOR UPDATE` after the existing advisory lock). etcd `acceptFleetRunnerAttempt` txn. | The fence must be held by this plan and nonce, and the plan must not be abandoned. If `AuthorityEpoch < current`, re-bind: `Generation+1`, `AuthorityEpoch=current`. Existing lineage, recovery and stop-proof checks are unchanged. **Q11 (PG legacy, registered targets only):** recovery of a source whose latest run is not proven terminal is refused (`fleet_target_recovery_requires_stopped_source`), using WP5 observers. |
| **Evidence writes** | Legacy heartbeat/advance `UPDATE` predicates; `enforceFleetReconciliationAdmission` (fence and epoch `FOR SHARE` right after its advisory lock, before the attempt `FOR UPDATE`); etcd `UpdateFleetRunnerAttempt`, `acceptFleetReconciliation` | If the fence epoch is below current: heartbeat, advance and *succeeded* checkpoints are refused with `fleet_target_authority_superseded`. *Failed* checkpoints and cancel stay allowed (Q10). |
| **Release: succeeded** | the advance-to-`complete` mutation | Fence freed; `last_release={plan, succeeded, now}` |
| **Release: never submitted** | PG `ResetFleetGitHubDispatchPreSubmit` path (external-Mac execute, `:555`) | Fence freed; reason `dispatch_not_submitted`. The ordinary lane's no-op delete (1.6) is **not** a release path. |
| **Release: terminal** | signed op `fleet.target.fence-release` (2.5), mode `terminal` | No live attempt (via `ProjectExpiry`). Every distinct GitHub run that ever hosted an attempt, plus the bound apply run, is proven `completed` by the WP5 observers. The proof is stored in the operation payload. Effect: fence freed (`released_terminal`) and the holder plan is **permanently abandoned** (m16). |
| **Release: abandon (break-glass; B1)** | same op, mode `abandon` | No live attempt. `now - max(SubmissionStartedAt or DispatchCreatedAt, latest attempt HeartbeatExpiresAt) ≥ AbandonMinimumAge`. A GitHub listing snapshot from `RecoverBoundPlan` / WP5 observers (including "absent") is stored as evidence. Effect: fence freed (`abandoned`), and the plan is written to `fleet_target_abandoned_plans` or `/v3/fleet-target-abandoned/<plan>`. |

**What abandonment refuses.** In the same txn, abandonment makes the following
refuse permanently for that plan and nonce (code `fleet_target_holder_abandoned`):
- **PG:** `FinishFleetGitHubDispatch` (add `AND NOT EXISTS abandoned` to its `UPDATE`), dispatch, execute, rerun, legacy start, and recovery.
- **etcd:** `FinishFleetGitHubDispatch`, `acceptFleetRunnerAttempt` and dispatch re-POST compare `CreateRevision(abandoned)==0`.

A run that appears later therefore cannot register an attempt. Attempt
registration is the execution boundary.

**Occupancy (M3).**
- `Free`: not held.
- `Active`: held **and** a live, unexpired holder attempt exists.
- Any other held state is `Uncertain`, with one of these reasons:
  - `DispatchSubmissionUnresolved`
  - `NoLiveAttempt`
  - `AttemptTerminalWithoutSuccess`
  - `AuthoritySuperseded`

Heartbeat expiry never frees a target.

**Authority epoch.**
- Both stores provide `FleetAuthorityEpoch(ctx)` and `AdvanceFleetAuthorityEpoch(ctx, expected, reason)`, which is a CAS.
- Advancing touches no history. Old-epoch fences become `Uncertain/AuthoritySuperseded` until re-bind or release.
- Restore wiring is per Q4.

### 2.3 Fleet resource, status, observations

Types live in **`fleet/controller`** (`types.go`).

```go
type Resource struct {
    SchemaVersion string // "norn.fleet-resource/v1"
    Name, TargetID string
    Desired DesiredRevision; DesiredHistory []DesiredRevision // bounded 20
    Policy  ApprovalPolicy                                    // server-derived
    Watermarks map[string]Watermark                           // per source: {ObservedAt, Sequence} of latest applied (M10)
    Status  Status                                            // derived cache; never read by admission
    Revision, AuthorityEpoch, ObservationSequence int64
    CreatedAt, UpdatedAt time.Time
}
type DesiredRevision struct{ Generation int64; PlanID, CommitSHA, Repository string
    Verification string /* "github-merged-plan" | "operator-declared" */; AcceptedAt time.Time; AcceptedBy string }
type ApprovalPolicy struct{ ProtectedBranch, MergedPullRequest, PlanWorkflowSucceeded, BoundPlanDigest, AuthorizedDispatch, OwnerApprovalEnvelope bool
    EnvironmentReviewers, IndependentReview string /* "not_enforced" */ }
type Status struct{ ObservedGeneration, LastAppliedGeneration int64; LastApplied *AppliedRef
    Active *ActiveRefs /* PlanID, DispatchRunID, AttemptIDs, FenceGeneration, Occupancy, OccupancyReason */
    Conditions []Condition; NextAction string; EvaluatedAt time.Time }
type Condition struct{ Type string; Status string /* True|False|Unknown */; Reason, Message string
    ObservedAt time.Time; EvidenceRefs []string; ObservationSequence int64 }
type Observation struct{ Sequence int64; Source string /* provider|state|runtime */; ObservedAt, ReceivedAt time.Time
    Facts ObservationFacts; EvidenceRefs []string; Reporter string; Applied bool }
```

**Desired-to-applied linkage (M11).**
- `POST resources/{name}/desired {planId, expectedRevision}`. The server calls the existing `fleetGitHub.ResolveApprovedPlan(planID, env)` and uses its `ApprovedHeadSHA` as `CommitSHA`, with `Verification="github-merged-plan"`.
- If the GitHub App is not configured, the request may give `commitSha` explicitly and is stored as `operator-declared` (Q8).
- `LastApplied` is derived as follows:
  1. take the fence's `last_release` with reason `succeeded`;
  2. read that plan's dispatch binding `ApprovedHeadSHA`;
  3. find the desired-history entry with that `CommitSHA`.
- If the entry has aged out of the 20-entry history, the commit is still shown, with `LastAppliedGeneration=0` and reason `GenerationOutOfHistory`.
- `NextAction` values `review_plan` and `dispatch_plan` are cut.

**Freshness.**
- `ObservationFreshness = 15m` (Q5).
- Conditions derived from observations older than that are `Unknown/ObservationStale` both at derive time **and at read time**: the read API re-applies the rule against `now` (M9).
- Checkpoint evidence never makes readiness `True`.

**Monotonic ingestion (M10).**
- Server-assigned `Sequence`.
- `ReceivedAt-24h ≤ ObservedAt ≤ ReceivedAt+1m`.
- `Applied = ObservedAt > Watermarks[source].ObservedAt`. On applied, update the watermark in the same tx or txn.
- Derivation uses only each source's watermark row. On equal `ObservedAt`, the failure/false value wins.
- Pruning (100 per resource) **never** deletes a row referenced by any watermark.
- Bounds: facts ≤ 16 KiB; ≤ 20 evidence refs.

**Conditions** (`controller.DeriveStatus`, pure):

| Condition | True | False | Unknown |
|---|---|---|---|
| `ProviderStateKnown` | Fresh, identity matches, no error. | Identity mismatch, or `Error` set. | Stale or none. |
| `NodesEnrolled` | Enrolled == Expected. | Fewer enrolled. | Stale, none, or counts absent. |
| `RuntimeReady` / `IngressReady` | Fresh `true`. | Fresh `false`. | Stale or none. |
| `DriftDetected` | Fresh drift. | Fresh no-drift. | Stale or none. |
| `ReconciliationRequired` | `DesiredNotApplied`, `DriftDetected`, or `OutcomeUncertain`. | `UpToDate`. | `ExecutionInProgress` (Active). |

**`NextAction`**, in priority order:
1. `resolve_uncertain_outcome`
2. `await_runner`
3. `refresh_observations`
4. `investigate_drift`
5. `none`

There is no automatic repair.

**Storage.**
- PG migration 49:
  - `fleet_resources(name PK, target_id FK, document JSONB, revision, authority_epoch, observation_sequence, created_at, updated_at)`
  - `fleet_observations(resource, sequence, source, observed_at, received_at, applied, facts, evidence_refs, reporter, PK(resource,sequence))`
  - `MinimumWriterVersion` 32.
- etcd keys:
  - `/v3/fleet-resources/<name>`
  - `/v3/fleet-observations/<name>/<%020d>`

**Reconcile write (M9).** The status write recomputes from inputs in one atomic unit.

`ReconcileFleetResource(ctx, name, derive func(controller.Input) controller.Status) (*Resource, error)`:
- **PG:** one transaction:
  1. resource `FOR UPDATE`;
  2. epoch `FOR SHARE`;
  3. fence and holder dispatch/attempts read;
  4. watermark observations read;
  5. derive;
  6. write with `revision+1` and `authority_epoch=current`.
- **etcd:**
  1. read all inputs;
  2. derive;
  3. commit a txn that compares the ModRevision of the resource key, epoch key, fence key, holder preparation/binding keys and every holder attempt key, plus each watermark observation key;
  4. retry up to 3 times, then `ErrStatusPreconditionFailed`.
- Any concurrent controller, an old one included, recomputes from current inputs, so its write is either correct or rejected. `StatusPrecondition.DesiredGeneration` is dropped (m13).

### 2.4 Reconciler

`fleet/controller/reconciler.go`: `Reconciler{Store, Clock, RescanInterval=60s, MaxBatch=100}`.

- `Notify(name)` coalesces only *pending* names. A notify during an in-progress reconcile of the same name re-enqueues it (m12).
- A periodic rescan lists up to `MaxBatch` resources and enqueues them.
- `reconcileOne` calls `ReconcileFleetResource` and writes only when the derived status differs, or when `EvaluatedAt` is older than `RescanInterval`.
- Handlers call an optional `FleetNotifier` after commits.
- It runs **only** in the PG Fleet authority-only process and in `runEtcdFleetRuntime` (Q6).
- There is no leader lease.

### 2.5 HTTP API (additive)

**Placement.** Routes are registered **only where the reconciler runs**:
the authority-only PG router allowlist and the etcd runtime. They are not on the
general router (Q6). All are POST or GET, so CORS is unchanged (M17).

| Method / path (`/api/v1/fleet/...`) | Scope (checked in-handler; M17) | Notes |
|---|---|---|
| `POST targets` | `platform:operate` (Q7) | Signed op `fleet.target.register` in the existing ledger (m17). Idempotent if identical. |
| `GET targets/{id}` | `api:read` | Identity, aliases, fence, derived occupancy and reason. |
| `POST targets/{id}/fence/release` | `admin` (Q7; no step-up, see H3) | Signed op `fleet.target.fence-release`, `{mode: terminal\|abandon, expectedGeneration, reason}`. |
| `GET resources`, `GET resources/{name}` | `api:read` | Freshness re-applied at read time. |
| `POST resources/{name}/desired` | `api:write` | See 2.3. |
| `GET resources/{name}/observations` | `api:read` | `limit ≤ 100`. |
| `POST resources/{name}/observations` | `fleet:operate` CI principal; intent `observe`; `RefProtected`; repository = `GitHubActionsFleetAllowedRepository`; environment must resolve via an `environment:` alias to the resource's target (Q3) | Fails closed until operators add `observe` to `GitHubActionsFleetAllowedIntents`. |

**Signed target operations.**
- New optional typed admission `OperationAcceptance.FleetTargetMutation *FleetTargetMutationAdmission` (`json:",omitempty"`). It is used only by the two new kinds, so existing fingerprints are unchanged.
- PG applies it inside `acceptWithGuard` (`store/operation_acceptance.go:112`). etcd applies it in an `acceptOperationAggregate` branch.

**Middleware.**
- Every new handler checks scope itself, as the existing Fleet handlers do (`handler/fleet.go:156`).
- `controlScopeForRequest`'s `/attempts` matcher is narrowed to `strings.HasPrefix(path, "/api/v1/fleet/plans/")`. All current matches are already under that prefix, so existing behavior is unchanged.
- The capability feature `fleet-resource-v1` is additive.

**New error codes on existing routes.** All are 409, and they occur only when the registry is non-empty:
- `fleet_target_execution_occupied`
- `fleet_target_unregistered`
- `fleet_target_alias_conflict`
- `fleet_target_authority_superseded`
- `fleet_plan_revalidation_required`
- `fleet_target_holder_abandoned`
- `fleet_target_recovery_requires_stopped_source` (Q11)

---

## 3. Work packages

### Conventions (all WPs)

- Edit only the files listed for your WP.
- Never change existing assertions, error codes or JSON shapes.
- Follow T8: no fence facts in fingerprinted structs.
- Run `gofmt -l .` (must be empty), `go vet` on touched packages, and `go build -buildvcs=false ./...`.
- **Every test command uses `v2/scripts/go-test-strict`** (created in WP1). It:
  - runs `NORN_TEST_REQUIRE_INTEGRATION=1 go test -count=1 -v -run '<regex>' <pkgs> [extra flags]`;
  - fails if the exit status is non-zero, if no `=== RUN` line appears, or if any `--- SKIP` or `[no tests to run]` appears.
- New integration suites get their env through `internal/integrationtest` (WP1):
  - `PG(t)` reads `NORN_TEST_DATABASE_URL`;
  - `Etcd(t)` reads `NORN_TEST_ETCD_TLS_*` when set, otherwise `NORN_TEST_ETCD_ENDPOINTS`;
  - both `t.Fatal` instead of `t.Skip` when `NORN_TEST_REQUIRE_INTEGRATION=1`.
- Each shared suite has its own top-level test per backend (M15).

### Order and parallelism (M14; parallel WPs have disjoint file sets)

```
1. WP1
2. WP2 ∥ WP5
3. WP3 ∥ WP6            (after WP2)
4. WP4                  (after WP3)
5. WP7 ∥ WP10           (WP7 after WP4+WP6; WP10 after WP4)
6. WP8a ∥ WP8b ∥ WP11   (WP8a/b after WP5+WP7; WP11 after WP10)
7. WP9a → WP9b          (after WP8a+WP8b)
8. WP12 → WP13 → WP14   (WP12 after WP9b+WP11)
```

`main.go` and `etcd_fleet_runtime.go` are edited only by WP9b, WP12 and WP13, which are serialized.

### WP1: Contract, ADR, safety-net tests, test tooling (must land first)

- **Create:**
  - `docs/v3/fleet-controller/lifecycle-contract.md`: every route in 1.2 × backend → handler, store function, phases, credential boundary, approval rule, error codes, and **owning regression test** (exact names from `go test -list`). Include D1–D12, the dead functions (1.1), the 1.6 defect, and the same-plan concurrent-executor gap (Q11).
  - `docs/v3/adrs/0009-fleet-controller.md`: decisions 2.1–2.5, the M2 writer floor and H1–H5.
  - `v2/scripts/go-test-strict` (bash, `set -euo pipefail`).
  - `internal/integrationtest/integrationtest.go`.
  - `fleet/fleettest/doc.go` (package stub for the shared suites).
- **Modify:** `docs/v3/implementation-status.md` and `docs/v3/README.md`.
- **Tests:** add only what the contract audit finds unowned. At minimum:
  - `TestFleetRunnerLegacyReadWritesExpiry` (`store/fleet_runner_integration_test.go`, D8);
  - `TestFleetRunnerHeartbeatReplaySameSequence` (`handler/fleet_runner_test.go`, D12);
  - `TestV3FleetGitHubDispatchPreparationRetainsRawNonceEtcd` (`etcdstore/v3_fleet_github_dispatch_integration_test.go`, D10, asserting current behavior);
  - `TestDispatchPreSubmitFailureLeavesSubmitting` (`handler/fleet_github_presubmit_test.go`, asserting the current 1.6 defect so any fix is explicit).
- **Verify:**
  - `v2/scripts/go-test-strict ./handler 'TestFleetRunnerHeartbeatReplaySameSequence|TestDispatchPreSubmitFailureLeavesSubmitting'`
  - `v2/scripts/go-test-strict ./store 'TestFleetRunnerLegacyReadWritesExpiry'` (PG)
  - `v2/scripts/go-test-strict ./etcdstore 'TestV3FleetGitHubDispatchPreparationRetainsRawNonceEtcd'` (etcd)
  - `go test -list '.*' ./handler ./store ./etcdstore . | grep -i fleet` (every cited name present)
- **Size:** docs, plus ≤ 300 lines of code and tests.

### WP2: Pure helpers (≤ 300 lines; zero behavior change)

- **Create:** `fleet/lifecycle/{phases.go,lineage.go,expiry.go,stop.go}` with `_test.go`.
- **Modify:** replace bodies (not call sites' semantics) in:
  - `handler/fleet.go` (`fleetReconciliationPhases`; `fleetPlanRequiresDrain` keeps its number decoding and calls `RequiresDrain`)
  - `handler/fleet_runner.go` (`nextFleetRunnerPhase`, `fleetRunnerPhases`)
  - `handler/fleet_attempts.go` (`nextFleetReconciliationPhase`)
  - `store/fleet_runner_attempt_acceptance.go` (`validateFleetRunnerAttemptLineage`, `validFleetRunnerPredecessorStop` called with `time.Now()`)
  - `store/operation_acceptance_types.go` (`FleetRunnerPredecessorStopEvidence` becomes a **type alias**)
  - `store/fleet_reconciliation_acceptance.go` (`fleetAcceptancePlanRequiresDrain`)
  - `etcdstore/v3_fleet_runner_attempt.go` (`fleetValidPhase`, `fleetPlanRequiresDrain`, `fleetProjectExpiredAttempt`, `fleetValidateLineage`)
- **Unit tests:**
  - `TestPhasesPerProfile`
  - `TestNextPhaseTerminal`
  - `TestRequiresDrainTable`
  - `TestValidateLineageOrderInsensitive`
  - `TestProjectExpiryNeverMutatesTerminal`
  - `TestValidStopEvidenceRejectsStaleFutureWrongRun`
- **Verify:**
  - `v2/scripts/go-test-strict ./fleet/lifecycle '.' -race`
  - `v2/scripts/go-test-strict ./handler 'Fleet'`
  - `v2/scripts/go-test-strict ./store 'Fleet' -race` (PG)
  - `v2/scripts/go-test-strict ./etcdstore '^TestV3Fleet'` (etcd)
  - `v2/scripts/go-test-strict ./handler 'TestEtcdFleetRunnerHTTPAdmissionAndEvidenceGate|TestFleetRunnerAttemptHTTPUsesCompoundSignedAcceptance|TestFleetReconciliationHTTPUsesTypedAtomicAcceptance'` (both)
  - Required regressions: `TestFleetRunnerAttemptResolveRejectsLineageTampering` and the etcd replay tests stay green.

### WP3: Fence domain (pure), PG target/epoch/registry storage, target suite on PG

- **Create:**
  - `fleet/lifecycle/target.go` and `fence.go` (2.1b), with `_test.go`
  - `store/fleet_targets_migration.go` (migration 48, writer version 32)
  - `store/fleet_targets.go`: `RegisterFleetTarget` (tx-internal function reused by WP9a), `GetFleetTarget`, `ResolveFleetTargetForPlan`, `GetFleetTargetFence`, `FleetAuthorityEpoch`, `AdvanceFleetAuthorityEpoch`
  - `fleet/fleettest/target.go`: `RunFleetTargetConformance(t, TargetHarness)`
  - `store/fleet_target_conformance_test.go`: `TestFleetTargetConformancePostgres`
- **Modify:** `store/postgres.go` (migration list, `ControlSchemaWriterVersion`), `controlrecovery/registry.go`, `retention/inventory.go`.
- **Unit tests:**
  - `TestCanonicalTargetRules` (m7)
  - `TestOutcomeTable`: free; held + live attempt = Active; held + no attempt = Uncertain/NoLiveAttempt; held + submitting = Uncertain; held + expired = Uncertain; held + old epoch = Uncertain
  - `TestDecideAcquireRevalidation` (M4, every release reason)
  - `TestDecideBindRebindsOnNewEpoch` (M13)
  - `TestDecideEvidenceWriteUnderOldEpoch` (Q10)
  - `TestDecideReleaseAbandonMinimumAge`
- **Suite cases:**
  - `RegisterIdempotentAndAliasConflict`
  - `AliasesImmutable`
  - `RegistryGenerationBumps`
  - `RegisterRefusedWhileInFlight` (M1)
  - `EpochStartsAtOneAndAdvancesByCAS`
  - `AdvanceEpochPreservesHistory`
- **Verify:**
  - `v2/scripts/go-test-strict ./fleet/lifecycle '.' -race`
  - `v2/scripts/go-test-strict ./store '^TestFleetTargetConformancePostgres$|SchemaMigration' -race`
  - `v2/scripts/go-test-strict ./controlrecovery '.'`
  - `v2/scripts/go-test-strict ./retention 'TestPayloadInventoryCoversEveryControlTable'`
- **Size:** about 750 lines.

### WP4: etcd target/epoch/registry storage

- **Create:**
  - `etcdstore/v3_fleet_targets.go` (same method set as WP3; the registration txn uses a prefix ModRevision compare)
  - `etcdstore/fleet_target_conformance_test.go`: `TestFleetTargetConformanceEtcd`
- **Verify:** `v2/scripts/go-test-strict ./etcdstore '^TestFleetTargetConformanceEtcd$'`
- **Size:** about 450 lines.

### WP5: GitHub observers (B2; `githubapp` only)

- **Create:** `githubapp/observe.go`:
  - `ObserveApplyRunByNonceHash(ctx, planID, fleetEnvironment string, approved *Dispatch, nonceHash string, runAttempt int64) (*ApplyRunObservation, error)` reuses `verifyApplyRunByNonceHash` (`githubapp/client.go:816`) plus `/actions/runs/{id}/attempts/{n}`.
  - `ObserveRecoverRun(ctx, boundApplyRunID int64, nonceHash string, runID, runAttempt int64, recordedHeadSHA string) (*ApplyRunObservation, error)` (**revised in WP5 review** to match norn-fleet `.github/workflows/recover.yml`, which is a separate workflow triggered by `workflow_run` on apply or by manual `workflow_dispatch`, titled `Recover Fleet apply <apply_run_id>`). It verifies:
    - canonical repo URL, echoed run ID, `head_branch` = default branch;
    - path = `Config.RecoverWorkflow` (default `recover.yml`, must differ from `ApplyWorkflow`);
    - `head_sha` = the SHA recorded on the attempt (not the approved head);
    - title exactly `Recover Fleet apply <boundApplyRunID>`;
    - `workflow_dispatch`: input `apply_run_id` = bound run ID and `dispatch_nonce` hashes to the stored hash; `workflow_run`: bound by the exact title (the REST run object exposes no triggering run); other events are rejected;
    - no actor restriction (human dispatch is legitimate; this is completion evidence, not authorization).

    Anything unprovable fails closed. **WP8a/WP8b must wire `RecoverWorkflow` through `config/config.go` and pass the attempt's recorded run ID/attempt/SHA.**
  - `ListPlanRuns(ctx, planID string) ([]RunSummary, error)`, the abandon evidence snapshot.
- **Tests (`githubapp/observe_test.go`, `httptest` server):**
  - `TestObserveApplyRunByNonceHashMatchesOnlyBoundRun`
  - `TestObserveRecoverRunRejectsForeignWorkflowOrPlan`
  - `TestObserveRecoverRunAcceptsBothTriggersWithoutActorRestriction`
  - `TestObserveRecoverRunReportsInProgress`
  - `TestListPlanRunsBounded`
- **Verify:** `v2/scripts/go-test-strict ./githubapp 'Observe|ListPlanRuns' -race`
- **Depends:** WP1. **Size:** about 450 lines.

### WP6: Lifecycle conformance at the HTTP boundary (B3)

- **Create:**
  - `fleet/fleettest/lifecycle.go`: `RunFleetLifecycleConformance(t, LifecycleHarness)`.
    - `LifecycleHarness` methods: `Profile() lifecycle.Profile`; `SeedPlan(action) PlanRef`; `SeedDispatch(plan) DispatchRef`; `Start(plan, run RunRef) Resp`; `Recover(plan, run RunRef, stop *StopEvidence) Resp`; `Heartbeat/Advance/Cancel(attempt, …) Resp`; `Checkpoint(attempt, phase, status) Resp`; `Get(attempt) Resp`; `ExpireAttempt(attempt)`.
    - `Resp{HTTPStatus int; Code string; Attempt *fleet.RunnerAttempt}`. The PG legacy harness converts via `lifecycle.FromLegacy`, defined here in `fleet/lifecycle/legacy.go`.
  - `handler/fleet_conformance_pg_test.go`: `TestFleetLifecycleConformancePostgres`. It drives the **live legacy handlers** through `httptest` with a CI `AccessPrincipal`, real PG and the real checkpoint route.
  - `handler/fleet_conformance_etcd_test.go`: `TestFleetLifecycleConformanceEtcd`. It drives `EtcdFleetRunnerHandler` (fake observer pattern from `TestEtcdFleetRunnerHTTPAdmissionAndEvidenceGate`).
- **Cases** (expectations branch only on `Profile()`):
  - `StartReplaySameIdentity`
  - `StartRejectsDispatchMismatch`
  - `HeartbeatOrderingAndRevision`
  - `HeartbeatRejectedAfterExpiry`
  - `AdvanceRequiresCurrentPhaseEvidence`
  - `PhaseSequencePerProfile`
  - `AdvanceCompleteTerminal`
  - `CheckpointRejectsWrongPhaseOrInactiveAttempt`
  - `CheckpointReplayAfterLostResponse` (crash after checkpoint)
  - `RecoveryLineageAndLimit`
  - `RecoveryStopProofPerProfile`
  - `ReadsDoNotWriteExceptLegacyExpiry`
- **Verify:**
  - `v2/scripts/go-test-strict ./handler '^TestFleetLifecycleConformancePostgres$'` (PG)
  - `v2/scripts/go-test-strict ./handler '^TestFleetLifecycleConformanceEtcd$'` (etcd)
- **Depends:** WP2. **Size:** about 750 lines.

### WP7: Fence suite cases

- **Create:** `fleet/fleettest/fence.go`: `RunFleetFenceConformance(t, FenceHarness)`, where `FenceHarness` embeds `LifecycleHarness` and adds:
  - `RegisterTarget`
  - `Dispatch(plan, fakeGitHubOutcome)`
  - `AdvanceEpoch`
  - `Release(mode, proof)`
  - `Occupancy`
  - `SetRunTerminal`
- **Modify:** `handler/fleet_conformance_pg_test.go` and `handler/fleet_conformance_etcd_test.go`, adding **stub** methods only (`t.Fatal("WP8 not implemented")`). Do not add top-level fence tests here.
- **Cases:**
  - `SecondPlanBlockedWhileHeld`
  - `AliasDoesNotBypass`
  - `UnregisteredClusterRefusedWhenRegistryNonEmpty`
  - `EmptyRegistryPreservesLegacyBehavior`
  - `RegisterWhileInFlightRefused`
  - `CompleteReleasesFence`
  - `ExpiryDoesNotRelease` (provider mutation succeeded, checkpoint lost)
  - `NoAttemptAfterDispatchIsUncertain`
  - `DispatchAmbiguousKeepsOccupiedThenAbandonResolves` (B1)
  - `AbandonedPlanCannotStartRecoverOrFinish`
  - `TerminalReleaseRequiresEveryRunTerminal` (B2)
  - `RecoverySamePlanKeepsFence`
  - `EpochAdvanceRefusesHeartbeatAdvanceSuccessCheckpoint`
  - `FailedCheckpointAndCancelAllowedAfterEpochAdvance`
  - `EpochAdvanceBeforeFirstAttemptRebinds` (M13)
  - `RevalidationAfterAnyRelease` (M4)
  - `RecoveryRefusedWhileSourceRunNotTerminal` (Q11; legacy profile only)
- **Verify:** `go build -buildvcs=false ./... && go vet ./handler ./fleet/...`. The suite runs in WP8.
- **Depends:** WP4, WP6. **Size:** about 600 lines.

### WP8a: Fence integration, PG legacy (live path)

- **Modify:**
  - `store/fleet_github_dispatches.go`: add `MarkFleetGitHubDispatchSubmittingFenced` and `MarkFleetGitHubDispatchRerunSubmittingFenced`; release in `ResetFleetGitHubDispatchPreSubmit`; `FinishFleetGitHubDispatch` refuses abandoned plans.
  - `store/fleet_runner_attempts.go`: `CreateFleetRunnerAttempt` and `RecoverFleetRunnerAttempt` bind; heartbeat/advance get `UPDATE` predicates; advance-to-complete releases.
  - `store/fleet_reconciliation_acceptance.go`: fence/epoch `FOR SHARE` after the advisory lock; succeeded checkpoints are refused under the old epoch.
  - `handler/fleet_github.go`: pre-check before reservation; resolution; error mapping; `fleetGitHub` interface gains the WP5 methods.
  - `handler/fleet_runner.go`: Q11 stop check via WP5; error mapping.
  - `handler/fleet_conformance_pg_test.go`: implement the harness; add `TestFleetFenceConformancePostgres`.
- **Do not touch:** `store/fleet_attempts.go` or `store/fleet_runner_attempt_acceptance.go` beyond WP2. PG V3 is not fenced.
- **From the WP3 review:** the pure `Decide*` functions cannot see abandonment. Every dispatch, rerun, recovery, attempt-create and checkpoint path must check `fleet_target_abandoned_plans` itself; revalidation alone is not sufficient. Wire `githubapp.Config.RecoverWorkflow` through `config/config.go` and pass each attempt's recorded run ID/attempt/SHA to `ObserveRecoverRun`.
- **From the WP7 review:** implement `FenceHarness` per `fleet/fleettest/fence.go` (factory per case; `AgeHolder` backdates storage, never sleeps; server gathers release proof via WP5 observers, never the client). Add direct tests for the rerun fence (M5) and the `dispatch_not_submitted` exemption, which have no harness operation.
- **Extra test:** `handler/fleet_github_presubmit_test.go` gains `TestDispatchAmbiguousOrdinaryLaneOccupiesTarget`.
- **Verify:**
  - `v2/scripts/go-test-strict ./handler '^TestFleet(Lifecycle|Fence)ConformancePostgres$|TestDispatch'` (PG)
  - `v2/scripts/go-test-strict ./store 'Fleet' -race` (PG)
  - `v2/scripts/go-test-strict ./handler 'Fleet'` (PG)
- **Size:** about 800 lines.

### WP8b: Fence integration, etcd

- **Modify:**
  - `etcdstore/v3_fleet_github_dispatch.go` (acquire in the `AcceptFleetGitHubDispatch` txn; `FinishFleetGitHubDispatch` abandoned compare)
  - `etcdstore/v3_fleet_runner_attempt.go` (bind, epoch compares, complete releases)
  - `etcdstore/v3_fleet_reconciliation.go`
  - `etcd_fleet_github_dispatch.go` (pre-check, race-loser re-read m10, mapping)
  - `handler/fleet_etcd_attempts.go` (mapping)
  - `handler/fleet_conformance_etcd_test.go` (harness and `TestFleetFenceConformanceEtcd`)
- **Create:** `etcdstore/v3_fleet_fence_race_integration_test.go`: `TestFleetFenceTwoAdapterAcquireRaceEtcd`.
- **From the WP3 review:** same abandoned-plan check on every path as WP8a; fence key must exist from registration (a missing fence is an error, never free).
- **From the WP7 review:** same `FenceHarness` contract as WP8a; `TestFleetFenceTwoAdapterAcquireRaceEtcd` covers the two-adapter acquire race.
- **Verify:**
  - `v2/scripts/go-test-strict ./handler '^TestFleet(Lifecycle|Fence)ConformanceEtcd$'`
  - `v2/scripts/go-test-strict ./etcdstore 'TestFleetFenceTwoAdapterAcquireRaceEtcd|^TestV3Fleet'`
  - `v2/scripts/go-test-strict . 'TestEtcdFleetGitHubDispatch'`
- **Size:** about 650 lines.

### WP9a: Signed target mutations (store level, both backends)

- **Create:**
  - `store/fleet_target_mutation.go`: `FleetTargetMutationAdmission{Kind: register|release; Target…; Release{Mode, ExpectedGeneration, Proof, Snapshot}}`; normalize/validate; the PG guard in `acceptWithGuard`.
  - `etcdstore/v3_fleet_target_mutation.go`: the `acceptOperationAggregate` branch.
- **Modify:** `store/operation_acceptance_types.go` (new optional field), `store/operation_acceptance.go` (guard hook), `etcdstore/v3_operation_store.go` (branch).
- **Tests:** add to `fleet/fleettest/target.go` `RunFleetTargetConformance`:
  - `RegisterIsSignedAndReplays`
  - `ReleaseIsSignedReplaysAndAbandons`
  - `ExistingFingerprintsUnchanged` (golden fingerprints of a capacity-plan and a reconciliation acceptance computed before and after)
- **Server-only proof (review, B2):** `FleetTargetReleaseAdmission` is intent only (`mode`, `expectedGeneration`, `reason`). The GitHub proof and listing digest are `store.FleetTargetReleaseEvidence` in `OperationAcceptance.FleetTargetReleaseEvidence` (`json:"-"`, not in `requestMaterial` or the fingerprint), passed as the last argument of `NewFleetTargetMutationAcceptance`. The store re-validates it with `DecideRelease` against DB facts and records it in operation metadata under `fleetTargetReleaseEvidence` (excluded from the fingerprint; caller-supplied values are discarded). Pinned by `TestFleetTargetReleaseEvidenceServerOnly`.
- **Verify:**
  - `v2/scripts/go-test-strict ./store '^TestFleetTargetConformancePostgres$' -race`
  - `v2/scripts/go-test-strict ./etcdstore '^TestFleetTargetConformanceEtcd$'`
  - `v2/scripts/go-test-strict ./store '^TestFleetTargetReleaseEvidenceServerOnly$|^TestAtomicAcceptance|^TestFleetRunnerAttemptAcceptance|^TestFleetReconciliationAcceptance' -race` (the existing acceptance tests; there is no test named `OperationAcceptance`)
  - `v2/scripts/go-test-strict ./etcdstore 'Fleet|Operation'`
- **Size:** about 650 lines.

### WP9b: Target and fence routes

- **From the WP9a review (release proof contract, B2):**
  - Decode the request body into an intent-only DTO: register `{provider, providerAccount, stateBackend, aliases}`; release `{mode, expectedGeneration, reason}`; abandon-plan `{planId, reason}`. Never decode proof, run IDs or a snapshot digest from the client; the body must not reach `FleetTargetReleaseEvidence` (it is `json:"-"`).
  - Order: authenticate, check scope, build the identity (actor = principal, key = `Idempotency-Key`). Optionally `ResolveIdentity` first so a replay skips GitHub. Then gather evidence with the WP5 observers: for terminal, `ObserveApplyRunByNonceHash` on the bound apply run (sets `BoundApplyRunCompleted`) and `ObserveRecoverRun` for every distinct run in the holder's attempts' `RunnerAttemptID`s (each completed one goes in `CompletedRunnerAttemptIDs`); for abandon and abandon-plan, the run-listing snapshot (`RecoverBoundPlan`, "absent" included), whose SHA-256 is `ListingSnapshotSHA256` (64 lowercase hex). Pass it as the `evidence` argument of `store.NewFleetTargetMutationAcceptance` (nil for register), then `operationStore.Accept`.
  - The store re-checks the evidence against DB facts and the live fence generation; evidence is recorded in operation metadata, not fingerprinted, so a same-key retry replays even if GitHub moved on.
- **Authorization is not enforced in the store (WP9a).** The handler must check `platform:operate` for `POST targets` (register) and `admin` for release (both modes) and abandon-plan, before any observer call or `Accept`, on both backends. Record the checked scopes in `AcceptanceAuditContext.Scopes`.
- **Error mapping:** `store.CodeFleetTargetExpectedGenerationMismatch` (`fleet_target_expected_generation_mismatch`, 409) is returned as a `*lifecycle.FenceError` for a stale `expectedGeneration` or a holder that changed while being decided.
- **From the WP7 review:** define the `expectedGeneration` mismatch code; list the six release codes (`fleet_target_fence_not_held`, `fleet_target_has_live_attempt`, `fleet_target_terminal_proof_incomplete`, `fleet_target_abandon_too_soon`, `fleet_target_abandon_snapshot_required`, `fleet_target_release_evidence_mismatch`) in §2.5 and the OpenAPI contract.
- **Create:**
  - `handler/fleet_targets.go`
  - `etcd_fleet_targets.go`
  - `handler/fleet_targets_test.go`:
    - `TestRegisterFleetTargetRequiresPlatformOperate`
    - `TestReleaseRequiresAdminAndExpectedGeneration`
    - `TestTerminalReleaseObservesEveryRun`
    - `TestAbandonRequiresMinimumAge`
    - `TestAttemptsMatcherNarrowedToPlans` (M17)
  - `etcd_fleet_targets_test.go`: `TestEtcdFleetTargetRoutesEtcd`.
- **Modify:** `main.go` (authority-only allowlist; matcher narrowing; capability) and `etcd_fleet_runtime.go`.
- **Verify:** `v2/scripts/go-test-strict . 'FleetTarget|FleetAuthorityOnly'` and `v2/scripts/go-test-strict ./handler 'FleetTarget|AttemptsMatcher'` (both env vars).
- **Size:** about 600 lines.

### WP10: Resource and observation storage

- **Create:**
  - `fleet/controller/types.go`
  - `store/fleet_resources_migration.go` (migration 49)
  - `store/fleet_resources.go`
  - `etcdstore/v3_fleet_resources.go`
  - `fleet/fleettest/resource.go`: `RunFleetResourceConformance`
  - `store/fleet_resource_conformance_test.go`
  - `etcdstore/fleet_resource_conformance_test.go`
- **Modify:** `store/postgres.go`, `controlrecovery/registry.go`, `retention/inventory.go`.
- **Cases:**
  - `DesiredBumpsGenerationHistoryBounded`
  - `ObservationSequenceMonotonic`
  - `OlderObservationStoredNotApplied`
  - `PruneNeverResurrectsOlderObservation` (M10)
  - `ObservationTimestampBounds`
  - `ReconcileWriteSeesLatestInputs` (M9: insert a failure observation between the derive callback's start and commit; PG must serialize, etcd must retry)
  - `ConcurrentReconcileWritesConverge`
  - `OldEpochWriterRecomputes`
  - `DesiredChangeDuringExecutionKeepsBindings` (fence, dispatch and attempt rows are byte-identical before and after `PUT desired`)
- **Verify:**
  - `v2/scripts/go-test-strict ./store '^TestFleetResourceConformancePostgres$' -race`
  - `v2/scripts/go-test-strict ./etcdstore '^TestFleetResourceConformanceEtcd$'`
  - `v2/scripts/go-test-strict ./controlrecovery '.'`
- **Depends:** WP4. Parallel with WP7/WP8. **Size:** about 800 lines.

### WP11: `DeriveStatus` (pure)

- **Create:** `fleet/controller/derive.go` and `derive_test.go`.
- **Tests:**
  - `TestDeriveSuccessfulDeployment` (desired gen 2 = merge SHA X applied; a later desired gen 3 = SHA Y shows `DesiredNotApplied`; gen 2 shows `LastAppliedGeneration=2`)
  - `TestDeriveDrift`
  - `TestDeriveStaleIsUnknown`
  - `TestReadTimeFreshnessDowngrade`
  - `TestDeriveNoAttemptIsUncertain`
  - `TestDeriveInterruptedExecutionIsUncertain`
  - `TestDeriveDispatchAmbiguous`
  - `TestDeriveDesiredChangedDuringExecution`
  - `TestDeriveOlderObservationCannotClearFailure`
  - `TestDeriveTargetIdentityMismatch`
  - `TestDeriveEpochSuperseded`
  - `TestDeriveGenerationOutOfHistory`
- **Verify:** `v2/scripts/go-test-strict ./fleet/controller 'Derive|ReadTime' -race`
- **Size:** about 550 lines.

### WP12: Reconciler and wiring

- **From the WP7 review:** add `DesiredChangeDuringExecutionKeepsBindings` (a desired-revision change mid-execution must not rebind the active plan/dispatch/attempt).
- **From the WP11 review:** storage (`store/fleet_resources.go`, `etcdstore/v3_fleet_resources.go`) must fill `Input.HolderDispatchRunID` from the dispatch/binding `RunID`, and, when `fence.LastRelease.Reason == "succeeded"`, fill `Input.LastReleaseAttempts` with that plan's attempts (etcd: also compare those attempt keys). Otherwise M11 `lastApplied` never advances. Also: `ProviderStateKnown` must be `Unknown` (reason `TargetIdentityMissing`) when a provider observation carries no `targetId`, per the §2 table (orchestrator decision; adjust `derive.go` and its test).
- **Create:** `fleet/controller/reconciler.go` and tests:
  - `TestReconcilerRetriesThenGivesUp`
  - `TestReconcilerRescanFindsMissedEvent`
  - `TestNotifyDuringReconcileReenqueues` (m12)
- **Shared:** `fleet/fleettest/reconciler.go` `RunFleetReconcilerConformance`, with top-level `TestFleetReconcilerConformance{Postgres,Etcd}` in new files `store/fleet_reconciler_conformance_test.go` and `etcdstore/fleet_reconciler_conformance_test.go`. Cases:
  - `TwoReconcilersConverge`
  - `RestartedReconcilerRecomputes`
  - `EpochAdvanceReflectedInStatus`
- **Modify:**
  - `FleetNotifier` hooks in `handler/fleet.go`, `handler/fleet_github.go`, `handler/fleet_runner.go`, `handler/fleet_etcd_attempts.go`, `etcd_fleet_github_dispatch.go`, `handler/fleet_targets.go`, `etcd_fleet_targets.go`
  - start/stop in the authority-only path of `main.go` and in `etcd_fleet_runtime.go`
- **Verify:**
  - `v2/scripts/go-test-strict ./fleet/controller 'Reconciler|Notify' -race`
  - `v2/scripts/go-test-strict ./store '^TestFleetReconcilerConformancePostgres$'`
  - `v2/scripts/go-test-strict ./etcdstore '^TestFleetReconcilerConformanceEtcd$'`
  - `v2/scripts/go-test-strict . '^TestEtcdFleetRuntimeProcess$'`
- **Size:** about 650 lines.

### WP13: Resource routes

- **From the WP11 review:** the read route must call `controller.DowngradeStale(status, now)` before returning.
- **Create:**
  - `handler/fleet_resources.go`
  - `etcd_fleet_resources.go`
  - `handler/fleet_resources_test.go`:
    - `TestDesiredResolvesMergedPlanOrOperatorDeclared`
    - `TestObservationIngestRequiresObserveIntentProtectedRefAndAlias`
    - `TestObservationIngestBounds`
    - `TestResourceReadRequiresAPIRead`
    - `TestApprovalPolicyReportsNonEnforcedReview`
    - `TestResourceRoutesAbsentFromGeneralRouter`
  - `etcd_fleet_resources_test.go`: `TestEtcdFleetResourceRoutesEtcd`
- **Modify:** `main.go` (authority-only allowlist and capability) and `etcd_fleet_runtime.go`.
- **Verify:**
  - `v2/scripts/go-test-strict ./handler 'FleetResource|ResourceRoutes|Observation|ApprovalPolicy'`
  - `v2/scripts/go-test-strict . 'FleetResource|^TestEtcdFleetRouterPGFreeGitHubConfiguredProcess$'`
- **Size:** about 600 lines.

### WP14: Restore wiring and qualification

- **Modify:**
  - `controlrecovery/restore.go`: after verification succeeds, `RestorePassive` advances the fleet authority epoch on the restored target and reports `FleetAuthorityEpoch` in `RestoreReport` (Q4).
  - `v2/scripts/test-etcd-three-member-fleet`:
    - export `NORN_TEST_REQUIRE_INTEGRATION=1`;
    - **healthy stage:** add `go-test-strict` runs of `^TestFleet(Target|Resource|Reconciler)ConformanceEtcd$` and `^TestFleet(Lifecycle|Fence)ConformanceEtcd$`, plus `TestFleetAuthorityRestoreEtcd/seed`;
    - **partition stage:** `TestFleetFenceTwoAdapterAcquireRaceEtcd` only;
    - **post-restore stage:** `TestFleetAuthorityRestoreEtcd/verify`, which advances the epoch on **actually restored** data and asserts history, Uncertain fences, refused heartbeats and re-bind (M16).
- **Create:**
  - `controlrecovery/fleet_epoch_restore_integration_test.go`: `TestFleetAuthorityRestorePostgres`. It uses the existing bundle/`RestorePassive` path into a separate `pgtest` database, with no `TEMPLATE`.
  - `etcdstore/fleet_authority_restore_integration_test.go` (two phases selected by subtest; seed data is under a fixed prefix the script retains).
  - `docs/v3/fleet-controller/qualification-<date>.md`.
- **Verify:** all of §4.

### Qualification matrix → tests

| Matrix item | Tests |
|---|---|
| Two controllers / old controller resumes | `RunFleetResourceConformance/ConcurrentReconcileWritesConverge`, `/OldEpochWriterRecomputes`, `/ReconcileWriteSeesLatestInputs`; `RunFleetReconcilerConformance/TwoReconcilersConverge` |
| Two plans incl. aliases | `RunFleetFenceConformance/SecondPlanBlockedWhileHeld`, `/AliasDoesNotBypass`, `/UnregisteredClusterRefusedWhenRegistryNonEmpty`, `/RegisterWhileInFlightRefused`; `TestFleetFenceTwoAdapterAcquireRaceEtcd` |
| Provider mutation succeeds, checkpoint lost | `RunFleetFenceConformance/ExpiryDoesNotRelease`; `TestDeriveInterruptedExecutionIsUncertain` |
| Checkpoint succeeds, executor crashes | `RunFleetLifecycleConformance/CheckpointReplayAfterLostResponse` (**live PG legacy** and etcd) |
| Dispatch ambiguous | `RunFleetFenceConformance/DispatchAmbiguousKeepsOccupiedThenAbandonResolves`, `/AbandonedPlanCannotStartRecoverOrFinish`; `TestDispatchAmbiguousOrdinaryLaneOccupiesTarget`; `TestDeriveDispatchAmbiguous` |
| Desired changes during execution | `RunFleetResourceConformance/DesiredChangeDuringExecutionKeepsBindings`; `TestDeriveDesiredChangedDuringExecution` |
| Old observations after newer failures | `RunFleetResourceConformance/OlderObservationStoredNotApplied`, `/PruneNeverResurrectsOlderObservation`; `TestDeriveOlderObservationCannotClearFailure` |
| Authority restore | `TestFleetAuthorityRestorePostgres` (real `RestorePassive`); `TestFleetAuthorityRestoreEtcd` (real snapshot restore stage); `RunFleetFenceConformance/EpochAdvance*` |
| PG and three-member etcd | every `TestFleet*Conformance{Postgres,Etcd}`, on PG, single-node etcd and the three-member TLS stages, with skips made fatal |

---

## 4. Local test plan

**Machine (checked 2026-10-03).**
- Installed: `go1.26.1 darwin/arm64`, Docker, and Homebrew `etcd`, `etcdctl`, `psql`, `pg_ctl`, `initdb` (postgresql@16).
- Image `quay.io/coreos/etcd@sha256:a055da833a7c013b836ed0822e8ec1f99b059658be255ad8d0fcd31b635ae3d6` (v3.5.17) is present.

**PostgreSQL** (pattern from `v2/scripts/test-m4-capacity-local.sh:131-145`):
```sh
scratch=$(mktemp -d /private/tmp/claude-501/norn-fleet-pg.XXXX)
initdb -D "$scratch/pg" --username=norn_fc --auth=trust --no-sync
mkdir "$scratch/sock"
pg_ctl -D "$scratch/pg" -o "-k '$scratch/sock' -h '' -p 55432" -l "$scratch/pg.log" -w start
createdb -h "$scratch/sock" -p 55432 -U norn_fc norn_fc
sock_q=$(python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1],safe=""))' "$scratch/sock")
export NORN_TEST_DATABASE_URL="postgresql://norn_fc@/norn_fc?host=$sock_q&port=55432&sslmode=disable"
# teardown: pg_ctl -D "$scratch/pg" -m fast stop
```

**Single-node etcd:**
```sh
docker run -d --rm --name norn-fc-etcd -p 127.0.0.1:23790:2379 \
  quay.io/coreos/etcd@sha256:a055da833a7c013b836ed0822e8ec1f99b059658be255ad8d0fcd31b635ae3d6 \
  /usr/local/bin/etcd --listen-client-urls http://0.0.0.0:2379 --advertise-client-urls http://127.0.0.1:23790
export NORN_TEST_ETCD_ENDPOINTS=http://127.0.0.1:23790   # teardown: docker stop norn-fc-etcd
```

**Three-member TLS etcd:** `v2/scripts/test-etcd-three-member-fleet`. It
needs docker, openssl, etcdctl, go, `timeout` and python3. After WP14 it runs
the Fleet suites in the stages described there, and fails on any skip.

**Full run** (from `v2/api`, with both env vars exported):
```sh
gofmt -l . ; go vet ./fleet/... ./store/... ./etcdstore/... ./handler/... ./githubapp/... ; go build -buildvcs=false ./...
S=../scripts/go-test-strict
$S ./fleet/... '.' -race
$S ./githubapp 'Observe|ListPlanRuns' -race
$S ./store '^TestFleet(Target|Resource|Reconciler)ConformancePostgres$' -race
$S ./etcdstore '^TestFleet(Target|Resource|Reconciler)ConformanceEtcd$|TestFleetFenceTwoAdapterAcquireRaceEtcd'
$S ./handler '^TestFleet(Lifecycle|Fence)Conformance(Postgres|Etcd)$'
$S ./store 'Fleet|SchemaMigration' -race
$S ./etcdstore '^TestV3Fleet'
$S ./handler 'Fleet'
$S . 'EtcdFleet|FleetAuthorityOnly|PGFree|FleetTarget|FleetResource'
$S ./controlrecovery '.' ; $S ./retention '.'
../scripts/test-etcd-three-member-fleet
```

**Pass criteria.**
- Every line exits 0. `go-test-strict` already enforces ≥1 test run, no `--- SKIP` and no `[no tests to run]`.
- No pre-existing test assertion was modified.
- The three-member script prints its final PASS line, with the quorum-loss stage refusing writes.
- Known exclusion: the Darwin sampler handler test noted in `docs/v3/implementation-status.md` (~:1186) is reported separately. The `'Fleet'` handler regex does not match it; check this with `go test -list`.

---

## 5. Open questions: provisional defaults

All defaults below were **accepted by the human on 2026-10-04** (H1 writer floor 32 and H6 narrowed Change 2 confirmed explicitly).

**Q1. Fence rollout.** ACCEPTED by human 2026-10-04.
- Opt-in by data, with a registry generation checked or locked at every admission.
- Registration is refused while a plan is in flight (M1). The writer floor applies (M2/H1).
- With an empty registry, behavior is unchanged.

**Q2. Revalidation.** ACCEPTED by human 2026-10-04.
- Refuse a plan whose `StartedAt` is before `last_release_at + 5m` for any release reason except `dispatch_not_submitted`.
- No inventory-digest comparison.

**Q3. Observations.** ACCEPTED by human 2026-10-04.
- Fail-closed `observe` intent on a protected ref.
- Repository is bound, and the environment must resolve through an alias to the target.
- Verification does not gate admission. `verifiedAt` is dropped until change 5.

**Q4. Epoch advance trigger.** ACCEPTED by human 2026-10-04.
- In scope, minimally: PG `RestorePassive` advances the epoch on the restored target after verification.
- etcd: the three-member post-restore stage plus a runbook note.
- There is no production etcd restore command yet (H5).

**Q5. Freshness.** ACCEPTED by human 2026-10-04.
- A 15-minute constant, evaluated at derive time and at read time. Revisit with change 5.

**Q6. Placement.** ACCEPTED by human 2026-10-04.
- Reconciler and resource/target routes run only in the PG authority-only process and the etcd runtime.
- They are not on the general router.

**Q7. Authorization.** ACCEPTED by human 2026-10-04.
- Registration: `platform:operate` on both backends.
- Release (terminal and abandon): `admin` on both. Each is a signed operation in the existing ledger.
- Step-up: see H3.

**Q8. Desired revision.** ACCEPTED by human 2026-10-04.
- `POST desired {planId}`, resolved through `ResolveApprovedPlan` (`github-merged-plan`).
- Without the GitHub App: an explicit `commitSha` labeled `operator-declared`.
- The policy and status never imply protected-main provenance for `operator-declared`.

**Q9. Legacy versus V3 on PG.** ACCEPTED by human 2026-10-04.
- Migrating to V3 is out of scope.
- Fence and suite work target PG legacy plus etcd. PG V3 is unfenced and unrouted.
- D10 is recorded and unchanged.

**Q10. Epoch effect on in-flight runners.** ACCEPTED by human 2026-10-04.
- After an epoch advance, heartbeat, advance and succeeded checkpoints are refused.
- Failed checkpoints and cancel are allowed.
- First attempt and recovery re-bind under the new epoch.

**Q11 (new, from M12). Same-plan concurrent executors on PG legacy.** ACCEPTED by human 2026-10-04.
- Once a plan's target is registered, legacy recovery is refused unless the source attempt's GitHub run (the apply run via `ObserveApplyRunByNonceHash`, a recover run via `ObserveRecoverRun`) is proven `completed`.
- Unregistered targets keep today's behavior, and the contract documents that gap.

**Decisions raised by the review beyond Q1–Q11**, each with a provisional default:

- **H1. Writer floor (M2).** ACCEPTED by human 2026-10-04.
  - Migration 48 sets `MinimumWriterVersion=32`. Once it is applied, older binaries can no longer write to the control DB, **including Mini**, whether or not any target is ever registered.
  - The alternative is to refuse registration until no connected writer is below 32. That needs writer tracking that does not exist today.
  - Confirm the rollout impact.
- **H2. etcd has no writer floor.** ACCEPTED by human 2026-10-04.
  - The runbook requires stopping every pre-fence etcd Fleet runtime before the first target registration.
  - The code cannot enforce this.
- **H3. Step-up for release.** ACCEPTED by human 2026-10-04.
  - Existing step-up is exec-specific (`handler/step_up.go:208` `verifyExecStepUp` takes an app ID).
  - Default: `admin` only, with no step-up, in this pass. Generic step-up would be new work.
- **H4. Ordinary-lane pre-submit defect (1.6).** ACCEPTED by human 2026-10-04.
  - Leave behavior unchanged. Pinned by `TestDispatchPreSubmitFailureLeavesSubmitting`. The abandon path resolves such states.
  - Switching to `ResetFleetGitHubDispatchPreSubmit` is a separate behavior change.
- **H5. Abandon minimum age and etcd restore.** ACCEPTED by human 2026-10-04.
  - `AbandonMinimumAge = 30m`.
  - Production etcd restore wiring is deferred to the restore runbook work.
- **H7. Pre-registration abandon (from the WP3 review).** ACCEPTED by human 2026-10-04.
  - Gap: a failed plan stays `dispatched` with a non-succeeded attempt, which blocks registration of its cluster indefinitely, and abandon only applied to registered targets. The first registration also strands in-flight plans on other clusters as unregistered (fail closed, but not live).
  - Decision: the signed, admin-only abandon (same `AbandonMinimumAge` and GitHub run-listing snapshot digest) also applies to plans on **unregistered** clusters. It writes `fleet_target_abandoned_plans` (etcd equivalent) keyed by plan, and registration's in-flight scan treats abandoned plans as not in flight. Implemented in WP9a (store) and WP9b (route).
- **H6. Change 2 deliverable narrowed.**
  - The proposal asks for "one domain service" for admission, heartbeat, advancement, recovery and checkpoint validation.
  - This plan shares only pure helpers plus fence and epoch decisions, and proves equivalence with the HTTP-boundary suite. Fully unifying admission would change D1–D12.
  - Accept the narrowed deliverable, or schedule unification together with a Q9 migration.

**Risks.**
- WP2 and WP9a touch signed-acceptance code. The type alias and the `omitempty` field must leave canonical envelopes and fingerprints identical; WP9a's `ExistingFingerprintsUnchanged` checks this.
- etcd execution txns gain registry, fence and epoch compares. Registration and epoch advance are rare, so contention is negligible.
- A plan's occupancy reads `Uncertain/NoLiveAttempt` for the seconds between dispatch and runner registration. This is display-only, since both states block a second plan.

---

## 6. Review disposition

| Finding | Resolution |
|---|---|
| **B1** Uncertain states have no release path | Fixed: signed break-glass **abandon**, `admin`, minimum age, GitHub snapshot evidence. It permanently refuses finish, start, recovery and rerun for the plan (2.2; WP7 `DispatchAmbiguousKeepsOccupiedThenAbandonResolves`; WP9a/b). |
| **B2** PG cannot observe by raw nonce; recover runs are unobservable | Fixed: WP5 adds `ObserveApplyRunByNonceHash` and `ObserveRecoverRun`. Terminal release proves every run that hosted an attempt, plus the bound run. |
| **B3** Suite targets the unrouted PG V3 path | Fixed: WP6 suite at the HTTP boundary over the live legacy handlers and `EtcdFleetRunnerHandler`. PG V3 fencing is dropped (WP8a "do not touch"). |
| **M1** Registry and alias reads are outside the snapshot | Fixed: registry generation (PG row lock, etcd ModRevision); immutable aliases; registration refuses in-flight plans (prefix compare on etcd). |
| **M2** Mixed-version writers | Fixed on PG by writer floor 32. etcd has no mechanism, raised as H2. Rollout impact raised as H1. |
| **M3** `Active` too permissive | Fixed: Active ⇔ held ∧ live attempt; everything else is Uncertain. |
| **M4** Revalidation only after success | Fixed: any release except `dispatch_not_submitted`, with 5m skew. |
| **M5** Rerun unfenced after release | Fixed: `MarkFleetGitHubDispatchRerunSubmittingFenced`; abandonment refuses reruns. |
| **M6** Pre-submit release path is a no-op | Confirmed (`store/fleet_github_dispatches.go:129`, `handler/fleet_github.go:857,867-869`). Recorded as a defect, pinned by a WP1 test, not counted as a release path; decision H4. |
| **M7** Lock-order inversion | Fixed: one global order (2.2). Legacy heartbeat/advance use `UPDATE` predicates with no new locks. Checkpoint admission takes the fence `FOR SHARE` before the attempt. |
| **M8** WP2 over-abstracted | Fixed: WP2 is pure helpers only (≤300 lines). `Decide*` is removed except the new fence decisions. T1–T8 apply only to fence and epoch checks. Deliverable narrowing is raised as H6. |
| **M9** Status CAS does not cover inputs; freshness only at write | Fixed: `ReconcileFleetResource` derives in-tx (PG) or under full input compares (etcd). Read-time freshness downgrade. |
| **M10** Pruning resurrects observations | Fixed: per-source watermarks; pruning never deletes watermark rows; `PruneNeverResurrectsOlderObservation`. |
| **M11** LastApplied linkage undefined | Fixed: desired is set from a merged plan's `ApprovedHeadSHA` via `ResolveApprovedPlan`; linkage is through `last_release` → binding → history; realistic test. |
| **M12** Same-plan concurrent executors | Raised as Q11, with a provisional stop-proof requirement on registered targets. Gap documented in the contract. |
| **M13** First attempt stranded by epoch | Fixed: bind re-binds for first attempts too; `EpochAdvanceBeforeFirstAttemptRebinds`. |
| **M14** Parallel WPs conflict | Fixed: new order (§3). WP1 first; parallel sets have disjoint files. Shared harness stubs land in WP7 before WP8a ∥ WP8b. `main.go` edits are serialized. |
| **M15** Commands pass silently | Fixed: `go-test-strict`, `NORN_TEST_REQUIRE_INTEGRATION`, a top-level test per suite per backend, and fatal skips in the script. |
| **M16** Restore qualification not faithful | Fixed: PG uses real `RestorePassive` into a separate DB. etcd uses the script's real snapshot-restore stage. Q4 wired minimally. |
| **M17** Route authorization hazards | Fixed: in-handler scope checks; `/attempts` matcher narrowed to `/fleet/plans/`; POST-only new routes (CORS unchanged). |

No finding was rebutted. The minor findings were folded in as follows:
- m1: §1 facts corrected.
- m2: dead functions listed.
- m3: T8.
- m4: stop proof uses `time.Now()`.
- m5: per-backend number decoding kept.
- m6: `root:` dropped.
- m7: canonicalization rules specified.
- m8: epoch compared by ModRevision.
- m9: T7.
- m10: race loser re-reads the fence.
- m11: occupancy pre-check before reserving.
- m12: Notify coalesces only pending names.
- m13: `DesiredGeneration` dropped from the precondition.
- m14: `fleet_authority_epoch` naming; target routes kept distinct from app `fleet-target`.
- m15: full digest and encoded URL.
- m16: release permanently abandons the holder plan.
- m17: signed operations for registration and release.
- m18: no `HolderAttemptID`.

All of the review's scope cuts were taken; none breaks an acceptance criterion.
