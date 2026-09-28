# Mini control baseline refresh — 2026-09-28

At 05:25–05:27 UTC, the authenticated Norn inventory script read the live Mini
API, and a separate owner-local, read-only PostgreSQL query checked WAL archive
settings. No deployment, backup, database write, workload, or route mutation
was performed. The temporary inventory responses were not retained as release
artifacts.

| Observation | Current readback |
| --- | --- |
| API version | `v2.20.0-platform-30-ga5da8ef` |
| Installed release path | `releases/a5da8ef15d12e9eca7561e90b90d96f6dc652a21` |
| App records | 27, including two `watchtower` records |
| Active operations | 0 at the instant of collection |
| Active incidents | 13 |
| Service manifest entries | 44 |
| Fleet | `fleet_configured=false`, 0 node pools |
| Host status | `ok` |
| Current v2 production readiness | `blocked` |
| PostgreSQL archive mode and timeout | `off`, `0` |
| PostgreSQL archived and failed WAL counts | `0`, `0` |

The source release for the [private-copy M5 rehearsal](m5-mini-private-copy-schema45-2026-09-27.md)
has not moved. This readback does not revalidate the private copy against the
latest v3 candidate, prove a production-key backup, or establish an off-host
restore. It confirms that the [M0 recovery decision](m0-mini-control-recovery-decision.md)
remains a release prerequisite. The inactive/dead job and duplicate-app
exceptions in the [earlier detailed baseline](m0-mini-baseline-refresh-2026-09-26.md)
also remain visible; their ownership still needs review before selecting the
representative upgrade fixture.
