# ADR 0010: Fleet controller-run executor (B-lite)

Status: accepted, 2026-10-04 (human decisions D1-D8). Scope: changes 5-7 of the
Fleet controller plan ([`plan-5-7.md`](../fleet-controller/plan-5-7.md), revision 3,
responding to [`plan-5-7-review.md`](../fleet-controller/plan-5-7-review.md)). It
builds on [ADR 0009](./0009-fleet-controller.md) (target, fence, epoch, resource
status). This record fixes decisions and the exact payload shapes later work
packages must agree on. It enables no runtime behavior by itself.

The shapes are pinned in code at `v2/api/fleet/executor/shapes.go`, in
`v2/api/contract/control-v1.openapi.yaml`, and cross-checked by
`contract/executor_contract_test.go`. Route, phase, scope and error-code rows
are in [`lifecycle-contract.md`](../fleet-controller/lifecycle-contract.md) section 7.

## Context

Staging create and scale-up today needs three human touches, three workflows and
a nonce/OIDC bridge between Norn and GitHub. The human's direction: the platform
holds the deploy credentials and runs the fleet through the controller; GitHub
only says "converge to revision X". This reverses proposal constraint 4. The Norn
API process still holds no provider credentials.

## Decision: B-lite

A pinned Python loop runs under systemd on an always-on management executor host
(second droplet in the management root, separate DO team). The Go authority-only
process only records desired state, approvals, grants and attestations and
arbitrates the fence. There is no claim protocol, executor registry, executor
nonce lane, incarnation counter, migration 50 or writer-floor bump: **the writer
stays at 32 and no migration is added.** Extra state lives in existing JSONB
(`fleet_resources.document`, `fleet_runner_attempts.metadata`) and in ledger
operations. Pre-executor v32 binaries fail closed: they do not know `fleet:execute`
and require `principal.CI`.

Rejected: running in the authority process (provider keys beside the App and audit
keys; a restart kills applies); a Go supervisor with claim and registry (about 17
new parts); Nomad batch jobs (new infrastructure); a resident self-hosted GitHub
runner holding SOPS keys (D7).

## D1-D8

- **D1 shape.** B-lite as above. The loop uses the existing runner-attempt routes;
  a new executor branch acquires and binds the fence and inserts the attempt in
  one transaction. The executor never uses the GitHub OIDC path.
- **D2 merge gate.** Config-only diffs auto-apply after trigger verification. Any
  executable path (`scripts/`, `ansible/`, `modules/`, `cloud-init/`, the executor
  itself, anything not explicitly `config` or `inert`) needs a one-click
  `fleet.converge.approve`. Destructive or contraction plans are GitHub lane only
  (`destructive_requires_github_lane`). Reported through `approvalPolicy`.
- **D3 secrets.** SOPS/age now, OpenBao later behind the same
  `CredentialProvider`. Norn issues attempt- and phase-scoped credential grants
  (`fleet.credential-grant`, 2 hour expiry); the provider refuses to issue without
  a grant ID. Phases run as a different OS user with all capabilities dropped.
  Management sits in a separate DO team. Per-phase scoping is hygiene, not
  containment; the control against hostile phase code is the D2 approve.
- **D4 management plane out of scope.** Everything else is built and tested
  locally (Lima VM, fake GitHub and DO, MinIO). Real staging qualification (Q2)
  is gated on the management plane existing.
- **D5 defaults.** 60 s ETag poll. A commit is verified when the branch `protected`
  flag is set and a successful `validate.yml` push run exists at that SHA. Converge
  always targets the newest verified tip. Production stays on the GitHub lane.
  `autoRecoveryBudget` is 0 until Q2 rows 3-5 pass, then 1 per plan.
- **D6 stop-safe host.** Always on; can be stopped or powered off at any moment.
  Durable intent is fsynced before any call; every phase resumes or recovers
  through attempt, checkpoint and stop-proof machinery. No on-demand power-on.
- **D7 no resident GitHub runner.** The GitHub lane (production, destructive,
  contraction, external-Mac, pilot) uses an operator-provisioned ephemeral runner
  for one session. Nothing GitHub executes ever runs on the executor host. The
  fence arbitrates between the two lanes.
- **D8 executor token.** Device token, scope `fleet:execute` alone, platform
  `fleet-executor`, 7 day lifetime, self-rotated at 3.5 days or less remaining and
  only while idle, revoked immediately on rebuild or retirement.

## Stop proof: self-attestation

Executor-lane stop proof is a **self-attestation** by the loop, using the
`StopAttestationRequest` body below and decided by `executor.ValidStopAttestation`.
The body carries the attesting loop's own identity; the source attempt's identity
comes only from its stored metadata. It is valid only when:

1. the caller is the source attempt's **device** (`principal_subject`), so a
   rebuilt host, which enrolls as a new device, cannot attest;
2. `dropletId` and `machineId` equal the source's (a snapshot clone keeps
   `machine-id` but gets a new droplet ID);
3. `invocationId` differs from the source invocation (an invocation cannot vouch
   for itself);
4. `observedAt` is within 5 minutes (at most 1 minute ahead), as for GitHub stop
   evidence;
5. either `bootId` differs from the source boot (**different-boot proof**: the
   kernel that ran the phase is gone), or, on the same boot, `phaseUidEmpty`,
   `phaseProcessGroupReaped` and `sourceCgroupEmpty` are all true
   (**same-boot proof**: no `norn-phase` process, the phase group reaped, and the
   executor unit's cgroup subtree holds no process but the attesting loop). Each
   fact is a positive boolean, so an omitted field decodes to false and fails
   closed.

`sourceCgroupEmpty` is not "the old invocation's `cgroup.events` reads
`populated 0`": systemd reuses one cgroup for every invocation of a unit, so
that cgroup is the attester's own. The loop instead reads `cgroup.procs` of the
unit cgroup and of every descendant before it starts any child, and the fact is
true only when the attester's pid is the sole entry. This is a sound stop proof
because the unit runs `KillMode=control-group` with no `Delegate=`, and phases
have no capabilities (R1): a phase can neither move itself out of the unit
cgroup nor create a sub-cgroup. A process started outside it (cron, at; P5
denies both to `norn-phase`) still has the phase uid and fails `phaseUidEmpty`.
Phases do not run in a transient `systemd-run --scope` or `--unit`: that would
make emptiness directly readable, but it moves phases out of the unit's
`KillMode`, needs a privileged D-Bus call, and was rejected in plan §2.3.

This is not third-party evidence. It adds no capability beyond what root on the
host already has, and the uid check is sound only because phases cannot change
uid (R1). A **different host** can never attest; see provider evidence below.
Stale `tofu force-unlock` requires a valid attestation, an apply lock whose `Who`
is `norn-phase@<host>` of the source, `Created` within the source's lifetime, and
the lock ID recorded in the attestation's `tofuLock` first (else
`state_lock_foreign`).

## Provider evidence: what is trusted, and who

`providerEvidence` is the break-glass for a different or rebuilt host. Under D1
the API process holds no provider credential, so Norn **cannot** gather droplet
state itself, and B2's rule (proof is gathered by the server, never trusted from
the body) cannot be met for provider state. Chosen: **(a) an admin assertion,
plus every fact Norn can check itself.**

- **Trusted:** the admin principal who signs the `fleet.target.fence-release` or
  `fleet.target.abandon-plan` operation (`admin`, non-CI). `providerEvidence` is
  that admin's assertion that the old droplet is `off`, `archive` or
  `not_found`. It is labelled an assertion everywhere, lives in the signed,
  fingerprinted admission, and is never written to `FleetTargetReleaseEvidence`
  (the server-gathered GitHub proof), which it can never satisfy. The server
  stores `sha256:` of its canonical JSON under `fleetProviderEvidenceDigest`
  (caller values discarded).
- **Checked by the server, from its own state:** the admin scope; `dropletId`
  equals the holder's latest executor attempt metadata; the holder attempt's
  executor device is **revoked** (`fleet_executor_device_active` otherwise), so
  the old loop can no longer heartbeat, checkpoint, advance or obtain a grant;
  the H5 30 minute age gate; shape and a one-hour `observedAt`.
- **Not checked by anyone in Norn:** the droplet's actual provider state. A phase
  already running with an issued grant is bounded only by the admin's assertion
  and by the OpenTofu Spaces lockfile.

Rejected: (b) evidence gathered by another executor with a read-only grant. That
executor is equally a self-reporting host (DO responses are unsigned), it holds
every staging credential, it needs the deferred observe grant (C2), and on a
rebuild it is the party that wants the fence freed. Giving the API process a
read-only DO token was rejected because it breaks D1.

## Validators and lanes

The lane is chosen by the **principal**, before any body is decoded, never by
the request shape:

| Principal | Lane | Validators (owner) |
|---|---|---|
| GitHub Actions OIDC, exact `fleet:operate` (`principal.CI` set) | GitHub | unchanged: `validateFleetRunnerStart`, `validOptionalFleetWorkflowURL`, `validateFleetRunnerDispatchBinding`, `validateFleetRunnerAttemptAcceptance`, the CI branch of `fleetRunnerPrincipalOwnsAttempt`, `lifecycle.ValidStopEvidence`, `DecideRelease` GitHub proof |
| managed token, exact `fleet:execute`, `CI == nil`, platform `fleet-executor` | executor | `executor.AttemptStartRequest.Validate` and `AttemptMetadata.Validate` (E2/E2e start), a separate executor branch of `fleetRunnerPrincipalOwnsAttempt` (E2/E2e), `CredentialGrantRequest.Validate` and `ValidStopAttestation` (E5) |
| `admin`, non-CI | either (break-glass) | release/abandon admission; `ProviderEvidence.Validate` only when the holder is on the executor lane (E5) |
| anything else | none | `insufficient_scope` |

Rules for E2 and E2e:
- No GitHub-lane validator gains a parameter, flag or exemption. The executor
  branch never calls them.
- The executor start body is `executor.AttemptStartRequest`, decoded with
  `DisallowUnknownFields` after the principal is known. `dispatchNonce`,
  `sourceDispatchRunId`, `pilotRunId` and the external-Mac digests are unknown
  fields there and are refused. The server stores `sourceDispatchRunId = 0` and
  `pilotRunId = ""`.
- On etcd the attempt stays operation kind `fleet.runner-attempt` (payload
  `lane: "executor"`), admitted through a new, separate acceptance field. The
  store refuses an acceptance carrying both it and `FleetRunnerAttempt`, as it
  already refuses runner-attempt plus reconciliation.

The identifier spaces are disjoint, so neither validator can admit the other's
attempt:
- attempt ID `executor:…` versus `github-actions:…`;
- `workflowUrl` `norn-executor://…` versus `https://…` (every GitHub check
  requires https; `ValidStopEvidence` requires `https://github.com`);
- `sourceDispatchRunId` 0 versus positive;
- no dispatch nonce versus a required one.

So GitHub observers can never mark an executor attempt completed: a GitHub-lane
`terminal` or `abandon` release of an executor holder fails closed, and the
executor-lane rule (attestation or provider evidence) is the only way to free
it. Pinned by `TestLaneIdentifiersAreDisjoint`.

## Pinned shapes

All JSON is camelCase. New fields on existing structures are `omitempty`; existing
fingerprints are unchanged. Every new ledger operation follows the
`fleet.target.*` pattern: typed intent in the body, `Idempotency-Key` required,
server fills identity, server-gathered facts go in operation metadata outside the
fingerprint. Error codes follow `fleet/lifecycle/fence.go`: `executor.Code…`
constants named after the exact string, `executor.CodeStatus` for the HTTP status,
and `*executor.Error{Code, Reason}`.

**Converge plan.** An ordinary `fleet.capacity-plan` operation with
`action: "converge"` and one additive omitempty object `converge`:
`{resource, repository, commitSha, baseCommitSha?, pathClass: config|executable,
requiresApproval, mergeActor?, pullRequest?, generation}`. A plan without it has
byte-identical canonical JSON and digest (golden test
`TestConvergeExtensionKeepsCapacityPlanDigest`). `planSha256` on the attempt is
the hex part of the plan's `digest` (`sha256:<hex>`). An executable plan always
has `requiresApproval: true`.

**Rescan.** `POST /api/v1/fleet/resources/{name}/converge`, no body,
`Idempotency-Key` required, non-CI `platform:operate` or the executor principal.
It may write a `fleet.capacity-plan` operation, so it is keyed. It records a new
converge plan only when the verified tip has no admissible converge plan (the tip
is newer than the last applied commit, or the newest plan for it was refused with
`fleet_plan_revalidation_required`); otherwise it records nothing. Repeating it is
therefore harmless, and a key replay returns the first response.

**`fleet.converge.approve`.** Route `POST /api/v1/fleet/plans/{planID}/converge/approve`,
`platform:operate`, non-CI. Body `{commitSha, resume?, sourceAttemptId?}`;
`sourceAttemptId` iff `resume`. Ledger payload `{planId, commitSha, resume?,
sourceAttemptId?}`, `Ref = planId`. `commitSha` must equal the plan's commit
(`fleet_converge_commit_mismatch`). A resume approve is consumed by exactly one
recovery attempt (`fleet_converge_approval_consumed`). Attempt start refuses a
`requiresApproval` plan with no matching approve (`fleet_converge_approval_required`).

**`fleet.executor.stop-attestation`.** Route `POST /api/v1/fleet/executor/stop-attestations`,
executor principal. Body `{sourceAttemptId, dropletId, machineId, bootId,
invocationId, phaseUidEmpty, phaseProcessGroupReaped, sourceCgroupEmpty,
observedAt, tofuLock?: {id, operation, who, created}}`, identity fields being the
attester's. Ledger payload adds `planId` (server-derived from the attempt),
`Ref = planId`. Decision as in "Stop proof" (`fleet_executor_stop_attestation_invalid`).

**`fleet.credential-grant`.** Route `POST /api/v1/fleet/plans/{planID}/credential-grants`,
executor principal. Body `{attemptId?, phase, types[]}`; response
`{grantId, expiresAt, attemptToken?, repoToken?}`. Ledger payload `{grantId, planId, attemptId?, phase,
types (sorted, unique), deviceId, tokenId, expiresAt}`, expiry 2 hours. Grant
phases are the pseudo-phases `plan` (no attempt), `preflight` (a live owned
V3 attempt at `provider_applying`, including recovery), and `recover` (a live recovery attempt; the
union of all types), and otherwise **real lifecycle phase names**
that must equal the attempt's `currentPhase` exactly. There is no alias. The table
depends on the attempt's backend, because executor plans never drain:

| Phase | PG (legacy no-drain list) | etcd (canonical list) |
|---|---|---|
| `plan` | do-read, spaces-rw, repo-read | same |
| `preflight` | — | bootstrap-dir, ssh-node on a live owned first attempt at `provider_applying` or nonterminal recovery attempt at its current phase |
| `prechange_verified` | — (not in the list) | norn-attempt |
| `provider_applying` | — (not in the list) | do-write, spaces-rw, norn-attempt |
| `infrastructure_applied` | do-write, spaces-rw, norn-attempt (tofu apply runs here; its checkpoint follows the apply) | spaces-read, ssh-node, bootstrap-dir, release-github, norn-attempt |
| `inventory_generated` … `readiness_verified` | spaces-read, ssh-node, bootstrap-dir, release-github, norn-attempt | same |
| `old_nodes_drained` | — | norn-attempt (evidence only) |
| `recover` | union (including repo-read) | union (including repo-read) |

Before a V3 `provider_applying` grant can include `do-write`, the executor
records a signed `fleet.preflight-complete` attestation bound to its latest
unexpired preflight grant and owned attempt. Its evidence digest identifies a
non-secret local validation receipt. Norn verifies binding and order; the
trusted executor performs the private checks and revokes their local grant
files before requesting provider credentials. A grant alone does not prove
validation completed. These ledger admission reads and writes are not an
atomic cross-store transaction against a concurrent executor request; the
protected single-loop executor and phase-file revocation remain the execution
boundary. Recovery at any phase must attest its own fresh preflight before
receiving `do-write`. Recover and preflight grants record the attempt phase at
issuance; replay or completion after phase movement is refused.

**`repo-read` (no GitHub credential on the host).** `norn-fleet` is private, so
the executor must read it, but no deploy key or token is stored on the host. The
`plan` grant (the loop's `git fetch` before planning) and `recover` (at the start)
may include `repo-read`; no other phase may (`fleet_credential_type_not_allowed`).
Norn mints a GitHub App installation token restricted to the single configured
repository with `contents: read` only, valid until GitHub's expiry (about 1 h) or
the grant expiry, whichever is earlier, and returns it as `repoToken`. It is never
stored in the ledger, logs or operation metadata: the ledger records only that it
was issued and its expiry. A replay of the same Idempotency-Key does not mint a
second token. It returns the same token from the route's memory while unexpired,
otherwise 409 `fleet_repo_token_replay_expired` and the loop asks for a new grant
(memory is not persisted, so a Norn restart also refuses; chosen over persisting a
secret). Without a configured GitHub App the grant fails with 503
`fleet_repo_token_unavailable`.

This refines plan §2.3's `provider_applying` row, which named a phase the PG list
does not have.

**Attempt identity and metadata.** `runnerAttemptId =
executor:<dropletId>:<invocationId>:<planId>` (dropletId decimal, invocationId 32
lowercase hex, planId the path UUID); `workflowUrl = norn-executor://<dropletId>/<invocationId>`;
`sourceDispatchRunId = 0` (server-set); fence holder `HolderNonceSHA256 =
sha256("executor-lane:" + planID)` (hex). `principal_subject =
managed-device:<deviceId>`: this corrects plan §2.6's `managed-token:<tokenId>`,
because D8's self-rotation issues a new token ID and would orphan a live attempt,
while device deletion (D8) must cut every attempt the device owns. Start body
`executor.AttemptStartRequest` = `{schemaVersion, runnerAttemptId, commitSha,
planSha256, workflowUrl, resume?, heartbeatTimeoutSeconds?, operationClass?,
createdNodeCount?, executor}`, where `executor` is the metadata `{lane: "executor",
dropletId, machineId, bootId, invocationId, loopRelease, commitSha,
wheelhouseSha256, tofuVersion}` stored in `fleet_runner_attempts.metadata`.

**`providerEvidence`.** Optional field on the existing `fleet.target.fence-release`
and `fleet.target.abandon-plan` admissions: `{provider: "digitalocean", dropletId,
status: off|archive|not_found, observedAt, reference?}`. Rules as in "Provider
evidence". For executor-lane holders either a valid stop attestation or
`providerEvidence` replaces GitHub observers (else `fleet_provider_evidence_required`).

**Execution mode.** `POST /api/v1/fleet/resources/{name}/execution-mode`, admin,
body `{mode: github|controller, expectedRevision}`, ledger `fleet.resource.execution-mode`,
fence must be Free (`fleet_execution_mode_fence_held`). Stored only in
`fleet_resources.document`; never read inside execution transactions (T7). Default
`github`.

**Executor work view.** The executor principal reads the ordinary
`GET /api/v1/fleet/resources/{name}` (single resource, never the list) and no
plan route. `desired` gains the additive omitempty object `converge` =
`executor.ConvergeView {planSha256, requiresApproval, approved}`, present only
when the desired revision's plan is a converge plan. `planSha256` is the value
the attempt start carries; `approved` is true when a non-resume
`fleet.converge.approve` for that plan and commit exists. The loop starts a
`requiresApproval` plan only when `approved` is true, and admission still
enforces it. `status.active.resumeApproved` (omitempty boolean) is true when an
unconsumed resume approve names the holder plan's latest attempt and a valid
attestation or `providerEvidence` exists for it. The routes the executor
principal may call are exactly `executor.ExecutorRoutes` (E6).

**Enrollment.** The exchange's pending answer is unchanged and pinned:
`409 enrollment_not_approved` (pending or expired). The loop polls every 5 s
until the start response's `expiresAt`; any other refusal is terminal, and
`401 invalid_enrollment_verifier` (it counts toward lockout) is never retried.

**Resource status additions** (pinned in OpenAPI; E4b adds the constants beside
their siblings in `fleet/controller`, not in `fleet/executor`). `executionMode`
on the resource view; `approvalPolicy` gains `protectedBranchVerified`,
`validateWorkflowSucceeded`, `mergeGate` (`config_auto_executable_approve`),
`destructiveApproval` (`github_lane_dispatch`), `approvedBy`, `requiredReviews`
(`not_verified`), all omitted off this lane; verification and basis gain
`github-protected-main`; blocker kinds gain `trigger` and `approval`; blocker
reasons `trigger_unverified`, `history_rewritten`,
`destructive_requires_github_lane`, `state_lock_foreign`; next action
`approve_converge`; derived `status.active.resumeApproved`.

**Scopes.** `fleet:execute` alone (D8) reaches only `executor.ExecutorRoutes`: the
attempt and checkpoint routes, the grant and attestation routes, the rescan, the
single-resource read and token rotation. Approve needs `platform:operate`, non-CI
(D2). Execution mode, release and abandon need `admin`. No new route accepts a CI
principal.

## Consequences

- Standing staging credentials live on one host. Management is a separate DO team;
  a compromised executor host exposes all staging credentials. Alerts on
  attestations and `providerEvidence` use are required.
- There is no net GitHub-side reduction: `plan.yml` still exposes DO and Spaces to
  merged code. The mitigation is the D2 approve for executable changes.
- Lost: ephemeral provider keys, GitHub as an independent gate on this lane,
  third-party stop evidence.
- Required reviews and bypass need Administration: read and are reported
  `not_verified`.
- Break-glass release of an executor holder from a different host rests on an
  admin's assertion about provider state, not on proof.
- Rollback: set `executionMode=github`, stop the unit, revoke the device. No schema
  change.

## Validation

Owning tests: `fleet/executor/shapes_test.go` and `contract/executor_contract_test.go`
(this ADR); E1-E6 conformance suites per plan-5-7 section 4; Q1 locally and Q2 on real
staging. No qualification gate is claimed passed by this record.
