# Norn v3 / Fleet session resume — 2026-09-26

This is the current entry point for the next session. Read the detailed [M0–M3 handoff](m0-m3-handoff-2026-09-25.md) and [execution milestones](execution-milestones.md) for the contract and qualification history. Recheck Git, CI, Mini, Fleet, and provider state before acting; the observations below are a dated snapshot.

## Objective and release boundary

Make v3 work pragmatically through focused, reviewable Norn and Fleet changes. Prefer pure contracts, deterministic tests, isolated PostgreSQL/MySQL/Nomad/Consul/etcd fixtures, and private Mini copies before provisioning or mutating live resources. Keep source intent, signed proof, runtime state, and release qualification distinct. No v3 deployment, protected-master merge, provider cutover, or milestone sign-off is authorized by a green PR alone.

M0–M3 are the current grouped integration scope. M4–M9 remain the app-capacity, upgrade, migration, release, and adoption path. Do not report an overall milestone percentage as verified: the gates in the detailed handoff remain open, and implementation/test counts are not a release-completion denominator.

## Verified Git and CI snapshot

- Norn checkout: `/Users/arti/Desktop/Claude/norn-v3-m0-m3-release-integration`, branch `codex/v3-m0-m3-release-integration`, draft [PR #76](https://github.com/antiartificial/norn/pull/76). The last tested code head before this handoff note was `30b14b9742ddc26171a27561757dfac4a936f52f`; all eight reported checks succeeded at that head on 2026-09-26. No new local source-completion work below is in that code head.
- Fleet companion: draft [PR #176](https://github.com/antiartificial/norn-fleet/pull/176), published head `2f2edf4538031b1e1c00682fdcbe950ec3d03139`. Its `contract` check reports failure; the failed job log was unavailable on this check, so diagnose the current CI failure before claiming its cause or rerunning.
- The `norn-fleet` checkout at `/Users/arti/Desktop/Claude/norn-fleet` is `main`, ahead 4 and behind 157 relative to `origin/main`. Do not reset or clean it as part of PR work without inspecting its commits and worktrees.
- Norn worktree inventory at this snapshot: main `/Users/arti/Desktop/Claude/norn` on `feature/durable-app-recovery-ui`; detached `norn-platform-pilot260908a`; active PR worktree above. Preserve dirty worktrees and remove a worktree only after verifying it is no longer active and contains no unique work.

## Working local change — not yet verified or published

The active PR worktree has three uncommitted files:

- `v2/api/store/mysql_source_snapshot_completion.go` (new): attempts to terminalize a retained, signed MySQL source snapshot operation as `succeeded` under its exact live claim while retaining the source runtime fence and locked MySQL account.
- `v2/api/cmd/norn-mysql-maintenance/source.go`: invokes that completion after retention proof; same-key replay requires terminal success and reports `status=succeeded retention=retained-proved`.
- `v2/api/worker/wordpress_deploy_nomad_integration_test.go`: expects terminal success and asserts expired-operation recovery does not change it.

The gap is real: the source command currently retains its artifact but leaves its operation `running` after exit; generic expiry recovery has no source-operation branch. The proposed fix is **unreviewed and untested**. Do not present it as completed. Finish the store integration test (reject completion before retention; accept after signed proof; verify fence stays held and terminal state survives recovery), run `gofmt`, focused tests, and the disposable `v2/scripts/test-wordpress-restore-qualification.sh` fixture. Review the SQL/claim race boundary, update the source-command and main handoff docs, then selectively commit and push if green. Use only the configured human Git identity and no AI/coauthor trailers. Check PR #76 CI at the resulting exact head.

## Evidence and remaining gates

- Local compiled commands have exercised signed deployed-source admission, S3-emulator retention, signed restore admission, restore, recovery, same-key replay, wrong-target rejection, and WordPress HTTP on the recovered MySQL 8.4 target. Direct MySQL adapter TLS/dump tests and private PostgreSQL-backed tests also passed. WordPress is the application-level data/resumption fixture; it does not replace the direct MySQL adapter tests.
- A fresh exact-PR-head private Mini copy passed migrations 1–39, reader/writer contract 5/31, passive startup, and full original-row fingerprints across 253,463 copied rows. This was owner-local socket work; live Mini stayed at `v2.20.0-platform-30-ga5da8ef`, with zero active operations and no Fleet pools at the last read-only check. Recheck before use.
- Local MinIO and separate-process emulator checks do not prove real provider durability or separate-node restore. `NORN_TEST_S3_CONFIG_FILE` was absent in the working shell; no real S3 test object was created. A dedicated pre-existing versioned/object-locked bucket and approved credential are required for that qualification. The provider test creates compliance-locked objects for 24 hours; inspect its exact scope before running.
- Open M2 gates include real-provider/cross-node retention and restore, signed failure reconciliation, least-privilege fence account, managed MySQL/WordPress restore and rollback, and Mini runtime qualification. Open M3 gates include protected live Fleet bootstrap, member/fault/restore/soak proof, and the Fleet CI contract check. M0/M1/M5 and release gates remain as listed in the detailed handoff.
- On the local `desktop-linux` Docker context, the most recent inventory showed **zero containers**. Images, 290 volumes, and build cache were left untouched. Clean disposable test containers/scratch as each fixture ends; inspect before pruning any pre-existing images or volumes.

## First actions in the next session

1. Read this file and `git status --short --branch` in the Norn PR worktree; verify PR #76/#176 heads and checks.
2. Complete and test the uncommitted source-operation fix, then update/push the focused PR slice only if it passes.
3. Continue the pure/local M2 and M3 qualification that does not require new cloud resources. Keep provider, separate-node, managed DB, and live Mini/Fleet gates explicitly pending until their own evidence exists.
4. Refresh the gate disposition after each slice; report milestone progress by passed/open gates rather than a guessed percentage.
