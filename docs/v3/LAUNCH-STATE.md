# Norn v3 launch state

Observed 2026-10-04 19:07 UTC. This is the current handoff for the pragmatic
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

**Fleet:** The same live Mini inventory returned `fleet_configured=false`, zero
node pools, and zero Fleet plans. The most recent protected disposable Fleet
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

**Prepared successor:** Owner-only readiness observations through 18:58 UTC
record `pilot261004a`, its exact-SAN public certificate, a replacement GitHub
App key verified against the repository installation, a replacement Tailscale
OAuth client that read the live ACL/auth-key list, and saved temporary ownership
for the authority, runner and node tags. No one-use machine keys or devices
were issued at that observation. The certificate's provisional proof binds an
expired 14:00 UTC retirement time, and the local cost record explicitly says
`billableCreateAllowed=false`. Its earlier price and provider-zero reads must
be refreshed for the actual launch window. This is preparation, not a Fleet
resource or authorization to create billable compute.

**Source:** Protected Norn `master` and the Mini both resolve to `26147c39`.
Protected Fleet `main` resolves to
`f8d181482578e5fef72f4f3529f4e6e782f410a4`, two commits after the
failed apply's source. The local `codex/v3-release-integration` branch at
`88b52e1f` is not merged and is behind newer protected Norn work. Do not use
that branch, the old run, or its plan artifact as moving release authority.
The main `/Users/arti/Desktop/Claude/norn` checkout has unrelated local
changes and must be preserved.

## Milestone reconciliation

No M0–M9 gate has a recorded owner sign-off. “Running” below describes runtime
state, not a signed milestone. The historical percentage estimates and dated
status snapshots have been removed from the current milestone document; Git
history retains them.

| Gate | Current finding | Next proof that changes the disposition |
| --- | --- | --- |
| M0 baseline | Mini source and current service counts are observed; historical before/after identity equality is incomplete. | Review catalog/owner decisions and a fresh baseline bound to the current signed release. |
| M1 control semantics | Signed v3 code is running on Mini; local ownership/fencing tests exist. | Cross-process and downstream-effect recovery proof on the protected path. |
| M2 profiles and retention | Mini uses development/PostgreSQL; profile, binding and archive work has local evidence. | Real provider retention and separate-node restore, plus reviewed legacy app binding behavior. |
| M3 etcd Fleet | Three-member local TLS/RBAC/restore rehearsals exist; no live etcd Fleet. | Protected five-node bootstrap, actual membership/quorum, fault/restore, alarms, rotation and soak. |
| M4 capacity and ingress | Local admission, replica and two-ingress fixtures exist; no provider-backed route or loaded scale/drain proof. | Live placement, public ingress, workload write/read, 2→3→2 and drain under load. |
| M5 Mini upgrade rehearsal | Mini now runs the v3 source, but the original one-way transition's before snapshot was not retained. Later signed private-copy restore/shadow evidence does not repair that missing comparison. | Protected isolated transition rehearsal from a representative current backup, workload identity/route comparison, and operator review. Do not replay the consumed live fence. |
| M6 running upgrades and app DB | Journals, authority checks and local transfer fixtures exist. | Protected running-upgrade and actual app DB cutover/interruption recovery. |
| M7 mobility | An isolated Mini fixture source ran web, worker and tick; it wrote three items, acknowledged three jobs and recorded seven ticks. Web was fenced read-only, worker/tick stopped, and a five-dimensional private restore/transfer comparison passed. No Fleet target or complete move exists. | Reverify the source fence, perform final `NOLOGIN`/zero-session role fence at target-ready cutover, then prove target data/files/work, traffic and rollback/recovery. |
| M8 release qualification | Signed Mini source and exact-source no-cloud checks exist; the disposable Fleet apply stopped before provider mutation. | Version matrix, provider-backed fitness, soak/fault results, client parity and owner sign-off. |
| M9 adoption | Mini API runs v3 code, but controlled adoption is not signed; Fleet is absent. | Independently verified Fleet deployment, Mini preservation and selected app adoption under the reviewed rollout scope. |

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
2. **Rebind prepared successor `pilot261004a` to a real launch window.** Its
   exact-hostname public certificate, new GitHub App key, replacement Tailscale
   OAuth client and three temporary tag-owner delegations already exist.
   Its provisional `retireBy=2026-10-04T14:00:00Z` is expired. Refresh the
   run-bound price, cutoff/retirement, TLS verification and provider-zero
   observations before dispatch. The broader user pilot ceiling is USD 20;
   the protected workflow ceiling is USD 10. The retained local cost record
   still says `billableCreateAllowed=false`; obtain the fresh protected
   owner-dispatch and plan approvals for the actual window.
3. **Complete the protected precreate handoff in the right order.** Issue fresh
   one-use Tailscale machine keys under the saved temporary delegation, set up
   the management authority and exact repository runner, and transfer the
   existing `pilot261004a` full chain and key with
   `ansible/playbooks/pilot-runner-tls-precreate.yml` before Fleet apply.
   Recheck exact-SAN, system-CA chain, key match, owner/mode and validity past
   the new `retireBy + 1 hour` on the runner, then set the two protected
   **path** variables to the verified files. Do not put PEM bytes in GitHub
   variables. Restore original tag owners after enrollment or abort.
4. **Dispatch a fresh exact-main plan/apply and measure the actual Fleet.**
   Bind the Norn plan and Fleet artifact to the same current protected SHA.
   Watch the numbered attempt and checkpoints through terminal state; verify
   provider inventory, three etcd members, Consul/Nomad enrollment, both
   ingresses and public TLS, then run the bounded workload/fault matrix.
   Reserve the retirement window; retire Fleet before management and
   independently prove provider and backend final zero. Keep cost/billing
   verification distinct from technical zero.
5. **Then finish Mini/release qualification.** Rehearse M5 on a private current
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
- `pilot261003d` emergency retirement: owner-only local incident record at
  `/Users/arti/.config/norn/fleet-next-full-e2e/incident-retirement-pilot261003d/`,
  especially `status-20261004T0918Z.json`, management state/provider-zero
  receipts and backend-bucket/key absence receipts. Its 09:18 UTC status is a
  snapshot; the later old-access retirement receipt supersedes its access fields.
- `pilot261004a` preparation and old-access retirement: owner-only
  `/Users/arti/.config/norn/fleet-next-full-e2e/next-pilot-readiness-20261004.md`,
  `old-access-retirement-pilot261004a.json`,
  `pilot-ingress-tls-pilot261004a.json`, and
  `cost-approval-pilot261004a.json`. The readiness note is an operator log,
  not a protected plan, owner-dispatch audit or live Fleet proof.
- Historical management pilot results:
  `/Users/arti/Desktop/Claude/.norn-pilot-evidence/fleet-precreate/` has
  run-bound receipts. [October 3 release integration notes](https://github.com/antiartificial/norn/blob/codex/v3-release-integration/docs/v3/execution-milestones.md)
  describe earlier attempts but are unmerged and predate the latest apply.
- The [2026-09-29 handoff](session-resume-2026-09-29.md) is historical; its
  Mini version, spend approval status and next run ID are superseded here.

The failed Fleet apply alone proves no Fleet provider mutation; the separate
management-retirement receipts and independent provider observations support
the management resource-zero claim. Billing finalization requires separate
verification. The old pilot identities were subsequently retired; the new
pilot OAuth and temporary tag delegation need their own end-of-run cleanup.
