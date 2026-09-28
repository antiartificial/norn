# V3 release work: current handoff

Updated 2026-09-28. This is the single current entry point. Recheck the exact
Git heads, CI, Mini, Fleet and provider state before acting. Historical handoff
snapshots remain in Git history; topic notes in this directory retain the
detailed contracts and test evidence.

## Goal and release boundary

Qualify one initial v3 release for both the existing Mini (v2/PostgreSQL to
v3/PostgreSQL, preserving workloads and application data) and a fresh,
independent three-member etcd Fleet. Then rehearse moving one representative
app from Mini to Fleet. [Execution milestones](execution-milestones.md) define
M0–M9. M8 is release qualification; M9 is separately scoped adoption. No v3
Mini/Fleet deployment, protected-master merge, provider cutover or milestone
sign-off has occurred. The signed gate count remains **0/10**.

## Current branches and live checks

- Norn draft [PR #76](https://github.com/antiartificial/norn/pull/76):
  `codex/v3-m0-m3-release-integration` at
  `7a0c98960cfd690a9eb073f9f9ae9cfb1173ea4c`, targeting
  `feature/norn-v3-planning-handoff`. All nine reported checks passed at this
  head on 2026-09-27. The worktree was clean before this M0 checkpoint and
  handoff cleanup; the current edits are not yet included in that PR head.
- Fleet draft [PR #176](https://github.com/antiartificial/norn-fleet/pull/176):
  `codex/fleet-m3-host-etcd` at
  `6267655052b209b22dc8b3421cb9339f797af9bc`, targeting `main`.
  Hosted `contract` failed before runner assignment under the GitHub account
  billing/spending-limit condition; local tests do not replace hosted CI.
- On 2026-09-28 the Mini inventory returned healthy host status, zero active
  operations, 29 app entries, 13 active incidents and `fleet_configured=false`.
  Production readiness remained blocked. This is a read-only platform
  snapshot, not a control-backup or release result.

## Pragmatic next lane: Mini control recovery

The owner selected a **15-minute target** for the expendable development Mini,
accepting a 24-hour fallback if the tighter mechanism proves disproportionate.
The Mini now has `com.norn.control-checkpoint`, a user LaunchAgent with a
600-second interval to leave room inside the 15-minute target for upload and
schedule jitter. It decrypts only the `NORN_DATABASE_URL` from the
inactive, owner-only v3 binding stage, creates a read-only custom dump and
digest manifest, and keeps successful local checkpoints for 48 hours. It then
uploads the verified pair to private personal DigitalOcean Space
`norn-mini-control-temp-20260928-3830ba` in `nyc3` with a bucket-scoped key,
reads the full remote dump back to verify SHA-256 and length, and publishes
the manifest last. The Space has a seven-day expiration rule for `control/`.
The broader key used only to create the Space was revoked.

The first scheduled remote run exited 0. Its exact object was downloaded on a
second Mac, verified against the remote manifest, and restored into an
isolated PostgreSQL 17 container with 28 public tables. The disposable
container, image and local download were removed. This proves one checkpoint,
transfer and private restore. It does **not** yet prove a 15-minute bound over
host sleep, login/logout, scheduler failure or sustained operation; nor a
source-host-loss drill with identities/auth/history, a signed M5 candidate,
or the draft 30-minute end-to-end RTO. The last measured control PostgreSQL
`archive_mode` was `off`; this is frequent full dumps, not PITR. The
one-hour-fresh `norn.legacy-control-backup/v1` proof belongs to the M5 upgrade
transition; keep that age rule separate from checkpoint retention. See the
[recovery decision](m0-mini-control-recovery-decision.md),
[measured budget](m0-mini-control-budget-proposal-2026-09-26.md), and
[protected M5 backup path](m5-protected-backup-private-restore.md).

Next: observe multiple scheduled intervals, alert on a missed/failed remote
checkpoint, rehearse recovery after source-host loss with identity/auth and
history checks, and measure the complete operator recovery time. The one
restored dump was produced with the staged database URL, not the active M5
audit-signing key. Keep M0 and M5 open until their distinct proof requirements
are met. When this temporary setup is no longer needed, unload and remove the
LaunchAgent, remove its scoped credential from the Mini, revoke the key named
`norn-mini-control-temp-20260928`, empty and delete the named Space, then
remove its local checkpoints only after verifying no retained recovery need.

## Other release gates

| Gate | Next decisive evidence |
| --- | --- |
| M1 | External-effect fencing and reconciliation coverage; Mini compatibility and rollback. |
| M2 | Real-provider retention and separate-node restore; managed MySQL/WordPress recovery; archive/profile parity. |
| M3 | Protected separate-host bootstrap, quorum/fault/restore/soak, PG-free operation and hosted Fleet contract CI. |
| M4 | Normal etcd app admission and worker dispatch, observed ingress weight, loaded placement/drain on protected hosts. See the [M4 sequence](m4-etcd-app-admission-sequence-2026-09-26.md). |
| M5 | Production-key protected backup, exact signed candidate, isolated Mini upgrade and rollback preserving jobs/routes/identities. |
| M6–M7 | Running v3 upgrade, supported app DB cutover, and one representative Mini-to-Fleet app move with rollback. |
| M8–M9 | Signed qualified release, client/fault/soak evidence, then separately approved adoption. |

The direct PR-to-`master` path is not reviewable: the 2026-09-27 read-only
merge simulation found 126 conflicts and different required CI contexts.
Integrate in dependency-ordered slices against current protected `master`,
preserving its macOS and pilot checks as well as v3 tests. Only a reviewed
exact commit merged to protected `master` can enter the signed production
release lane. The release integration audit remains available with
`git show 7a0c9896:docs/v3/release-gate-status-2026-09-26.md`.

## Worktree safety

Preserve unrelated Norn and Fleet worktrees. This checkout is the draft Norn
PR branch; the Fleet companion checkout is
`/Users/arti/Desktop/Claude/norn-fleet-m3-host-etcd`. The main
`/Users/arti/Desktop/Claude/norn-fleet` checkout was previously ahead and
behind its upstream and must not be reset as part of v3 work. Run
`git status --short --branch` before staging or changing either repository.
