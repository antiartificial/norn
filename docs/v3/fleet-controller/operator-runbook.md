# Fleet controller operator runbook

Audience: operators of the Norn Fleet authority (PostgreSQL authority-only
process, or the etcd Fleet runtime) on `feature/v3-fleet-controller`. This is
the runbook that [ADR 0009](../adrs/0009-fleet-controller.md) (H2) and
[`final-review.md`](./final-review.md) (M2) call for. Design background:
[`plan.md`](./plan.md) §2.2, §2.5 and §5. Pilot process setup:
[`fleet-pilot.md`](../../v2/operations/fleet-pilot.md).

All routes are under `/api/v1/fleet/`. They are registered only in the PG
Fleet authority-only router and the etcd Fleet runtime, never on the general
router (Q6). Every mutating route below requires an `Idempotency-Key` header
(1 to 200 characters) and refuses CI workload identities. Mutations are signed
operations in the existing ledger; the response is the operation receipt
(201 on first accept, 200 on replay) and `ref` is the subject. Request bodies
are intent only: the server derives identities, evidence and proof.

## 1. Rollout prerequisites

Two irreversible effects exist before any target is registered.

- **PG writer floor (H1).** Migration 48 raises the control schema's minimum
  writer version to 32 (`store.FleetTargetFenceWriterVersion`; migration 49
  keeps it at 32). Once any process applies it, every binary below writer 32
  can no longer write to the control database, **Mini included**, whether or
  not a target is ever registered. There is no rollback to an older writer
  after it applies. The startup schema contract reported to platform upgrade
  is now writer 32 / catalog 49 / catalog minimum writer 32.
- **etcd has no writer floor (H2).** The code cannot stop an old etcd Fleet
  runtime from writing past the fence. Old runtimes must be stopped by the
  operator before the first target is registered.

Checklist, in order:

1. Inventory every process that writes the PG control database (Mini, the
   authority-only process, platform upgrade tooling) and every etcd Fleet
   runtime. Confirm each runs this branch's binary (writer 32 / catalog 49).
2. Upgrade or stop every older writer. Do this before applying migration 48.
   Accept that Mini and older binaries are locked out afterwards.
3. etcd only: stop every pre-fence etcd Fleet runtime. Start only new
   runtimes.
4. Check for plans in flight on the clusters you will register (section 2).
   Finish them, or abandon them (section 4), first.
5. Decide the aliases (immutable). Section 2.
6. Decide the reconciler placement (section 7). It runs by default; confirm
   the process that serves the Fleet routes is the one that runs it.
7. If CI will report observations, configure the `observe` intent first
   (section 5). Until then readiness stays `Unknown`.
8. If you need terminal release, abandon or merged-plan desired revisions,
   confirm the Fleet GitHub App is configured (`fleet_github_not_configured`
   503 otherwise) and `NORN_FLEET_GITHUB_RECOVER_WORKFLOW` matches your
   recovery workflow (section 6).
9. Restore planning (section 8): know which backend you run and what its
   restore does.

## 2. Registering a target

`POST /api/v1/fleet/targets`, scope `platform:operate`, non-CI token.

```json
{
  "provider": "digitalocean",
  "providerAccount": "acct-123",
  "stateBackend": "s3://norn-state/fleet/staging",
  "aliases": ["cluster:nyc3-staging", "environment:staging/nyc3"]
}
```

The 201 operation's `ref` is the target ID (`tgt_` plus 64 hex characters).
Registering an identical target again is idempotent (200).

**Canonical identity.** The target ID is `tgt_` + sha256 of the canonical
`{provider, providerAccount, stateBackend}`. Provider and account are
lowercased and trimmed. `stateBackend` must be an absolute URI with a host;
scheme and host are lowercased, the path keeps its case, a trailing `/` is
removed, and query, fragment and userinfo are rejected. Two spellings of the
same account and backend are one target. The identity is declared, not
verified: registration does not prove the account or backend, and admission
does not depend on verification (Q3).

**Aliases.** Two kinds exist, and aliases are immutable once bound:

- `cluster:<name>` resolves a plan through `plan.Payload["cluster"]`.
- `environment:<lane>` names the Fleet lane (the environment of the Fleet
  document, for example `environment:staging/nyc3`). If the dispatch lane has
  an `environment:` alias it must name the same target as the plan's
  `cluster:` alias, else `fleet_target_alias_conflict`. The same alias is what
  authorizes CI observations (section 5).

An alias already bound to a different target is refused with
`fleet_target_alias_conflict` and nothing is written. Registering an existing
target again with extra aliases adds them. Choose names carefully: there is no
rename or removal.

**In-flight plans.** Registration is refused with
`fleet_target_registration_in_flight` while any plan whose cluster or
environment would resolve to the new target is in flight: a dispatch in
`submitting`, `dispatched` or `rerun_submitting` (PG), or a preparation or
binding (etcd), whose latest attempt is not `succeeded` and which has not been
abandoned. Wait for it to finish, or abandon it (section 4). A failed plan
that stays `dispatched` blocks registration until it is abandoned (H7).

**Behaviour once the registry is non-empty.** With an empty registry every
admission behaves as before this branch. After the first registration, a plan
whose cluster resolves to no registered target is refused with
`fleet_target_unregistered` at dispatch, execute, rerun, start and recover.
Registering the first target therefore strands in-flight plans on every other
cluster: they fail closed. Register those clusters, or abandon the plans
(section 4). Registered targets are guarded by the fence: one holder plan per
target, acquired at dispatch submit, bound at first attempt and recovery,
released at completion.

Read a target with `GET targets/{id}` (identity, aliases, fence, occupancy)
or `GET targets/{id}/fence` (fence and occupancy only), scope `api:read`.

## 3. Reading Fleet status

Create a resource for a target (`POST resources/{name}`, scope
`platform:operate`, body `{"targetId":"tgt_..."}`; 201 created, 200 existing;
name is 1 to 128 lowercase letters, digits, `.`, `_`, `-`). Set the desired
revision with `POST resources/{name}/desired`, scope `api:write`:

```json
{"planId": "<capacity plan UUID>", "expectedRevision": 0}
```

The server resolves the merged plan's approved commit through the GitHub App
(`github-merged-plan`). Without the App, send `{"commitSha": "<40 hex>",
"expectedRevision": N}` instead; it is recorded as `operator-declared` and
claims no protected-main provenance. `expectedRevision` is required; a stale
one returns `fleet_resource_revision_conflict`. The plan's cluster must
resolve to the resource's target (`fleet_resource_plan_target_mismatch`).

Read with `GET resources` and `GET resources/{name}` (scope `api:read`). One
record holds `desired` and `desiredHistory` (what was requested), `status`
conditions and `observed` watermarks (what exists), `status.active` (what is
running), `blocker` (what blocks progress) and `status.nextAction` (what to do
next), plus `approvalPolicy` and `controller` liveness. Freshness is
re-applied at read time: an observation older than 15 minutes reads
`Unknown/ObservationStale`.

`controller.state`: `running` (the reading process's reconciler rescanned
within 3 intervals), `stale` (it did not), `unknown` (this process runs no
reconciler or has not rescanned yet). With `unknown`, status may be empty or
out of date; see section 7.

### 3.1 Occupancy (`status.active`)

`Free` (fence not held; `active` absent), `Active` (held and a live,
unexpired holder attempt exists), or `Uncertain` (held, anything else).
Heartbeat expiry never frees a target.

| Uncertain reason | Meaning | Operator action |
|---|---|---|
| `DispatchSubmissionUnresolved` | The holder's dispatch is not `dispatched` or `bound` (still `prepared`, `submitting` or `rerun_submitting`): GitHub may or may not have started a run. Also the state the ordinary-lane pre-submit defect (H4) leaves behind. | Check GitHub for runs of the plan. If one is running, let it register an attempt. Otherwise wait 30 minutes and abandon (section 4). Terminal release is not possible without a bound run. |
| `NoLiveAttempt` | Held, dispatch bound, but no attempt is live: the runner has not registered yet (normal for seconds after dispatch), or its lease expired. | Wait for the runner. If it persists, inspect the run in GitHub: recover through the existing recovery flow, or if the run completed, terminal release; if it is gone, abandon after 30 minutes. |
| `AttemptTerminalWithoutSuccess` | The latest attempt ended `failed` or `cancelled`. The fence stays held. | Terminal release once GitHub shows every run completed, then dispatch a new plan; or recover the attempt through the existing recovery flow. |
| `AuthoritySuperseded` | The fence's authority epoch is older than the current epoch, normally after a PG restore (section 8). | Re-bind by recovery (or the first attempt of a bound plan), or release. See section 8. |

### 3.2 Conditions

Six conditions, sorted by type. `Unknown` always means "cannot claim"; it is
never green.

| Condition | Reason | Status | Meaning and action |
|---|---|---|---|
| `ProviderStateKnown` | `Fresh` | True | Fresh provider observation, identity matches, no error. |
| | `ProviderError` | False | The observation reported `error`. Read the message; fix the provider or state access. |
| | `TargetIdentityMismatch` | False | Observation's `targetId` differs from the resource's target. Wrong resource wiring or wrong account; verify the executor's identity. |
| | `TargetIdentityMissing` | Unknown | Observation named no `targetId`, so identity is unproven. Fix the reporter to send it. |
| | `NoObservation` | Unknown | None received. Enable CI observation (section 5). |
| | `ObservationStale` | Unknown | Older than 15 minutes. Check the observe job. |
| `NodesEnrolled` | `EnrolledMatchesExpected` | True | `enrolledNodes` equals `expectedNodes`. |
| | `EnrolledBelowExpected` | False | Fewer enrolled than expected; enroll or investigate nodes. |
| | `CountsAbsent` | Unknown | The observation lacks the counts. |
| | `NoObservation`, `ObservationStale` | Unknown | As above. |
| `RuntimeReady`, `IngressReady` | `ObservedReady` | True | Fresh `ready` / `ingressReady` is true. |
| | `ObservedNotReady` | False | Fresh report is false; investigate the runtime or ingress. |
| | `NoObservation`, `ObservationStale` | Unknown | As above. |
| `DriftDetected` | `ObservedDrift` | True | Fresh `drift` is true. See `investigate_drift`. |
| | `NoDriftObserved` | False | Fresh no-drift reading. |
| | `NoObservation`, `ObservationStale` | Unknown | As above. |
| `ReconciliationRequired` | `ExecutionInProgress` | Unknown | Fence held with a live attempt. Wait. |
| | `OutcomeUncertain` | True | Fence held, Uncertain. Resolve it (3.1, section 4). The message gives when abandon becomes allowed. |
| | `DesiredNotApplied` | True | The desired commit has not been applied by a succeeded release. Dispatch an approved plan for it. |
| | `DriftDetected` | True | Applied, but drift observed. |
| | `UpToDate` | False | Applied and a fresh no-drift reading. |
| | `NoObservation`, `ObservationStale` | Unknown | Applied, but drift cannot be judged. |

`status.lastApplied` carries a `reason` when the applied commit cannot be
mapped to a generation: `GenerationOutOfHistory` (aged out of the 20-entry
history; `lastAppliedGeneration` is 0) or `CommitNotDesired`.

### 3.3 `status.nextAction`

Evaluated in this priority order; Norn never repairs automatically.

| Value | Meaning | Operator action |
|---|---|---|
| `resolve_uncertain_outcome` | The holder is Uncertain. | Follow the reason table (3.1) and section 4. |
| `await_runner` | A live attempt holds the fence. | Wait; watch the run in GitHub. |
| `refresh_observations` | Some observation-backed condition is `Unknown`. | Run or fix the observe job (section 5). |
| `investigate_drift` | Fresh drift is observed. | Inspect the drift, then plan and dispatch through the normal reviewed flow. |
| `none` | Nothing pending. Includes `DesiredNotApplied` with no drift: dispatching is a manual, reviewed step. | None. |

### 3.4 `blocker`

Absent when nothing blocks. Otherwise the first match in this order:

| `kind` | When | `reason` / `condition` | Action |
|---|---|---|---|
| `execution` | The fence is held. | The occupancy reason (the Uncertain reasons above), or `Active`. Includes `planId`. | As 3.1. |
| `condition` | A condition other than `ReconciliationRequired` is `False`. | `condition` names it; `reason` as in 3.2. | As the condition row. |
| `reconciliation` | `ReconciliationRequired` is `True`. | `DesiredNotApplied` or `DriftDetected`. | As the condition row. |
| `observation` | A condition is `Unknown`. | `NoObservation`, `ObservationStale`, `CountsAbsent` or `TargetIdentityMissing`. | Refresh observations. |

## 4. Releasing a fence

Use these only when the holder is `Uncertain` (3.1) and the routine paths
(waiting, recovery) have not resolved it. Both release routes also **permanently
abandon the holder plan**: a new plan is required, and that plan is subject to
revalidation (below). **Abandon cannot be undone.** A late runner for an
abandoned plan can never register an attempt, finish a dispatch, start,
recover, rerun or checkpoint (`fleet_target_holder_abandoned`).

Both modes need no live attempt (`fleet_target_has_live_attempt`) and a free
fence is refused (`fleet_target_fence_not_held`). First read the fence:
`GET targets/{id}/fence` gives `generation`, `held`, `holderPlanId` and
`occupancy`.

`POST /api/v1/fleet/targets/{id}/fence/release`, scope `admin` (no step-up):

```json
{"mode": "terminal", "expectedGeneration": 7, "reason": "run 123 failed, verified in GitHub"}
```

`expectedGeneration` is required; a stale value, or a holder that changed
while the request ran, returns `fleet_target_expected_generation_mismatch`.

| | `terminal` | `abandon` |
|---|---|---|
| Scope | `admin` | `admin` |
| Use when | The run(s) ended and GitHub can prove it: `AttemptTerminalWithoutSuccess`, or `NoLiveAttempt` with a completed run. | Proof is impossible or the run is gone or never started: `DispatchSubmissionUnresolved`, or a run that will not complete. |
| Evidence | The server gathers it from GitHub. The bound apply run and every distinct run that hosted an attempt must be `completed`. Otherwise `fleet_target_terminal_proof_incomplete`. | The server stores a digest of the GitHub run listing for the plan (an empty listing is recorded, not treated as proof of absence). Failure to list: `fleet_target_observation_failed` 502. |
| Age gate | None. | At least 30 minutes since the latest of submission start, dispatch creation and any attempt's lease expiry, else `fleet_target_abandon_too_soon`. Missing snapshot: `fleet_target_abandon_snapshot_required`. |
| Result | Fence freed as `released_terminal`; plan permanently abandoned. | Fence freed as `abandoned`; plan permanently abandoned. |
| Needs | Fleet GitHub App (`fleet_github_not_configured` 503 otherwise). | Same. |

`fleet_target_release_evidence_mismatch` means the mode contradicts the
holder's attempts (for example `terminal` evidence for a holder with no
bound run).

**Abandon a plan directly, including on an unregistered cluster (H7).**
`POST /api/v1/fleet/targets/abandon-plan`, scope `admin`:

```json
{"planId": "<capacity plan UUID>", "reason": "failed plan blocking registration"}
```

Same age gate and run-listing snapshot as `abandon`. If the plan holds a
target's fence, the fence is freed; either way the plan is recorded abandoned,
and registration's in-flight scan then ignores it. Use it to unblock
registration (`fleet_target_registration_in_flight`) and to retire plans
stranded as `fleet_target_unregistered` after the first registration. A plan
with no dispatch nonce yet returns `fleet_target_release_evidence_mismatch`.
Re-abandoning returns `fleet_target_holder_abandoned`.

**After a release,** the next plan must be created after the release by more
than 5 minutes (`fleet_plan_revalidation_required` otherwise). The one
exception is the `dispatch_not_submitted` release, which never ran anything.

**Ordinary-lane pre-submit defect (H4).** If an ordinary-lane dispatch fails
before submission, its dispatch row stays `submitting` and the fence stays
held. This is unchanged behaviour. Clear it by abandon after 30 minutes.

## 5. The `observe` CI intent

Observations are the only way `ProviderStateKnown`, `NodesEnrolled`,
`RuntimeReady`, `IngressReady` and `DriftDetected` become anything but
`Unknown`. `POST resources/{name}/observations` is accepted only from a GitHub
Actions workload identity (no human or API token). It fails closed until
enabled.

Enable it:

1. Add `observe` to `NORN_GITHUB_ACTIONS_FLEET_ALLOWED_INTENTS`. The
   authority-only startup validator accepts exactly `apply,recover` or
   `apply,recover,observe`.
2. Set `NORN_GITHUB_ACTIONS_FLEET_ALLOWED_REPOSITORY` to
   `owner/repo@<repository id>@<owner id>`. The token's repository, repository
   ID and owner ID must match. The authority-only validator also requires the
   allowed refs `refs/heads/main` and the allowed environment `staging`.
3. Set `NORN_FLEET_GITHUB_ENVIRONMENT`. The token's GitHub environment must
   equal it.
4. The target must carry `environment:<lane>`, where `<lane>` is the Fleet
   document's environment this server dispatches (for example `staging/nyc3`),
   and the resource must be bound to that target.

The token must carry scope `fleet:operate` and intent `observe`, on a protected
ref; the reporter is derived from the verified run. A mismatch returns 403
`fleet_observation_identity_denied`. The CI identity can never register
targets, release fences, or set desired revisions.

Body:

```json
{
  "source": "provider",
  "observedAt": "2026-10-04T12:00:00Z",
  "facts": {"targetId": "tgt_...", "enrolledNodes": 3, "expectedNodes": 3},
  "evidenceRefs": ["https://..."]
}
```

`source` is `provider` (`targetId`, `error`, `enrolledNodes`,
`expectedNodes`), `state` (`drift`) or `runtime` (`ready`, `ingressReady`).
Bounds: `observedAt` within 24 hours before and 1 minute after receipt; facts
at most 16 KiB; at most 20 evidence refs of at most 512 bytes
(`fleet_observation_out_of_bounds`, 400). The response says `applied` or
`superseded`: an observation older than the source's watermark is stored but
never clears a newer reading. List with `GET resources/{name}/observations`
(`limit` 1 to 100, newest first).

## 6. `NORN_FLEET_GITHUB_RECOVER_WORKFLOW`

Name of the recovery workflow file, default `recover.yml`. Norn uses it to
recognise recovery runs of a plan when it proves a run completed:

- terminal release (section 4) proof for attempts hosted by a recovery run;
- the Q11 stop check on PG legacy: for a registered target, recovery of a
  source attempt is refused with `fleet_target_recovery_requires_stopped_source`
  until the source's GitHub run is proven `completed`. Unregistered targets keep
  the old unchecked behaviour (the documented Q11 gap).

It must differ from the apply workflow (`NORN_FLEET_GITHUB_APPLY_WORKFLOW`'s
file). Set it if your recovery workflow is not `recover.yml`; a wrong name
makes terminal release and recovery report incomplete proof.

## 7. Reconciler

The reconciler derives resource status from the fence, attempts, dispatches
and observations, and writes it with a compare-and-set. It is **on by
default**; set `NORN_FLEET_RECONCILER=false` to disable it. Values are parsed as booleans (`true/1/t`, `false/0/f`, any case); any other value fails startup rather than being ignored. (Earlier builds
needed `NORN_FLEET_RECONCILER=true`; see M1.) It runs only in the PG Fleet
authority-only process and in the etcd Fleet runtime (Q6), never in the
general router's process, so a deployment that serves Fleet routes from
elsewhere has `controller.state=unknown`. It rescans every 60 seconds and
after every accepted Fleet mutation, writes only when the derived status
changed, and has no leader lease: concurrent reconcilers converge. With it
disabled, a new resource has an empty status and reads show
`controller.state=unknown`; the data is honest but does not explain anything.

## 8. Restore

**PG.** `RestorePassive` (`controlrecovery/restore.go`) restores the bundle,
validates it, then advances the authority epoch on the restored database by
compare-and-set (`epoch+1`, reason `restore:<bundleId>`). It rewrites no
fence, attempt or history row. Effects on in-flight work:

- Every held fence now trails the epoch and reads `Uncertain /
  AuthoritySuperseded`.
- Heartbeat, advance and *succeeded* checkpoints from old runners are refused
  with `fleet_target_authority_superseded` (409). *Failed* checkpoints and
  cancel are still accepted.
- The first attempt of a bound plan and a recovery attempt re-bind the fence
  under the new epoch at generation+1.

Recovery: stop any runner still running for the plan in GitHub, then either
recover the attempt (re-bind), or release the fence (section 4; terminal
needs GitHub proof, abandon needs the 30-minute age). Plans that completed
before the restore are unaffected.

If the advance itself fails, `RestorePassive` returns the error with no
report and the target stays passive (nothing is started). The target is no
longer empty, so a second `RestorePassive` into it is refused. Recover by
discarding the target schema and restoring into a fresh empty target. The
equivalent manual step on the restored schema is the same CAS the code runs:
`UPDATE fleet_authority_epoch SET epoch = epoch + 1, activated_at = now(),
reason = 'restore:<bundleId>' WHERE singleton AND epoch = <restored epoch>`;
do it by hand only if the target cannot be rebuilt, and do not repeat it.

**Branch dependency.** The end-to-end PG bundle path (create bundle, restore,
epoch advance) does not work on this branch's base: `CreateBundle` and
`RestorePassive` miss the Mini extension tables and columns, so
`TestCreateVerifyRestorePassiveRoundTrip` fails on the base too. The fix is on
the separate branch `fix/v3-controlrecovery-mini-extension`; it must merge
before the PG restore runbook can be run end to end. Until then only the
narrow `TestFleetAuthorityEpochAdvanceAfterRestorePostgres` covers the epoch
step.

**etcd.** Epoch wiring on restore is deferred (H5). There is no production
etcd restore command and nothing advances the etcd authority epoch outside
tests and the qualification script's post-restore stage. After an etcd
snapshot restore, held fences are not marked superseded: a runner that
progressed after the snapshot may still be accepted. Do not restore etcd
while runners are active; afterwards inspect each held fence
(`GET targets/{id}/fence`) against GitHub and use section 4 for any plan you
cannot verify.

## 9. Error codes

All are `application/problem+json` with the code in `code`. 401/403 from the
auth layer (`insufficient_scope`, 403, including a CI identity on a mutating
route) apply to every route. Rows marked "existing routes" occur on the
pre-existing plan, dispatch, execute, rerun, attempt and checkpoint routes,
and only when the registry is non-empty.

| Code | HTTP | Where | Meaning and operator action |
|---|---|---|---|
| `fleet_target_execution_occupied` | 409 | existing routes | Another plan holds the target's fence. Wait, or resolve the holder (sections 3, 4). |
| `fleet_target_unregistered` | 409 | existing routes, release, resource desired | Cluster resolves to no registered target while the registry is non-empty. Register it, or abandon the plan. |
| `fleet_target_alias_conflict` | 409 | register, existing routes | Alias bound to another target, or the lane alias names a different target than the cluster alias. Fix aliases (immutable) or the plan. |
| `fleet_target_authority_superseded` | 409 | existing routes | Fence authority epoch is older than current (after restore). Recover to re-bind, or release (section 8). |
| `fleet_plan_revalidation_required` | 409 | existing routes | The plan started within 5 minutes of the target's last release (except `dispatch_not_submitted`). Create a new plan. |
| `fleet_target_holder_abandoned` | 409 | existing routes, abandon-plan | The plan was permanently abandoned. Create a new plan; never reusable. |
| `fleet_target_recovery_requires_stopped_source` | 409 | recover (PG legacy, registered) | The source attempt's GitHub run is not proven completed. Stop or wait for the run, then retry. |
| `fleet_target_registration_in_flight` | 409 | register | A plan on this target is in flight. Finish or abandon it. |
| `fleet_target_fence_not_held` | 409 | release, abandon-plan | Nothing to release. Re-read the fence. |
| `fleet_target_has_live_attempt` | 409 | release, abandon-plan | A runner is live. Wait for lease expiry or stop the run. |
| `fleet_target_terminal_proof_incomplete` | 409 | release (terminal) | GitHub does not show every run completed. Wait, or use abandon. |
| `fleet_target_abandon_too_soon` | 409 | release (abandon), abandon-plan | Under 30 minutes since last holder activity. The resource blocker message states when it is allowed. |
| `fleet_target_abandon_snapshot_required` | 409 | release (abandon), abandon-plan | No run-listing snapshot in the evidence. Retry. |
| `fleet_target_release_evidence_mismatch` | 409 | release, abandon-plan | Mode contradicts the holder (for example no dispatch nonce or no bound run). Pick the other mode. |
| `fleet_target_expected_generation_mismatch` | 409 | release | Stale `expectedGeneration` or the holder changed. Re-read the fence. |
| `fleet_target_not_found` | 404 | targets, release, resources | Target ID not registered. |
| `invalid_fleet_target_id` | 400 | targets, resources | ID is not `tgt_` plus 64 lowercase hex. |
| `invalid_fleet_target_request` | 400 | register, release, abandon-plan | Bad body: identity, alias kind, missing `expectedGeneration`, mode. |
| `invalid_fleet_plan_id` | 400 | abandon-plan, desired | `planId` is not a UUID. |
| `fleet_plan_not_found` | 404 | abandon-plan, desired | No such capacity plan. |
| `fleet_plan_read_failed` | 503 | abandon-plan, desired | Plan store read failed. Retry. |
| `fleet_target_read_failed` | 503 | targets, release, resources | Store read failed. Retry. |
| `fleet_github_not_configured` | 503 | release, abandon-plan, desired | Fleet GitHub App missing or unusable. Configure it. (`desired` accepts `commitSha` only when no App is configured at all.) |
| `fleet_target_observation_failed` | 502 | release, abandon-plan | GitHub run listing could not be read. Retry. |
| `invalid_idempotency_key` | 400 | register, release, abandon-plan | Missing or over-long `Idempotency-Key`. |
| `operation_acceptance_unavailable` | 503 | register, release, abandon-plan | Signed operation store unavailable. |
| `operation_actor_ambiguous` | 409 | PG register, release, abandon-plan | Credential does not give a stable actor. Use a managed token. |
| `invalid_fleet_resource_name` | 400 | resources | Name is not 1 to 128 of `a-z0-9._-`, starting alphanumeric. |
| `invalid_fleet_resource_request` | 400 | resources | Bad body, missing or negative `expectedRevision`, bad `commitSha`, `planId` with no App, bad `limit`. |
| `fleet_resource_not_found` | 404 | resources | Create it first. |
| `fleet_resource_revision_conflict` | 409 | desired | Changed since `expectedRevision`. Re-read and retry. |
| `fleet_resource_target_mismatch` | 409 | create | Resource already bound to a different target. |
| `fleet_resource_plan_target_mismatch` | 409 | desired | The plan's cluster resolves to a different target. |
| `fleet_github_plan_not_ready` | 409 | desired | Merge the plan's PR and wait for its protected-branch plan workflow. |
| `fleet_github_plan_unproven` | 502 | desired | GitHub could not prove the approved commit. Retry. |
| `fleet_github_dispatch_failed` | 502 | desired | GitHub could not resolve the plan. Retry. |
| `fleet_resource_store_failed` | 503 | resources | Store failure. Retry. |
| `fleet_observation_identity_denied` | 403 | observations | Not an `observe`-intent GitHub Actions identity from the bound repository, protected ref, lane environment and target alias (section 5). |
| `invalid_fleet_observation` | 400 | observations | Bad `source` or missing `observedAt`, or invalid JSON. |
| `fleet_observation_out_of_bounds` | 400 | observations | Time, size or evidence bounds exceeded. |

One mapping gap: on the PG general router `POST plans/{id}/github/reconcile`,
a refusal for an abandoned plan surfaces as 500 `fleet_github_receipt_failed`,
not `fleet_target_holder_abandoned` (final-review m2).
