# Q1 qualification: Fleet controller executor lane, local end to end (2026-10-05)

Status: **not qualified.** Of the twelve Q1 rows, nine pass as run (row 12's
recovery tail needed a harness accommodation that is itself a finding) and three
fail on product defects: rows 3, 5 and 10. Seven product defects (bugs 1 to 7)
block the lane, and there are a logging gap and VM-script defects (bugs 8 and 9).
Nothing in the product code was patched.

**Update:** the defects were fixed and this qualification was re-run the same day; see the last section,
"Re-run after fixes (2026-10-05)". The result is not qualified-as-is above, qualified in effect below.

| | |
|---|---|
| Norn | `/Users/arti/Desktop/Claude/norn-v3-fleet-executor`, branch `feature/v3-fleet-executor` at `6714a309` (uncommitted additions listed in section 8) |
| norn-fleet | `/Users/arti/Desktop/Claude/norn-fleet-controller-executor`, branch `feature/fleet-controller-executor` at `56ff5f6` (unchanged) |
| Harness | `v2/scripts/test-fleet-executor-local` (new), `v2/api/fleet_executor_local_authority_test.go` (new, build tag `fleetlocal`) |
| Date and host | 2026-10-05, an Apple-silicon Mac, Lima 2.2.1 (`vz`), VM `norn-exec` (Ubuntu 24.04.5, kernel 6.8, aarch64, 4 CPU, 4 GiB) |
| Tools in the VM | OpenTofu 1.10.6 (`use_lockfile=true`), sops 3.9.4, age 1.2.1, MinIO built from source (see 3.4), Python 3.12.3 |

Everything is local and fake. No real DigitalOcean, GitHub or cloud credential
was used: DO and Spaces credentials in the SOPS file are fixtures, the GitHub
App key is a throwaway RSA key, and the "state backend" is MinIO in the VM.

## 1. Result per row

PASS means the row's claim held as run. "W1" is the harness accommodation
described in 3.5 (`TF_CLI_ARGS=-no-color` in the phase environment), needed
because of product bug 5.

| Row | Claim | Result | One line |
|---|---|---|---|
| 1 | merge, then automatic converge (config-only) | **PASS** | Watcher recorded the plan within its 60 s poll with `requiresApproval=false`; the loop converged with no human action; one attempt; `ingress-03` is in the MinIO state. |
| 2 | executable diff blocks on approval, `norn fleet approve`, converges | **PASS** | Plan needed an approve; after more than one poll no attempt existed and the journal said `wait: the converge plan needs an approve`; after the approve one attempt succeeded. |
| 3 | kill a phase mid-apply, stop attestation, `--resume`, recovers | **FAIL as shipped; PASS with W1** | Attestation was correct (uid empty, cgroup empty, pgid reaped). Recovery then refused the loop's own stale lock as `state_lock_foreign` with empty fields (bug 5). With W1 the lock ID went into the attestation first, was unlocked, and the recovery attempt succeeded. |
| 4 | `systemctl restart` mid-configure | **PASS** | Same boot, new invocation, no `norn-phase` process survived, attestation posted, `--resume` finished from `nodes_configured` (checkpoints show the phase split across the two attempts). |
| 5 | VM power-off mid-apply, boot, attestation with new `boot_id`, resume | **FAIL** (2 of 2 runs) | The attestation with the new `boot_id` and the resume work. But the hard power-off left empty loose objects in the executor's git clone and the recovery wedged on `git fetch failed` until the clone was removed by hand (bug 6). MinIO also lost the stale lock, so "owned lock after power-off" was not exercised. |
| 6 | lost attempt-start response replays | **PASS** | 6a: first response dropped, transport retry reused the key, exactly one attempt. 6b: all five responses dropped, loop exited, the journal replay (6th request, same key) returned the stored attempt, attested, resumed. |
| 7 | authority restart mid-run | **PASS** | Authority stopped for 22 s while an apply was gated; `supervise` heartbeats hit `Connection refused`, retried, run finished with one attempt and no recovery. |
| 8 | GitHub-lane dispatch racing the executor on one fence | **PASS** | 8a: executor holds the fence, GitHub dispatch got 409 `fleet_target_execution_occupied`, nothing was sent to the fake GitHub. 8c: simultaneous start, exactly one winner, the other got the same 409; the real loop then logged `busy`. |
| 9 | force-push gives `history_rewritten` | **PASS** | Rescan and status show `history_rewritten`, no plan recorded, nothing applied; also when the old commit is pruned (compare 404); cleared by reverting main. |
| 10 | stale `.tflock` from a killed real `tofu apply`: owned unlocked, foreign refused; kill during the recovery's own `tofu plan` | **FAIL** (see 5) | Owned lock: unlocked only with W1 (bug 5). Kill of the recovery's own `tofu plan` left an `OperationTypePlan` lock and the next recovery refused it locally, but the refusal could not be reported and the recovery could not continue because the loop picked the wrong source attempt (bug 7). The foreign-lock variant with one recovery passes (row 10b). |
| 10b | added: foreign lock refused, reported, cleared, single recovery completes | **PASS** (W1) | Lock held by `root@host`, `OperationTypePlan`: not unlocked, `fleet.converge.refusal` recorded, `state_lock_foreign` blocker shown, `tofu force-unlock -force <other id>` refused by tofu on MinIO and the lock stayed, operator cleared it, recovery completed. |
| 11 | token self-rotation while idle | **PASS** | With the recorded expiry set to +2 h the next idle tick rotated: same device, new 7-day token, old token 401, scope unchanged, file 0600, loop keeps polling. |
| 12 | cloned VM disk (new droplet ID) cannot attest | **PASS** (core claim; recovery tail needed W1) | With the metadata service answering 9999 the loop logged `belongs to another droplet`, recorded no attestation, a forged attestation for 9999 got 409 `fleet_executor_stop_attestation_invalid`, `norn fleet converge --resume` refused, no recovery started. Original droplet then attested and recovered. |

Other required checks:

| Check | Result |
|---|---|
| VM-only unit tests (`PhaseHasNoCapabilities`, `PhaseHasNoCapabilitiesFromTheLoopUser`, `PhaseCannotReadAgeKey`, `AttestationRequiresEmptyPhaseUidRealCgroup`) | **PASS**, 4 of 4, run by name in the VM as root with `NORN_EXEC_VM=1`. The default `norn-exec-vm test` is **unsafe** and did not complete (bug 8). |
| etcd runtime: executor lane gated off | **PASS**. The real `norn-api` on etcd: capabilities lack `fleet-executor-v1`; eight lane-only routes (converge rescan, credential-grants, converge-refusals, converge/approve, provider-evidence, stop-attestations, enrollments, enrollments/approve) answer 501 `backend_route_unsupported`. |
| Real Ansible role | Applied in full (44 ok, 20 changed, 2 skipped). See 3.3. |

## 2. Product findings

Ordered by how much they block. Each has file and line, evidence, and what
the harness did about it. Evidence files are in the work directory
(`$Q1_WORK/evidence/`, section 8) and the relevant lines are quoted here.

### Bug 1. The recovery lock probe cannot run with a plugin-backed provider (P3)

- `scripts/executor/recover.py:142` runs the probe as `child(st, "lock-probe", ...)`.
  `scripts/executor/converge.py:77-79` makes each child's `TMPDIR` equal to
  `$NORN_PHASE_DIR/tmp`, and `converge.py:217` sets `NORN_PHASE_DIR` to
  `<work_root>/scratch-<40 hex commit>/ph/<label>`.
- OpenTofu starts each provider as a go-plugin child that binds a unix socket
  `plugin<digits>` in `$TMPDIR`. `sun_path` holds 107 bytes. With the role's
  `work_root` `/var/lib/norn-fleet-phase` the path is 109 bytes for
  `lock-probe` (103 to 105 for `plan`, `init`, `apply`, `sanity`, which is why
  the first converge works).
- Evidence: with `hashicorp/external` the probe failed with `failed to instantiate
  provider ... Unrecognized remote plugin message` and the handler returned
  `Result(status='failed', terminal=True, detail='the state lock probe failed
  without reporting a lock')`. Any provider, including the DigitalOcean one,
  uses the same mechanism, so this should hit every real recovery. Table in
  `evidence/bug1-lock-probe-plugin-socket.txt`.
- The loop never logs a failed result (bug 9), so the journal showed only
  `valid fleet:` and the init output once a minute while recovery was stuck.
- Harness: the OpenTofu fixture uses no provider plugin (`terraform_data` only),
  so the probe runs. Real DigitalOcean was not reproduced; the path arithmetic
  and the plugin failure were.

### Bug 2. A converge plan refused for revalidation is never re-recorded; the lane wedges (E6 wiring)

- `v2/api/fleet/lifecycle/fence.go:166` (`DecideAcquire`) refuses a plan whose
  `StartedAt` is before `LastRelease.At + 5 min` with
  `fleet_plan_revalidation_required`. That compares two fixed instants, so the
  refusal never goes away. `v2/api/store/fleet_executor_attempts.go:385` applies it
  to executor starts.
- The loop reacts by calling the rescan (`loop.py:225-229`). The watcher records a new
  plan only if the newest plan is not `Admissible`
  (`v2/api/fleet/controller/watcher.go:339`), and the PG wiring makes `Admissible`
  false only for **abandoned** plans: `v2/api/main.go:2304-2307`. A
  revalidation-refused plan stays admissible, so every rescan answers `exists`.
- Reproduction (environment E1, see section 4): release at 10:17:54, the next merge produced
  plan `58a2a249` at 10:20:30. The executor got
  `409 fleet_plan_revalidation_required` at 10:21:57, 10:22:58, 10:24:00, 10:25:01
  and on every later tick, and `norn fleet converge staging` printed
  `staging: exists tip eb8bd82f75a5 plan 58a2a249-...`. Past 10:22:54 (release plus
  5 min) it was still refused. Evidence: `evidence/proxy-before-reset-1.log`
  (`body=... fleet_plan_revalidation_required`) and
  `evidence/attempts-before-reset-1.txt`.
- Impact: any merge recorded within 5 minutes after a release, or recorded while
  an apply is still running (so its `StartedAt` precedes the release), can never
  start. The next merge does re-plan, which hides the problem on a busy repo and
  strands a quiet one.
- Harness: every row waits until `last_release_at + 5 min` before pushing a commit
  (`wait_revalidation_window`). This made the whole run slower (about 5 minutes per
  converge) but hid nothing else.

### Bug 3. Recovery cannot resume an attempt that stopped at phase `complete` (P3)

- `scripts/executor/recover.py:69` defines `RESUME_PHASES = ("infrastructure_applied", *POST_APPLY)`,
  and `recover.py:313` fails any other start phase with `cannot resume at '<phase>'`.
  `reconcile.py` handles `complete` (it records the final evidence and advances),
  so a stop between the advance out of `readiness_verified` and the final advance
  leaves an attempt that the executor can never finish.
- Evidence (environment E1, plan `0dddb5e4`): journal
  `executor failed: recover left attempt 9db78460-... live (cannot resume at 'complete': only the PG legacy no-drain lifecycle is wired (plan-5-7 hand-offs))`, and
  `fleet_runner_attempts` shows both attempts at `complete`, `abandoned`
  (`evidence/attempts-before-reset-1.txt`). The way out is an admin abandon after
  the 30 minute age gate, which was not waited for: the environment was reset.
- How it happened here: the phase-side `NornClient` (`scripts/reconcile.py:237`)
  retries only `URLError`, so one 30 s read timeout (caused by a network stall in
  the harness, see 3.6) ended `reconcile-readiness_verified` with exit 1 while the
  attempt was already at `complete`. This is an ordinary transient fault, not an
  exotic one.

### Bug 4. The executor venv has no YAML parser, so `sanity.py` fails on a role-provisioned host (P5)

- `scripts/sanity.py:13-30` (`load_yaml`) uses `yaml` or falls back to `ruby`. The
  unit puts `/opt/norn-fleet-executor/venv/bin` first on `PATH`
  (`ansible/roles/fleet_executor/templates/norn-fleet-executor.service.j2:15`), the
  phases inherit it, `python3` is the venv interpreter, the wheelhouse
  (`requirements-pilot.txt`) has no PyYAML, the role installs no `ruby`.
- Evidence, in the VM with the unit's `PATH` and no `ruby`:
  `YAML parser unavailable: [Errno 2] No such file or directory: 'ruby'`, exit 2;
  `venv/bin/python3 -c "import yaml"` is `ModuleNotFoundError`. `reconcile.py`
  uses the same loader.
- Harness: `apt-get install ruby` in the VM before the first converge, so every
  converge in this run had a working fallback. The failure above was reproduced
  afterwards, on the provisioned VM, by running `sanity.py` with the unit's `PATH`
  and with `ruby` hidden. The Ubuntu system python has `yaml` (6.0.1), so either
  `--system-site-packages` or a pinned PyYAML wheel would fix it.

### Bug 5. A real stale lock reads as foreign: tofu's lock error is coloured and the parser does not allow for it (P3)

- `scripts/executor/recover.py:76-80` runs the probe without `-no-color`, and
  `recover.py:105-107` parses `^\W*ID:` with `re.MULTILINE`. OpenTofu 1.10.6 writes
  `ESC[31m|ESC[0m ESC[0m  ID: ...` to a non-tty stderr, and `\W*` cannot cross
  `31m`, so no field matches.
- Evidence (row 3, as shipped): lock `{"ID":"71eb7d7d-...","Operation":"OperationTypeApply","Who":"norn-phase@lima-norn-exec","Created":"2026-10-05T16:10:39Z"}`,
  inside `[startedAt, attestation]`, owned in every respect; journal
  `state lock ? is not the source's: refusing to unlock`; status
  `blocker execution/state_lock_foreign: ... Clear the lock`. The raw bytes are in
  `evidence/bug5-owned-lock-parsed-as-foreign.txt`.
- Impact: M5's whole purpose (unlock the executor's own lock after attestation) never
  succeeds on a real tofu. Every apply-kill recovery needs an operator.
- Harness accommodation W1: `TF_CLI_ARGS=-no-color` is added to the `spaces-rw`
  and `spaces-read` entries of the SOPS file, so it reaches every tofu phase
  through the credential path. The role's `check_secrets.py` rejects the extra key,
  so the harness copies that file in after the role ran. `NO_COLOR` does not work
  on tofu 1.10.6. Everything marked W1 in this document used it.

### Bug 6. Power loss corrupts the executor's repository clone and the loop never repairs it (D6, P1/P2)

- `scripts/executor/repo.py:60-90` (`fetch_repo`, `ensure_commit`) assumes the clone is
  sound: any git error is `GitError`, logged as `git fetch failed`, and retried
  forever.
- Evidence, 2 of 2 runs: after `limactl stop --force` and boot, `find
  .../repo/.git/objects -type f -size 0` found 6 empty loose objects including the
  plan's commit; the journal repeated
  `git fetch failed: error: object file .git/objects/2a/4d83511e... is empty`
  every minute; the attestation and resume had worked. The recovery completed only
  after `rm -rf /var/lib/norn-fleet-executor/repo` (the harness `reclone`
  command), when the loop cloned again.
- D6 says the host can be powered off at any moment without losing anything. A
  corrupt clone breaks that: the recovery needs the recorded commit.
- Caveat: Lima's `--force` stop discards more host-side cache than a droplet power
  cut might, but a real power cut also loses unflushed loose objects (git does not
  fsync them by default).
- Also in this row: the stale `.tflock` written seconds before the power-off was not
  in MinIO after boot, so MinIO lost the object. The M5 lock logic after a power cut
  was not exercised.

### Bug 7. `status.active.attemptIds` is sorted by ID, the executor treats the last entry as the newest (E4b/E6 with P1/P3)

- `v2/api/fleet/controller/derive.go:446` does `sort.Strings(ids)`.
  `scripts/executor/loop.py:128-130` uses `active["attemptIds"][-1]` as the
  recovery source and `scripts/executor/recover.py:226` checks
  `ids[-1:] == [source_attempt_id]`. With two attempts of one plan the "last" id is
  the newest only half the time.
- Evidence (row 10): attempts `#1 8febeb9e` (canceled) and `#2 0973f018`
  (recovery 1). `status.active.attemptIds = ["0973f018", "8febeb9e"]`. The second
  recovery used `8febeb9e` as its source (proxy log `GET .../attempts/8febeb9e...`)
  and its `state_lock_foreign` report failed on every tick with
  `409 fleet_converge_refusal_rejected: sourceAttemptId is not the plan's latest
  recoverable executor-lane attempt` (`v2/api/store/fleet_executor_ops.go:186`). After the operator cleared the lock the loop did not recover
  (the 30 minute back-off record from the failed report also blocks the re-probe).
  `evidence/bug7-attempts.txt`.
- Impact: a plan that needs two recoveries (the P3 hand-off scenario: kill during
  the recovery's own plan) is stuck half the time, and the blocker text never reaches
  the operator.
- Harness: row 10b uses a single recovery so it does not depend on the order.

### Bug 8. VM script defects (`v2/scripts/norn-exec-vm`)

- `provision` downloads MinIO from `https://dl.min.io/server/minio/release/linux-${arch}/minio`
  (lines 133-134). It answers **410 Gone**, so `provision` fails after tofu, sops
  and age. The harness cross-builds MinIO from source (`go install
  github.com/minio/minio@latest`, `GOOS=linux GOARCH=arm64`) and drops the binary in
  place; the script then proceeds. The build version reads `DEVELOPMENT.GOGET`.
- `test` (line 209) with its default modules **kills every process in the VM**.
  `test_executor_credentials.NoSecretInArgv.test_interrupted_phase_group_is_killed`
  uses a fake `setpriv` that runs the child as the current uid (root) while
  `kill_phase_processes` (`scripts/executor/credentials.py:87`) sees that
  `norn-phase` exists, picks `target = -1` and signals `kill(-1, SIGKILL)` as root.
  The run printed three results, then died inside that test with exit 255 (ssh lost). The VM survived because
  systemd restarts everything (uptime and boot id unchanged, user services restarted).
  It happens only after `provision` has created `norn-phase`; a fix is to run those tests with a
  user that does not exist or to stub `pwd`.
- The script is committed with mode 100644 (not executable).

### Bug 9. Failed handler results are not logged

`scripts/executor/loop.py:~233-245`: a `Result` with `status="failed"` and no
attempt is `terminal`, the intent is finished, `run_work` returns `"wait"` and
nothing is logged. This hid bugs 1, 2 and 5 behind a quiet journal. Recovery
`waiting` results (`recover.py` `Result("waiting", detail=...)`) are equally silent.

## 3. Setup, what ran, and the accommodations

### 3.1 Topology

| Where | What |
|---|---|
| Mac | The Norn authority on its own PostgreSQL database (`norn_q1_<timestamp>` on 127.0.0.1:55432, dropped at the end; `norn_fc` and `norn_fc_recovery` untouched). A fake GitHub (REST over http and https plus git smart-HTTP via `git http-backend`, bare repo `antiartificial/norn-fleet`). A logging fault proxy between the VM and Norn. The etcd runtime for the gate check (127.0.0.1:23790, own prefix). |
| VM | The real systemd unit from the Ansible role, `executor.py loop` under `norn-exec`, phases under `norn-phase`; real `tofu` against MinIO; the fake DO metadata service on 169.254.169.254. `github.com` and `norn.test` resolve to loopback aliases with VM-local TLS terminators (`socat`) that forward to the Mac, so `executor_norn_url` is `https://norn.test` and `executor_repo_url` is `https://github.com/antiartificial/norn-fleet.git`, exactly as the role requires. The local CA is installed in the VM trust store. |
| Fixture | `norn-fleet` `scripts/` (so the converged tree's `plan_policy.py`, `sanity.py`, `ssh_key_preflight.py`, `validate_release_contract.py` are the real ones), the real `environments/staging/nyc3/cluster.yaml`, a provider-free OpenTofu root (nodes are `terraform_data` resources derived from `cluster.yaml`, outputs `nodes` and `ingress_ip`), and reconcile hooks that are local scripts (`q1-hook`). `ssh_key_preflight` and `validate_release_contract` run for real against a generated key and a phase-owned secret tree. |

### 3.2 The authority is a test binary, not the production entrypoint

`validateFleetAuthorityOnly` (`v2/api/main.go:1432`) refuses this topology:
it needs `sslmode=verify-full` on the PostgreSQL DSN (the local server has no
TLS) and, via `handler.fleetGitHubConfig` (`handler.go:214`), a Fleet GitHub client
that in authority-only mode must be `https://api.github.com`. The harness
(`v2/api/fleet_executor_local_authority_test.go`, `-tags fleetlocal`,
`Q1_AUTHORITY=1`) loads the same configuration, logs what production validation
says (`Fleet authority-only mode requires PostgreSQL sslmode=verify-full`), and
then calls `serveFleetAuthorityOnly(cfg, db)`: the real router, reconciler,
converge watcher and executor lane. To keep the production URL it patches
`http.DefaultTransport` in the test process so `api.github.com:443` dials the fake
GitHub's TLS port and trusts the harness CA. Nothing else is bypassed. This is the
only process that is not the shipped binary; the etcd gate row runs the real
`norn-api`.

### 3.3 The real Ansible role

`ansible/playbooks/management-executor.yml` was applied to the VM over ssh with
`ansible-playbook` (core 2.21.3): 44 ok, 20 changed, 0 failed, 2 skipped. The
skipped tasks are the optional private-management token install
(`executor_private_management_token_src` unset) and the "Report an enrolled host"
message (the host was not enrolled yet). Later `--tags secrets` runs redelivered the
SOPS file (6 ok, 1 changed).

What overlapped: `norn-exec-vm provision` ran first (it creates the same users,
directories, tmpfiles rule, tools, venv and wheelhouse from the same hashed
requirements), so many role tasks reported `ok` instead of `changed`, and the role
replaced the script's `/usr/local/bin/{tofu,sops,age}` with its checksum-verified
symlinks. The role's release archive, wheelhouse archive (taken from the script's
wheelhouse), tool checksums and tree hash were computed for this run.
Nothing from the role was skipped by choice. What the role does not install and the
harness added: the CA and TLS terminators, `ruby` (bug 4), the `q1-hook` gate script,
and the phase-owned secret tree that `validate_release_contract.py` reads.

### 3.4 MinIO

Built from source (bug 8), version `DEVELOPMENT.GOGET`, go1.26.1 linux/arm64,
`use_lockfile=true` conditional writes worked (412 `PreconditionFailed` on a held
lock). A lock object created seconds before a hard stop did not survive it.

### 3.5 Accommodations, all tied to findings

| | Accommodation | Finding |
|---|---|---|
| W1 | `TF_CLI_ARGS=-no-color` delivered through the SOPS `spaces-*` entries | bug 5 |
| | Wait until `last_release_at + 5 min` before each push | bug 2 |
| | `ruby` installed in the VM | bug 4 |
| | Provider-free OpenTofu fixture | bug 1 |
| | MinIO cross-built | bug 8 |
| | `reclone` after a power-off | bug 6 |
| | Test accelerators: removing `lock-foreign/*.json` to skip the 30 min re-probe back-off after the operator cleared a lock; setting the recorded token expiry to +2 h for row 11 | none |
| | A 5-attempt `connect-timeout=2,retry` on the TLS terminators | harness network, 3.6 |

### 3.6 A harness network fault that is worth knowing

Lima's user-mode network dropped a SYN once: a phase request through the TLS
terminator waited 68 s (1+2+4+8+16+32 s of SYN retries) and the phase's 30 s read
timed out, which is how bug 3 was reached. `connect-timeout=2,retry=10,interval=0.2`
on the `socat` units fixed it and it did not recur. The same stall in production would
be an ordinary transient fault, which is why bug 3 and the missing retry in
`scripts/reconcile.py:237` matter.

## 4. Row notes

Row-by-row raw evidence (generated from the harness's result files) is in
Appendix A. Notes that are not obvious from it:

- **Four environments.** The environment was reset three times without recreating
  the VM or reapplying the role (new PostgreSQL database, new fixture repository,
  empty MinIO state, executor re-enrolled). E0 (09:55 to 10:16) produced the evidence
  for bug 1 (the first recovery) and a fixture mistake (a macOS `._` entry in the
  catalog archive, fixed). E1 (10:16 to 10:43) produced the evidence for
  bugs 2 and 3 and an aborted first run of row 2. E2 (10:43 to 12:34) ran rows 1, 2,
  3, 4, 6, 7, 12, 10 and the first run of row 5. After bug 7 wedged a plan the
  environment was reset again; E3 (12:34 on) ran the second run of row 5, then rows
  10b, 9, 11 and 8. Each block in the appendix carries its start time.
- **Row 1** was run twice in E2: the first run's only failed check was a harness
  expectation about the MinIO state (the change was a no-op for OpenTofu), the second
  run, with a real node change, passed.
- **Row 2** was first run in E1, where the harness network fault of 3.6 hit the
  converge (the approval-blocking part passed); the PASS and the appendix evidence are
  from the rerun in E2.
- **Row 3's as-shipped failure** is not in the harness result file because the
  accommodation was applied while the row was waiting; the as-shipped evidence is in
  bug 5 and `evidence/bug5-owned-lock-parsed-as-foreign.txt`. The same applies to
  rows 5, 10, 10b and 12, where W1 was in place for the whole run.
- **Row 5 was run twice** (E2 at 11:45, E3 at 12:36), both failing the same way.
  Run 1 is also in the appendix.
- **Row 6b** shows 6 requests with one `Idempotency-Key`: five dropped responses
  and then the replay from `intent.json` by the next invocation.
- **Row 7** covered one outage of 22 s. It was not repeated across a phase boundary
  and the executor's own calls (`NornAPI`) were not hit during it (only the phase
  heartbeat was), so the loop-side retry path is untested by this row.
- **Row 8**: the executor-lane contender in 8c was this harness calling the real
  start route with the executor's real token while the real loop was stopped, not the
  loop itself; the GitHub-lane contender used the real dispatch route against the fake
  GitHub (merged-PR lookup, green plan run, artifact zip, workflow dispatch, run
  lookup, `/app`). The fence stays held by the GitHub plan after the row (it cannot
  be abandoned for 30 minutes).
- **Row 9** pruned the bare repository so GitHub's compare answers 404; the fake
  GitHub implements `compare` from the git history.
- **Row 11** does not show rotation being skipped while a phase runs; the unit
  tests cover that.
- **Row 12** used `norn-exec-vm droplet-id 9999` inside the loop's 30 s restart
  window; the forged attestation used the executor's real token from the VM.

## 5. Row 10 in detail

| Step | Observed |
|---|---|
| Gated apply killed with `kill -9` | Lock 1 `OperationTypeApply`, `Who norn-phase@lima-norn-exec`, left in MinIO. |
| Loop restarts, attests | Correct. |
| Resume #1 | Probe finds lock 1, owned; attestation with `tofuLock.id == lock 1`; **only with W1** (bug 5). `force-unlock -force <lock 1>` ran after Norn accepted the attestation. |
| Harness kills the recovery's own `tofu plan` (watcher in the VM, 1.5 s into the plan) | Lock 2 `OperationTypePlan`, `Who norn-phase@...` left. The recovery attempt dies, loop exits, restarts, attests. |
| Resume #2 | Probe finds lock 2: `state lock 03567366-... is not the source's: refusing to unlock` (correct, the P3 note). Report to Norn fails (bug 7). |
| `tofu force-unlock -force 00000000-...` on MinIO | `Failed to unlock state: lock id "0000..." from s3 does not match existing lock`; lock 2 stayed (the P3 requirement). |
| Operator `force-unlock` of lock 2 | Unlocked. The executor did not recover (bug 7, plus the back-off record). |

Row 10b repeats the foreign-lock half with one recovery and completes (PASS).

## 6. Not run / limits

- Nothing from the Q1 list was left NOT RUN. Row 5's M5-after-power-off part was
  not exercised because MinIO lost the lock (3.4).
- Row 10's full P3 scenario ran once. Because bug 7 depends on a random UUID order
  it fails about half the time; a clean pass was not obtained.
- Real DigitalOcean provider behaviour (bug 1) was reproduced with a different
  go-plugin provider, not DigitalOcean's.
- The loop's `Restart=always`, `StartLimitIntervalSec=0` and 30 s `RestartSec` were
  exercised many times (every PhaseDied exit); `RestartPreventExitStatus=3`
  (TokenLost) was not.
- The `observe` intent, private management and the release-host paths are out of
  scope for Q1.

## 7. Reproducing

```sh
# VM: v2/scripts/norn-exec-vm up   (MinIO needs the source build, bug 8)
python3 v2/scripts/test-fleet-executor-local all          # everything, row 8 last
python3 v2/scripts/test-fleet-executor-local setup        # then: vm, bootstrap, baseline, rows 1 2 ...
python3 v2/scripts/test-fleet-executor-local vm --as-shipped   # without accommodation W1 (rows 3, 5, 10, 12 fail)
python3 v2/scripts/test-fleet-executor-local teardown     # Mac side only; the VM is left running
```

The script is committed non-executable here (run it with `python3`); the helper
sub-commands `reset`, `tls`, `reclone` and `gate` exist because rows leave the VM in a
state that needs them.

## 8. State at the end

- Mac processes stopped (authority, fake GitHub, proxy, etcd runtime); the PostgreSQL
  database `norn_q1_20261005123447` dropped. `norn_fc` and `norn_fc_recovery` were
  never touched.
- The VM `norn-exec` is left running with the role applied; the executor unit was
  stopped so it does not poll a dead authority.
- Uncommitted additions in the Norn worktree: `v2/scripts/test-fleet-executor-local`,
  `v2/api/fleet_executor_local_authority_test.go` (build tag `fleetlocal`), and this
  file. The norn-fleet worktree is unchanged. Nothing was committed, stashed or
  switched.
- The harness work directory (`results/row-*.json`, `evidence/`, logs) was
  `/private/tmp/claude-501/-Users-arti-Desktop-Claude/4a55e0f6-8634-4bbc-a2af-9a3d72880b6d/scratchpad/q1/work`.

## Appendix A. Raw evidence per row

Times in the harness lines are UTC (`Z`); the journal excerpts are CDT (UTC-5).

### Evidence: row 1 (results) - PASS

Started 2026-10-05T15:58:50Z, finished 2026-10-05T16:04:40Z. Harness checks failed: none

```text
ok: baseline applied at 2531a8376f
harness: waiting 288s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
merge: pushed fe16a406e3 to protected main of the fake GitHub; validate.yml push run is auto-green; no rescan, no approve
ok: plan c6524a0d requiresApproval=False (config path class)
ok: status never asks for an approve
applied: lastApplied=fe16a406e3 generation=4
attempts: #1 851de894 succeeded/complete retryOf= inv=d5097447 boot=ccceca27
ok: exactly one attempt, succeeded
ok: no fleet.converge.approve exists for this plan (no human action)
ok: MinIO state holds terraform_data.node[ingress-03]
MinIO state serial: 3
executor journal: 2026-10-05T11:04:32 python[20315]: Plan: 1 to add, 0 to change, 0 to destroy.
    2026-10-05T11:04:32 python[20341]: plan actions: create=1, no-op=6
    2026-10-05T11:04:32 python[20375]: Apply complete! Resources: 1 added, 0 changed, 0 destroyed.
```

### Evidence: row 2 (results) - PASS

Started 2026-10-05T15:51:27Z, finished 2026-10-05T15:58:33Z. Harness checks failed: none

```text
harness: waiting 305s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
rescan: staging: recorded  tip 2531a8376f73be2ea1b9fc93f9325747c62583aa  plan 629f9eb5-c554-46b1-920a-bb64c2ca0941
ok: plan 629f9eb5 requiresApproval=true approved=false
ok: status nextAction is approve_converge
status while blocked: converge requiresApproval=true approved=false refused=false
      blocker  approval/approval_required: the converge plan changes executable paths or is the first converge; approve it (POST plans/{planID}/converge/approve) before the executor starts
      next     approve_converge
ok: no attempt exists after more than one executor poll
ok: executor journal says: wait: the converge plan needs an approve
norn fleet approve: idempotency key: norn-fleet-approve-d3c3e49b9b8112e24a072086fe8b8551
attempts: #1 65e6cb06 succeeded/complete retryOf= inv=d5097447 boot=ccceca27
ok: one attempt, succeeded, after the approve
ok: one fleet.converge.approve in the ledger
ok: MinIO state holds terraform_data.row2
executor journal: 2026-10-05T10:57:25 python[18949]: wait: the converge plan needs an approve
    2026-10-05T10:58:26 python[19906]: Plan: 1 to add, 0 to change, 0 to destroy.
    2026-10-05T10:58:26 python[19933]: plan actions: create=1, no-op=5
    2026-10-05T10:58:26 python[19967]: Apply complete! Resources: 1 added, 0 changed, 0 destroyed.
```

### Evidence: row 3 (results) - PASS

Started 2026-10-05T16:04:40Z, finished 2026-10-05T16:17:23Z. Harness checks failed: none

```text
harness: waiting 304s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
pushed + rescan: a363591f82 staging: recorded  tip a363591f825ce9c733ec5ae0850d5f7791e36d01  plan 59243150-0326-4679-953d-e81095d8d505
ok: executable path class: plan requires an approve
norn fleet approve: idempotency key: norn-fleet-approve-081f0f01c69af64d00d8dbaccb8b321a
ok: one running attempt while tofu apply is in flight
MinIO lock before the kill: {"ID": "71eb7d7d-fb09-f624-c9ec-0cc1b213ae70", "Operation": "OperationTypeApply", "Info": "", "Who": "norn-phase@lima-norn-exec", "Version": "1.10.6", "Created": "2026-10-05T16:10:39.027204887Z", "Path": "norn-exec-state/staging/nyc3/terraform.tfstate"}
ok: an apply lock owned by norn-phase@<host> is held in MinIO
kill: 21008 tofu -chdir=/var/lib/norn-fleet-phase/a363591f825ce9c733ec5ae0850d5f7791e36d01/environments/staging/nyc3 apply -lock-timeout=5m /var/lib/norn-fleet-phase/scratch-a363591f825ce9c733ec5ae0850d5f7791e36d01/out/fleet.tfplan
    killed=0
new invocation: d5097447 -> bf34f1bd
journal: 2026-10-05T11:10:38 python[20947]: Plan: 1 to add, 0 to change, 0 to destroy.
    2026-10-05T11:10:38 python[20974]: plan actions: create=1, no-op=7
    2026-10-05T11:10:40 python[18949]: executor failed: converge left attempt ba31fba9-3cb5-49c0-9042-01b319cc1449 live (apply failed with exit code 247): exiting so the next invocation can attest
    2026-10-05T11:10:40 systemd[1]: norn-fleet-executor.service: Main process exited, code=exited, status=1/FAILURE
    2026-10-05T11:10:40 systemd[1]: norn-fleet-executor.service: Failed with result 'exit-code'.
    2026-10-05T11:11:10 systemd[1]: norn-fleet-executor.service: Scheduled restart job, restart counter is at 1.
    2026-10-05T11:11:10 systemd[1]: Started norn-fleet-executor.service - Norn Fleet controller executor (ADR 0010).
    2026-10-05T11:11:10 python[21116]: busy: fence is Active
stop attestation #1: {"bootId": "ccceca27", "dropletId": "4242", "invocationId": "bf34f1bd", "phaseUidEmpty": true, "sourceAttemptId": "ba31fba9", "sourceCgroupEmpty": true, "phaseProcessGroupReaped": true}
ok: attestation: phase uid empty, cgroup empty, process group reaped
ok: attestation names the source attempt and this droplet
status: active   plan 59243150-0326-4679-953d-e81095d8d505  attempts ba31fba9-3cb5-49c0-9042-01b319cc1449  fence gen 5  Active  resumeNeeded ba31fba9-3cb5-49c0-9042-01b319cc1449
      blocker  approval/approval_required: attempt ba31fba9-3cb5-49c0-9042-01b319cc1449 stopped and its resume approval is spent or absent; run: norn fleet converge staging --resume
      next     approve_converge
ok: the stale .tflock is still in MinIO after the kill
norn fleet converge --resume: retry with: norn fleet approve 59243150-0326-4679-953d-e81095d8d505 --resume --source-attempt ba31fba9-3cb5-49c0-9042-01b319cc1449 --commit a363591f825ce9c733ec5ae0850d5f7791e36d01 --idempotency-key norn-fleet-converge-53189ec5ba88ee4a9401cd5c974fb187
attempts: #1 ba31fba9 abandoned/infrastructure_applied retryOf= inv=d5097447 boot=ccceca27; #2 d4b8480b succeeded/complete retryOf=ba31fba9 inv=bf34f1bd boot=ccceca27
ok: recovery attempt retries the source and succeeded
ok: the recovery's attestation recorded tofuLock.id == the stale lock's ID before the unlock
stop attestation (recovery): {"bootId": "ccceca27", "tofuLock": {"id": "71eb7d7d-fb09-f624-c9ec-0cc1b213ae70", "who": "norn-phase@lima-norn-exec", "created": "2026-10-05T16:10:39.027204Z", "operation": "OperationTypeApply"}, "dropletId": "4242", "invocationId": "bf34f1bd", "phaseUidEmpty": true, "sourceAttemptId": "ba31fba9", "s
ok: the lock is gone from MinIO after recovery
ok: the gated resource exists in state
journal: 2026-10-05T11:10:38 python[20947]: Plan: 1 to add, 0 to change, 0 to destroy.
    2026-10-05T11:10:38 python[20974]: plan actions: create=1, no-op=7
    2026-10-05T11:10:40 python[18949]: executor failed: converge left attempt ba31fba9-3cb5-49c0-9042-01b319cc1449 live (apply failed with exit code 247): exiting so the next invocation can attest
    2026-10-05T11:10:40 systemd[1]: norn-fleet-executor.service: Main process exited, code=exited, status=1/FAILURE
    2026-10-05T11:10:40 systemd[1]: norn-fleet-executor.service: Failed with result 'exit-code'.
    2026-10-05T11:11:10 systemd[1]: norn-fleet-executor.service: Scheduled restart job, restart counter is at 1.
    2026-10-05T11:11:10 systemd[1]: Started norn-fleet-executor.service - Norn Fleet controller executor (ADR 0010).
    2026-10-05T11:11:10 python[21116]: busy: fence is Active
    2026-10-05T11:12:11 python[21116]: state lock ? is not the source's: refusing to unlock
    2026-10-05T11:17:13 python[22744]: Plan: 1 to add, 0 to change, 0 to destroy.
    2026-10-05T11:17:13 python[22771]: plan actions: create=1, no-op=7
    2026-10-05T11:17:14 python[22793]: Apply complete! Resources: 1 added, 0 changed, 0 destroyed.
```

### Evidence: row 4 (results) - PASS

Started 2026-10-05T16:17:23Z, finished 2026-10-05T16:24:29Z. Harness checks failed: none

```text
harness: waiting 303s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
ok: config-only: no approve
attempt at the gate: running/nodes_configured
ok: attempt is at nodes_configured with the hook running
systemctl restart: active
ok: new systemd invocation bf34f1bd -> 7149c881
ok: no norn-phase process survived the restart (KillMode=control-group)
stop attestation: {"bootId": "ccceca27", "dropletId": "4242", "invocationId": "7149c881", "phaseUidEmpty": true, "sourceAttemptId": "37646b78", "sourceCgroupEmpty": true, "phaseProcessGroupReaped": true}
ok: same boot, different invocation; attested by the restarted loop
ok: no norn-phase process and an empty unit cgroup
norn fleet converge --resume: retry with: norn fleet approve 017db7f2-5616-41eb-8b19-704234fec777 --resume --source-attempt 37646b78-c32a-44a1-95d1-f2edfab3c794 --commit e538c9eb24e22ff13d9aa2ef711a05834cad624e --idempotency-key norn-fleet-converge-7fb0d3b0d5f99636c7bca3ee4e5a09ba
attempts: #1 37646b78 canceled/nodes_configured retryOf= inv=bf34f1bd boot=ccceca27; #2 86aa6d07 succeeded/complete retryOf=37646b78 inv=7149c881 boot=ccceca27
ok: recovery attempt succeeded
checkpoints: infrastructure_applied/37646b78/succeeded; inventory_generated/37646b78/succeeded; nodes_configured/86aa6d07/succeeded; nodes_enrolled/86aa6d07/succeeded; readiness_verified/86aa6d07/succeeded; complete/86aa6d07/succeeded
journal: 2026-10-05T11:23:19 python[23265]: plan actions: no-op=8
    2026-10-05T11:23:19 python[23299]: Apply complete! Resources: 0 added, 0 changed, 0 destroyed.
    2026-10-05T11:23:22 systemd[1]: Stopping norn-fleet-executor.service - Norn Fleet controller executor (ADR 0010)...
    2026-10-05T11:23:22 systemd[1]: norn-fleet-executor.service: Main process exited, code=exited, status=143/n/a
    2026-10-05T11:23:22 systemd[1]: norn-fleet-executor.service: Failed with result 'exit-code'.
    2026-10-05T11:23:22 systemd[1]: Stopped norn-fleet-executor.service - Norn Fleet controller executor (ADR 0010).
    2026-10-05T11:23:22 systemd[1]: Started norn-fleet-executor.service - Norn Fleet controller executor (ADR 0010).
    2026-10-05T11:23:23 python[23495]: busy: fence is Active
    2026-10-05T11:24:24 python[23662]: plan actions: no-op=8
```

### Evidence: row 6 (results) - PASS

Started 2026-10-05T16:24:39Z, finished 2026-10-05T16:38:29Z. Harness checks failed: none

```text
harness: waiting 295s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
proxy log (attempt start): 11:30:27.843 POST /api/v1/fleet/plans/2918e556-b3c3-4c02-bf65-417c3071d9c2/attempts -> 201 (drop_response) idem=executor-attempt:76697c7
    11:30:28.852 POST /api/v1/fleet/plans/2918e556-b3c3-4c02-bf65-417c3071d9c2/attempts -> 200 (pass) idem=executor-attempt:76697c7
    11:30:29.253 POST /api/v1/fleet/plans/2918e556-b3c3-4c02-bf65-417c3071d9c2/attempts/1e241c9d-10be-4d8b-a4ff-0ec7612235df/heartbeat -> 200 (pass) idem=
    11:30:29.987 POST /api/v1/fleet/plans/2918e556-b3c3-4c02-bf65-417c3071d9c2/attempts/1e241c9d-10be-4d8b-a4ff-0ec7612235df/advance -> 200 (pass) idem=
    11:30:30.623 POST /api/v1/fleet/plans/2918e556-b3c3-4c02-bf65-417c3071d9c2/attempts/1e241c9d-10be-4d8b-a4ff-0ec7612235df/advance -> 200 (pass) idem=
    11:30:31.563 POST /api/v1/fleet/plans/2918e556-b3c3-4c02-bf65-417c3071d9c2/attempts/1e241c9d-10be-4d8b-a4ff-0ec7612235df/advance -> 200 (pass) idem=
    11:30:32.186 POST /api/v1/fleet/plans/2918e556-b3c3-4c02-bf65-417c3071d9c2/attempts/1e241c9d-10be-4d8b-a4ff-0ec7612235df/advance -> 200 (pass) idem=
    11:30:32.803 POST /api/v1/fleet/plans/2918e556-b3c3-4c02-bf65-417c3071d9c2/attempts/1e241c9d-10be-4d8b-a4ff-0ec7612235df/advance -> 200 (pass) idem=
    11:30:33.107 POST /api/v1/fleet/plans/2918e556-b3c3-4c02-bf65-417c3071d9c2/attempts/1e241c9d-10be-4d8b-a4ff-0ec7612235df/advance -> 200 (pass) idem=
ok: first attempt-start response dropped, retry passed
ok: the retry reuses the same Idempotency-Key
attempts: #1 1e241c9d succeeded/complete retryOf= inv=7149c881 boot=ccceca27
ok: exactly one attempt exists: the lost response created it, the replay returned it
journal 6a: 2026-10-05T11:30:27 python[23987]: plan actions: no-op=8
    2026-10-05T11:30:29 python[24022]: Apply complete! Resources: 0 added, 0 changed, 0 destroyed.
harness: waiting 307s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
6b stop attestation: {"bootId": "ccceca27", "dropletId": "4242", "invocationId": "56d1bcdb", "phaseUidEmpty": true, "sourceAttemptId": "26982eb9", "sourceCgroupEmpty": true, "phaseProcessGroupReaped": true}
6b journal: 2026-10-05T11:36:34 python[24388]: plan actions: no-op=8
    2026-10-05T11:36:50 python[23495]: executor failed: Norn POST /api/v1/fleet/plans/1dab1d10-bd1d-464c-9db3-ef6cd51f4a00/attempts failed with HTTP 0: unreachable
    2026-10-05T11:36:50 systemd[1]: norn-fleet-executor.service: Main process exited, code=exited, status=1/FAILURE
    2026-10-05T11:36:50 systemd[1]: norn-fleet-executor.service: Failed with result 'exit-code'.
    2026-10-05T11:37:20 systemd[1]: norn-fleet-executor.service: Scheduled restart job, restart counter is at 1.
    2026-10-05T11:37:20 systemd[1]: Started norn-fleet-executor.service - Norn Fleet controller executor (ADR 0010).
    2026-10-05T11:37:20 python[24412]: busy: fence is Active
ok: 6b: exactly one attempt exists although five responses were lost
6b proxy log: 11:36:34.852 POST /api/v1/fleet/plans/1dab1d10-bd1d-464c-9db3-ef6cd51f4a00/attempts -> 201 (drop_response) idem=executor-attempt:78dee04
    11:36:35.862 POST /api/v1/fleet/plans/1dab1d10-bd1d-464c-9db3-ef6cd51f4a00/attempts -> 200 (drop_response) idem=executor-attempt:78dee04
    11:36:37.917 POST /api/v1/fleet/plans/1dab1d10-bd1d-464c-9db3-ef6cd51f4a00/attempts -> 200 (drop_response) idem=executor-attempt:78dee04
    11:36:41.972 POST /api/v1/fleet/plans/1dab1d10-bd1d-464c-9db3-ef6cd51f4a00/attempts -> 200 (drop_response) idem=executor-attempt:78dee04
    11:36:50.032 POST /api/v1/fleet/plans/1dab1d10-bd1d-464c-9db3-ef6cd51f4a00/attempts -> 200 (drop_response) idem=executor-attempt:78dee04
    11:37:20.387 POST /api/v1/fleet/plans/1dab1d10-bd1d-464c-9db3-ef6cd51f4a00/attempts -> 200 (pass) idem=executor-attempt:78dee04
ok: 6b: the journal replay is the 6th request with the same key and returns the stored attempt
norn fleet converge --resume: retry with: norn fleet approve 1dab1d10-bd1d-464c-9db3-ef6cd51f4a00 --resume --source-attempt 26982eb9-6735-441a-84b5-601537f6919c --commit 7d853ccd49684465fff0415ae0225a40bb60c6ce --idempotency-key norn-fleet-converge-84ffd9221ab67c4e4e9c50dbf7f45d6e
6b attempts: #1 26982eb9 canceled/infrastructure_applied retryOf= inv=7149c881 boot=ccceca27; #2 014bc9d9 succeeded/complete retryOf=26982eb9 inv=56d1bcdb boot=ccceca27
```

### Evidence: row 7 (results) - PASS

Started 2026-10-05T16:38:29Z, finished 2026-10-05T16:45:31Z. Harness checks failed: none

```text
harness: waiting 304s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
pushed + rescan: 4859b9ea58 staging: recorded  tip 4859b9ea5866e1c67ce4ff8c0438abbf797ae39e  plan c6e47ed3-817f-4109-8822-90dd014bd355
ok: executable path class: plan requires an approve
norn fleet approve: idempotency key: norn-fleet-approve-df1936725fcb6b6b7ce70d4f3116ad64
apply is running; sleeping so a supervise heartbeat falls into the outage
authority stopped: pid 22311
authority restarted: pid 33976 on the same database
attempts: #1 0fdb7cc3 succeeded/complete retryOf= inv=56d1bcdb boot=ccceca27
ok: one attempt, succeeded, with no recovery needed
proxy errors during the outage: 11:45:07.350 GET /api/v1/fleet/plans/c6e47ed3-817f-4109-8822-90dd014bd355/attempts/0fdb7cc3-b339-44b2-9ddf-442bef68aef2 -> upstream error [Errno 61] Connection refused
    11:45:08.384 GET /api/v1/fleet/plans/c6e47ed3-817f-4109-8822-90dd014bd355/attempts/0fdb7cc3-b339-44b2-9ddf-442bef68aef2 -> upstream error [Errno 61] Connection refused
    11:45:10.417 GET /api/v1/fleet/plans/c6e47ed3-817f-4109-8822-90dd014bd355/attempts/0fdb7cc3-b339-44b2-9ddf-442bef68aef2 -> upstream error [Errno 61] Connection refused
    11:45:14.449 GET /api/v1/fleet/plans/c6e47ed3-817f-4109-8822-90dd014bd355/attempts/0fdb7cc3-b339-44b2-9ddf-442bef68aef2 -> upstream error [Errno 61] Connection refused
ok: resource created
journal: 2026-10-05T11:44:26 python[25104]: Plan: 1 to add, 0 to change, 0 to destroy.
    2026-10-05T11:44:27 python[25131]: plan actions: create=1, no-op=8
    2026-10-05T11:45:16 python[25165]: Apply complete! Resources: 1 added, 0 changed, 0 destroyed.
```

### Evidence: row 8 (results) - PASS

Started 2026-10-05T18:11:36Z, finished 2026-10-05T18:21:08Z. Harness checks failed: none

```text
fake GitHub primed: plan PR merged at 5573397ff2, plan workflow green, apply dispatch endpoint live
harness: waiting 99s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
pushed + rescan: dd2ba99b22 staging: recorded  tip dd2ba99b22f07556f2dcb0021a3fdd1133685caa  plan 92219746-d5db-4ba4-806a-e8ff59d4c4f1
ok: executable path class: plan requires an approve
norn fleet approve: idempotency key: norn-fleet-approve-960ffb2b5855fbbd2bdc54edc106cb21
ok: fence held by the executor plan 92219746
GitHub-lane capacity plan: fbc312f7-0a33-4c78-8bd8-0d4d26c293ef (ingress pool desired 3)
8a GitHub dispatch while the executor holds the fence: HTTP 409 fleet_target_execution_occupied
ok: GitHub lane refused with fleet_target_execution_occupied
ok: no workflow dispatch was sent to the fake GitHub
8a executor attempt: #1 9a8e2283 succeeded/complete retryOf= inv=98130bf4 boot=92518a25
harness: waiting 302s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
GitHub-lane capacity plan: b49280f6-70cf-4184-947d-9577fa9c03a0 (ingress pool desired 3)
8c GitHub-lane dispatch: HTTP 201 succeeded
8c executor-lane attempt start (executor token): HTTP 409 fleet_target_execution_occupied
ok: exactly one lane acquired the fence (['github'])
ok: the other lane got 409 fleet_target_execution_occupied (HTTP 409 fleet_target_execution_occupied)
fence after the race: {"held": true, "holder_plan_id": "b49280f6-70cf-4184-947d-9577fa9c03a0", "generation": 5, "last_release_reason": "succeeded"}
ok: the fence is held by exactly the winner
fake GitHub dispatches: [{"at": "2026-10-05T18:19:52Z", "plan": "b49280f6-70cf-4184-947d-9577fa9c03a0", "run": 1001}]
real loop after restart: 2026-10-05T13:19:53 python[6660]: busy: fence is Uncertain
    2026-10-05T13:20:52 python[6660]: busy: fence is Uncertain
status: active   plan b49280f6-70cf-4184-947d-9577fa9c03a0  attempts -  fence gen 5  Uncertain
      blocker  execution/NoLiveAttempt: the target mutation fence is held by plan b49280f6-70cf-4184-947d-9577fa9c03a0 (Uncertain)
      next     resolve_uncertain_outcome
```

### Evidence: row 9 (results) - PASS

Started 2026-10-05T18:08:19Z, finished 2026-10-05T18:09:35Z. Harness checks failed: none

```text
ok: lastApplied is 5573397ff2
force-push: main now 9da5b22046 (a new root); the old tip 5573397ff2 is no longer reachable from main
norn fleet converge (rescan): staging: history_rewritten  tip 9da5b2204667db85bb78fbd01a308b106648e2a0  plan -  fleet_executor_trigger_unverified tip is diverged relative to the last applied commit
ok: the rescan reports history_rewritten
status: desired  gen 3  commit 5573397ff2b6b6d17952e7a74800c0b32e9c6794  plan 1e9eed29-a299-43de-81ba-fc442dda35d8
      applied  gen 3  commit 5573397ff2b6b6d17952e7a74800c0b32e9c6794  plan 1e9eed29-a299-43de-81ba-fc442dda35d8
      blocker  trigger/history_rewritten: the protected branch tip is not a descendant of the last applied or newest planned commit; the watcher never moves backwards. Restore main so it descends from the last applied commit (for example revert on main), after which the next poll records the new tip (tip is diverged relative to the 
      next     refresh_observations
ok: status carries blocker history_rewritten
ok: no new converge plan was recorded for the rewritten tip
ok: lastApplied unchanged; nothing was applied
after pruning the bare repo (compare 404): staging: history_rewritten  tip 9da5b2204667db85bb78fbd01a308b106648e2a0  plan -  fleet_executor_trigger_unverified last applied commit is not comparable with the tip
ok: history_rewritten also when the last-applied commit no longer resolves (compare 404)
operator revert: main force-pushed back to 5573397ff2 (the applied tip)
rescan after revert: staging: none  tip 5573397ff2b6b6d17952e7a74800c0b32e9c6794  plan -
ok: blocker clears once main descends from the last-applied commit again
ok: status no longer shows history_rewritten
```

### Evidence: row 10 (results) - FAIL

Started 2026-10-05T17:16:57Z, finished 2026-10-05T17:31:06Z. Harness checks failed: ['aborted: 1']

```text
harness: waiting 304s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
pushed + rescan: 90004d3e15 staging: recorded  tip 90004d3e1514c344ab6682a3ab224ac1c17d16f9  plan 84eae304-7480-4258-a8f4-fcc694123c64
ok: executable path class: plan requires an approve
norn fleet approve: idempotency key: norn-fleet-approve-87b3c1cc68fd405d994a5037f6070a6e
lock 1 (held by the real tofu apply): {"ID": "4ece05f1-a8f4-a738-5c51-eec91e7d908d", "Operation": "OperationTypeApply", "Info": "", "Who": "norn-phase@lima-norn-exec", "Version": "1.10.6", "Created": "2026-10-05T17:22:57.880359715Z", "Path": "norn-exec-state/staging/nyc3/terraform.tfstate"}
kill -9 tofu apply: 5886 tofu -chdir=/var/lib/norn-fleet-phase/90004d3e1514c344ab6682a3ab224ac1c17d16f9/environments/staging/nyc3 apply -lock-timeout=5m /var/lib/norn-fleet-phase/scratch-90004d3e1514c344ab6682a3ab224ac1c17d16f9/out/fleet.tfplan
    killed=0
ok: the killed apply left lock 1 in MinIO
norn fleet converge --resume (1st): retry with: norn fleet approve 84eae304-7480-4258-a8f4-fcc694123c64 --resume --source-attempt 8febeb9e-21e1-423c-ac7a-756cffb74e71 --commit 90004d3e1514c344ab6682a3ab224ac1c17d16f9 --idempotency-key norn-fleet-converge-1f1bbe61895745d507000c03ae21a15f
killed the recovery's own tofu plan: killed-10346
ok: owned lock: recovery 1 recorded tofuLock.id == lock 1's ID, then force-unlocked it
attestation with tofuLock: {"bootId": "b54c4d21", "tofuLock": {"id": "4ece05f1-a8f4-a738-5c51-eec91e7d908d", "who": "norn-phase@lima-norn-exec", "created": "2026-10-05T17:22:57.880359Z", "operation": "OperationTypeApply"}, "dropletId": "4242", "invocationId": "108a0bc4", "phaseUidEmpty": true, "sourceAttemptId": "8febeb9e", "sou
lock 2: {"ID": "03567366-0896-93eb-5b47-749a74345fc7", "Operation": "OperationTypePlan", "Info": "", "Who": "norn-phase@lima-norn-exec", "Version": "1.10.6", "Created": "2026-10-05T17:24:30.700412418Z", "Path": "norn-exec-state/staging/nyc3/terraform.tfstate"}
ok: the killed `tofu plan` left an Operation=OperationTypePlan lock
attempts: #1 8febeb9e canceled/infrastructure_applied retryOf= inv=b075f01b boot=b54c4d21; #2 0973f018 running/infrastructure_applied retryOf=8febeb9e inv=108a0bc4 boot=b54c4d21
norn fleet converge --resume (2nd): retry with: norn fleet approve 84eae304-7480-4258-a8f4-fcc694123c64 --resume --source-attempt 0973f018-ef94-4ce0-9465-5b7d28433276 --commit 90004d3e1514c344ab6682a3ab224ac1c17d16f9 --idempotency-key norn-fleet-converge-f8f518b2f6dd2c4fdd9afa450a0c840d
ABORTED: 1
```

### Evidence: row 10b (results) - PASS

Started 2026-10-05T17:47:38Z, finished 2026-10-05T18:08:10Z. Harness checks failed: none

```text
harness: waiting 305s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
pushed + rescan: 5573397ff2 staging: recorded  tip 5573397ff2b6b6d17952e7a74800c0b32e9c6794  plan 1e9eed29-a299-43de-81ba-fc442dda35d8
ok: executable path class: plan requires an approve
norn fleet approve: idempotency key: norn-fleet-approve-63bb8eaaf8ee00adecd07ec65f873cf6
kill -9 tofu apply: 2679 tofu -chdir=/var/lib/norn-fleet-phase/5573397ff2b6b6d17952e7a74800c0b32e9c6794/environments/staging/nyc3 apply -lock-timeout=5m /var/lib/norn-fleet-phase/scratch-5573397ff2b6b6d17952e7a74800c0b32e9c6794/out/fleet.tfplan
    killed=0
stale lock of the killed apply (owned): {"ID": "5a162bbe-03fb-cf71-16ab-c3d17347c6ac", "Operation": "OperationTypeApply", "Info": "", "Who": "norn-phase@lima-norn-exec", "Version": "1.10.6", "Created": "2026-10-05T17:53:40.533133756Z", "Path": "norn-exec-state/staging/nyc3/terraform.tfstate"}
remove it: deleted
foreign planner (root): killed-2833
foreign lock: {"ID": "00076fc0-169a-402f-31a4-107b8f65940b", "Operation": "OperationTypePlan", "Info": "", "Who": "root@lima-norn-exec", "Version": "1.10.6", "Created": "2026-10-05T17:54:13.840099167Z", "Path": "norn-exec-state/staging/nyc3/terraform.tfstate"}
ok: a lock held by root@host with Operation=OperationTypePlan is in MinIO
norn fleet converge --resume: retry with: norn fleet approve 1e9eed29-a299-43de-81ba-fc442dda35d8 --resume --source-attempt 5b9c1ac1-23d7-400b-a5cf-3cd418506f6f --commit 5573397ff2b6b6d17952e7a74800c0b32e9c6794 --idempotency-key norn-fleet-converge-d6d3f81e0a2f153eb8e01623acc4e63d
status: blocker  execution/state_lock_foreign: the OpenTofu state lock is not the executor's own, so recovery did not unlock it (lock 00076fc0-169a-402f-31a4-107b8f65940b held by root@lima-norn-exec). Clear the lock (tofu force-unlock after confirming nothing is running); the executor re-probes after its back-off, no other actio
      next     await_runner
ok: fleet.converge.refusal state_lock_foreign is in the ledger
refusal payload: {"code": "state_lock_foreign", "tofuLock": {"id": "00076fc0-169a-402f-31a4-107b8f65940b", "who": "root@lima-norn-exec", "created": "2026-10-05 17:54:13.840099167 +0000 UTC", "operation": "OperationTypePlan"}, "sourceAttemptId": "5b9c1ac1-23d7-400b-a5cf-3cd418506f6f"}
ok: the refusal names the source attempt
ok: no attestation carries a tofuLock: nothing was unlocked
ok: the foreign lock is still in place
tofu force-unlock -force <wrong id> (MinIO, use_lockfile): [31mFailed to unlock state: lock id "00000000-0000-0000-0000-000000000000" from s3 does not match existing lock
    Lock Info:
      ID:        00076fc0-169a-402f-31a4-107b8f65940b
      Path:      norn-exec-state/staging/nyc3/terraform.tfstate
      Operation: OperationType
ok: tofu refuses a force-unlock with another lock ID and leaves the lock
operator: tofu force-unlock -force <the lock id>: te has been successfully unlocked![0m[32m
    
    The state has been unlocked, and OpenTofu commands should now be able to
    obtain a new lock on the remote state.[0m
ok: the operator cleared the lock
attempts: #1 5b9c1ac1 abandoned/infrastructure_applied retryOf= inv=1adbca51 boot=92518a25; #2 5ae74aac succeeded/complete retryOf=5b9c1ac1 inv=98130bf4 boot=92518a25
ok: the one recovery succeeded after the lock was cleared
ok: resource created
journal: 2026-10-05T12:53:39 python[2602]: Plan: 1 to add, 0 to change, 0 to destroy.
    2026-10-05T12:53:40 python[2645]: plan actions: create=1, no-op=6
    2026-10-05T12:53:41 python[695]: executor failed: converge left attempt 5b9c1ac1-23d7-400b-a5cf-3cd418506f6f live (apply failed with exit code 247): exiting so the next invocation can attest
    2026-10-05T12:53:41 systemd[1]: norn-fleet-executor.service: Main process exited, code=exited, status=1/FAILURE
    2026-10-05T12:53:41 systemd[1]: norn-fleet-executor.service: Failed with result 'exit-code'.
    2026-10-05T12:54:11 systemd[1]: norn-fleet-executor.service: Scheduled restart job, restart counter is at 1.
    2026-10-05T12:54:11 systemd[1]: Started norn-fleet-executor.service - Norn Fleet controller executor (ADR 0010).
    2026-10-05T12:54:12 python[2781]: busy: fence is Active
    2026-10-05T12:55:12 python[2781]: state lock 00076fc0-169a-402f-31a4-107b8f65940b is not the source's: refusing to unlock
    2026-10-05T12:56:21 python[3145]: Plan: 1 to add, 0 to change, 0 to destroy.
    2026-10-05T12:56:22 python[3173]: plan actions: create=1, no-op=6
    2026-10-05T13:08:03 python[3195]: Apply complete! Resources: 1 added, 0 changed, 0 destroyed.
```

### Evidence: row 11 (results) - PASS

Started 2026-10-05T18:09:35Z, finished 2026-10-05T18:11:21Z. Harness checks failed: none

```text
token.json before: {"deviceId": "884471aa-8988-4469-bfc8-ac8f83bb9cfe", "expiresAt": "2026-10-12T17:35:12.618236Z", "scopes": ["fleet:execute"], "tokenId": "norn_b7dc20d5-9755-45af-b1db-52c386bb0bcb"}
ok: old token works (HTTP 200)
recorded expiry set to: 2026-10-05T20:09:35Z (token itself unchanged)
token.json after: {"deviceId": "884471aa-8988-4469-bfc8-ac8f83bb9cfe", "expiresAt": "2026-10-12T18:10:07.161117Z", "scopes": ["fleet:execute"], "tokenId": "norn_6e015d55-d050-438f-a927-01d540ad3c65"}
ok: same device after rotation
ok: new token lives about 7 days (7.00 d)
ok: scope is still exactly fleet:execute
ok: old token revoked (HTTP 401), new token works (HTTP 200)
ok: token file is 0600 norn-exec
proxy log (rotate): 13:10:07.165 POST /api/v1/auth/rotate -> 200 (pass) idem=
ok: the loop keeps polling with the new token
ok: unit still active (no exit 3)
```

### Evidence: row 12 (results) - PASS

Started 2026-10-05T17:07:57Z, finished 2026-10-05T17:16:57Z. Harness checks failed: none

```text
harness: waiting 296s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
pushed + rescan: 3ddf237969 staging: recorded  tip 3ddf237969842cc0a1a76f61be1bc4dec2d916b5  plan dab476d0-c478-4f3f-9306-f734abf69f99
ok: executable path class: plan requires an approve
norn fleet approve: idempotency key: norn-fleet-approve-09186ab7c1c48ba7cd256222cdaa3295
kill: 4901 tofu -chdir=/var/lib/norn-fleet-phase/3ddf237969842cc0a1a76f61be1bc4dec2d916b5/environments/staging/nyc3 apply -lock-timeout=5m /var/lib/norn-fleet-phase/scratch-3ddf237969842cc0a1a76f61be1bc4dec2d916b5/out/fleet.tfplan
    killed=0
droplet id: metadata service now answers 9999 (this is the cloned disk's new droplet)
journal: 2026-10-05T12:11:49 python[4164]: plan actions: create=1, no-op=10
    2026-10-05T12:12:50 python[4480]: Plan: 1 to add, 0 to change, 0 to destroy.
    2026-10-05T12:12:50 python[4507]: plan actions: create=1, no-op=10
    2026-10-05T12:13:51 python[4838]: Plan: 1 to add, 0 to change, 0 to destroy.
    2026-10-05T12:13:51 python[4866]: plan actions: create=1, no-op=10
    2026-10-05T12:13:53 python[695]: executor failed: converge left attempt e96819b9-fbec-4352-a495-45ca5c8554b9 live (apply failed with exit code 247): exiting so the next invocation can attest
    2026-10-05T12:13:53 systemd[1]: norn-fleet-executor.service: Main process exited, code=exited, status=1/FAILURE
    2026-10-05T12:13:53 systemd[1]: norn-fleet-executor.service: Failed with result 'exit-code'.
    2026-10-05T12:14:23 systemd[1]: norn-fleet-executor.service: Scheduled restart job, restart counter is at 1.
    2026-10-05T12:14:23 systemd[1]: Started norn-fleet-executor.service - Norn Fleet controller executor (ADR 0010).
    2026-10-05T12:14:23 python[5008]: open attempt belongs to another droplet: needs providerEvidence, not attestation
    2026-10-05T12:14:23 python[5008]: busy: fence is Active
ok: loop logs: open attempt belongs to another droplet: needs providerEvidence, not attestation
ok: no stop attestation was recorded
forged attestation (dropletId=9999): HTTP 409 {"type": "https://norn.dev/problems/fleet_executor_stop_attestation_invalid", "title": "Conflict", "status": 409, "code": "fleet_executor_stop_attestation_invalid", "detail": "attesting droplet or machine differs from the source attempt", "requestId": "xenon-2
ok: Norn refuses the clone's attestation (HTTP 409)
ok: still no attestation in the ledger
status: active   plan dab476d0-c478-4f3f-9306-f734abf69f99  attempts e96819b9-fbec-4352-a495-45ca5c8554b9  fence gen 11  Active
      blocker  execution/Active: the target mutation fence is held by plan dab476d0-c478-4f3f-9306-f734abf69f99 (Active)
      next     await_runner
norn fleet converge --resume while only the clone is up: rc=1 fleet converge --resume: staging has no stopped attempt awaiting a resume approve (it needs a stop attestation or provider evidence first); see `norn fleet status staging`
ok: the clone started no recovery attempt (still only the source attempt)
stop attestation (original droplet): {"bootId": "b54c4d21", "dropletId": "4242", "invocationId": "b075f01b", "phaseUidEmpty": true, "sourceAttemptId": "e96819b9", "sourceCgroupEmpty": true, "phaseProcessGroupReaped": true}
ok: the original droplet attests
attempts: #1 e96819b9 abandoned/infrastructure_applied retryOf= inv=4ad50031 boot=b54c4d21; #2 5bad26e5 succeeded/complete retryOf=e96819b9 inv=b075f01b boot=b54c4d21
```

### Evidence: row 5 (results) - FAIL

Started 2026-10-05T17:36:24Z, finished 2026-10-05T17:47:38Z. Harness checks failed: ['the stale .tflock survived the power-off', 'stale lock unlocked via the attested tofuLock id']

```text
harness: waiting 301s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
pushed + rescan: 911fb2d248 staging: recorded  tip 911fb2d248c846adfc28cf20aee883bebc28a12b  plan bab978db-4e14-414e-9ebc-6dad0c26d6f3
ok: executable path class: plan requires an approve
norn fleet approve: idempotency key: norn-fleet-approve-cf56c230a879fbfe1d19304d9958451d
before power-off: boot_id=b54c4d21-0485-40f3-b736-e7dc32a2b841 attempt=539c2aec lock={"ID": "5d896556-0e19-8763-ecf4-ff3a8b6c77ea", "Operation": "OperationTypeApply", "Info": "", "Who": "norn-phase@lima-norn-exec", "Version": "1.10.6", "Created": "2026-10-05T17:42:17.927654544Z", "Path": "norn-exec-state/staging/nyc3/terraform.t
ok: apply lock held at the moment of power-off
power-off: limactl stop --force
VM status: norn-exec    Stopped    127.0.0.1:63836    vz        aarch64    4       4GiB      100GiB    ~/.lima/norn-exec
limactl start: time="2026-10-05T12:42:36-05:00" level=info msg="READY. Run `limactl shell norn-exec` to open the shell."
ok: boot_id changed b54c4d21 -> 92518a25
unit after boot: active
stop attestation: {"bootId": "92518a25", "dropletId": "4242", "invocationId": "1adbca51", "phaseUidEmpty": true, "sourceAttemptId": "539c2aec", "sourceCgroupEmpty": true, "phaseProcessGroupReaped": true}
ok: attestation carries the NEW boot_id (the old attempt's boot differs)
ok: source attempt recorded the old boot_id (b54c4d21)
CHECK FAILED: the stale .tflock survived the power-off
norn fleet converge --resume: retry with: norn fleet approve bab978db-4e14-414e-9ebc-6dad0c26d6f3 --resume --source-attempt 539c2aec-225c-434f-b780-29831de8e288 --commit 911fb2d248c846adfc28cf20aee883bebc28a12b --idempotency-key norn-fleet-converge-929c440175b180939f6a1865b37999c0
attempts: #1 539c2aec abandoned/infrastructure_applied retryOf= inv=f8363e2d boot=b54c4d21; #2 70f16905 succeeded/complete retryOf=539c2aec inv=1adbca51 boot=92518a25
ok: recovery attempt succeeded after the reboot
CHECK FAILED: stale lock unlocked via the attested tofuLock id
ok: lock gone
ok: resource created by the recovery
journal (since boot): 2026-10-05T12:42:28 systemd[1]: Started norn-fleet-executor.service - Norn Fleet controller executor (ADR 0010).
    2026-10-05T12:42:29 python[695]: busy: fence is Active
    2026-10-05T12:43:29 python[695]: git fetch failed: error: object file .git/objects/91/1fb2d248c846adfc28cf20aee883bebc28a12b is empty
    2026-10-05T12:43:29 python[695]: error: object file .git/objects/91/1fb2d248c846adfc28cf20aee883bebc28a12b is empty
    2026-10-05T12:43:29 python[695]: error: object file .git/objects/91
    2026-10-05T12:44:29 python[695]: git fetch failed: error: object file .git/objects/91/1fb2d248c846adfc28cf20aee883bebc28a12b is empty
    2026-10-05T12:44:29 python[695]: error: object file .git/objects/91/1fb2d248c846adfc28cf20aee883bebc28a12b is empty
    2026-10-05T12:44:29 python[695]: error: object file .git/objects/91
    2026-10-05T12:45:30 python[695]: git fetch failed: error: object file .git/objects/91/1fb2d248c846adfc28cf20aee883bebc28a12b is empty
    2026-10-05T12:45:30 python[695]: error: object file .git/objects/91/1fb2d248c846adfc28cf20aee883bebc28a12b is empty
    2026-10-05T12:45:30 python[695]: error: object file .git/objects/91
    2026-10-05T12:46:30 python[695]: git fetch failed: error: object file .git/objects/91/1fb2d248c846adfc28cf20aee883bebc28a12b is empty
    2026-10-05T12:46:30 python[695]: error: object file .git/objects/91/1fb2d248c846adfc28cf20aee883bebc28a12b is empty
    2026-10-05T12:46:30 python[695]: error: object file .git/objects/91
    2026-10-05T12:47:31 python[1995]: Plan: 1 to add, 0 to change, 0 to destroy.
    2026-10-05T12:47:31 python[2021]: plan actions: create=1, no-op=5
    2026-10-05T12:47:31 python[2044]: Apply complete! Resources: 1 added, 0 changed, 0 destroyed.
```

### Evidence: row 5 (results-run1) - FAIL

Started 2026-10-05T16:45:41Z, finished 2026-10-05T17:06:44Z. Harness checks failed: ['the stale .tflock survived the power-off', 'aborted: 1']

```text
harness: waiting 293s so a new plan is recorded after the previous release + 5 min (bug 2 accommodation)
pushed + rescan: 2a4d83511e staging: recorded  tip 2a4d83511e380aa2eeb445f93992bea081f10475  plan 16d1d03b-71dc-4887-bf86-3a5a8ccea6a5
ok: executable path class: plan requires an approve
norn fleet approve: idempotency key: norn-fleet-approve-d1fdd69dfc964b5de30f7ddb940983ce
before power-off: boot_id=ccceca27-02a5-49df-b9d2-e4bcd4f632ef attempt=585a252b lock={"ID": "eb0783fa-880d-1a53-45d2-568cc527319e", "Operation": "OperationTypeApply", "Info": "", "Who": "norn-phase@lima-norn-exec", "Version": "1.10.6", "Created": "2026-10-05T16:51:27.054375981Z", "Path": "norn-exec-state/staging/nyc3/terraform.t
ok: apply lock held at the moment of power-off
power-off: limactl stop --force
VM status: norn-exec    Stopped    127.0.0.1:55514    vz        aarch64    4       4GiB      100GiB    ~/.lima/norn-exec
limactl start: time="2026-10-05T11:51:40-05:00" level=info msg="READY. Run `limactl shell norn-exec` to open the shell."
ok: boot_id changed ccceca27 -> b54c4d21
unit after boot: active
stop attestation: {"bootId": "b54c4d21", "dropletId": "4242", "invocationId": "4ad50031", "phaseUidEmpty": true, "sourceAttemptId": "585a252b", "sourceCgroupEmpty": true, "phaseProcessGroupReaped": true}
ok: attestation carries the NEW boot_id (the old attempt's boot differs)
ok: source attempt recorded the old boot_id (ccceca27)
CHECK FAILED: the stale .tflock survived the power-off
norn fleet converge --resume: retry with: norn fleet approve 16d1d03b-71dc-4887-bf86-3a5a8ccea6a5 --resume --source-attempt 585a252b-505a-4429-9ebd-c4b47148413f --commit 2a4d83511e380aa2eeb445f93992bea081f10475 --idempotency-key norn-fleet-converge-de583ed089144a13b52d56a5e78354ed
ABORTED: 1
```

### Evidence: row etcd-gate (results) - PASS

Started 2026-10-05T16:46:00Z, finished 2026-10-05T16:46:16Z. Harness checks failed: none

```text
bootstrap token: rc=0
ok: an etcd managed operator token exists (api:read api:write platform:operate)
etcd runtime up: pid 34167 on http://127.0.0.1:18805, prefix /norn-q1-20261005114600/
capabilities: backend=etcd mode=normal-fleet features=['etcd-normal-router-v1', 'fleet-authority-only-v1', 'fleet-v1', 'managed-token-revocation', 'managed-token-lifecycle', 'fleet-github-oidc-exchange', 'signed-operation-acceptance', 'fleet-inventory', 'fleet-app-target-configuration', 'durable-fleet-capacity-plans', 'database-
ok: capabilities do not advertise fleet-executor-v1
ok: capabilities list no executor-lane endpoint
GET fleet/resources/staging: HTTP 401 unauthorized
  (not lane-only: exists for ordinary scopes): HTTP 401
POST fleet/resources/staging/converge: HTTP 501 backend_route_unsupported
ok: POST fleet/resources/staging/converge is not served (HTTP 501 backend_route_unsupported)
POST fleet/plans/00000000-0000-4000-8000-000000000000/attempts: HTTP 401 unauthorized
  (not lane-only: exists for ordinary scopes): HTTP 401
POST fleet/plans/00000000-0000-4000-8000-000000000000/credential-grants: HTTP 501 backend_route_unsupported
ok: POST fleet/plans/00000000-0000-4000-8000-000000000000/credential-grants is not served (HTTP 501 backend_route_unsupported)
POST fleet/plans/00000000-0000-4000-8000-000000000000/converge-refusals: HTTP 501 backend_route_unsupported
ok: POST fleet/plans/00000000-0000-4000-8000-000000000000/converge-refusals is not served (HTTP 501 backend_route_unsupported)
POST fleet/plans/00000000-0000-4000-8000-000000000000/converge/approve: HTTP 501 backend_route_unsupported
ok: POST fleet/plans/00000000-0000-4000-8000-000000000000/converge/approve is not served (HTTP 501 backend_route_unsupported)
POST fleet/plans/00000000-0000-4000-8000-000000000000/provider-evidence: HTTP 501 backend_route_unsupported
ok: POST fleet/plans/00000000-0000-4000-8000-000000000000/provider-evidence is not served (HTTP 501 backend_route_unsupported)
POST fleet/executor/stop-attestations: HTTP 501 backend_route_unsupported
ok: POST fleet/executor/stop-attestations is not served (HTTP 501 backend_route_unsupported)
POST enrollments: HTTP 501 backend_route_unsupported
ok: POST enrollments is not served (HTTP 501 backend_route_unsupported)
POST enrollments/approve: HTTP 501 backend_route_unsupported
ok: POST enrollments/approve is not served (HTTP 501 backend_route_unsupported)
unauthenticated attestation POST: HTTP 501
ok: no route and no auth path for the lane
ok: no converge watcher started on the etcd runtime
```

## Re-run after fixes (2026-10-05)

Status: **all twelve Q1 rows, 10b, 2b and the etcd gate pass; nothing was NOT RUN from the list of rows.** No product
defect was found in this run. Four harness defects were found and fixed (section 9.5), and what was not exercised
is listed in 9.4. The accommodations of the first run for bugs 1, 2 (partly), 4 and 5 are gone; the
rest are listed in 9.3.

| | |
|---|---|
| Norn | `/Users/arti/Desktop/Claude/norn-v3-fleet-executor`, branch `feature/v3-fleet-executor`, product code at `5da7d433` (bugs 2, 7, 8 and follow-ups); harness changes in the commit that adds this section |
| norn-fleet | `/Users/arti/Desktop/Claude/norn-fleet-controller-executor`, branch `feature/fleet-controller-executor`, `bc1f8a1` (bugs 1, 3, 4, 5, 6, 9 and follow-ups), provisioned with the real Ansible role |
| VM | the same Lima VM `norn-exec` (Ubuntu 24.04, aarch64, OpenTofu 1.10.6), `/opt/norn-fleet-executor` removed first, ruby purged, role applied from `bc1f8a1` (44 ok, 12 changed, 0 failed, 3 skipped), `PyYAML` checked by the role's own "venv imports PyYAML" task |
| Date | 2026-10-05, 14:28 to 17:47 (CDT). Three environments (new PostgreSQL database, repository, empty MinIO state, re-enrolled executor each), see 9.6. |

### 9.1 Result per row

PASS/FAIL/NOT RUN is for this re-run. "First" is the first run's result from section 1.

| Row | Claim | First | Re-run | One line |
|---|---|---|---|---|
| 1 | merge, automatic converge (config-only) | PASS | **PASS** | Watcher recorded the plan, no approve, one attempt, `ingress-03` in the MinIO state. Now with a real provider (`hashicorp/null`) in the root and `sanity.py` running on the role-provisioned PATH with no ruby. |
| 2b (new) | merge inside the 5 minute revalidation window converges unattended after it | n/a (bug 2) | **PASS** | Pushed 5 s after a release; plan recorded 32 s after the release; the executor was refused `409 fleet_plan_revalidation_required` and rescanned every tick; the window closed; the stale plan was superseded (`ccf0b45e` to `a23746e2`) and applied at 373 s after the release, one attempt, no approve, no attestation. The harness did not wait, rescan or approve. |
| 2 | executable diff blocks on approval, `norn fleet approve`, converges | PASS | **PASS** | Unchanged: no attempt across more than one poll, `wait: the converge plan needs an approve`, one attempt after the approve. |
| 3 | kill a phase mid-apply, attestation, `--resume`, recovers | FAIL as shipped, PASS with W1 | **PASS, no W1** | Attestation correct; the recovery's attestation carried `tofuLock.id` equal to the stale apply lock's ID (`2e0fa2f6-...`), the lock was force-unlocked, recovery attempt succeeded. The lock probe ran with a plugin-backed provider (bug 1) and read the coloured lock error (bug 5). |
| 4 | `systemctl restart` mid-configure | PASS | **PASS** | Same boot, new invocation, no `norn-phase` process, checkpoints split across the two attempts. |
| 5 | power-off mid-apply, new `boot_id` attestation, resume | FAIL (2 of 2) | **PASS (5 completed runs)** | Every run: new boot_id in the attestation, stale lock unlocked, recovery succeeded, no manual `reclone`. No natural power cut left an empty object in the clone (4 of 4 clean, see 9.2). Run 5 injected the first run's damage and the loop re-cloned by itself and recovered. Details in 9.2. |
| 6 | lost attempt-start response replays | PASS | **PASS** | 6a one attempt after one lost response; 6b six requests with one `Idempotency-Key`, the journal replay returned the stored attempt, attested, resumed. |
| 7 | authority restart mid-run | PASS | **PASS** | 22 s outage, the loop's `GET .../attempts/<id>` requests got `Connection refused` and retried; one attempt, no recovery. |
| 8 | GitHub-lane dispatch racing the executor on one fence | PASS | **PASS** | 8a `409 fleet_target_execution_occupied`, no dispatch sent. 8c: exactly one winner (this time the GitHub lane, 201), the executor start got the same 409; the real loop then logged `busy: fence is Uncertain`. |
| 9 | force-push gives `history_rewritten` | PASS | **PASS** | Also when the old commit is pruned (compare 404); clears on revert. |
| 10 | stale `.tflock` of a killed `tofu apply`; kill during the recovery's own `tofu plan` (bug 7) | FAIL | **PASS** | Owned lock unlocked (recovery 1 attested `tofuLock.id` = lock 1). Recovery 1's own `tofu plan` was killed, leaving an `OperationTypePlan` lock. `status.active.attemptIds` was `["caf4bbab","199a032d"]` (oldest first, newest is `199a032d`). Recovery 2 used the right source, refused the foreign lock, and the `state_lock_foreign` report was recorded and shown. Operator `force-unlock` of the right ID cleared it, a third attempt (retry of `199a032d`) succeeded. Run once (see 9.4). |
| 10b | foreign lock refused, reported, cleared, single recovery completes | PASS with W1 | **PASS, no W1** | `root@host` `OperationTypePlan` lock: not unlocked, `fleet.converge.refusal` recorded, a wrong-ID `force-unlock` refused by tofu on MinIO, operator cleared, one recovery completed. |
| 11 | token self-rotation while idle | PASS | **PASS** | Same device, new 7-day token, old token 401, scope `fleet:execute`, file 0600, loop keeps polling. |
| 12 | cloned disk (new droplet ID) cannot attest | PASS (tail needed W1) | **PASS** | Whole row, including the original droplet's attestation and recovery, with no W1. Forged attestation `409 fleet_executor_stop_attestation_invalid`. |
| etcd gate | etcd runtime serves no executor-lane route | PASS | **PASS** | The real `norn-api` on etcd: no `fleet-executor-v1` capability, eight lane-only routes answer `501 backend_route_unsupported`. |
| `norn-exec-vm test` | default command, now under `unshare` | did not complete, killed every VM process | **PASS** | 92 tests OK, 0 skipped, in 7.3 s. The four VM-only tests (`PhaseHasNoCapabilities`, `...FromTheLoopUser`, `PhaseCannotReadAgeKey`, `AttestationRequiresEmptyPhaseUidRealCgroup`) ran and passed. Boot ID, uptime and the MainPIDs of the executor, minio, fake-do-metadata, socat and sshd were identical before and after. Run while the executor was idle. |

NOT RUN: nothing from the requested list. Not exercised, with reasons, are in 9.4.

### 9.2 Bug by bug

| Bug | First run | Now |
|---|---|---|
| 1 recovery lock probe socket path | harness used a provider-free root | Root uses `hashicorp/null` 3.2.4 (a real go-plugin provider; `null_resource.anchor` is in every plan). Every lock probe (rows 3, 5, 10, 10b, 12) worked on the standard `work_root` path. **Fixed.** |
| 2 revalidation-refused plan wedges | waited 5 min in the harness before every push | Row 2b: converges unattended after the window with no harness help. **Fixed.** Other rows still wait (9.3). |
| 3 recovery cannot resume at `complete` | reached once by a 68 s network stall | **Not exercised** (needs a stop between `readiness_verified` and `complete`). Unit tests only. |
| 4 no YAML parser in the venv | `ruby` installed as a workaround | The wheelhouse was built per the runbook (both architectures, `pip download --require-hashes`); **the aarch64 PyYAML hash passed** `pip download` on the Mac and the role's offline `--no-index --require-hashes` install in the VM. `venv/bin/python -c "import yaml"` is 6.0.3 from the venv (`include-system-site-packages = false`; the system python has 6.0.1). No ruby in the VM. `sanity.py` ran in every converge. **Fixed.** |
| 5 coloured tofu lock error | W1 `TF_CLI_ARGS=-no-color` in the SOPS file | No W1; the role's SOPS file is the unmodified fixture. Owned locks unlocked in rows 3, 5, 10, 10b (unlock) and 12. **Fixed.** |
| 6 power cut corrupts the clone | 2 of 2 runs, manual `reclone` | 4 natural power cuts: 0 empty objects after boot each time, recovery needed no help. A fifth run truncated 6 of the clone's newest object files (packs and their indexes) to 0 bytes before the power-off (the first run's damage, an injection, `Q1_ROW5_CORRUPT=1`); after boot there were 6 empty files and the journal said `the repository clone ... is damaged (its repository, HEAD or objects are unreadable): cloning it again`, then the recovery succeeded. **Fixed**; the natural cuts are clean because the clone is now fsynced (`core.fsync`, `fetch.unpackLimit=1`), which also means the repair path only ran under injection. |
| 7 `attemptIds` sorted by ID | wedged a plan about half the time | Row 10: two recoveries, correct source for the second, report accepted. **Fixed** (one run; with random IDs the old code failed half the time, so one pass is weak evidence, backed by the order being by attempt number now, see `derive.go`). |
| 8 VM script defects | `test` killed the VM; MinIO 410 | `norn-exec-vm test` passes and is safe. `provision` and `build-minio` were **not re-run** (the VM already had MinIO); the harness's MinIO source build is still its fallback. |
| 9 failed handler results not logged | silent journal | Visible in this run: `converge failed (terminal=True): init failed with exit code 1; attempt -` (the provider could not be downloaded, 9.5) and `recover waiting (terminal=False): state_lock_foreign; attempt -`. **Fixed.** |

Row 5 runs: (1) fsync of the MinIO lock object, clean clone, PASS; (2) fsync, PASS; (3) no fsync (`Q1_ROW5_NOSYNC=1`):
MinIO lost the lock object, as in the first run, so "unlock after a power cut" is not exercised in this run, clone
clean, recovery PASS; (4) injection that the power cut discarded (the injection was written before the fsync, so it
was lost; the clone was clean), PASS; (5) injection ordered before the fsync, so it survived: repaired, PASS. A sixth
start in the first environment aborted on a harness defect (9.5) and is not counted. The fsync is a harness
accommodation: MinIO does not fsync, a real Spaces backend is durable. The fsync can also flush the clone (an ext4
journal commit), which is why run 3 exists without it.

### 9.3 Accommodations that remain

| | Accommodation | Why |
|---|---|---|
| | Every row except 2b waits until the previous release + 5 min before it pushes (`wait_revalidation_window`). Rows 1, 2, 3, 4, 5, 6, 7, 8, 10, 10b and 12 wait; row 9 force-pushes without waiting (it records no plan) and row 11 pushes nothing. | So each row's own timeouts (plan within 150 s, etc.) stay meaningful. Without the wait the row would also pass but take up to 5 more minutes and the timeouts would need raising. Row 2b is the only row that tests the window. |
| | `hashicorp/null` is served from OpenTofu's implied local mirror (`/usr/local/share/terraform/plugins`) with the committed lock file's hashes. The registry download fails because `github.com` resolves to the fake GitHub in the VM and the registry serves checksums from `github.com`. | Harness network only. The plugin is still a real go-plugin child, so bug 1's socket path is exercised. |
| | Row 5 fsync of the MinIO lock object; Row 5 injection (run 5). | See 9.2. |
| | MinIO cross-built from source (kept from the first run). | `dl.min.io` 410. |
| | Test accelerators: remove `lock-foreign/*.json` after the operator clears a lock (30 minute back-off); shorten the recorded token expiry (row 11). | As in the first run. |
| | Authority from a test binary (`-tags fleetlocal`). | `validateFleetAuthorityOnly` pins a verify-full DSN, as in section 3.2. |

Removed: W1; the ruby install (and ruby was purged and checked absent); the provider-free fixture; manual `reclone`.

### 9.4 Not exercised and weak spots

- Bug 3 (resume at `complete`) was not reached.
- Row 10's P3 scenario ran once; row 7's outage was one 22 s outage.
- `norn-exec-vm provision` / `build-minio` were not re-run.
- The runbook's wheelhouse step, as written for a first provision, works. The role extracts the archive with
  `creates: requirements.txt` (`ansible/roles/fleet_executor/tasks/install.yml:262-268`), so on a host that already
  has an older wheelhouse the extraction is skipped and the digest assert fails; the runbook's "upgrading the release"
  paragraph lists three release variables, not the wheelhouse ones. Not tested here (the harness removed
  `/opt/norn-fleet-executor/{venv,wheelhouse,requirements.txt}` first); it fails loudly, not silently.
- The loop's rescan after a revalidation refusal uses one idempotency key per plan and generation
  (`scripts/executor/loop.py:229-231`); the rescans in row 2b were repeated every tick with that key and the supersede
  worked after the window, so this did not matter here.

### 9.5 Harness defects found and fixed

1. The pairing line was read from the whole journal, so a second `vm` run approved a stale enrollment code.
2. A new PKI was not picked up by the VM: `update-ca-certificates` without `--fresh` and `socat` never restarted
   (it reads the certificate once).
3. A repeated row 5 or row 8 rewrote an identical file, so its commit was empty and the watcher answered `none`
   (`fresh_name`). Row 8c read `WHEELHOUSE_SHA256`, which the VM script (not the role) wrote.
4. `/tmp` is wiped by row 5's reboot, so row 10's `tofu force-unlock` inspected an empty directory and printed
   `LocalState not locked`; `vm_tofu` now rebuilds the inspection tree. This cost one partial row 10 and 10b in the
   first environment (killed before 10b did anything; the product part of row 10 had behaved correctly), which were
   rerun from a clean environment.

The provider-download failure (the first baseline converge failed with `init failed`, retried every minute until the
mirror was installed) was a harness network effect, not a product defect.

### 9.6 Runs and state

| Environment | Rows |
|---|---|
| 1 (14:28) | 1, 2b, 2, 3, 4, 6, 7, 5 (twice, the second aborted), 12, 10 (product part passed, harness defect 4) |
| 2 (16:20) | 10, 10b, 5 (three runs), 9, 11, etcd gate, 8 (aborted twice by harness defect 3) |
| 3 (17:24) | 5 (injection), 8 |

Environments were recreated with the harness `reset` (new PostgreSQL database, repository, MinIO state, executor
re-enrolled; the VM and the role stayed). End state: Mac processes stopped; the databases `norn_q1_20261005142801`,
`norn_q1_20261005161946` and `norn_q1_20261005172428` dropped (the second and third by `reset`/`teardown`); `norn_fc` and
`norn_fc_recovery` untouched; the VM `norn-exec` running with the executor unit stopped. The work directory
(`results*/`, proxy and authority logs) is `<scratchpad>/q2/`. Run the harness with `python3`; `vm --as-shipped` no
longer exists. New env vars: `Q1_ROW5_NOSYNC=1`, `Q1_ROW5_CORRUPT=1`.

## Re-run on master integration (2026-10-10)

Status: **qualified on the integrated code.** Every requested row, the etcd gate and `norn-exec-vm test` passed; rows 4, 9, 11 and 12 were out of the requested scope and were NOT RUN. No product defect was found. Three harness defects are listed below. Nothing in the product code was patched.

| | |
|---|---|
| Norn | `/Users/arti/Desktop/Claude/norn-fleet-executor-master`, branch `feature/fleet-executor`, HEAD `8ccd3d3a` (executor lane on current `origin/master`, including the Fleet GitHub-lane fixes for cancelled runs and the reconcile route). Harness change in the commit that adds this section |
| norn-fleet | `/Users/arti/Desktop/Claude/norn-fleet-controller-executor`, branch `feature/fleet-controller-executor`, `9a6c798` (143 upstream commits merged; PLAN `resource_changes` / destructive consistency check; `tofu init -reconfigure`) |
| VM | Lima `norn-exec` (Ubuntu 24.04, aarch64, OpenTofu 1.10.6). Previous executor release, venv, wheelhouse and `requirements.txt` removed, then the real Ansible role applied from `9a6c798` (43 ok, 13 changed, 0 failed, 4 skipped) with the offline wheelhouse built from `scripts/requirements-executor.txt` (`venv/bin/python` imports PyYAML 6.0.3, no ruby) |
| Date | 2026-10-10, 14:43 to 18:36 (CDT). Two environments (new PostgreSQL database, repository, MinIO state each) |

| Row | Claim | Result | One line |
|---|---|---|---|
| 1 | automatic converge, real `hashicorp/null` provider | **PASS** | Plan recorded, no approve, one attempt, converged. |
| 2 | executable diff blocks on approval, `norn fleet approve`, converges | **PASS** | No attempt before the approve; one attempt after it. |
| 2b | merge inside the revalidation window self-heals | **PASS** | Converged unattended after the window with no harness help. |
| 3 | kill a phase mid-apply, attestation, `--resume` | **PASS** | Recovery unlocked the loop's own stale lock and succeeded. |
| 5 | power-off mid-apply, new `boot_id` attestation, resume (run once) | **PASS** | Natural power cut (no injection, no `Q1_ROW5_NOSYNC`), recovery needed no help. |
| 6 | lost attempt-start response replays (extra) | **PASS** | |
| 7 | authority restart mid-run (extra) | **PASS** | |
| 8 | GitHub-lane race on one fence (master's GitHub-lane code) | **PASS** | 8a: GitHub dispatch got 409 `fleet_target_execution_occupied`, nothing sent to GitHub, executor attempt succeeded. 8c: GitHub lane won (201), the executor start got 409 `fleet_target_execution_occupied`; the real loop then logged `busy: fence is Uncertain`. |
| 10 | stale lock of a killed `tofu apply`; kill during the recovery's own plan | **PASS** | |
| 10b | foreign lock refused, reported, cleared, single recovery | **PASS** | |
| etcd gate | etcd runtime serves no executor-lane route | **PASS** | Eight lane-only routes answer 501 `backend_route_unsupported`; no converge watcher on etcd. |
| `norn-exec-vm test` | default command in the VM | **PASS** | 92 tests OK; boot ID unchanged across the run. |
| 4, 9, 11, 12 | | **NOT RUN** | Not in the requested set. |

Harness defects found (no product bug):

1. The ports 18800 to 18805 were hard-coded and 18800 was already held by an unrelated `ssh -L` tunnel to another authority, so the harness's authority could not bind and the harness talked to that other authority (401s; nothing was sent that mattered). Fixed: `Q1_PORT_BASE` (default 18800) offsets all local ports. This run used 28800.
2. The VM clock jumped during the first pairing, so `vm_now()` was later than the journal's pairing line and `pairing_line` timed out after 240 s. The enrollment was approved by hand with the harness helpers. Not fixed (environmental, Lima clock); a re-run of `vm` works.
3. A row 8 run interrupted mid-way left a stopped executor and a leftover gate file, and the next run aborted with `aborted: 1`. It was re-run from `reset`, `vm`, `bootstrap`, `baseline` and passed. The `reset` sub-command is the supported way out of that state.

Row 5 note: the PASS here is a single natural power cut. The clone-repair path was not exercised (no injection).

End state: Mac processes stopped; database `norn_q1_20261010175031` dropped (the earlier `norn_q1_20261010144503` was dropped by `reset`); `norn_fc` and `norn_fc_recovery` untouched; the svc2 PostgreSQL and etcd left running; VM `norn-exec` running with the executor unit stopped. Work directory `<scratchpad>/q4/` (`rows1.log`, `row8b.log`, `work/results/`).
