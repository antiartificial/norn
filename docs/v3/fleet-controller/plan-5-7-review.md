# Review: plan-5-7.md (Fleet controller changes 5–7)

Reviewer stance: skeptical senior review against the real code. Worktree
`feature/v3-fleet-executor` (`790f86fe`, Go root `v2/api`); norn-fleet read-only
at `daf7bf3`. `plan:` = `docs/v3/fleet-controller/plan-5-7.md`. `norn:` paths are
relative to `v2/api/`, `fleet:` to the norn-fleet checkout.

**Verdict.** The diagnosis in §1 is good, and the direction "platform holds the keys,
GitHub only triggers" is achievable. But the plan is **not ready for a yes**:

- It builds on a management host that does not exist.
- It overstates the security gain.
- Its stop proof does not hold in several failure cases.
- It adds about 17 new moving parts where 5–6 would do.

It cuts the operator's back-and-forth (5 touches → 1). It does not cut the
build and qualification back-and-forth, and that is what the human asked to
minimise. §6 below proposes a smaller variant.

---

## 1. Fact checks

| Claim (plan line) | Verdict | Evidence |
|---|---|---|
| 12 workflows, 5,722 lines (plan:33) | correct | `ls fleet:.github/workflows \| wc -l` = 12; `wc -l` total 5,722 |
| apply staging runner at `apply.yml:108` (plan:39) | off by 3 | `runs-on` is `fleet:.github/workflows/apply.yml:111` |
| 3 OIDC exchanges on the standard lane (plan:86) | correct for non-contraction | exchanges at `apply.yml:818, 869, 922, 967, 1009`; 869 and 922 sit in contraction-only steps (`:849`, `:906`) |
| "`resolveApprovedPlan` checks no check runs and no branch protection; `ProtectedBranch` is asserted but not verified" (plan:222-226) | **half wrong** | Required checks: correct. `norn:githubapp/client.go:612-650` checks only that the PR is merged, a `plan.yml` push run on `DefaultBranch` succeeded at the merge SHA, and there is one artifact. Branch protection: wrong for the lane as a whole. Runner admission refuses any token whose OIDC `ref_protected` is not `true` (`norn:handler/github_actions_oidc.go:292`, `norn:handler/fleet_runner.go:727`). So `ProtectedBranch=true` is backed today by GitHub's own signed claim at attempt start. The executor lane **removes** that check. `VerifyProtectedCommit` replaces an existing control; it does not close a gap. |
| "authority reachable only over Tailscale" (plan:214-217) | true *as designed*, but misleading | Norn binds loopback and is "exposed solely through Tailscale Serve" (`fleet:docs/runbooks/management-authority-runner-state.md:57-58`). The cloud firewall also allows runner-private → authority:443 (`:16-18`, `fleet:environments/management/nyc3/main.tf:160-170`). The conclusion "GitHub cannot reach it" holds only for *hosted* runners. A **self-hosted runner on the management runner host** is on the tailnet and inside that firewall rule. The plan never considers that option (see §6, option E). |
| "existing management *runner* host from `environments/management/nyc3`" (plan:149, 153-157) | **wrong** | That root "is an offline configuration and admission scaffold. It has not created anything, … and does not provide a complete Fleet apply executor" (`management-authority-runner-state.md:9-11`). The staging readiness review says the staging root "does not provision the independent management authority, runner, state backend" and "authorizes no cloud apply" (`fleet:docs/runbooks/staging-v3-protected-rehearsal-readiness-2026-09-28.md:3-4, 15-17`). The host is a declared droplet (`main.tf:122-131`), not a running one. |
| GitHub App can do `VerifyProtectedCommit` (plan:226-233) | **not with current permissions** | The App is documented with only `Actions: write, Contents: write, Pull requests: write` + metadata (`docs/v2/operations/fleet.md:101-102`). Every installation token is down-scoped to `contents`/`pull_requests`/`actions` (`norn:githubapp/client.go:263, 372, 402, 1062`). Branch-protection detail (`GET …/branches/{b}/protection`) needs **Administration: read**. Check runs need **Checks: read**. Commit statuses need **Commit statuses: read**. None of these is held, and adding them needs the installation owner to re-approve the App. |
| "3 reads per converge" (plan:140) | understated | Each converge needs compare, protection, check-runs, statuses, `cluster.yaml` contents and the head read: about 5–6 calls, plus one head read per resource every 60 s even when idle. |
| "Human touches: 1 (merge)" (plan:140) | understated | Someone still authors the change. Either a hand-edited PR, or `norn fleet plan` + `norn fleet github pr` + merge, which is 3 touches (plan:287 keeps the `sourcePlanId` path). The artifact review and dispatch touches really do go away. |
| Norn token: "each incarnation exchanges [the managed token] for a 15-minute token" (plan:187-191) | route does not exist | The only token routes are rotate/revoke and the GitHub OIDC exchange (`norn:etcd_fleet_runtime.go:278`; `main.go:1939`). No managed-token → short-token exchange exists, and no WP builds one (E2, plan:525). |
| Migration 50 / floor 33 (plan:523, 618) | numbering right; need is unproven | The latest migration is 49 (`norn:store/fleet_resources_migration.go:50`). Migration 49 deliberately did **not** bump the floor (`:3-7`). Floor is currently 32. |
| "Option C: no Nomad on management" (plan:151) | correct | `docs/v2/operations/fleet-pilot.md:84-86`. Running Nomad only for the executor is new infrastructure. Nomad on the staging Fleet breaks the fleet-down rule. Rejection stands. |

---

## 2. Blockers

**B1. The plan executes on a host that does not exist, and no WP creates the management plane.**
Evidence is in the fact table above (`management-authority-runner-state.md:9-11`).
E11 is only the host *role* (plan:534). E13 needs "real staging + management hosts"
(plan:536). Neither WP provisions the authority, the runner and managed PG through
the separate bootstrap lane: about $170/month, plus Tailscale enrollment, host-key
gates and SSH closure (runbook :44-70, readiness :28-41). Change 6 cannot be
qualified until that lane has run once.

*Fix:* add an explicit prerequisite WP "E-1: management plane created through the
existing bootstrap lane". Without it, state that change 6 ships unqualified. Drop
the word "existing" from Option B.

**B2. The blast-radius table is wrong about GitHub. A merge to `main` gets standing credentials.**
plan:208 says a compromised GitHub org gets "nothing provider-side; at most a
merge". But the executor runs phase code **from the converged commit**
(plan:398-399, 244). That covers `scripts/` and `ansible/`, and also OpenTofu
`modules/`, where an `external` data source or `local-exec` runs at plan time.
That code runs with the DO write token, Spaces RW, the SSH key, the bootstrap
secrets and the release token in the environment. Non-destructive merges on
staging auto-apply with **no human touch**, and independent review is
`not_enforced` (plan:265). Today a merge already exposes DO and Spaces through
`plan.yml` (push to `main`, `fleet:.github/workflows/plan.yml:176-181`), but there
is still a human `norn fleet github apply` before mutation. So B gives **no
reduction** of GitHub-side exposure, and it removes the last human gate before
mutation. §3's "real reduction" (plan:485-486) does not hold. During migration,
the GitHub runner agent and the executor share a host (plan:156-157). Any
workflow that reaches local root on that host reads the executor's age key.

*Fix:*
- Make the merge gate the declared security boundary, and verify it.
  `VerifyProtectedCommit` must check required PR reviews, no admin bypass (or a
  ruleset with no bypass actors) and no force-push. This needs Administration:
  read or the rulesets API.
- Or require `approve` for any converge whose diff touches code paths
  (`scripts/`, `ansible/`, `modules/`, `cloud-init/`). Only `environments/*`
  data changes would then auto-apply.
- Correct the table either way. Never co-host the GitHub runner agent with the
  executor, or treat the co-hosted period as "GitHub = root on executor".

**B3. The stop proof is host-local and can be forged by a clone or a rebuild. Abandon loses its evidence.**
plan:292 says "systemd single instance plus a host flock; a higher incarnation
proves every attempt bound to a lower one has stopped". That holds only if:
- the registering process is on the **same kernel**;
- the unit kills its whole cgroup.

The failure cases:
- **Restored snapshot, cloned VM, or rebuilt host with the same executor ID:**
  it registers `incarnation+1` while the original host may still be running
  `tofu apply`. That is a false proof, and two mutation owners.
- **Supervisor crash with orphaned children:** this is safe only with
  `KillMode=control-group` (systemd's default) and no `setsid`/scope escape. The
  plan says "process-group kill" (plan:383) and never pins the unit semantics.
- **Partition:** the old supervisor stays alive, so no new incarnation can
  register (correct). Then abandon after 30 minutes uses "the executor registry
  snapshot" as evidence (plan:340-342). That is a DB row, not evidence that the
  partitioned host stopped calling DigitalOcean.
- **What replaced GitHub's `completed`:** the GitHub lane's terminal release and
  Q11 used a third party, GitHub, attesting `completed` (`plan.md:282, 286`;
  `operator-runbook.md:243-244`). The replacement here is **self-attestation by
  the principal being fenced**. That is acceptable inside one administrative
  domain, but the ADR must say so.

*Fix:*
- Bind executor identity to the host's `machine-id` + boot ID and record
  systemd's `INVOCATION_ID` in attempt metadata.
- A stop attestation is valid only from the same machine-id with a new boot ID
  or invocation ID, and must report the old cgroup empty (`cgroup.events
  populated 0`).
- Registering an executor ID from a **different** machine-id is an admin-signed
  operation. It needs provider evidence that the old droplet is off or destroyed
  (DO droplet status, captured by the operator).
- Abandon on the executor lane needs the same evidence.
- Name the OpenTofu Spaces lockfile (`use_lockfile=true`) as the provider-side
  backstop for concurrent `apply`, and forbid automated `force-unlock` (see M5).

---

## 3. Majors

**M1. Too many new moving parts for "very streamlined".**
New surfaces:
1. a separate service;
2. a Go supervisor (about 700 lines) with its own signed release and pin;
3. a Python entrypoint;
4. a long-poll claim protocol;
5. a dispatch `lane` plus 4 new states;
6. an executor registry;
7. an incarnation counter;
8. an executor principal;
9. a token exchange (unbuilt);
10. the converge watcher;
11. `VerifyProtectedCommit` plus new App permissions;
12. migration 50;
13. writer floor 33;
14. etcd keys;
15. two new signed ops;
16. a GitHub variable that gates `plan.yml`/`drift.yml`;
17. a host role.

§5.3's `phaseContract` skew rule (plan:571-577) exists only because the supervisor
and the phase code version separately. Several of these duplicate code that
already exists:
- `runner_attempt.py supervise` already does process-group supervision and
  heartbeats (`fleet:scripts/runner_attempt.py:152-190`);
- the token file and refresh contract already exists in Python;
- Norn already has a generation-fenced claim type
  (`norn:store/operations.go:125-137`).

*Fix:* see §6.

**M2. The claim protocol has a lost-response deadlock, and the fence can be held with no executor.**
- `TestExecutorClaimDeliversNonceOnce` (plan:525) plus qualification row (5),
  "drop the claim response" (plan:536), means a lost claim response leaves a
  `claimed` dispatch whose nonce nobody holds. The design never says how that
  resolves.
- Acquiring the fence at `prepare` (plan:289) means a stopped executor leaves the
  target held by a `prepared` row. That row has no attempt and no defined
  release.
- The nonce exists only to carry trust across the GitHub boundary
  (`fleet_runner.go:701-703`). On the executor lane the executor's own token
  already authenticates it, so the nonce is redundant.

*Fix:* drop prepare/claim/nonce on this lane. The executor calls attempt start,
and one transaction does `DecideAcquire` → `DecideBind` → insert attempt, with the
existing `Idempotency-Key` making a lost response safe. Then there is no
unclaimed-held state and nothing to deliver once.

**M3. Mode switch versus GitHub-lane acquire is a TOCTOU race. Mode has two sources of truth.**
The fence is taken at `submitting` (`norn:store/fleet_github_dispatches.go:127-175`).
A GitHub dispatch that is `prepared` does not hold it. If the route-entry
`executionMode` check (plan:561-562) runs outside that transaction, a mode switch
can commit between the check and `MarkFleetGitHubDispatchSubmittingFenced`.
Separately, `NORN_FLEET_EXECUTION_MODE_STAGING` (plan:567) is a second copy of the
mode that Norn cannot see. If it is stale, `plan.yml`/`drift.yml` take the same
state lock (`plan.yml:292`; `drift.yml:245`, `-lock-timeout=5m`) while the
executor applies. The executor's plan or apply then fails on the lock and burns
the recovery budget. Staging contraction requires flipping both copies each
time (plan:568-570), which is the opposite of streamlined.

*Fix:*
- Read `executionMode` inside the acquire and bind transactions, under the fence
  lock. In etcd, compare on the resource revision in the same txn.
- The mode-switch operation locks the fence row `FOR UPDATE`.
- Remove staging from `plan.yml`/`drift.yml` in the same norn-fleet PR, rather
  than adding a gating variable.
- The executor uses `-lock-timeout` and classifies a lock conflict as "retry
  later", not "failure".

**M4. Per-phase scoping is not a security boundary.**
The supervisor decrypts the whole file once per claim (plan:174). The age key is
`0400` **owned by the executor user** (plan:163-164). Children run as the same
user, so any phase child, and any merged `scripts/` change (B2), can read the
age key and decrypt everything.

Real blast radius of the host key:
- the DO write token;
- Spaces RW;
- the SSH key to all staging nodes;
- the bootstrap secret dir;
- the release GitHub token;
- the Norn executor token.

DO tokens are team-wide, not project-scoped. If management and staging share a
DO account (readiness doc :62-63 shows one personal `doctl` context), the staging
executor's write token can also destroy the management authority and its PG.
That violates "ordinary staging apply/recover credentials cannot change it"
(`docs/v2/operations/fleet-pilot.md:117-120`).

*Fix:*
- Children run under a separate uid that cannot read the key.
- Decryption is done by the supervisor (root-owned helper or a different uid).
- Put management in a **separate DO team** from staging.
- State plainly that per-phase scoping is hygiene against accidental leaks, not
  containment.

**M5. Automatic recovery will usually hit a stale OpenTofu lock.**
Killing `tofu apply` with the process group (plan:322-324) or a host crash leaves
the Spaces `.tflock`. Recovery "re-plan under the state lock" (plan:329) then
fails. The current workflows only use `-lock-timeout=5m` (`recover.yml:1208, 1333`).

*Fix:* record the lock ID in attempt metadata when apply starts. After a valid
stop proof (B3), recovery may `force-unlock` **only that lock ID**. Any other
lock is `Uncertain`.

**M6. The executor accepts any reachable SHA, which enables rollback.**
plan:242-244 has the executor check only "reachable from `origin/main`". A
compromised authority (plan:209) can then converge any historical commit,
including a known-bad one.

*Fix:* the executor requires `claimed == origin/main HEAD` at fetch time, **and**
that the commit is a descendant of its own locally recorded last-applied SHA.
Reverts arrive as new head commits, so this costs nothing.

**M7. Polling semantics are underspecified.**
- **Force-push.** With force-push, `compare(lastApplied, head)` returns
  `diverged`, which plan:229 does not handle. *Fix:* treat as
  `blocker=history_rewritten` and require `approve`; verify
  `allow_force_pushes=false`.
- **Check-suite race.** A head with pending checks must be re-polled, not
  skipped or recorded as failed. Only a completed failure blocks.
- **Coalescing.** Several merges coalesce correctly only if the path filter
  diffs **last-applied..head**, not last-seen..head.
- **Reverts.** Reverting a scale-up is a contraction, so the executor lane
  refuses it (plan:417-419) and the operator must switch lanes. State this.
- **Rate limits.** Use `ETag`/`If-None-Match` on the head read. A 304 is free
  against the rate limit.

**M8. WP conflicts and parallelism errors.**
- **`main.go` ownership.** plan:517-518 says `main.go` is edited only by E3 and
  E8. Several other WPs need route allowlist entries, scope checks or watcher
  startup there (`main.go:1839-1845, 1886-1900, 2206-2216`;
  `etcd_fleet_runtime.go:133-139`):
  - E6: the converge route, the execution-mode route, and starting the watcher;
  - E7: the observation principal branch;
  - E12: the approve route;
  - E2: the executor routes.
- **E7 ∥ E8 overlap.** Both touch `handler/fleet_resources.go` (the resource
  read view versus the observation identity) and `main.go`. Run them in
  sequence.
- **Missing token exchange.** No WP builds the token exchange (fact table).
- **E4 is oversized and only fake-tested.** It ports `apply.yml:256-1209`, much
  of which is pilot and external-Mac code, plus the recover standard path, as
  one WP tested only against a fake `tofu`. Equivalence with the real bash is
  first observable at E13. Calling it "fully local" (plan:550) is true of the
  tests, not of qualification. Split E4 into plan/apply versus
  reconcile/recover, and add a recorded-transcript comparison against one real
  `apply.yml` run.
- **E12 is unqualified.** E12 lands after E13 (plan:547), so the approval path
  is never staging-qualified. Add an E13 row for it.
- **E10 branch.** E10 targets the NornUI branch `fix/connection-backups`
  (plan:15-16), which is unrelated. Give it its own branch.

---

## 4. Minors

- plan:39: `apply.yml:108` should be `:111`.
- plan:140: "3 reads" should be about 6 per converge, plus a head poll per
  resource per minute.
- plan:140: "1 human touch" should read "1 merge, plus however the change is
  authored".
- plan:196: DO has no public API for minting scoped personal tokens. "Derived"
  tokens are a manual console step. Rotation also needs a delivery path for the
  new `executor.sops.yaml` to the host (a git pull of the private ops repo needs
  yet another deploy key). Name that path.
- plan:287: watcher-created `fleet.capacity-plan` ops are signed by the server,
  not an operator, which weakens ledger provenance. Record the merge actor and
  the PR number in the payload.
- plan:584-585: "the only one-way step" is not true. Re-encrypting secrets to the
  executor and moving keys out of GitHub environments are also operationally
  one-way until undone.

---

## 5. Safety invariants: assessment

| Invariant | Holds? | Condition |
|---|---|---|
| One mutation owner per target | yes at the Norn level (shared fence). Provider level only via the tofu lockfile | needs M3 (mode inside the transaction) and B3 (no clone or rebuild false proof) |
| Heartbeat expiry never releases | yes | plan:346-347 is consistent with `ExpiryDoesNotRelease` |
| A missing checkpoint never means "did not happen" | yes for provider work (always re-plans). For hooks, only if `configure` stays idempotent, which is true today | needs M5, or recovery fails rather than misinfers. That is safe but not streamlined |

---

## 6. Recommended simplification ("B-lite")

The goal is the same outcome (1 merge, platform-held keys, GitHub only triggers)
with roughly a third of the new surface.

1. **No Go supervisor.** A systemd unit runs `executor.py loop` from a **pinned
   norn-fleet release installed by Ansible**. That release is the trusted base:
   SHA verification, decryption and the claim loop. Phase code comes from the
   converged commit, as plan:398 proposes.
   - Observation is a systemd timer.
   - It reuses `runner_attempt.py supervise` and the token-file contract.
   - Then there is one release artifact, and no `phaseContract` skew rule.
2. **No claim protocol, dispatch lane, nonce, registry, incarnation counter or
   migration 50.**
   - The executor polls the existing `GET /fleet/resources`.
   - For a resource that is verified, in controller mode and not yet applied, it
     calls the existing attempt-start route. A new executor binding branch
     acquires and binds the fence and inserts the attempt in one transaction
     (PG and etcd).
   - Executor identity, machine-id, invocation ID and the tofu lock ID go in the
     existing `fleet_runner_attempts.metadata` JSONB and the
     `runner_attempt_id`/`workflow_url` strings (`store/fleet_runner_attempts.go:96-107`).
   - `executionMode` goes in the existing `fleet_resources.document` JSONB
     (`fleet_resources_migration.go:9-21`).
   - No column means no floor bump. Writer floor stays 32. A v32 writer meeting an
     executor attempt fails closed, because its GitHub observer cannot parse
     `norn-executor://`.
   - E0 must name any fact that truly has no home before migration 50 is
     allowed.
3. **Trigger: poll with the permissions the App already has.**
   - Branch `protected` comes from `GET /branches/main` (contents/metadata read).
   - Green checks come from a `validate.yml` push run at that SHA with
     `conclusion=success`. Use the Actions API, the same pattern as
     `resolveApprovedPlan` (`client.go:625-640`).
   - Add Administration: read (or the rulesets API) **only if** the human wants
     B2's review enforcement verified. That is the one permission change worth
     making.
4. **Stop proof as in B3.** Same machine-id, new boot or invocation ID, empty
   cgroup. A different host needs admin-signed provider evidence.
5. **Option E belongs in the decision table.** E is a push-triggered workflow on
   the self-hosted management runner, with keys held runner-local in SOPS, and
   the runner asking Norn for the fence at attempt start. It needs:
   - no new service, no new principal and no new stop proof;
   - GitHub `completed` stays as third-party evidence;
   - existing OIDC, `ref_protected` and observers.

   The cost: the GitHub runner agent co-resides with standing keys. Mitigations
   are a runner group limited to the `main` workflow, an environment branch
   policy and B2's review enforcement. E is the lowest-code way to meet the
   literal requirement. The human should choose between B-lite and E with both
   costed.

---

## 7. Open questions: recommended answers

1. **Placement.** B-lite on the management runner host, after E-1 creates that
   host. If the human cares more about code size than about co-resident keys,
   choose E. B′ gains nothing, because neither host exists yet.
2. **Trigger.** Poll every 60 s with ETag. Verify through Actions (validate run
   success) and the branch `protected` flag, which needs no new App permissions.
   Add Administration: read only for review enforcement (B2). No push workflow.
3. **Production auto-apply.** Moot for 5–7, because production stays on the
   GitHub lane (plan:420). When it moves, every production plan needs manual
   `approve`.
4. **Recovery budget.** 1, but only after M5 (lock-ID-scoped unlock) and B3. Until
   then, 0, and the operator resumes.
5. **Writer floor 33.** Reject. Design out migration 50 (§6.2) and keep floor 32.
   Revisit only if E0 proves a column is unavoidable.
6. **Credential store.** Accept SOPS/age with no broker, on these conditions:
   - children run under a different uid from the key owner (M4);
   - management goes in a separate DO team;
   - rotation names a delivery path;
   - the plan states that a host compromise yields every staging credential.

---

## Revision 2 check (2026-10-04)

**Verdict: ready-with-nits.** One fix is required before E0, and it is one line.
Every B, M and fact finding is resolved in the plan body (§2.1–§2.10, §3, §4),
not only in §9. D1–D5 are honoured.

**Rebuttals, assessed against the code.**
- **M3: accepted.** T7 is stated word for word at `plan.md:202`. Both lanes
  take the same fence row (`DecideAcquire`/`DecideBind`,
  `fleet/lifecycle/fence.go:157-200`), so the fence alone gives exclusion. A
  stale mode only leads to a refused acquire. The second copy of the mode is
  gone.
- **M5: accepted.** No route writes attempt metadata after start. Grant IDs
  ride on checkpoints, but the apply checkpoint lands after apply, which is too
  late. Who, Operation, a `Created` window and a valid attestation together
  identify the source attempt's lock, because the fence excludes any other
  apply.
- **Scope cut: accepted.** It removes E12 and keeps destructive approval on
  a lane that is already qualified. State the cost in §2.10: a reverted
  scale-up needs the full GitHub flow (`norn fleet plan`, then PR, merge,
  artifact and dispatch). Staging also keeps its GitHub runner, its
  environment secrets and `plan.yml`.

**Must fix.**
- **R1. The uid split leaks through ambient capabilities (D3, B3).**
  - §2.3 gives `norn-exec` ambient `CAP_SETUID`/`CAP_SETGID`. The unit sets
    `NoNewPrivileges=no`.
  - A setuid from one nonzero uid to another does not clear the ambient set
    (capabilities(7)). The ambient set also survives `execve`. So `norn-phase`
    children inherit `CAP_SETUID`, can `setuid` back to `norn-exec`, and can
    read the age key.
  - That also hides them from the "no `norn-phase` process" check in the stop
    proof.
  - *Fix:* call `setpriv … --inh-caps=-all --ambient-caps=-all
    --bounding-set=-all`. Add the test `PhaseHasNoCapabilities` to P1.

**Nits.**
- **N1.** M1 in §9 says "5 new routes" but lists 6: converge, grants,
  stop-attestation, host-retired, approve and mode.
- **N2.** `norn fleet converge --resume` does not say how the resume intent
  reaches the loop. Is it a document flag, or something the converge op
  records? With a budget of 0 this is the main recovery path.
- **N3.** §2.8 invokes `executor.py observe`, but P1 owns `loop.py`. No WP
  owns the entrypoint.

**Streamlining.** Revision 1 versus revision 2:
- **Revision 1:** about 17 parts and about 14 WPs (E0–E13).
- **Revision 2:**
  - 1 loop and 1 watcher;
  - 1 scope and 1 capability;
  - 6 routes;
  - 5 new operation kinds, plus a `converge` action on `fleet.capacity-plan`;
  - 2 uids and 1 timer;
  - about 25 new files;
  - **20 WPs.**

Revision 2 has fewer mechanisms but more, smaller WPs. Three cuts are safe:

- **C1. Drop `fleet.executor.host-retired`.**
  - Add an admin-only `providerEvidence` field to the existing admin-signed
    `fleet.target.fence-release` and `fleet.target.abandon-plan`
    (`handler/fleet_targets.go:271, 322`).
  - Accept a stop attestation as evidence there too.
  - This removes 1 operation kind, 1 route and 1 CLI command, and B3 still
    holds.
- **C2. Defer observation: P4, E7, the observe grant and the timer.**
  - The `observe` intent is unconfigured today, so this loses nothing.
  - Keep `drift.yml` for staging.
  - This removes 2 WPs and changes no invariant.
- **C3. Fold E4c into E2 and E2e.** It is the same holder-and-lastApplied
  derivation over the same store files.

Keep these operation kinds:
- `fleet.converge.approve` (D2);
- `fleet.credential-grant` (D3);
- `fleet.executor.stop-attestation` (B3);
- `fleet.resource.execution-mode` (admin-only; the existing `SetDesired`
  route needs only `ScopeAPIWrite`, `handler/fleet_resources.go:631`).

**Parallelism.** No two WPs in the same step share a file. There are
ownership gaps and backwards dependencies:
- **P1.** The etcd holder-facts synthesis is in `etcdstore/v3_fleet_fence.go:174-190`.
  It sets `DispatchState` only from preparation or binding keys, and no WP owns
  that file. Executor plans would then not report `bound` on etcd. Assign the
  file to E2e.
- **P2.** On etcd, `fleet.capacity-plan` is recorded in
  `etcd_fleet_runtime.go:480-530`, which only E6 may edit. E4a therefore cannot
  build etcd converge plans. Either move a shared plan builder into E4a, or
  give that work to E6.
- **P3.** E2 (step 3) tests things built in later steps:
  - `RecoveryRequiresValidAttestation` needs E5 (step 4);
  - `ApprovalRequiredForExecutable` needs E4b (step 5).

  Either E0 pins both payload shapes and E2 reads the ledger directly, or
  those tests move.
- **P4.** P4 runs alongside P1 but needs P1's `grants.py` and
  `credentials.py`. P3 probably reuses functions from P2's `converge.py`, and
  the two also run in parallel.

**Local testability: mostly realistic. Name the gaps.**
- The host is darwin, so `PhaseRunsAsOtherUid`, the cgroup and `setpriv`
  tests run only inside the Q1 Linux VM (Lima or UTM, not a container). A
  container cannot change `boot_id` or represent `KillMode` faithfully.
- The droplet ID and the metadata service must be faked.
- Stale-lock provenance (Who, Created, Operation) is tested only against fake
  `tofu`. Add one Q1 row that runs real `tofu` against a local S3 backend
  (MinIO) with `use_lockfile=true`.
- P2's equivalence with the GitHub lane is static, as §6 already concedes.
