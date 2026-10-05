# ADR 0009: Fleet target fence, authority epoch and resource status

Status: accepted, 2026-10-04 (human disposition of every decision below,
including H1's writer floor and H6's narrowed Change 2 deliverable). Scope:
changes 1-4 of the Fleet controller implementation plan
([`plan.md`](../fleet-controller/plan.md)), revision 2, which itself responds
to [`plan-review.md`](../fleet-controller/plan-review.md). This record
captures the decisions; implementation lands across WP2-WP14 and does not by
itself enable any new runtime mutation.

## Context

Fleet has three attempt implementations today (PG legacy live, PG V3
unrouted, etcd V3 live) that intentionally keep different behavior for a
dozen specific concerns (D1-D12 in
[`lifecycle-contract.md`](../fleet-controller/lifecycle-contract.md)). None
of the three has a concept of a provider/account/state-backend *target*, a
mutual-exclusion fence over who may execute against that target, or an
authority epoch that survives a restore. A plan's protected GitHub dispatch
and its runner attempts can both be left in a durably ambiguous state with no
release path (plan-review.md B1), and PG cannot observe GitHub by nonce hash
or observe a recover run at all (B2). The review also found the originally
proposed shared behavioral suite would have targeted the unrouted PG V3 path
instead of the live legacy handlers (B3).

## Decision

### Scope (2.1, H6)

Share only two things across backends, as pure Go in `fleet/lifecycle`:
zero-behavior-change helpers (phase tables, lineage, expiry, stop-evidence
validation — WP2), and the new fence/epoch decision functions (`Outcome`,
`Decide{Acquire,Bind,EvidenceWrite,Release}` — WP3). Each backend keeps its
own read, commit, request schema and D1-D12 branches. This narrows the
proposal's "one domain service for admission, heartbeat, advancement,
recovery and checkpoint validation" to pure helpers plus fence/epoch
decisions, proved equivalent only at the HTTP boundary (WP6). Fully unifying
admission would change D1-D12 and is not accepted in this pass.

### Target identity, registry and mutation fence (2.2)

A target is `{Provider, ProviderAccount, StateBackend}`, canonicalized
server-side (lowercase/trim provider and account; absolute URI with
lowercase scheme/host for the state backend; no query, fragment or userinfo)
into a derived `tgt_<sha256>` ID. Aliases (`cluster:<name>`,
`environment:<lane>`; `root:` dropped) are immutable in this pass and resolve
a plan to its target; a mismatched environment alias is
`fleet_target_alias_conflict`.

The fence is a lock, not a ledger: it stores a generation, held/holder
plan+nonce, authority epoch and last-release reason, but never an attempt ID.
Holder attempts are always re-derived from `fleet_runner_attempts` or the
etcd attempt keys. A registry singleton generation (PG row lock / etcd
ModRevision) is read in every fence-relevant admission. An empty registry
leaves every admission unchanged (Q1); a non-empty registry refuses an
unregistered target's plan with `fleet_target_unregistered`. Registration
itself is refused while any plan resolving to the new target is in flight
(`fleet_target_registration_in_flight`).

Fence transitions (acquire/rerun/bind/evidence-write/release) are specified
per plan.md §2.2's table and are not restated here; the operative points are:
revalidation after any release reason except `dispatch_not_submitted`, with a
5-minute skew (Q2); an epoch-superseded fence refuses heartbeat, advance and
*succeeded* checkpoints, but still allows failed checkpoints and cancel
(Q10); and bind re-binds (`Generation+1`, current epoch) for both the first
attempt and recovery, so neither is stranded by an epoch advance (M13).

Occupancy is a derived, admission-never-reads cache: `Active` iff held AND a
live unexpired holder attempt exists; every other held state is `Uncertain`
with a specific reason (M3). Heartbeat expiry alone never frees a target.

### Break-glass release (B1)

Two signed release modes close the "stuck forever" gap the review found:

- **Terminal**: every GitHub run that ever hosted an attempt, plus the bound
  apply run, is proven `completed` (WP5 observers); the holder plan is then
  permanently abandoned.
- **Abandon**: no live attempt, and
  `now - max(SubmissionStartedAt or DispatchCreatedAt, latest HeartbeatExpiresAt)
  >= AbandonMinimumAge` (30 minutes, H5); a GitHub listing snapshot (including
  "absent") is stored as evidence.

Abandonment permanently refuses `FinishFleetGitHubDispatch`, dispatch,
execute, rerun, legacy start and recovery for that plan and nonce
(`fleet_target_holder_abandoned`) in the same transaction, on both backends.

### GitHub observation (B2)

PG cannot reuse the etcd-only `ObserveApplyRunAttempt` (it needs the raw
nonce, which PG never persists past migration 44) and no backend can observe
a recover run. WP5 adds `githubapp.ObserveApplyRunByNonceHash` (reuses the
already-private `verifyApplyRunByNonceHash`) and `ObserveRecoverRun`
(verifies workflow path, actor, repository, head SHA and that the plan ID is
bound in the run's `display_title` or inputs; fails closed if it cannot
bind), plus `ListPlanRuns` for abandon evidence.

### Suite boundary (B3)

The shared conformance suite (`fleet/fleettest`, WP6+) drives the **live**
legacy PG handlers and `EtcdFleetRunnerHandler` over HTTP with a CI
principal, not the unrouted PG V3 acceptance path. PG V3 is not fenced (Q9);
Change 1's unrouted/dead status for it is unchanged.

### Resource, status and observations (2.3)

A new `fleet/controller` package defines a per-named-resource `Status`
derived purely from `Input` (`DeriveStatus`, WP11): `ProviderStateKnown`,
`NodesEnrolled`, `RuntimeReady`/`IngressReady`, `DriftDetected` and
`ReconciliationRequired`, each `True`/`False`/`Unknown`, with freshness fixed
at 15 minutes (Q5) and re-applied at read time, not only at derive time (M9).
`LastApplied` is derived from the fence's last `succeeded` release, through
that plan's dispatch binding's `ApprovedHeadSHA`, into the bounded
(20-entry) desired history; an aged-out match still shows the commit, with
`LastAppliedGeneration=0` (`GenerationOutOfHistory`). Desired revisions
resolve through `ResolveApprovedPlan` (`github-merged-plan`) or, without the
GitHub App, an explicit `operator-declared` commit that the policy/status
never implies is protected-main (Q8). Observation ingestion is monotonic
per-source via a watermark, pruning never deletes a watermark row (M10,
closing the review's resurrection finding), and the reconcile write recomputes
from a fully-compared input snapshot — one PG transaction, or an etcd txn
comparing every input key with up to 3 retries (M9).

### Reconciler and routes (2.4-2.5)

The reconciler and the new target/resource routes run only where a Fleet
runtime already runs today: the PG authority-only process and
`runEtcdFleetRuntime` (Q6) — never the general router. Registration and
release are signed operations in the existing ledger
(`fleet.target.register`, `fleet.target.fence-release`), gated by
`platform:operate` and `admin` respectively (Q7), with no step-up beyond
`admin` in this pass (H3; generic step-up is new work, not undertaken here).
New routes are POST/GET only, so CORS is unchanged (M17), and the
`/attempts` scope matcher in `controlScopeForRequest` narrows to
`/api/v1/fleet/plans/` (already true of every current match).

## Writer floor (M2, H1-H2)

PG migration 48 sets `MinimumWriterVersion = FleetTargetFenceWriterVersion =
32` (current `ControlSchemaWriterVersion = SnapshotExportIntentWriterVersion
= 31`, defined in `store/snapshot_export_intent_migration.go`; latest migration 47). Once
applied, any PG writer below schema version 32 — including Mini — can no
longer write to the control database, regardless of whether a Fleet target
is ever registered. The rejected alternative (refuse registration until no
connected writer is below 32) needs writer tracking that does not exist
today, and is not built in this pass; operators must confirm rollout impact
before applying migration 48 in a mixed-version fleet.

etcd has no equivalent writer-version mechanism (H2) and the code cannot
enforce one. The runbook must stop every pre-fence etcd Fleet runtime before
the first target registration.

## Other accepted defaults (H3-H5)

- **H3**: release requires `admin` only; no generic step-up this pass.
- **H4**: the §1.6 ordinary-lane pre-submit defect (`DeletePreparedFleetGitHubDispatch`
  no-ops against a `submitting` row) is left unchanged and pinned by
  `TestDispatchPreSubmitFailureLeavesSubmitting`; the abandon path is the
  resolution for states it leaves behind. Switching to
  `ResetFleetGitHubDispatchPreSubmit` is a separate, not-yet-accepted change.
- **H5**: `AbandonMinimumAge = 30m`; production etcd restore wiring is
  deferred to the restore runbook, with only the three-member qualification
  script's post-restore stage exercising it here (Q4).

## Consequences

- A registered target changes behavior under contention; an empty registry
  is unchanged, so rollout is opt-in by registration, not by this ADR alone.
- WP2 and WP9a touch signed-acceptance code (a type alias, one `omitempty`
  field); existing fingerprints must stay byte-identical, checked by WP9a's
  `ExistingFingerprintsUnchanged`.
- Occupancy can read `Uncertain/NoLiveAttempt` for the seconds between
  dispatch and runner registration; this is display-only, since both states
  already block a second plan.

## Validation

Qualification matrix items and their owning tests are listed in
plan.md §3's "Qualification matrix → tests" table and exercised by
`v2/scripts/test-etcd-three-member-fleet` (WP14). No milestone or qualification
gate is claimed passed by this record; see
[`implementation-status.md`](../implementation-status.md).
