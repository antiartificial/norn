# Mini Trove and bookmark workload check — 2026-09-26

Read-only point-in-time checks on Mini at approximately 23:45 UTC. No Nomad
job was forced, no bookmark sync or Like Trove capture was started, and no
credential value or personal bookmark content was copied into this record.

| Workload | Observation | Release-fixture meaning |
| --- | --- | --- |
| `like-trove` | Service and its periodic parents are running in Nomad. The retained daily-capture child submitted at 20:17 UTC completed with one `complete` allocation and task exit code 0. | Treat as an active service plus periodic-work fixture. A completed task is not proof of every downstream enrichment or future X API budget. |
| Field Harbor bookmarks | The AM and PM sync parents are enabled and running, scheduled at 08:10 and 20:10 America/Chicago. The owner-local `bookmarks-meta.json` records `lastIncrementalSyncAt=2026-09-26T13:21:59.287Z` and 41,093 bookmarks. No AM/PM child remains in the current Nomad job list, so the file timestamp is the direct observed progress signal. | Preserve the bookmark volume and application PostgreSQL binding independently of the Norn control DB. Verify the next scheduled child and ingestion result during release qualification. |
| Bookmark session | One read-only GraphQL `Bookmarks` GET with `count=1` returned HTTP 200, a data object, and zero GraphQL errors. The `X_CT0` and `X_AUTH_TOKEN` values were loaded only inside Mini from a mode-`0600` owner file. | The bookmark session worked at this instant. It can expire later and does not measure paid X API credits. |
| `ft-trove` | Source declares `deploy: true`, but no Nomad base job exists. | Keep distinct from running `like-trove`; it is a declared-only M0 exception. |

The exact X API account balance, spend cap, and remaining paid credits were
not exposed by these runtime checks. A successful Like Trove capture and a
successful browser-session GraphQL bookmark read do not prove a numeric credit
balance or guarantee the next scheduled run. This check leaves M0 ownership,
backup, and upgrade gates open.

## X API billing console check — 2026-09-27 04:41 UTC

A read-only check of the signed-in X Developer Console account that lists the
`trovely` app showed **$10.00 remaining X API credit**, **$6.15 current spend**
for the **Sep 19–Oct 19, 2026** billing cycle, and a billing-cycle cap shown as
**Unlimited**. Auto Recharge was **Off**. This establishes that the account's
prepaid X API balance was not exhausted at that instant; it does not attribute
the spend to a particular app, guarantee the next capture, or change billing
settings. The bookmark-session validity observation above remains separate.
