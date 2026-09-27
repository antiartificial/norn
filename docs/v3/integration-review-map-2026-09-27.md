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

The next OIDC comparison found a typed-nil identity-store regression in the
PostgreSQL exchange after the shared interface refactor. The prior code
rejected a nil `h.db`; the integrated wrapper passed it as a non-nil
interface value. The wrapper and shared exchange now fail with 503 before
parsing an assertion when their backing store is missing, including a typed
nil Fleet store. Focused negative tests passed. Separately, the Fleet OIDC
single-use assertion and issued-token registry integration test passed
against a disposable real etcd instance; its listener was stopped afterward.
This verifies those bounded cases, not all production GitHub claim policies
or Fleet runner recovery.

The focused Fleet runner ownership test passed locally: a token needs the
exact `fleet:operate` scope, runner attempt ID, and canonical workflow URL
for its signed GitHub run attempt; the test rejects another run and a
compatibility `api:write` token. The release manifest and artifact helper
suites also passed locally (9 and 8 tests). The workflow diff adds the
effect runner to the bundle without changing the protected release trigger
or permissions. These checks do not replace exact-head protected CI or a
review of the full Fleet dispatch/recovery path.

At head `22a000c516e1f9eb41847745e97c88f2bd13143b`, a direct
`origin/master...HEAD` workflow comparison confirmed that
`platform-release.yml` changes only the bundle build line for
`norn-effect-runner`. The protected `master` dispatch check, secretless build
jobs, `platform-release` publish environment, `contents: write` only on the
publish job, signing, remote asset comparison, and immutable-publication
checks are unchanged. The effect runner is also required by the bundle
manifest and installed and verified by the platform-upgrade path. The exact
head's reported GitHub checks all passed, including release bundle rehearsal.
This closes the workflow-delta review only; it does not sign the candidate or
prove the full admission/rollback chain.

The later M4 ingress-observer slice adds one more binary build line to
`platform-release.yml` and its exact manifest set. A local secretless bundle
rehearsal built and imported the candidate with an ephemeral test signature;
the manifest and artifact helper suites passed. The bundle test now builds
UI dependencies in a temporary copy so a worktree's `node_modules` symlink
cannot mutate another checkout. Re-review this expanded workflow diff against
protected `master` at the final candidate head before signing. The observer
has no Fleet installation or traffic-publication authority yet.
The `platform_upgrade_integration_test.py` fixture now boots its synthetic
candidate API in passive/check mode. Its signed fetch/import test passed
locally with global Git config disabled, reaching schema-safe preflight and
verifying the imported release. The fixture uses a synthetic API and ephemeral
signing key, so it does not establish representative Mini state, production
signature, off-host restore, or rollback for M5.

The production rollback entrypoint also lacked the durable-dependency guard
used by the shared release queue. An authorized request could reach a nil
store or pipeline during a degraded startup. It now returns 503 before
decoding the request when either dependency is absent. Focused handler tests
cover both cases and the response cache policy. This is an entrypoint
availability fix, not a rollback or release-gate qualification.

The staging qualification entrypoint similarly now rejects a missing release
pipeline with 503 before parsing or accepting a request. Focused tests cover
the unavailable dependency and no-store header. PR #77 checks at exact head
`4f8f7bd591c380536c5c22f28f36edc405f16eea` all completed without a
reported failure; recheck CI on subsequent heads.

A later release-scope review found that scoped tokens were checked for a
nonempty environment but not against the environment of the control plane
handling the request. The shared release-scope helper now requires the exact
current environment and, for CI tokens, matching CI identity environment.
Focused tests reject a staging-scoped promotion token on production before
request processing. Existing legacy/admin and API-write compatibility paths
remain separate; the change narrows scoped release tokens only.

## Lane 1 Mini-preservation checkpoint

At exact head `7e21e324a410f40c72682ec7ed696660cb2240ad`, the hosted V3
`api` job passed with PostgreSQL and etcd service fixtures. The job runs
`go test ./...` under `v2/api` with `NORN_TEST_DATABASE_URL` set, so the
real-binary passive startup test is eligible and is not skipped for a missing
database URL. Source review confirms the PostgreSQL startup path checks schema
compatibility before telemetry, recovery, watchers, and workers; passive mode
requires `check`, validates loopback binding, and serves only health, version,
and schema. The passive check uses a read-only repeatable-read transaction
while validating serving-writer compatibility. None of `main.go`, `startup`,
`startup_runtime.go`, or `store/schema_migrations.go` changed after the last
[exact integrated-head Mini private-copy rehearsal](release-gate-status-2026-09-26.md).

This supports the passive-startup checkpoint only. It does not establish a
production-key backup, off-host restore, unchanged live identity/traffic after
upgrade, or a compatible rollback target. M0, M1, and M5 remain open.

## Lane 5 ingress stop condition

Source review at head `4d8a024a` found that the private etcd
`finishClaimedDeployment` path validates `ActiveWeight` against the accepted
desired weight but accepts the terminal region value supplied by its caller.
`ingress.ObserveRenderedRoute`, node endpoint probes, and public-host probes
are separate read-only helpers; no durable region result binds their output
to the exact accepted deployment, route generation, complete Fleet ingress
inventory, and public load-balancer response. The local two-ingress fixture
therefore cannot authorize a positive active weight on Fleet. Keep normal
etcd app dispatch closed until the publisher and proof-to-result boundary are
implemented and exercised on protected separate hosts. A claimed Nomad
health result or caller-filled region value is insufficient for this gate.

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
