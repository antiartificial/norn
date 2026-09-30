# Mini M0 baseline refresh — 2026-09-30

Status: read-only evidence refresh for M0 review. This document does not accept
an ADR, approve a numerical release budget, sign a milestone, or authorize a
Mini, Fleet, provider, Tailscale, or pilot mutation.

## Evidence boundary and provenance

At 15:13–15:14 UTC, the authenticated inventory script read the live Mini API.
A separate read-only SSH session inspected launchd, the Mini Norn checkout, two
application source checkouts, and the rendered Nomad jobs named `mail-mcp` and
`signal-sideband`. No deployment, database write, job registration, route
change, backup, provider operation, or credential read was performed.

The source repositories were refreshed before this record was written:

| Surface | Protected source observed |
| --- | --- |
| Norn | `origin/master` at `2ba6ab826f5bfd42e989b29ab7f44c11daace9db` |
| Fleet | `origin/main` at `5d54dba78a841eac1e909bfcb1a6f89f5ef08379` |
| NornUI | `origin/main` at `742445c491cd58c3e8992dab2ccafb212d938fab` |

The Mini reports API version `v2.20.0-platform-30-ga5da8ef`. Its local Norn
checkout was at `42a397a3773f1f91572abfe9d17944938fcd9246` on
`deployed/42a397a-host-recovery`, 21 commits behind its refreshed
`origin/master`. The running version and installed signed release manifest,
not that checkout, are the binary provenance authorities. This refresh did not
rehash the installed binary or release manifest, so that exact digest remains
a required maintenance-window check.

The raw authenticated responses remained in an owner-local temporary inventory
directory and were not added to Git. Their SHA-256 digests allow an operator
with authorized access to compare the exact captures without publishing app
definitions, private endpoints, or configuration:

| Response | SHA-256 |
| --- | --- |
| `version.json` | `5f9b1d0d3a78850c44a3c38ceb323a80a94ef393918878031c59e51b2bec1314` |
| `apps.json` | `5a32c91187e22de07c4acd1ff09f5cfbedcd2ba7805a9688880e48a5360e09c6` |
| `services_manifest.json` | `8a39d782d7cecdaf44b24cc7c7266ea2bf6dd7f2835c70d12ee04763d99e3b2b` |
| `operations_active.json` | `3032f8f1f755699caa5cea65e7daf461d3d87d96f40c2614c375e92a0ab382b3` |
| `events_active.json` | `174c30859d19264fac4432177b24e646829a5bb59c075bd551dce5d5732f66fe` |
| `host_status.json` | `a0e6cdb27a52bc579a1ba4d70de6d6074c58061d42281070fafab64fefc6cf64` |
| `production_readiness.json` | `aba49a0703e2f3414d779884f844f0a839d20cf265baf6c4659721a021849676` |
| `fleet_node_pools.json` | `9df3775c5b8416d0ac68f66284019d8bd7af1159c17399e56bb1905c52d00f29` |

## Current control and runtime readback

| Observation | Current readback |
| --- | --- |
| App records | 29 |
| Service manifest entries | 46 |
| Active operations | 0 at collection time |
| Active incidents | 13 |
| Host status | `ok` |
| Production readiness | `blocked`: 7 passed, 1 warning, 18 failed |
| Fleet | `configured=false`, 0 node pools |
| Running API supervisor | `com.norn.api`, running through `norn-api-sops-launcher` |

The [September 28 refresh](m0-mini-baseline-refresh-2026-09-28.md) recorded 27
app records and 44 service entries. The two-record increase is accounted for by
an additional discovered InfraSpec for each of `mail-mcp` and
`signal-sideband`. The follow-up below identifies their source directories;
it does not establish who created them or authorize their removal.

The inventory still reports known disabled or unhealthy applications and
snapshot/secret warnings. Host status `ok`, production readiness `blocked`, and
13 active incidents describe different checks and must not be collapsed into a
single health claim. Fleet remains unconfigured on this Mini; protected Fleet
source and no-cloud evidence do not prove a live Fleet.

## Duplicate `mail-mcp` identity join

The API returns two `mail-mcp` records. Both declare the same repository URL,
branch, process name `mcp`, port 18080, one replica, and 256 MiB memory. After
secret references and process environment were omitted, their canonical
SHA-256 digests were:

- record A: `bbfdc389d025598513cfc11abd97762e0e2d3116044a9bf6fdaa2bf9de88a3d7`
- record B: `a7f4864dd41761db0c3655a877bf4d70ef96e3f6e7f8f870b21172902f41170b`

Record A declares 25 CPU shares, a `/health` check, and the current tailnet
endpoint on port 18080. Record B declares 100 CPU shares, has no health check,
and contains a second tailnet endpoint plus one empty endpoint entry. The
service manifest contains two same-named `mail-mcp-mcp` entries reflecting
those two endpoint sets.

Read-only joins identify the active runtime configuration:

- Nomad has one running job with ID `mail-mcp`, version 3, one `mcp` task,
  25 CPU shares and 256 MiB memory.
- `/Users/0xadb/projects/mail-mcp/infraspec.yaml` has SHA-256
  `16628edaae7a8df0f6fc3eed69eff0e1e15510c3f5509f0f78e2e0e794a6b493`
  and declares the same 25/256 resources, health check, port and endpoint as
  record A.
- The source checkout is at `7a5dd2bb8c779dddc97ab289427f613cf1fcf2cf`
  and is dirty, including its InfraSpec and encrypted secret file. It is also
  24 commits ahead of its configured remote branch. The file hash records the
  inspected source state without treating the Git commit as the whole spec.
- No cloudflared hostname in the current owner-local ingress summary names a
  `mail-mcp` endpoint. The observed route is the tailnet endpoint from record A
  and the manifest; this refresh did not perform an endpoint request.

Therefore record A matches the single rendered Nomad job and the current dirty
source InfraSpec. Record B has no distinct Nomad job found by name. The
follow-up below identifies its source checkout, but does not establish who
created that checkout or whether an owner wants to retain it. Do not mutate
either source from this join alone.

## Duplicate `signal-sideband` identity join

The API returns two `signal-sideband` records. Both declare the same repository,
branch, `web` process, command, port 3001, health check, one replica, 1024 MiB
memory, PostgreSQL database name, public endpoint, and media volume. After
secret references and process environment were omitted, their canonical
SHA-256 digests were:

- record A: `ab9cc47f7b8b0b35c12dc852edcadbdc80453562e4ba7df262bd85932b852929`
- record B: `9f4f9c063bfb882faa8f3bf5de6ab59b1668d776cf7e2e734e08ded35c234fa6`

Record A declares 25 CPU shares and snapshot retention (`keep: 3`, enabled).
Record B declares 200 CPU shares and has no snapshot block. The service
manifest contains two same-named `signal-sideband-web` entries for the same
public endpoint. The cloudflared ingress summary contains that hostname once.

Read-only joins identify the active runtime configuration:

- Nomad has one running job with ID `signal-sideband`, version 0, one `web`
  task, 25 CPU shares and 1024 MiB memory.
- `/Users/0xadb/projects/signal-sideband/infraspec.yaml` has SHA-256
  `cff7a4ce50d717ac15b2d3477121ccefc0869c5fc89f8cae7a468a30d8bbe6c4`
  and declares the same 25/1024 resources, snapshot policy, database, volume,
  health check, port and endpoint as record A.
- The source checkout is at `31701b21777798c2b1e42881a643ec7ed09485af`
  and is dirty in its InfraSpec and encrypted secret file. The file hash records
  the inspected source state without claiming the commit alone defines runtime.

Therefore record A matches the single rendered Nomad job and the current dirty
source InfraSpec. Record B has no distinct Nomad job or route found. The
follow-up below identifies its source checkout, but does not establish who
created that checkout or its intended lifecycle. Do not infer that the
duplicate manifest row means two allocations or two independently configured
public routes.

## Read-only discovery-source follow-up — 18:16–18:27 UTC

A fresh authenticated inventory still returned 29 apps, 46 manifest entries,
zero active operations and no configured Fleet. Read-only Mini filesystem and
Git inspection then identified the extra InfraSpecs under the API's scanned
projects directory:

| API identity | Runtime-matching source | Additional discovered source | CPU shares | Additional source HEAD |
| --- | --- | --- | --- | --- |
| `mail-mcp` | `projects/mail-mcp/infraspec.yaml` | `projects/mail-mcp-v3-db-readiness/infraspec.yaml` | 25 versus 100 | `eec3db157a8cbcebcbde48bd30491256ce90c4bd` |
| `signal-sideband` | `projects/signal-sideband/infraspec.yaml` | `projects/signal-sideband-v3-readiness/infraspec.yaml` | 25 versus 200 | `871e45e621b6f07a99c77929e56155bcf0069762` |

Both additional checkouts were clean on `codex/v3-db-readiness` at inspection
time. Their `deploy: true` InfraSpecs account for the second named inventory
and manifest entries. Source at the API-reported `a5da8ef` version shows
`model.DiscoverAllApps` scanning every immediate child directory with a
valid named InfraSpec; it does not read an application table for this
inventory. The source's runtime discovery also includes enabled copies. The
observed rows match that mechanism, without proving the installed binary's
digest or asserting that a second Nomad job, route or database record exists.

Protected master now supports an explicit `.norn-discovery-ignore` marker for
retained source directories, but the installed Mini API version predates that
change. Resolution requires either a later signed Mini release followed by an
owner-approved marker on the retained readiness checkouts, or an owner-approved
relocation outside the scanned projects directory using the installed version.
The source checkouts and live runtime were not changed by this follow-up.

## Fixture and budget evidence boundary

The routine sanitized fixture remains
`TestSyntheticMiniControlUpgradeAndReaderBoundary` in
`v2/api/store/mini_synthetic_upgrade_integration_test.go`. It exercises invented
legacy-shaped control rows, current schema migration, idempotence, and reader /
writer compatibility fences. It does not contain Mini data or prove the
installed v2 binary, Nomad/Consul, routes, external effects, private restore,
or rollback.

The [control-store budget proposal](m0-mini-control-budget-proposal-2026-09-26.md)
still proposes 384 MiB warning, 512 MiB action, and 2 GiB private-restore
scratch thresholds from short observed growth samples. This refresh did not
remeasure database bytes, connection peaks, WAL, archive volume, or restore
time, so it neither accepts nor revises those numbers. The
[development-Mini recovery decision](m0-mini-control-recovery-decision.md)
continues to allow a fresh verified on-host upgrade backup without making a
host-loss RPO/RTO claim.

## Proposed decision-review worksheet

This table is a review aid only. Every disposition remains unchanged until a
named human reviewer records a decision in the decision register. Repository
ownership is delivery responsibility, not approval.

| Decision | Current disposition | Accountable repository role | Evidence needed for human review |
| --- | --- | --- | --- |
| ADR 0001 state/evidence/observability | Proposed | Norn | Accept or defer retention, archive retrieval and measured byte budgets. |
| ADR 0002 control store/fencing | Proposed | Norn; Fleet for etcd lifecycle | Complete consumer/effect inventory and PG-free Fleet startup qualification. |
| ADR 0003 profiles/database bindings | Proposed | Norn contracts/clients; Fleet provisioning | Name first supported app engines and review MySQL lifecycle/client parity evidence. |
| ADR 0004 scaling/placement | Proposed | Norn and Fleet | Decide scale precedence, placement/drain/replacement rules and measured availability targets. |
| ADR 0005 upgrade compatibility | Proposed | Norn and clients | Decide mixed-version/rollback window and API handoff budget from exact rehearsals. |
| ADR 0006 migration authority | Proposed | Norn, Fleet and selected app owners | Decide source fencing, cutover, reverse-recovery and provenance boundaries. |
| ADR 0007 auth aggregate/revocation | Accepted design invariant; runtime gate open | Norn | Retain acceptance; separately qualify crash/partition and execution-boundary fencing. |
| ADR 0008 Fleet app route authority | Proposed | Norn and Fleet | Review target/prior-route/generation and terminal public-proof contract. |
| Development Mini backup/rollback | Scope decision recorded; transition proof open | Norn release owner and Mini operator | Fresh backup, integrity/freshness verification, private restore with roles/ACLs/keys, installed-release digest and explicit rollback decision. |

No human approver is assigned by this document. No numerical proposal becomes
an SLA or release gate merely by appearing in this worksheet.

## Remaining M0 gaps

1. Decide whether the two V3 readiness source checkouts should remain under
   the scanned projects directory. If retained, exclude them from discovery
   after the installed API supports `.norn-discovery-ignore`; otherwise move
   them through a separately reviewed source-management action. Verify the
   resulting app and manifest identity counts without disturbing either live
   Nomad job. The identity of whoever created the copies remains unverified.
2. Record a named human decision and accepted/deferred budget for every
   M0-blocking row in the decision register. Do not infer acceptance from code.
3. At the maintenance window, pin the installed Mini release manifest and
   binary digest, create and verify a fresh protected backup, restore it
   privately with actual roles/ACLs/required keys, and record the rollback
   decision. This refresh is not that transition proof.
4. Reconcile the remaining app/job/route/volume/database ownership exceptions
   before selecting the representative M5 fixture. Duplicate record identity
   is only one part of the ownership map.

M0 remains open.
