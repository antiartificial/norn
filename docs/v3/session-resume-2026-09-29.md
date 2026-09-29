# Norn v3 / Fleet release handoff — 2026-09-29

This is the current resume point. Recheck repository heads, signed artifacts,
Mini health, provider inventory, prices, and credentials before mutation. Git
history retains the 2026-09-28 handoff and earlier snapshots. The release
contract and full exit gates remain in [execution milestones](execution-milestones.md).

## Release target and gate status

Initial v3 still requires both an existing Mini v2/PostgreSQL → v3/PostgreSQL
upgrade that preserves workloads and data, and a fresh DigitalOcean HA Fleet
with three etcd control members and separate application databases. A
representative application move is a separate rehearsal. Source integration,
a signed package, local drills, and a green PR check are evidence for their
specific layers; none alone signs M0–M9 or proves a live Fleet.

The immediate operational priority is a **bounded disposable Fleet pilot**,
followed by verified full retirement. The owner approved brief test spending
and wants time to run workloads on the pilot; no test resources should remain
up for hours without ongoing tests. A fresh run needs an agreed workload test
window, current quote, signed cutoff/retire-by, Gate 3 GO, exact protected
runner, reviewed plan, and a working finalizer before provider create.

## Verified source and runtime state

- Norn [PR #77](https://github.com/antiartificial/norn/pull/77) merged to
  `master` as `8ac21f8e24bc3add8b1f4d3a1ae34fd5d2cd9b21`. The public
  [platform release](https://github.com/antiartificial/norn/releases/tag/platform-8ac21f8e24bc3add8b1f4d3a1ae34fd5d2cd9b21)
  has platform archives, manifests, signatures, and SBOMs for Darwin and
  Linux. Asset presence is not signature verification or an installed release.
- Fleet [PR #176](https://github.com/antiartificial/norn-fleet/pull/176)
  and [#177](https://github.com/antiartificial/norn-fleet/pull/177) merged.
  [PR #188](https://github.com/antiartificial/norn-fleet/pull/188) merged to
  `main` as `36f9304c6139bd9e069e5e3158ba857f9d4c0c14` after its
  exact-head validation passed on a temporary self-hosted runner. It rejects
  an external-Mac pilot runner whose name differs from
  `norn-pilot-<run-id>-mac`. The runner and CI routing variable were removed
  after validation. GitHub-hosted Actions capacity remains exhausted; use the
  documented narrow self-hosted fallback for source checks and the exact
  protected pilot runner for plan/apply.
- The Mini still reports `v2.20.0-platform-30-ga5da8ef` at `/api/version`.
  A 2026-09-29 authenticated inventory found zero active Norn operations,
  host status OK, 29 app entries, and no configured Fleet node pools. This
  inventory does not prove a v3 Mini upgrade.

## Mini control recovery

The development Mini takes local control PostgreSQL custom-format checkpoints
about every ten minutes and publishes each verified archive to the temporary
personal NYC3 Space `norn-mini-control-temp-20260928-3830ba`. Its remote
health job uses a 15-minute freshness threshold. On 2026-09-29, the latest
remote archive was downloaded, its SHA-256 was checked against the manifest,
and it restored 28 public tables into an isolated database; the drill database
was removed. This is a verified logical restore, not a point-in-time or
host-loss availability guarantee. Before a Mini upgrade, repeat the protected
backup and restore for that exact maintenance window, drain operations, and
exercise the reviewed legacy-binary rollback fence.

The Mini and Space checkpoint jobs now keep a rolling 48-hour window. The
Space had 320 objects and about 2.25 GB before the remote retention change;
the first post-change upload and health check passed, and no objects were yet
old enough to prune. Confirm an aged pair is eventually removed. The Space,
its scoped credential, and scheduled jobs are temporary and must be retired
when this recovery bridge is no longer needed.

## Disposable Fleet attempt and cleanup

The 2026-09-29 `pilot260929e` attempt stopped **before compute creation**:
the admitted runner had a different name from the protected workflow's exact
selector. The scoped state Space, project, keys, Tailscale tags/tokens,
runner, local controller, and queued job were retired. The correction is now
on Fleet `main` via #188. Do not reuse that run's expired cutoff, credentials,
Gate 3 decision, backend, or source binding; create a fresh run.

A 2026-09-29 read-only inventory of the personal DigitalOcean account found
no Norn pilot Droplets, managed databases, load balancers, or projects. Two
stale offline pilot runner registrations were then removed after exact-name,
label, idle-state, and no-active-workflow checks; each GET returned 404.
Only the shared external-Mac finalizer runner remained registered. The
temporary Mini backup Space remains active and is separate from pilot compute.

## Next sequence

1. Agree on a short workload test window and derive a fresh run ID, quote,
   cutoff, and retire-by. Keep the existing owner authorization for brief test
   spending; do not infer that an old signed run approval is reusable.
2. Verify the exact released Norn candidate and current Fleet `main` source,
   then run local/GitHub qualification through the documented self-hosted
   fallback while hosted Actions capacity is unavailable.
3. Complete Gate 2 and Gate 3 for the fresh run. Register the exact protected
   runner name; check it against the workflow selector before backend or
   provider setup. Verify finalizer readiness and the signed retirement path.
4. Execute the reviewed plan/apply, prove provider inventory and runtime
   readiness, let the owner test workloads, then run the reviewed retirement
   and final-zero path. Independently verify provider absence and retain
   evidence and billing review.
5. Continue M5 Mini upgrade and M6/M7 application database/mobility work as
   separate gates. Do not count the disposable pilot as the Mini cutover or
   as full v3 release qualification.
