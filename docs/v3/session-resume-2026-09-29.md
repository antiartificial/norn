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
followed by verified full retirement. Earlier spending approval covered only
the external-Mac run and its fixed window. The larger paired $10/four-hour
proposal is not yet authorized. No test resources should remain up without
ongoing tests. The paired topology is the intended path to
real Fleet fitness evidence; it is one bounded qualification slice, not a
substitute for the full M0–M9 release contract. Use the focused
[Fleet fitness matrix](fleet-fitness-pilot-matrix-2026-09-29.md) and preserve
enough of the signed window for retirement and final-zero verification.

## Verified source and runtime state

- Norn [PR #77](https://github.com/antiartificial/norn/pull/77) merged to
  `master` as `8ac21f8e24bc3add8b1f4d3a1ae34fd5d2cd9b21`. The public
  [platform release](https://github.com/antiartificial/norn/releases/tag/platform-8ac21f8e24bc3add8b1f4d3a1ae34fd5d2cd9b21)
  has platform archives, manifests, signatures, and SBOMs for Darwin and
  Linux. On 2026-09-29 the Darwin/ARM64 and Linux/AMD64 bundles passed the
  repository's archive, manifest and Ed25519 signature verifier with the
  Mini's pinned public key. Neither package is installed on the Mini or a Fleet.
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
- Fleet [PR #189](https://github.com/antiartificial/norn-fleet/pull/189)
  merged to `main` as `01f4151374ecb84778003bf8ff8e14a2a2fa42a4`,
  with Aaron Barton as both Git author and committer. The receipt bridge now
  verifies a one-commit human PR fast-forward while retaining its signed
  receipt, parent, PR and protected-main checks. The full local contract suite
  passed 1,424 tests with three skips, and the required PR `contract` check
  passed on a temporary self-hosted Linux runner. The exact-main
  [no-cloud macOS qualification](https://github.com/antiartificial/norn-fleet/actions/runs/36621323531)
  passed and published artifact
  `pre-release-pilot-qualification-01f4151374ecb84778003bf8ff8e14a2a2fa42a4`
  (GitHub artifact SHA-256 `a57702ef56e89babd085c44e945a44af786f2af27244b48ff38199a79dffa631`).
  Its receipt reports 282 bounded tests and no cloud/Tailscale mutation or
  OIDC use. Both temporary runners deregistered; only the shared finalizer
  remains. A source change after this commit needs a new qualification.
- Fleet `main` advanced through the protected plan-bridge correction at
  `f5910c35c6a191153c743c032114d337ad67e9e3`. Human receipt
  [PR #194](https://github.com/antiartificial/norn-fleet/pull/194) then
  fast-forwarded the exact one-commit, receipt-only head
  `15f7f237bf5d91d636e1ebf5a1c871386781e34c` to protected `main` after its
  required `contract` check passed. GitHub reported the PR merged at that same
  SHA; its author and committer were both Aaron Barton. This is reviewed Fleet
  intent, not provider execution or live Fleet evidence.
- Protected qualification
  [run 36639496593](https://github.com/antiartificial/norn-fleet/actions/runs/36639496593)
  succeeded on attempt 2 at exact protected head `15f7f237`. Attempt 1 reached
  the artifact upload and failed only when that upload timed out. The retained
  receipt was downloaded as `R/pre-release-pilot-qualification-B.json` and
  verified for the resumed run. This is no-cloud qualification, not a provider
  apply or live runtime result.
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

The successor run `pilot260929g` reached partial pre-create preparation. Its signed
cutoff is `2026-09-30T00:00:00Z` and its `retireBy` is
`2026-09-30T01:00:00Z`. The signed Gate 3 `GO` is retained at
`R/gate3-go.json`. Controller prepare reached the verified dormant state with
HTTP 409 as the expected no-post-create-binding response, zero CAS writes and
the reviewed network route adopted. Local PKI and bootstrap material are
prepared.

The isolated `norn-pilot260929g-state` Space was created in NYC3 with versioning
enabled and its scoped key. The broad bootstrap key was revoked; the retained
bootstrap receipt is `R/backend/bootstrap-receipt.json`. No compute, load
balancer, managed database or live Fleet has been created. The four shared
finalizer variables were corrected and read back against the current run's
evidence root, final-zero output and finalizer key/key ID.

The exact Tailscale pilot tag-owner policy was saved and read back through the
authenticated administrator. Five short-lived node keys and their receipts
were then issued successfully under the existing authority/OAuth client.
Before provider compute, review found that the signed Gate 3 GO bound
`github-app-installation-evidence-p2.json`, while the protected workflow derives
and requires the canonical sibling `github-app-installation-evidence.json`.
The runtime exact-path check therefore rejects this GO. Because backend and
node-key preparation has already occurred, a new local preflight cannot
truthfully repeat the signed pre-Gate-4 absence observations in this epoch.
`pilot260929g` entered the supported partial-precreate abort path; the cleanup
and final-zero are now complete as recorded below. Preserve its original GO. None of this preparation is live
Fleet evidence or a milestone sign-off.

## Latest cleanup and launch preparation

Fleet [PR #195](https://github.com/antiartificial/norn-fleet/pull/195) merged
as `b5f546c` and [PR #196](https://github.com/antiartificial/norn-fleet/pull/196)
as `93539d5242e5e94e2f90eeea821fe8550f7fb0ac`. The latter passed
[1,429 contract tests](https://github.com/antiartificial/norn-fleet/actions/runs/36644956943)
and [285 protected no-cloud qualification tests](https://github.com/antiartificial/norn-fleet/actions/runs/36646242045).
The exact receipt and source Git trees were verified locally.

The state Space and scoped key are verified absent. The local controller,
its two routes and retire-by watchdog are closed. All five Tailscale keys were
retired with signed terminal observations. A v2 proof-reader defect required
each intermediate deletion to report no remaining keys; the actual signed
sequence was four, three, two, one, zero. Reviewed
[PR #197](https://github.com/antiartificial/norn-fleet/pull/197), commit
`538c775`, validates bounded remaining-key subsets and monotonic removal.
It passed 50 focused cleanup tests and all
[1,430 contract tests](https://github.com/antiartificial/norn-fleet/actions/runs/36647151475),
then merged as `172c2c3ec53d8e8541a228a8714d178bb4603d23`. That exact protected
head passed [286 no-cloud qualification tests](https://github.com/antiartificial/norn-fleet/actions/runs/36647958590);
the receipt, both Git trees and all qualification file digests were verified.

The supported local Phase-B CLI used that reviewed reader fix with the
unchanged intent prepared by exact protected `93539d5`. It removed only runner
217 and preserved finalizer runner 23. The signed final-zero record and raw
event chain verified at `2026-09-29T23:50:32Z`:
`R/partial-precreate-abort/evidence-final/partial-precreate-final-zero.json`.
`R/partial-precreate-abort/phase-b-executor.json` records the reader execution
revision. The full Tailscale policy was then restored to its original baseline;
`R/retention/policy-restoration-receipt.json` retains that separate observation.
This is partial-precreate technical zero; billing finalization remains separate.

The next useful run uses the paired disposable management/Fleet topology and
normal bootstrap: actual Norn v3 on three etcd-backed cloud control nodes,
plus two ingress nodes. Its Linux runner admission is corrected in PR #196.
The signed Norn `8ac21f8e` Linux bundle, official etcd v3.5.17 tools, complete
Linux management tool set and sealed Ansible environment are verified locally.
The exact protected Fleet checkout archive is also built and verified.
Neutral receipts, the concrete `pilot260929h` proposal and command recipe are
under `/Users/arti/.config/norn-fleet/v3-launch-prep-20260929`.

## Next sequence

1. Use qualified protected Fleet `172c2c3` and its verified checkout archive.
   Preserve the completed original pilot's final-zero and policy-restoration
   evidence.
2. Follow the [paired operator plan](fleet-next-epoch-operator-sequence-2026-09-29.md).
   The estimated four-hour infrastructure usage is $3.10, with a proposed $10
   total ceiling covering possible Spaces charges and teardown. This is not
   spend approval. Exact plans require staged backend and management setup;
   review each authorized stage before progressing.
3. Execute the reviewed paired Fleet plan with the signed v3 candidate and
   run the [fitness matrix](fleet-fitness-pilot-matrix-2026-09-29.md): exact etcd
   membership, authenticated Norn API continuity, separately labeled etcd CAS,
   one-control loss/rejoin, workload
   placement and ingress continuity. Reserve the final hour for retirement.
4. Retire Fleet before management and retain final technical-zero evidence.
   Billing verification remains separate.
5. Use observed results to advance M3/M4/M8 without declaring full release
   qualification. Rotation, rolling upgrade, destructive restore, soak and
   ingress scaling remain separate evidence. Mini M5 upgrade and M9 adoption
   stay independent; no Mini mutation is part of this Fleet run.
