# V3 integration review map — 2026-09-27

This map is for reviewing draft Norn [PR #77](https://github.com/antiartificial/norn/pull/77)
against protected `master`, then qualifying an exact candidate. It is not a
milestone sign-off or permission to merge or deploy.

At head `ff4ccf7e95e8eeaa6c611ddb81e74474d8f7955d`, GitHub reports 991
changed files, 149,576 additions, 3,160 deletions, and a mergeable draft.
The branch is 882 commits ahead of `origin/master`. The diff includes older
v2 work, multiple merged v3 implementation branches, and the protected-master
integration. A green suite is necessary but cannot replace path-by-path
review of this surface. Recheck these counts and the exact head before a
review decision.

## Review lanes in dependency order

| Lane | Main surface | Review question and stop condition |
| --- | --- | --- |
| 0. Baseline and protected controls | `.github/workflows`, release admission, Fleet runner authorization, external Fleet entrypoints | Compare the candidate to protected `master` at the current merge base. Preserve signed release, protected runner, and external Fleet admission controls. Stop if any default or permission is loosened without a separate reviewed contract. |
| 1. Mini preservation | `startup`, `controlrecovery`, schema migrations, legacy store reads, CLI | Show the exact current Mini source/schema can open passively with workers disabled, and that unchanged jobs, routes, identities, and keys survive an isolated upgrade and rollback. Source-copy compatibility is only a checkpoint; protected backup and clean-host restore remain open. |
| 2. M1 control and effects | `store`, `effect`, `pipeline`, `handler` acceptance | Trace each accepted mutation from signed intent through claim, external effect, ambiguous reply, successor recovery, and terminal record. Stop if an unresolved effect can be replayed or a stale claim can publish success. |
| 3. M2 database and evidence lifecycle | `database`, `archive`, `retention`, `artifactstore`, named consumers | Verify unchanged imported bindings, provider-specific backup/restore, archive-before-prune and recovery accounting. Stop if a target can be silently reselected or data can be pruned without recoverable evidence. |
| 4. M3 etcd authority | `etcdstore`, etcd router, worker, Fleet [PR #176](https://github.com/antiartificial/norn-fleet/pull/176) | Review operation/key invariants, quorum behavior, membership and full restore, with no mandatory control PostgreSQL path. Local three-member evidence does not close the protected separate-host gate. |
| 5. M4 app and ingress | `nomad`, managed deployment, `ingress`, Fleet [PR #177](https://github.com/antiartificial/norn-fleet/pull/177) | Require exact revision jobs, durable route generation authority, every-ingress publication/readback, endpoint probes and rollback before positive active weight. The two-ingress loopback fixture is local evidence only. Stop if desired Nomad/Consul weight is treated as observed traffic. |
| 6. Clients and remaining product paths | API contracts, CLI, web UI, NornUI client matrix, M6–M7 workflows | Confirm unsupported routes fail closed and clients retain old behavior. A fixture image and read-only app inventory do not prove app mobility or rolling upgrade. |
| 7. Candidate and release | Exact merged source, version matrix, signed bundle, fault/soak/growth and operator runbooks | Re-run protected checks at the exact reviewed head, produce the signed immutable candidate, then qualify M0–M8 evidence before any M9 adoption. A merge or bundle alone signs no gate. |

Review lanes may overlap where an entrypoint crosses packages. For each lane,
record the exact commit, the entrypoint/default-path comparison, negative and
recovery cases, test or protected-runtime evidence, reviewer, and unresolved
exceptions. Keep PR #77 draft while that record is incomplete. Independent
implementation merges can be considered only after their default behavior
and rollback boundary are reviewed; production qualification remains separate.

## Lane 0 source-review finding

The first protected-master comparison found one concrete response-policy
regression: the integrated `queueRelease` path had dropped
`preventSensitiveResponseCaching` from the entrypoint, although protected
`master` set `Cache-Control: no-store` and `Pragma: no-cache` before release
preflight/deployment responses. The header was restored on the shared queue
path and set at promotion entry before its early validation responses.
Focused cache-header and release-handler tests passed locally. This closes
that source finding only; signed-release admission, OIDC identity-store
changes, Fleet runner ownership, and exact-head protected CI still need
lane-0 review.

## Immediate blocking decisions

1. Choose Mini control-store RPO, off-host destination, and retention in the
   [M0 recovery decision](m0-mini-control-recovery-decision.md), then measure a
   protected clean-host restore.
2. Resolve GitHub account billing/spending status so Fleet PR #177's hosted
   contract job can start. Its local 1,410-test pass does not substitute for
   the required hosted lane.
3. Name the protected Fleet rehearsal target and owner-approved budget before
   provisioning. Qualify PR #176 before its stacked readback change, then
   exercise exact candidate pins across separate hosts.
