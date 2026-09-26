# Mini Watchtower source join — 2026-09-26

Read-only Mini API, Nomad, and checkout inspection at approximately 21:40 UTC. No app, job, source tree, or discovery marker was changed. The authenticated inventory response was reduced locally and removed after analysis.

| Evidence | Observation |
| --- | --- |
| Norn app inventory | Two `watchtower` records; both declare deployment and report the same one healthy allocation |
| Spec comparison | The two normalized InfraSpecs differ at `/processes/web/command`; their other fields compare equal |
| Nomad job | One running `watchtower` service job, version 41, with one running allocation and a successful latest deployment |
| Current checkout | `watchtower` at `8ea1c57`, clean, no `.norn-discovery-ignore` marker |
| Retained checkout | `watchtower.pre-git-20260916165652` at `ade8d26`, nine dirty paths, no discovery-ignore marker |

The [earlier source review](m0-mini-topology-refresh-2026-09-25.md) matched the deployed source commit to the current checkout. Today's read confirms the two checkouts still exist and the duplicate still maps to one live job. Since their web commands differ, choosing either record by name or array order would make a representative upgrade fixture ambiguous. The live allocation does not resolve which duplicate record a future discovery pass would select.

M0 source-owner action: confirm the current checkout as the intended source, explicitly exclude the retained dirty checkout through the supported discovery marker, then re-run the candidate binary's authenticated inventory and require exactly one Watchtower record whose command and source revision match the intended checkout. Preserve the retained checkout's nine dirty paths during that operation. This is a gate for fixture selection, not permission to change the live job.

## Candidate discovery exclusion — 2026-09-26

A fresh read-only Mini check found the latest successful Watchtower deployment
bound to commit `8ea1c575859f3ac507d1e95b5eed76b0ae4bf5f8`, exactly the
clean current checkout. The retained `watchtower.pre-git-20260916165652`
checkout remained at `ade8d26` with seven modified and two untracked paths.
The two installed-v2 API records matched the current and retained web commands
one-for-one. No marker existed in either checkout before this action.

The current PR's `model.DiscoverAllApps` was compiled into a disposable probe
and run against Mini's actual project directory. Before the marker it returned
27 apps and two Watchtower records. An empty, owner-owned regular
`.norn-discovery-ignore` file (mode `0644`) was then created **only** in the
retained checkout. The same candidate discovery code returned 26 apps and one
Watchtower record. Byte hashes, sizes, modes, and status codes for all nine
pre-existing dirty paths matched their private pre-change manifest; the marker
was the only new path. The disposable probe and manifest were removed.

The installed Mini v2 release predates the discovery-marker support, so its
authenticated API still reported 27 records and two Watchtowers afterward.
The current checkout stayed clean at `8ea1c57`; the live Nomad job remained
version 41, running with one running allocation. No deployment, API restart,
job mutation, or source-content cleanup occurred. The marker prepares
unambiguous discovery for the v3 candidate; a full candidate API inventory and
operator review of the retained checkout are still required before M0 sign-off.

## Candidate handler qualification — 2026-09-26

At PR #76 candidate head `afa3fb7`, a compiled Darwin/arm64 `handler` test
binary invoked the current `ListApps` handler against Mini's actual
`/Users/0xadb/projects` directory. It returned 26 records and exactly one
Watchtower record. The API-visible Watchtower spec in that response matched
the spec loaded from the current `watchtower` checkout. A deterministic local
fixture separately verifies that a retained checkout with the marker is
excluded by `ListApps`. The test binary and transfer directory were removed
from Mini and the local machine after the run.

The handler was constructed without a database or Nomad client. This confirms
candidate handler discovery and serialization against the present source
layout, but not authentication, live allocation enrichment, or full candidate
API startup. Installed v2 continues to report the duplicate until a v3
release is actually selected. Owner review of the retained checkout and the
remaining M0 fixture/restore decisions remain open.
