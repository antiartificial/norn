# Norn v3 / Fleet session resume — 2026-09-26

This is the current entry point for the next session. Read the detailed [M0–M3 handoff](m0-m3-handoff-2026-09-25.md) and [execution milestones](execution-milestones.md) for the contract and qualification history. Recheck Git, CI, Mini, Fleet, and provider state before acting; the observations below are a dated snapshot.

## Objective and release boundary

Make v3 work pragmatically through focused, reviewable Norn and Fleet changes. Prefer pure contracts, deterministic tests, isolated PostgreSQL/MySQL/Nomad/Consul/etcd fixtures, and private Mini copies before provisioning or mutating live resources. Keep source intent, signed proof, runtime state, and release qualification distinct. No v3 deployment, protected-master merge, provider cutover, or milestone sign-off is authorized by a green PR alone.

M0–M3 are the current grouped integration scope. M4–M9 remain the app-capacity, upgrade, migration, release, and adoption path. Do not report an overall milestone percentage as verified: the gates in the detailed handoff remain open, and implementation/test counts are not a release-completion denominator.

## Verified Git and CI snapshot

- Norn checkout: `/Users/arti/Desktop/Claude/norn-v3-m0-m3-release-integration`, branch `codex/v3-m0-m3-release-integration`, draft [PR #76](https://github.com/antiartificial/norn/pull/76). All eight checks passed at exact head `d072fbd` on 2026-09-26 ([run 36267168802](https://github.com/antiartificial/norn/actions/runs/36267168802)); the worktree was clean at the check before this note was updated.
- Fleet companion: draft [PR #176](https://github.com/antiartificial/norn-fleet/pull/176), published head `6850fd6482f4f5b34bb0b341d00b3d4678b64a81`. Its `contract` job did not start on that exact head: the GitHub run has no steps and the account billing/spending-limit annotation remains the known blocker. The pinned local Python contract suite passed 1,400 tests; hosted CI and protected bootstrap remain open.
- The `norn-fleet` checkout at `/Users/arti/Desktop/Claude/norn-fleet` is `main`, ahead 4 and behind 157 relative to `origin/main`. Do not reset or clean it as part of PR work without inspecting its commits and worktrees.
- Norn worktree inventory at this snapshot: main `/Users/arti/Desktop/Claude/norn` on `feature/durable-app-recovery-ui`; detached `norn-platform-pilot260908a`; active PR worktree above. Preserve dirty worktrees and remove a worktree only after verifying it is no longer active and contains no unique work.

## Source completion slice

The source completion change closes the local command gap in three code files:

- `v2/api/store/mysql_source_snapshot_completion.go` (new): attempts to terminalize a retained, signed MySQL source snapshot operation as `succeeded` under its exact live claim while retaining the source runtime fence and locked MySQL account.
- `v2/api/cmd/norn-mysql-maintenance/source.go`: invokes that completion after retention proof; same-key replay requires terminal success and reports `status=succeeded retention=retained-proved`.
- `v2/api/worker/wordpress_deploy_nomad_integration_test.go`: expects terminal success and asserts expired-operation recovery does not change it.

The gap was real: the source command retained its artifact but left its operation `running` after exit. The PostgreSQL-backed source intent test now rejects completion before retention, accepts it after signed proof, verifies the fence remains held, and checks that recovery preserves terminal success. Expired source and restore-recovery claims now fail for inspection while preserving the fence. The disposable compiled-command WordPress source/restore/resume fixture passed on 2026-09-26. The private recovery command now supports separate `--accept-only` and read-only `--inspect-only` steps; the fixture passed both before the effectful recovery. Check PR #76 CI at the final published head. These changes do not qualify a remote provider or live Mini/Fleet runtime.

## Evidence and remaining gates

- Local compiled commands have exercised signed deployed-source admission, S3-emulator retention, signed restore admission, restore, recovery, same-key replay, wrong-target rejection, and WordPress HTTP on the recovered MySQL 8.4 target. On code head `742bac1`, `bash v2/scripts/test-wordpress-restore-qualification.sh` passed the compiled `inspect-source` control and `--observe-external` paths (Nomad stopped, MySQL account locked, retained object verified) alongside the restored source marker, populated `wp_options`, and installed `wp_users` in 77.27 seconds. The disposable MySQL/PostgreSQL containers and Nomad/Consul listeners were absent after cleanup. The following docs-only head `d072fbd` passed all eight PR checks. Direct MySQL adapter TLS/dump tests and private PostgreSQL-backed tests also passed. WordPress is the application-level data/resumption fixture; it does not replace the direct MySQL adapter tests.
- A fresh exact-PR-head private Mini copy passed migrations 1–39, reader/writer contract 5/31, passive startup, and full original-row fingerprints across 253,463 copied rows. This was owner-local socket work; live Mini stayed at `v2.20.0-platform-30-ga5da8ef`, with zero active operations and no Fleet pools at the last read-only check. Recheck before use.
- Local MinIO and separate-process emulator checks do not prove real provider durability or separate-node restore. `NORN_TEST_S3_CONFIG_FILE` was absent in the working shell; no real S3 test object was created. A dedicated pre-existing versioned/object-locked bucket and approved credential are required for that qualification. The provider test creates compliance-locked objects for 24 hours; inspect its exact scope before running.
- Open M2 gates include real-provider/cross-node retention and restore, signed failure reconciliation, least-privilege fence account, managed MySQL/WordPress restore and rollback, and Mini runtime qualification. Open M3 gates include protected live Fleet bootstrap, member/fault/restore/soak proof, and the Fleet CI contract check. M0/M1/M5 and release gates remain as listed in the detailed handoff.
- On the local `desktop-linux` Docker context, the most recent inventory showed **zero containers**. Images, 290 volumes, and build cache were left untouched. Clean disposable test containers/scratch as each fixture ends; inspect before pruning any pre-existing images or volumes.

## First actions in the next session

1. Read this file and `git status --short --branch` in the Norn PR worktree; verify PR #76/#176 heads and checks.
2. Build the next M2 reconciliation slice for ambiguous source stop and account lock. `inspect-source --observe-external` now provides advisory observations, but `stop-intended` and `lock-intended` cannot yet advance from independently verified outcomes. Require exact accepted identity, live claim and fence, fresh external observation, and durable evidence before any state transition; never repeat an ambiguous Nomad stop or MySQL account mutation merely because its response was lost. The separate signed recovery reconciliation path already handles an expired recovery whose target is independently proved unlocked; a locked or indeterminate target remains fenced. Continue failure qualification for publication, transfer, import, and cleanup.
3. Continue the pure/local M2 and M3 qualification that does not require new cloud resources. Keep provider, separate-node, managed DB, and live Mini/Fleet gates explicitly pending until their own evidence exists.
4. Refresh the gate disposition after each slice; report milestone progress by passed/open gates rather than a guessed percentage.
