# Mini M0 read-only baseline refresh — 2026-09-26

The authenticated Mini inventory was collected at 21:38 UTC with the Norn platform inventory script. It is a point-in-time read of the live v2 control API; no deployment, job, database, or Fleet mutation was made. The detailed response files were held only in a local temporary directory and are not release artifacts.

| Observation | Current evidence |
| --- | --- |
| Running Mini API | `v2.20.0-platform-30-ga5da8ef` |
| App records | 27; `watchtower` still appears twice |
| Active operations | 0 |
| Active incidents | 13 |
| Service manifest entries | 44 |
| Fleet configuration | `fleet_configured=false`; 0 node pools |
| Host status | `ok` |
| Production readiness | `blocked` for the current v2 profile |

The app inventory still shows `ad-asset-verifier`, `ft-trove`, and `hello-norn` without a live base job. `its-alive-api` is still dead. The duplicate `watchtower` records both show one healthy allocation in the inventory; the [source join](m0-watchtower-source-join-2026-09-26.md) identifies their differing web commands and retained checkout. The latest [workload join](m0-mini-workload-join-2026-09-25.md) remains the detailed point-in-time app/job/route correlation. This refresh checks that its key exceptions persist; it does not re-prove every join.

Snapshot retention warnings remain for `field-harbor` and `turnkey-offer-intake`. The readiness result describes the current v2 development/compatibility posture; it is not a v3 candidate failure result. No active operation means the queue was clear at the instant of collection, not that a future upgrade drain is complete.

The later [inactive-job classification](m0-mini-inactive-job-classification-2026-09-26.md)
found all four exceptional source specs still declaring `deploy: true` in
dirty checkouts. Three have no current Nomad base job; `its-alive-api` has a
dead job with no current allocation. Their intended ownership and fixture
disposition still require review.

The later [Trove and bookmark workload check](m0-mini-trove-bookmark-workload-check-2026-09-26.md)
confirmed a completed Like Trove daily-capture child and a successful
bookmark-session read. It keeps the running `like-trove` workload separate
from the absent `ft-trove` declaration. Paid X API credit balance remains
unmeasured.

## M0 exit work still required

1. Confirm the clean deployed `watchtower` checkout as canonical, then verify the [staged discovery exclusion](m0-watchtower-source-join-2026-09-26.md) through the full v3 candidate API. The installed v2 API still reports both records. Classify the absent/dead jobs before deriving a representative upgrade fixture.
2. Assign owners and restore boundaries for the app volumes and database targets identified by the workload join; confirm unmatched ingress routes through destination and listener checks.
3. Retain a sanitized CI fixture and a separately protected private-data rehearsal plan. Re-run source/schema compatibility against the eventual exact candidate, including old-reader and rollback behavior.
4. Review the measured [control-store budget proposal](m0-mini-control-budget-proposal-2026-09-26.md), including the unsupported draft 15-minute backup RPO; accept byte, growth, capacity, and recovery budgets only after choosing and testing the backup mechanism. Resolve the proposed ADRs, named owners, and acceptance targets in the [decision register](decision-register-2026-09-24.md).

M0 has not passed. This refresh makes the next decisions concrete but does not authorize a Mini upgrade or Fleet provisioning.
