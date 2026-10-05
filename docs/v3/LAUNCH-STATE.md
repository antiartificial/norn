# Norn v3 launch state

Observed 2026-10-05 14:00 UTC. This is the single current handoff for the
pragmatic deployment objective. The [milestone contract](execution-milestones.md)
defines the exit criteria; percentages below are engineering estimates, not
owner sign-offs. Recheck external state before any new mutation.

## Current decision

**Norn v3 is not deployed on Fleet.** The Mini runs v3 source through the v3 API,
but its development profile and preserved applications do not qualify the Fleet
or complete the release. Fleet remains the first launch track. The next Fleet
attempt requires a new bounded pilot approval, fresh credentials, a repaired
partial-apply/recovery lane, and a complete workload and failure proof. Do not
reuse `pilot261004b` state, runner, keys or approvals.

The approved `pilot261004b` window was $20 maximum, four hours active plus one
hour for teardown, ending **2026-10-05 08:30 UTC**. The protected
[apply run 37273763546](https://github.com/antiartificial/norn-fleet/actions/runs/37273763546)
at Fleet `main` `ca52148d69eecd70a843938b72c2bc882a066c5d` stopped during provider
creation at 06:50:30 UTC with exit 143. The runner was online and idle after
termination; the signal sender is unproven. Norn attempt
`eb349686-c1ac-439d-b7b6-cf1fc2443d9f` was later marked abandoned after its
heartbeat expired. The remote Fleet state persisted at serial 3, lineage
`ffe289b7-a711-4ca6-b84d-7887c67f4d5c`, with five droplets, load balancer,
VPC, firewalls, project and tags. It did **not** contain either managed database,
although DigitalOcean had created both. No bootstrap, public ingress, etcd
quorum, application workload or scale proof followed. Automatic
[recovery run 37274493490](https://github.com/antiartificial/norn-fleet/actions/runs/37274493490)
failed at source-run name validation; the repository recovery inventory would
also hold on the two provider-only databases. Plain apply replay was unsafe.

At 13:47 UTC, well after the teardown deadline, a fresh DigitalOcean inventory
still showed seven pilot droplets, three managed databases, the Fleet load
balancer, four firewalls, two VPCs and two projects. This was an **overrun
incident**. Exact-ID emergency deletion began after preserving private state
and provider evidence. By 13:58 UTC, fresh DigitalOcean list calls showed zero
`pilot261004b` droplets, databases, load balancers, firewalls, VPCs, projects,
tags, volumes, snapshots, reserved IPs and SSH keys. Both pilot Spaces state
buckets, every retained object version and both scoped Spaces keys were removed;
a new full-access Spaces inventory key found zero pilot buckets and was revoked.
The exact GitHub pilot runner and the staging secrets/variables created for
this pilot were removed. DNS A lookup for
`pilot-pilot261004b.betabung.com` returned no address. These are technical
absence observations after **emergency unprotected cleanup**, not a signed
protected final-zero receipt. The final pilot-specific DigitalOcean bill is not
yet available. The account's month-to-date balance cannot be attributed to
this pilot, so compliance with the $20 cap remains unproven.

Two **offline Tailscale records remain** for the retired management authority
(100.120.209.89) and runner (100.122.147.29). The console shows no Fleet
machines from this pilot, and the five one-use Fleet auth keys are invalidated.
The broad OAuth client that issued those keys was revoked; a replacement
scoped client was requested but not generated. Exact device deletion is awaiting
required action-time user confirmation. The `pilot261004b` DigitalOcean API
token also remains active pending UI revocation after the dashboard session
expired; its local copy is still owner-only. Do not call this overall external
final zero until these access items and billing are reconciled.

## Mini and source

On 2026-10-04, the Mini's loopback v3 API returned `status: ok` and a signed
running source of `26147c39a554b73b6761a371edee5a9591c81d3f`, displayed
as `v2.20.0-platform-72-g26147c39`. The prefix is a platform version label;
the source includes v3 work. Consul, Nomad, PostgreSQL, S3, SOPS and the
Nomad/Consul connector were up. Authenticated inventory showed 28 app entries,
45 service entries, zero active operations and 25 active incidents. Those counts
include discovered and inactive entries and do not prove identity preservation
through the earlier one-way transition. The Mini reported
`fleet_configured=false`, zero node pools and zero Fleet plans at that cutoff.
Recheck before relying on its current runtime state.

Norn [PR #115](https://github.com/antiartificial/norn/pull/115) merged the bridge
title correction as `c56fa0c`. The signed
[platform-c56fa0c release](https://github.com/antiartificial/norn/releases/tag/platform-c56fa0cbef322d44abf905a423bea72df26292b2)
contains four verified bundles. Publication is not Mini promotion or Fleet
fitness. The local `codex/v3-release-integration` branch remains unmerged and
must not be treated as protected release authority. The main Norn checkout has
unrelated local changes that must be preserved.

## Pilot lineage

| Pilot | Most recent useful evidence | Disposition |
| --- | --- | --- |
| `pilot261003c` / `pilot261003d` | `c` and `d` exposed earlier gaps in provider evidence, TLS handoff and emergency cleanup. The `d` Fleet apply stopped before provider mutation; management was retired by emergency unprotected cleanup. | Historical failure evidence, not launch proof. Old `c`/`d` access items were retired in the later owner observation. |
| `pilot261004a` | Exact-SAN TLS and replacement GitHub App key supported complete management handoff and protected Fleet planning. Its apply stopped at the pre-provider retained-bucket inventory gate. Management and both backends were retired before its approved deadline. | Clean cloud/backend abort; two Tailscale device records and replacement OAuth client were still pending at the last observation. Recheck them separately. |
| `pilot261004b` | Protected plan `37273380169`, Norn plan `a25b25fa-c0b0-47e0-b9ec-836ce6cde3ed`, and apply `37273763546` reached live provider creation. State missed both databases. | Fleet never bootstrapped; emergency technical teardown occurred after the 08:30 UTC deadline. Access and billing closure remain open. |

Pilot `a` was the first complete management-to-Fleet handoff, but `b` was the
most recent and exposed the material partial-apply failure. A fresh pilot
should be based on the repaired contracts, not resumed from `a` or `b`.

## Milestone estimates

No M0–M9 gate has recorded owner sign-off. These rounded estimates measure work
against each full exit criterion; they are not schedule forecasts or an overall
launch percentage. The `b` pilot adds real provider-create evidence but no
running Fleet qualification.

| Gate | Estimated complete | Finding and next proof |
| --- | ---: | --- |
| M0 baseline | 55% | Mini source and service counts observed; historical before/after identity equality and current owner baseline remain. |
| M1 control semantics | 65% | Signed v3 code runs on Mini; protected cross-process and downstream-effect recovery proof remains. |
| M2 profiles and retention | 50% | Mini uses development/PostgreSQL; separate-node restore, provider retention and legacy binding review remain. |
| M3 etcd Fleet | 45% | Local three-member rehearsals and live five-node provider creation exist; no bootstrapped etcd membership, quorum, fault/restore, alarm, rotation or soak proof. |
| M4 capacity and ingress | 40% | Local admission and two-ingress fixtures exist; no live route, placement, loaded 2→3→2 or drain proof. |
| M5 Mini upgrade rehearsal | 35% | Mini runs v3 source, but the original one-way transition lacks its before snapshot. Rehearse on a representative private current backup. |
| M6 running upgrades and app DB | 25% | Journals and local transfer fixtures exist; protected running upgrade and app DB cutover/interruption proof remain. |
| M7 mobility | 30% | Isolated Mini fixture web/worker/tick and private restore comparison passed. No Fleet target or complete move exists. |
| M8 release qualification | 25% | Signed source, protected no-cloud checks and real provider-create evidence exist; provider-backed fitness, soak/fault results and owner sign-off remain. |
| M9 adoption | 10% | Mini API runs v3 code; Fleet and controlled application adoption are absent. |

## Next actions

1. Complete `pilot261004b` external closure: remove only the two named offline
   Tailscale management device records after action-time confirmation, revoke
   the exact DigitalOcean pilot token after sign-in/confirmation, verify no
   active pilot auth keys or OAuth client, and retain second independent
   provider-zero and billing observations. Keep the private state snapshots and
   deletion receipts; never call the emergency cleanup a protected final-zero.
2. Fix the protected partial-apply path before another create. Reproduce the
   exit-143 interruption, make state/provider reconciliation account for
   provider-only databases, and qualify an exact reviewed cleanup/recovery
   mechanism. The recovery source-run name check also requires correction.
3. Obtain a new bounded pilot envelope and fresh credentials. Verify the
   exact source, signed release, budget, backend and management handoff before
   a new protected plan/apply. Keep the issuing Tailscale OAuth client active
   until the five node keys have actually enrolled; revocation invalidated
   unused keys in `b`.
4. Prove live Fleet state: three etcd members/quorum, Consul/Nomad enrollment,
   two ingresses and trusted public TLS, a staging workload with write/read,
   fault and restore, and loaded 2→3→2 scaling and drain. Retire the disposable
   pilot inside its new envelope with independent provider/runtime/final-zero
   evidence. Then finish Mini transition and M0–M9 owner review.

## Evidence locations

- Owner-only `pilot261004b` handoff, raw Fleet and management state snapshots,
  two complete pre-cleanup inventories, run logs and exact-ID deletion records:
  `/Users/arti/.config/norn/fleet-next-full-e2e/successor-pilot261004b/fleet-protected-handoff/`.
- Protected [Fleet apply](https://github.com/antiartificial/norn-fleet/actions/runs/37273763546)
  and [automatic recovery](https://github.com/antiartificial/norn-fleet/actions/runs/37274493490).
- Exact dry-run incident planner and tests in isolated Fleet checkout
  `/Users/arti/.config/norn/fleet-next-full-e2e/worktrees/incident-fleet-orphan-cleanup`
  at `eb7db92e5cb7f136472c5f43c2c4a8e5c9d4bed0`. It was not the executor
  used for the later emergency teardown and does not provide protected proof.
- `pilot261004a` abort and backend receipts under
  `/Users/arti/.config/norn/fleet-next-full-e2e/successor-pilot261004b/` and
  older pilot evidence under `/Users/arti/.config/norn/fleet-next-full-e2e/`.

The final invoice and retained access cleanup are independent of the current
DigitalOcean resource-zero observation.
