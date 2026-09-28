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

## Branches and latest checked state

- Norn draft [PR #76](https://github.com/antiartificial/norn/pull/76):
  `codex/v3-m0-m3-release-integration` targets
  `feature/norn-v3-planning-handoff`. At the pre-edit check, head
  `7a0c98960cfd690a9eb073f9f9ae9cfb1173ea4c` had nine passing checks;
  the worktree was clean. The Mini checkpoint and handoff cleanup reached
  `e9ca9620b6fe358ef116f2c4d2440105a833e0de`; its Repository CI run
  `36450512637` passed all seven jobs. Recheck later heads before using CI.
  A later integration slice imported the master Fleet pilot and restored all
  six protected-master CI context names while retaining v3 checks. At
  `94cf2c38516a22e1ea6daa95bd85d9fb8d1d21fe`, run `36452946563`
  passed all 11 jobs, including macOS API/CLI, Fleet pilot and OpenAPI.
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
schedule jitter. It decrypts only the `NORN_DATABASE_URL` from the active,
owner-only API SOPS file, creates a read-only custom dump and
digest manifest, and keeps successful local checkpoints for 48 hours. It then
uploads the verified pair to private personal DigitalOcean Space
`norn-mini-control-temp-20260928-3830ba` in `nyc3` with a bucket-scoped key,
reads the full remote dump back to verify SHA-256 and length, and publishes
the manifest last. The Space has a seven-day expiration rule for `control/`.
The broader key used only to create the Space was revoked.
The Mini uploader and health probe use an owner-local Python venv with the
versions pinned in `v2/scripts/mini-control-checkpoint-requirements.txt`.
`com.norn.control-checkpoint-health` runs every 300 seconds and checks the
newest completed Space manifest's creation and publication age against 900
seconds, plus the dump size/digest metadata. It writes owner-only
`space-health.json` beside the local checkpoints and exits nonzero if stale
or unavailable. A normal check passed; forced stale and missing-credential
checks failed as intended. This is a local health signal, not an operator
notification or sustained RPO proof.
After the active binding, scheduled remote checkpoints at 16:31 and 16:41 UTC
both completed; the next five-minute health run at 16:42 UTC passed against
the latter object. This is a short observation window, not a full-cycle RPO.

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

On 2026-09-28 the staged SOPS ciphertext was promoted to the active API file
after confirming that it added only the exact database URL and first audit
signing key. The previous active ciphertext is saved at
`/Users/0xadb/.config/norn/api.env.enc.json.pre-v3-binding-20260928`.
The same installed v2 release restarted healthy. Before activation, 61,119
completed audit rows had no digest/key ID; there were no audit incidents or
Fleet capacity-plan operations. A fresh protected backup under the now-active
runtime URL/key was created as
`control-20260928T162830Z-985ead5a` (13,887,820 bytes,
SHA-256 `5afadf3f6aab4d1b5f16b3571d9d16375c830be0de8927121ef75f09f7bfeda8`).
The owner-only dump and proof remain on Mini under
`/Users/0xadb/.local/share/norn-control-protected-20260928/`, with verified
remote copies under `control/protected/` in the temporary Space. The
read-only legacy-baseline doctor passed runtime binding, backup proof,
legacy release, sole listener ownership, running process freshness, and
zero active operations. It blocked on the reviewed script and exact signed
v3 candidate release. A separate Mac downloaded the remote protected pair,
verified its SHA-256/length, and restored it into isolated PostgreSQL 17:
28 public tables, 61,125 audit rows, 483 operations and 483 deployments.
The disposable container, image, credential copy and downloaded bytes were
removed. This is an M5 backup/restore checkpoint, not an upgrade rehearsal.
The temporary Space's seven-day expiration is not a durable release policy.

Next: observe multiple scheduled intervals, connect failed health to an
actionable operator alert, rehearse recovery after source-host loss with
identity/auth and history checks, and measure the complete operator recovery
time. Keep M0 and
M5 open until their distinct proof requirements are met. When this temporary
setup is no longer needed, unload and remove both checkpoint LaunchAgents,
remove the scoped credential from the Mini, revoke the key named
`norn-mini-control-temp-20260928`, empty and delete the named Space, then
remove its local checkpoints only after verifying no retained recovery need.

## Other release gates

| Gate | Next decisive evidence |
| --- | --- |
| M1 | External-effect fencing and reconciliation coverage; Mini compatibility and rollback. |
| M2 | Real-provider retention and separate-node restore; managed MySQL/WordPress recovery; archive/profile parity. |
| M3 | Protected separate-host bootstrap, quorum/fault/restore/soak, PG-free operation and hosted Fleet contract CI. |
| M4 | Normal etcd app admission and worker dispatch, observed ingress weight, loaded placement/drain on protected hosts. See the [M4 sequence](m4-etcd-app-admission-sequence-2026-09-26.md). |
| M5 | Retain the production-key backup beyond the temporary Space policy; prepare the reviewed exact signed candidate and rehearse isolated upgrade/rollback preserving jobs/routes/identities. |
| M6–M7 | Running v3 upgrade, supported app DB cutover, and one representative Mini-to-Fleet app move with rollback. |
| M8–M9 | Signed qualified release, client/fault/soak evidence, then separately approved adoption. |

The direct PR-to-`master` path is still not reviewable. A refreshed read-only
merge simulation against `origin/master` at `0c21b661` found 126 conflicted
paths: 65 under `v2/api`, 19 under `v2/infra`, 18 under `v2/ui`, and smaller
workflow, script and documentation groups. The master-only Fleet pilot and
its six required CI contexts now pass on the draft v3 branch; code and
workflow conflicts with the 39 master-only commits remain. Integrate those
in dependency-ordered slices while preserving current master behavior and v3
tests. Only a reviewed exact commit merged to protected `master` can enter the
signed production
release lane. The release integration audit remains available with
`git show 7a0c9896:docs/v3/release-gate-status-2026-09-26.md`.

## Worktree safety

Preserve unrelated Norn and Fleet worktrees. This checkout is the draft Norn
PR branch; the Fleet companion checkout is
`/Users/arti/Desktop/Claude/norn-fleet-m3-host-etcd`. The main
`/Users/arti/Desktop/Claude/norn-fleet` checkout was previously ahead and
behind its upstream and must not be reset as part of v3 work. Run
`git status --short --branch` before staging or changing either repository.
