# Online upgrade lessons and capability proposal — 2026-11-07

This note turns the initial 2026-11-07 Mini refusal and recovery work into
platform-wide upgrade requirements. It is a proposal for the next engineering
slices, not a claim that online upgrades are implemented. The Mini later
completed its controller-mediated direct V3 transition and signed repair; that
bounded forward transition is recorded in [launch state](LAUNCH-STATE.md). It
did not establish uninterrupted service or general online-upgrade support.

## What the initial Mini attempt taught us

The initial attempt stopped safely before changing the database or promoting a release.
The candidate required schema 51 while the Mini was at 49, and raised the
minimum writer contract from 32 to 33. The ordinary upgrade lane could not
prove that every writer, including `norn-host-agent`, was stopped or compatible.
That refusal protected the live instance, but the available recovery-forward
path also could not start a new generation from the completed generation on
the current release. Separately, the repair note named a helper SHA that did
not support the documented recovery action; the signed plist bound a different
helper SHA that did.

The lesson is broader than “schedule a maintenance window”:

- A binary rollout is not safe until every database client has an explicit
  compatibility identity and the coordinator can observe it.
- A migration that raises a reader or writer floor is a compatibility boundary.
  It must be planned as a staged transition, not bundled invisibly into a
  routine restart.
- Recovery must support the next forward generation from the currently active
  generation, including after prior successful transitions. Operators should
  not have to edit pointers, receipts, or journals by hand.
- Operator instructions, signed manifests, and the exact installed controller
  binary must name the same immutable version and supported actions.
- Preflight should explain the precise blocker and the shortest safe route
  before it stops the operator mid-flow. The current refusal was correct, but
  it arrived after significant preparation and did not offer a seamless
  compatible rollout path.

The full observed evidence is in
[`m6-mini-v3-forward-upgrade-observation-2026-11-07.md`](m6-mini-v3-forward-upgrade-observation-2026-11-07.md).

The later successful direct transition does not erase this finding: it used a
controller-recovered, writer-fenced forward path and included a brief service
fence. It is a Mini direct-lane result, not a multi-node online upgrade proof.

## Proposed upgrade experience

The operator should submit one signed “upgrade to this release” intent. Norn's
controller should validate and plan the operation, perform compatible steps,
and expose one clear status with progress, expected interruption, and any
operator action. No manual runner or shell-level migration should be part of
the normal path.

1. **Plan before mutation.** Verify source and target signatures, artifacts,
   disk/resources, backup policy, health, current schema, every registered
   writer/reader identity, and the target's compatibility declaration. Produce
   a human-readable plan with expected downtime and an exact stop reason if
   blocked. Do not ask for a full OS restore when the operation only needs a
   database copy.
2. **Expand compatibly.** Apply additive, resumable schema expansion while
   current binaries continue to work. Prefer online DDL and bounded background
   backfills. Each migration declares whether it is additive, its locks and
   resource needs, and the oldest supported readers/writers before and after.
3. **Roll binaries with overlap.** Start a target instance in passive/readiness
   mode, verify its database compatibility and health, then shift traffic or
   leadership. Drain and stop old instances only after replacement health is
   established. For a single-node Mini, use process handoff where possible and
   state any unavoidable short API interruption upfront.
4. **Retire old contracts separately.** Remove old columns or raise compatibility
   floors only after all old binaries and writers are gone and the configured
   observation window passes. Keep contract cleanup separate from the release
   that first introduces the replacement representation.
5. **Recover by phase.** Persist an idempotent journal before each side effect.
   Before incompatible writes, allow binary rollback when the old binary still
   supports the expanded schema. After the compatibility boundary, keep a
   write fence and resume/repair forward; never imply that switching a symlink
   or restoring an old binary reverses data changes.
6. **Close with evidence.** Verify target version, schema/floors, all writer
   registrations, service health, workload/data invariants, and measured
   interruption. Retain source and target artifacts and a concise receipt.

## Operator procedure the controller should provide

The normal user-facing action should be **Plan upgrade** followed by one
**Upgrade** action when the plan is ready. The controller owns all shell,
runner, process and database operations. It should not ask the operator to find
keys, manually stage files, run one-off commands, approve stale inputs, or
interpret an opaque failed job.

### Before the upgrade action

1. Resolve the requested version to one exact signed release SHA and pin the
   controller/helper set that will execute it. Never let `HEAD`, a branch name,
   or a tag move after planning.
2. Read the active source identity, schema/floors, service topology, every DB
   client and startup mode, active-operation/admission state, current process
   manager bindings, disk/memory/network headroom, backup state and the chosen
   handoff path. Verify credentials are present, scoped and usable without
   displaying them.
3. Build and validate the exact candidate, migration sequence, controller
   actions, backup/private-restore proof, writer compatibility plan, readiness
   checks, interruption estimate and recovery boundary. The candidate must
   start passively on the planned expanded schema before live cutover.
4. Show one readiness result: `READY` or `BLOCKED`, with each blocker tied to
   evidence and one concrete resolution. Examples include “host agent is an
   unregistered writer,” “candidate cannot start on schema 50,” or “restore
   headroom is 8 GiB short.” A blocked plan performs no live changes.
5. Refresh short-lived credentials, approvals and signed one-use execution
   claims after all prerequisite approvals are complete. Waiting for human
   review must not consume an intent that then expires before execution. If any
   bound fact changes, mark the plan stale and explain what must be recollected.

### During execution

1. Acquire one controller lease and a durable local journal. Close admission to
   new upgrade-sensitive work, then drain only the operation classes that
   affect this platform transition. Preserve Fleet-only queued receipts and
   report them separately; do not silently discard or claim them.
2. Take a fresh recovery copy and validate a private restore before schema or
   binary changes. Use a database copy for database migration qualification;
   use a full macOS IPSW restore only when OS installation is itself under
   test.
3. Apply an online-compatible schema expansion while the old service remains
   available. Backfills need bounded batches, checkpoints, progress and safe
   retry semantics. Wait for all active readers/writers to meet the declared
   compatibility contract before activating new schema behavior.
4. Start the candidate in passive mode, validate API/auth/schema/host-agent
   contracts and workload reads, then atomically switch serving ownership and
   traffic. Keep the prior process available until the candidate has passed
   readiness and user-facing health checks. Measure the actual API, write and
   job interruption separately.
5. Retire the old process and old schema contract only after candidate health,
   writer ownership and the observation window pass. Schema contraction belongs
   to a later controller phase/release, not the initial expand-and-roll step.

### If something fails

| Failure point | Controller response |
| --- | --- |
| Planning/readiness | Do not mutate. Preserve the plan and show the single missing prerequisite with a direct resolution. |
| Before first schema effect | Release the claim, keep the current release active, and permit a fresh plan. |
| During additive expansion | Keep old code serving if still compatible; resume the exact idempotent migration phase from its checkpoint. Never relaunch the whole script blindly. |
| Before traffic/owner switch | Keep or restore old ownership only when its declared schema contract remains valid; otherwise retain the write fence and recover forward. |
| After candidate has accepted writes | Do not switch back to an incompatible old writer. Hold the fence, restore candidate availability, and resume forward or surface a precise operator action. |
| Controller/host/API restarts | A separate supervisor reopens the signed journal, proves the old executor is dead, reconciles actual process/schema/traffic state, and resumes the same plan digest. Never create a second executor while the first may still hold authority. |
| Ambiguous external effect | Reconcile exact resource identity and prove present/absent before retry or cleanup. An empty partial inventory is evidence, not automatically a terminal “absent” result. |

Every terminal receipt should state exact source and target SHAs, schema
versions/floors, controller SHA, plan digest, phase results, evidence digests,
workload/data comparison, interruption measured, and whether the outcome is
`succeeded`, `blocked`, `recovering`, or `operator-action-required`.

## Lessons to encode from recent attempts

- **Do not do expensive setup before the plan says it is needed.** The Tart
  IPSW guest was a poor prerequisite for a database/platform upgrade. Have the
  planner choose database-copy versus full-OS qualification from the behavior
  actually being tested, and estimate storage using that path.
- **Approval time is part of the intent lifecycle.** Signed one-use inputs
  expired while waiting at human gates. The controller should stage or refresh
  the exact execution claim after approvals and before dispatch; it should not
  silently reuse expired authorization.
- **Make uncertain outcomes explicit.** The Packer build failed and reported an
  empty inventory, but reconciliation still marked guest creation ambiguous.
  The later retirement run belonged to an earlier attempt. Bind every cleanup
  receipt to the exact attempted resource and do not claim absence based on a
  different run.
- **Pin instructions to executable identity.** A repair document named a
  helper that did not support the needed action; a different signed helper did.
  Preflight and release lint should compare the signed manifest, installed
  controller SHA, advertised action set and operator instructions.
- **The gate should be precise and non-surprising.** Disk floor, host identity,
  network/TCC policy, tool availability, credentials and database compatibility
  should all be checked together before mutation. A safe refusal is correct,
  but should include the shortest controller-managed route to `READY`.
- **Retries must be evidence-based.** A failed or timed-out operation needs an
  exact phase journal and a reconciliation pass before retry. Rate limits and
  review waits should use serialized bounded backoff and renewed claims, never
  duplicate workflow dispatches.
- **Keep operator choices at product boundaries.** The user already chose the
  independent Mini as the live development/staging upgrade target and accepts
  bounded service interruption there. Do not repeatedly ask whether to use a
  separate guest or whether an approved routine gate may proceed; do ask for a
  decision only where policy, data loss or an irreversible compatibility
  boundary remains genuinely unresolved.

## Capability changes to prioritize

### 1. Compatibility contracts for all writers

Register API, host agent, workers, schedulers, and migration processes with
instance identity, binary SHA, read/write contract versions, heartbeat, and
lease/epoch. The upgrade coordinator must be able to fence stale writers and
prove the active set is compatible or stopped. Unknown/unregistered writers
must fail closed with an actionable diagnosis.

### 2. Migration plan and compatibility lint

Extend migration metadata and release manifests so CI/controller lint can
reject a release that combines incompatible schema contraction with a rolling
binary rollout. Require expand/backfill/contract classification, compatibility
floors, idempotence/resume behavior, lock and duration estimates, and
postconditions. Validate the sequence against the prior and next release
contracts before signing or running it.

### 3. Reusable rolling-upgrade executor

Build one durable controller operation for plan → backup/checkpoint → expand →
passive candidate check → instance handoff → old-writer retirement → optional
later contract cleanup → verification. Each phase needs an idempotency key,
checkpoint, bounded retry policy, and explicit “safe to retry / must inspect /
resume forward” result. It should produce operator actions only for decisions
that genuinely require a human.

### 4. Generation-chain recovery and exact controller binding

Support a completed active generation as the predecessor for the next one.
Resolve immutable predecessor evidence by digest and signature, preserve
historical records, and atomically publish the new pointer. Validate before
execution that the installed controller SHA, signed API plist, repair
instructions, and advertised action set agree. A stale or unsupported helper
must be diagnosed before a transition begins.

### 5. Explicit availability objectives

Measure separately: API unavailable time, write pause, background-job pause,
and data-loss/recovery objective. Single-node mode can promise a short bounded
process handoff only after it is measured; continuous service needs redundant
serving instances and a tested leadership/traffic handoff. Report the observed
numbers per release rather than calling a healthy post-upgrade check “zero
downtime.”

## Suggested delivery order

1. Fix generation chaining and helper/documentation binding; make preflight
   identify each required action before mutation.
2. Add compatibility metadata/lint and complete writer inventory/fencing for
   API plus host agent.
3. Implement the executor's additive migration and passive-candidate handoff
   path with restart/interruption recovery.
4. Qualify on the Mini using the exact signed target, first proving a short
   bounded interruption and then reducing it; separately qualify Fleet's
   multi-instance rolling path for continuous API service.
5. Add contract cleanup only after mixed-version observation and compatibility
   expiry are proven.

The first measurable target should be a release-specific interruption budget,
not an unqualified “zero downtime” promise. The Mini is a useful real workload
pilot, but its single-node topology cannot establish Fleet's continuous-service
claim.
