# Mini M0 read-only baseline refresh — 2026-09-27

The authenticated Mini inventory was collected at approximately 21:39 UTC
through the Norn platform inventory script. This is a point-in-time read of the
installed v2 control API. It made no deployment, job, database, or Fleet change.
Detailed API responses are held in a local working directory, not in this
release document.

| Observation | Current evidence |
| --- | --- |
| Running Mini API | `v2.20.0-platform-30-ga5da8ef` |
| API and host status | `ok` |
| App records | 27; `watchtower` appears twice |
| Service manifest entries | 44 |
| Active operations | 0 |
| Active incidents | 13 |
| Fleet configuration | `fleet_configured=false`; 0 node pools |
| Production readiness | `blocked` for the installed v2 profile |

The four previously classified exceptions persist: `ad-asset-verifier`,
`ft-trove`, and `hello-norn` have no active allocation, and `its-alive-api`
has a dead job and no active allocation. Both `watchtower` records still show
one healthy allocation. Snapshot-retention warnings persist for
`field-harbor` and `turnkey-offer-intake`. These are runtime observations; the
[September 26 source and workload classification](m0-mini-baseline-refresh-2026-09-26.md)
remains the detailed ownership context. External account checks for an app
are outside the v3 release assessment.

This confirms that the installed Mini baseline has not advanced to a v3
candidate. M0 and M5 remain open. Before the representative upgrade rehearsal,
resolve the off-host control backup target and owner, classify the four
inactive/dead jobs for fixture inclusion, and capture a protected current-state
copy with no production mutation credentials or network route. An empty
operation queue at this instant is not a verified upgrade drain.
