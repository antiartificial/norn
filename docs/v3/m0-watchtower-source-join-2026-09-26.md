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
