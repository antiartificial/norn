# Norn v3 launch state

Observed 2026-10-04 21:55 UTC. This is the current handoff for the pragmatic
deployment objective. Recheck each external observation before a mutation;
links below distinguish live state, protected workflow evidence, and historical
local work. The [milestone contract](execution-milestones.md) remains the exit
criteria; this page records the current disposition and next actions.

## Target and current answer

The target has two independent runtime paths: the existing Mini serving the v3
API on PostgreSQL while preserving its applications, and a fresh DigitalOcean
Fleet with three etcd control members and separate application databases. Fleet
comes first for the remaining launch work. A representative application move,
fault/recovery qualification, and controlled adoption remain part of the full
release contract; a disposable pilot does not sign the release.

**Mini:** The live loopback API returned `status: ok` on 2026-10-04, with
Consul, Nomad, PostgreSQL, S3, SOPS, and the Nomad/Consul connector up. Its
`/api/version` and `/api/v1/releases` bound the running signed source to
`26147c39a554b73b6761a371edee5a9591c81d3f`, displayed as
`v2.20.0-platform-72-g26147c39`. The `v2.20.0` prefix is the platform version
label; the source includes the v3 work. The API reports the development
profile, not production admission. An authenticated inventory found host status
OK, 28 app entries, 45 service entries, zero active operations, and 25 active
incidents. Those counts include discovered/inactive apps and services; they do
not prove workload identity preservation across the earlier one-way transition.

**Fleet:** The last live Mini inventory returned `fleet_configured=false`, zero
node pools, and zero Fleet plans. A prior protected disposable Fleet
[apply run 37184229667](https://github.com/antiartificial/norn-fleet/actions/runs/37184229667)
failed at the pre-provider public ingress TLS gate on 2026-10-04 06:55:59 UTC.
Its durable attempt and provider mutation steps were skipped, so this run is
not a Fleet deployment. It targeted `pilot261003d` and protected Fleet SHA
`a3883f59f3b8dfa67161003cb130666871a67b77`. The workflow passed pilot
admission, but both initial-cold-start certificate path variables were empty.
The validator refused `pilot-pilot261003d.betabung.com`. No current Fleet
fitness, ingress or workload proof follows from that failed run.

**Prior-run cleanup:** `pilot261003d` created management infrastructure before
the Fleet attempt. Its emergency retirement destroyed 17 management resources.
The management OpenTofu state then held zero resources; two independent
DigitalOcean inventories at 07:32 and 07:33 UTC found zero. Both pilot backend
buckets returned 404 and pilot Spaces keys were absent. A later provider-name
inventory at 09:17 UTC found zero across six DigitalOcean resource categories.
This supports technical and cost-target zero, but the retirement is classified
as **emergency unprotected**; a protected final-zero receipt is unavailable.
Those access items were subsequently retired: at 17:19 UTC the old `c`/`d`
Tailscale machines were absent and both OAuth clients showed Revoked; at 17:13
UTC the old GitHub App key was deleted and the replacement key authenticated
the exact repository installation. These later owner observations supersede
the 09:18 incident-status access snapshot. Technical zero remains an emergency
unprotected result, not a protected final-zero receipt.

**Disposable successor:** `pilot261004a` used a verified exact-SAN certificate,
replacement GitHub App key, and fresh Tailscale credentials. It created the
management authority and runner, completed private handoff and protected Fleet
planning, then stopped before Fleet creation. The approved complete abort has
since destroyed management and both state backends. The two one-use Tailscale
auth keys and run-specific GitHub staging configuration are retired. Two
Tailscale device identities still await dashboard removal, and the pilot
replacement OAuth client awaits revocation. This is not an overall external
final-zero claim. Cloud and backend teardown finished before the approved
`2026-10-04T23:30:00Z` retirement deadline.

**Source:** The Mini still runs `26147c39`; protected Norn `master` now resolves
to `c56fa0cbef322d44abf905a423bea72df26292b2`, including the pilot apply
title correction. Protected Fleet `main` resolves to
`daf7bf3fb759f7603c8d08941187a09d907f37f7`, including the exact empty
backend bucket abort proof. The local `codex/v3-release-integration` branch at
`88b52e1f` is not merged and is behind newer protected Norn work. Do not use
that branch, the old run, or its plan artifact as moving release authority.
The main `/Users/arti/Desktop/Claude/norn` checkout has unrelated local
changes and must be preserved.

**Exact-main no-cloud qualification:** Protected Fleet
[run 37228131232](https://github.com/antiartificial/norn-fleet/actions/runs/37228131232)
completed successfully at 19:25 UTC on exact `f8d1814`. Its hosted receipt
records 286 bounded tests and `cloudMutationAllowed=false`,
`tailscaleMutationAllowed=false`, and `oidcUsed=false`. The owner-only receipt
is retained under `qualification-pilot261004a-f8-37228131232/` with SHA-256
`6d75fbf10a1dc24476b30b5da298467c6fd2a2b21e1a167dee89fbd8fd565c1f`.
This closes the exact-source no-cloud qualification gate; it does not create a
runner, plan, Fleet attempt or resource.

**Pilot precreate and management history:** A 19:32 UTC provider-name inventory found zero
`pilot261004a` matches before creation. The refreshed conservative quote is
USD 8.97 for a four-hour run, one-hour teardown reserve and full USD 5 Spaces
base, within the protected USD 10 ceiling. The updated local operator budget
record reflects the earlier user-approved USD 20 pilot and sets
`billableCreateAllowed=true`; it is not a protected infrastructure plan or a
fresh owner signature. Protected exact-main [backend owner-dispatch run
37229166242](https://github.com/antiartificial/norn-fleet/actions/runs/37229166242)
passed at 19:40 UTC on `f8d1814`. Paired Fleet and management Spaces state
backends were then created, with bucket-scoped credentials, versioning,
retention records and complete owner-only bootstrap receipts observed at
19:49 UTC. A separate 19:51 UTC owner-only receipt marks the management
bootstrap **static precreate inputs** complete: the source archive, signed
release helper, public assets, private authority inputs and fresh Tailscale
machine keys are staged. It explicitly defers provider-generated values until
after management creation. Protected exact-main management owner-dispatch
[run 37230120622](https://github.com/antiartificial/norn-fleet/actions/runs/37230120622)
then approved the create subject. The run-bound management create receipt is
complete, and the authority and repository runner came online on Tailscale.
The runner has a private authority route and a successful scoped `api:read`
probe. DigitalOcean public SSH firewalls were closed after bootstrap. The
strict OpenTofu refresh drift was reconciled, and the firewall closure receipt
and both host receipts are now complete. The existing exact-SAN full chain and
key were verified on the runner, and the run-bound Fleet handoff is complete.
Protected Fleet plans passed with 24 planned creates, bound to Norn plan
`575207c9-efcf-483c-992d-fd6957308ef8`; the corresponding control-plan PR #242
merged before the apply. The first protected apply,
[run 37233379210](https://github.com/antiartificial/norn-fleet/actions/runs/37233379210),
failed at the pre-provider recovery-inventory gate. Its check rejected historic
Fleet state-lock history because the observed precreate inventory did not prove
the required single paired empty retained Fleet bucket. The `apply` job stopped
before provider apply and no runner attempt or Fleet resource was created. This
is measured workflow evidence; the later abort receipt supplies the distinct
provider-zero proof for management.

**Abort and backend retirement:** [Fleet PR #243](https://github.com/antiartificial/norn-fleet/pull/243)
passed its protected contract check and merged as `daf7bf3`. It admits only the
declared empty Fleet backend bucket while still rejecting Fleet state, locks,
uploads and other provider resources. The refreshed management snapshot found
17 state-accounted resources. Protected owner [approval run 37235481822](https://github.com/antiartificial/norn-fleet/actions/runs/37235481822)
bound the complete abort. The fence removed runner 225 and powered off both
management droplets; the exact saved destroy plan then completed. The abort
receipt records empty management OpenTofu state and two independent management
provider-zero observations. A separate typed cleanup removed both Spaces state
buckets, their retained versions, and scoped keys, with `backendFinalZero=true`.
The cloud and backend teardown finished before the 23:30 UTC retirement
deadline. Two Tailscale device identities and the replacement OAuth client
still await removal; final external cleanup remains separate.

**Norn bridge correction:** Norn [PR #115](https://github.com/antiartificial/norn/pull/115)
passed both v3 CI and the protected legacy Repository CI checks, then merged as
`c56fa0c`. The two workflows were returned to their earlier disabled setting.
The correction is in source, but has not been deployed to Mini or used in a
successful Fleet run. It is not Fleet fitness evidence.

**Successor readiness:** Protected Fleet [qualification run 37237586889](https://github.com/antiartificial/norn-fleet/actions/runs/37237586889)
passed on current `daf7bf3` with 286 bounded tests and no cloud, OIDC or
Tailscale mutation. Its owner-only artifact is retained under
`qualification-daf7-37237586889/`. A read-only name inventory found no
`pilot261004b` resources across nine DigitalOcean compute categories; it does
not prove Spaces or DNS zero. Current unit prices yield a provisional $8.97
conservative quote for a four-hour maximum shape plus one teardown hour,
including the full $5 Spaces base. This is not billable approval. Official
[DigitalOcean Droplet](https://www.digitalocean.com/pricing/droplets),
[managed database](https://www.digitalocean.com/pricing/managed-databases),
[load balancer](https://docs.digitalocean.com/products/networking/load-balancers/details/pricing/)
and [Spaces](https://docs.digitalocean.com/products/spaces/details/pricing/)
rates were checked on 2026-10-04. Norn
[release run 37237574000](https://github.com/antiartificial/norn/actions/runs/37237574000)
built all four platform bundles for exact source `c56fa0c` and is waiting for
the protected publish review. No signed `c56fa0c` release has been published
or promoted yet.

## Milestone reconciliation

No M0–M9 gate has a recorded owner sign-off. “Running” below describes runtime
state, not a signed milestone. The percentages are rounded engineering judgment
of work completed against each gate's full exit criteria, based on evidence
available at this observation cutoff. They are not release sign-offs, schedule
forecasts, or weights for an overall launch percentage. The older dated status
snapshots remain in Git history.

| Gate | Estimated complete | Current finding | Next proof that changes the disposition |
| --- | ---: | --- | --- |
| M0 baseline | 55% | Mini source and current service counts are observed; historical before/after identity equality is incomplete. | Review catalog/owner decisions and a fresh baseline bound to the current signed release. |
| M1 control semantics | 65% | Signed v3 code is running on Mini; local ownership/fencing tests exist. | Cross-process and downstream-effect recovery proof on the protected path. |
| M2 profiles and retention | 50% | Mini uses development/PostgreSQL; profile, binding and archive work has local evidence. | Real provider retention and separate-node restore, plus reviewed legacy app binding behavior. |
| M3 etcd Fleet | 40% | Three-member local TLS/RBAC/restore rehearsals and a protected 24-create Fleet plan exist; the disposable management and state backends were retired before Fleet creation. No live etcd Fleet exists. | Protected five-node bootstrap, actual membership/quorum, fault/restore, alarms, rotation and soak. |
| M4 capacity and ingress | 40% | Local admission, replica and two-ingress fixtures exist; no provider-backed route or loaded scale/drain proof. | Live placement, public ingress, workload write/read, 2→3→2 and drain under load. |
| M5 Mini upgrade rehearsal | 35% | Mini now runs the v3 source, but the original one-way transition's before snapshot was not retained. Later signed private-copy restore/shadow evidence does not repair that missing comparison. | Protected isolated transition rehearsal from a representative current backup, workload identity/route comparison, and operator review. Do not replay the consumed live fence. |
| M6 running upgrades and app DB | 25% | Journals, authority checks and local transfer fixtures exist. | Protected running-upgrade and actual app DB cutover/interruption recovery. |
| M7 mobility | 30% | An isolated Mini fixture source ran web, worker and tick; it wrote three items, acknowledged three jobs and recorded seven ticks. Web was fenced read-only, worker/tick stopped, and a five-dimensional private restore/transfer comparison passed. No Fleet target or complete move exists. | Reverify the source fence, perform final `NOLOGIN`/zero-session role fence at target-ready cutover, then prove target data/files/work, traffic and rollback/recovery. |
| M8 release qualification | 20% | Signed Mini source, exact-source no-cloud checks, protected Fleet plan and complete pilot abort receipts exist; the disposable Fleet apply stopped before provider mutation. | Version matrix, provider-backed fitness, soak/fault results, client parity and owner sign-off. |
| M9 adoption | 10% | Mini API runs v3 code, but controlled adoption is not signed; Fleet is absent. | Independently verified Fleet deployment, Mini preservation and selected app adoption under the reviewed rollout scope. |

The latest detailed implementation notes on the unmerged integration branch
remain useful leads, but do not supersede this live inventory or protected
`master`/`main`. For the intended bounded pilot checks use the
[Fleet fitness matrix](fleet-fitness-pilot-matrix-2026-09-29.md). It does not
cover every M3/M4/M8 exit.

## Findings and exact next sequence

1. **Preserve the old-run closure.** The `c`/`d` Tailscale devices and OAuth
   clients and the old GitHub App key were retired by 17:19 UTC. Keep their
   owner-only action-time receipts with the emergency management-retirement
   packet. Recheck provider/account billing independently; never label this a
   protected final-zero.
2. **Finish pilot261004a external cleanup.** The complete abort and paired
   backend cleanup are evidenced. The two one-use Tailscale auth keys are
   deleted, the pilot GitHub runner and staging configuration are removed, and
   shared GitHub entries are preserved. Remove only the two recorded Tailscale
   devices and revoke the pilot replacement OAuth client when their dashboard
   confirmations are given. Recheck those identities and original tag ownership,
   then retain final external
   cleanup evidence. Technical zero does not settle the final invoice.
3. **Start a new bounded Fleet pilot.** The title verifier fix is merged but
   not deployed to a management authority. Exact-main no-cloud qualification
   passed for `daf7bf3`. A new pilot needs fresh cost and expiry approval,
   credentials, backend and management bootstrap, a fresh signed Norn release,
   a protected plan,
   and a guarded apply. Do not reuse this destroyed state, runner or expired
   approvals. Watch the numbered attempt and checkpoints; verify three etcd
   members, Consul/Nomad enrollment, both ingresses and public TLS, then run
   the bounded workload/fault matrix and retire it within its own envelope.
   The attempted apply run 37233379210 is evidence of a pre-provider failure,
   not a resumable Fleet deployment.
4. **Then finish Mini/release qualification.** Rehearse M5 on a private current
   backup without mutating live applications, complete the remaining M3–M8
   faults, upgrades, database and mobility proofs, and record explicit M0–M9
   decisions. The Mini's current healthy API is useful runtime evidence, not
   proof that those exits have passed.

## Evidence pointers and limits

- Mini: 2026-10-04 authenticated inventory via
  `norn_inventory.sh --remote mini`, including `/api/version`,
  `/api/v1/releases`, `/api/v1/fleet/node-pools`, `/api/v1/fleet/plans`,
  `/api/v1/host/status`, and `/api/ops/platform`. The inventory is a point in
  time, not sustained health or full application smoke.
- Fleet failed apply: [run 37184229667](https://github.com/antiartificial/norn-fleet/actions/runs/37184229667),
  its failed step log, and protected
  [`apply.yml`](https://github.com/antiartificial/norn-fleet/blob/a3883f59f3b8dfa67161003cb130666871a67b77/.github/workflows/apply.yml)
  / [`pilot_ingress_tls.py`](https://github.com/antiartificial/norn-fleet/blob/a3883f59f3b8dfa67161003cb130666871a67b77/scripts/pilot_ingress_tls.py).
- Exact-main hosted qualification: [run 37228131232](https://github.com/antiartificial/norn-fleet/actions/runs/37228131232)
  and owner-only `qualification-pilot261004a-f8-37228131232/pre-release-pilot-qualification.json`.
- Pilot precreate: [protected backend owner-dispatch run 37229166242](https://github.com/antiartificial/norn-fleet/actions/runs/37229166242);
  owner-only `provider-zero-check-pilot261004a-20261004T193244Z-refresh.json`,
  `price-quote-pilot261004a-20261004T193244Z-refresh.json`,
  `cost-approval-pilot261004a-20261004T1932Z-refresh.json`,
  `backend-precreate-pilot261004a/backend-bootstrap-receipt-{fleet,management}.json`,
  and `management-bootstrap-precreate-pilot261004a.json`. The backends were
  actual provider resources and are covered by the later typed cleanup receipt.
- Management create: [protected owner-dispatch run 37230120622](https://github.com/antiartificial/norn-fleet/actions/runs/37230120622),
  owner-only `management-create-evidence-pilot261004a/` create receipt and
  postcreate inventory, Tailscale enrollment and runner readiness observations,
  private authority `api:read` probe, completed OpenTofu firewall closure and
  two host receipts. The runner held verified TLS files and the run-bound
  Fleet handoff completed before it was fenced and retired.
- Protected Fleet plan and failed apply: [plan run 37233323499](https://github.com/antiartificial/norn-fleet/actions/runs/37233323499),
  24 planned creates and Norn plan `575207c9-efcf-483c-992d-fd6957308ef8`,
  then failed [apply run 37233379210](https://github.com/antiartificial/norn-fleet/actions/runs/37233379210).
  The apply stopped at the pre-provider recovery-inventory gate because its
  observed inventory did not prove exactly the required retained empty Fleet
  bucket. [PR #242](https://github.com/antiartificial/norn-fleet/pull/242)
  merged; neither the plan, PR, nor failed apply proves Fleet deployment.
- Complete abort: owner-only `abort-pilot261004a/abort-snapshot-daf7.json`,
  protected [approval run 37235481822](https://github.com/antiartificial/norn-fleet/actions/runs/37235481822),
  `abort-prepared-daf7.json`, and `abort-receipt-daf7.json`. The receipt reopens
  the fenced 17-resource destroy and proves management state empty with two
  independent provider-zero observations. [Fleet PR #243](https://github.com/antiartificial/norn-fleet/pull/243)
  supplied the exact retained-empty-bucket validator required for this path.
- Backend and external cleanup: owner-only `abort-pilot261004a/backend-cleanup-plan.json`,
  `backend-cleanup-receipt.json` (`backendFinalZero=true`), and
  `external-cleanup/{github-cleanup-receipt,tailscale-cleanup,tailscale-oauth-policy-audit}.json`.
  The Tailscale receipts record both auth keys deleted, two devices pending
  removal, the original OAuth client revoked, and the pilot replacement OAuth
  client still active. Neither the abort nor backend receipt claims overall
  external final zero or settled billing.
- Norn bridge title correction: [PR #115](https://github.com/antiartificial/norn/pull/115)
  passed the v3 and legacy protected checks and merged as `c56fa0c`. It is not
  yet deployed on Mini or a new management authority.
- Successor preflight: protected [qualification run 37237586889](https://github.com/antiartificial/norn-fleet/actions/runs/37237586889),
  owner-only `qualification-daf7-37237586889/pre-release-pilot-qualification.json`
  (286 tests), and `successor-pilot261004b/{provider-name-preflight,provisional-price-quote}.json`.
  The provisional quote keeps `billableCreateAllowed=false`; a fresh token and
  run-specific protected approval are still required. Norn signed
  [release run 37237574000](https://github.com/antiartificial/norn/actions/runs/37237574000)
  is waiting at its protected publish gate after four successful bundle jobs.
- `pilot261003d` emergency retirement: owner-only local incident record at
  `/Users/arti/.config/norn/fleet-next-full-e2e/incident-retirement-pilot261003d/`,
  especially `status-20261004T0918Z.json`, management state/provider-zero
  receipts and backend-bucket/key absence receipts. Its 09:18 UTC status is a
  snapshot; the later old-access retirement receipt supersedes its access fields.
- `pilot261004a` preparation and old-access retirement: owner-only
  `/Users/arti/.config/norn/fleet-next-full-e2e/next-pilot-readiness-20261004.md`,
  `old-access-retirement-pilot261004a.json`,
  `pilot-ingress-tls-pilot261004a-20261005T0100Z.json`, and
  `management-credentials-pilot261004a/tailscale-issuance/management-receipts/`.
  The readiness note is an operator log,
  not a protected plan, owner-dispatch audit or live Fleet proof.
- Historical management pilot results:
  `/Users/arti/Desktop/Claude/.norn-pilot-evidence/fleet-precreate/` has
  run-bound receipts. [October 3 release integration notes](https://github.com/antiartificial/norn/blob/codex/v3-release-integration/docs/v3/execution-milestones.md)
  describe earlier attempts but are unmerged and predate the latest apply.
- The [2026-09-29 handoff](session-resume-2026-09-29.md) is historical; its
  Mini version, spend approval status and next run ID are superseded here.

The failed Fleet apply alone proves no Fleet provider mutation. The separate
management abort and backend receipts prove cloud and backend zero for
`pilot261004a`. Its two recorded Tailscale devices await dashboard removal and
its pilot replacement OAuth client awaits revocation; overall external final
zero and final billing are not yet claimed.
