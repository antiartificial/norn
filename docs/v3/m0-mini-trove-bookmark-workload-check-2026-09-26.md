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

## Mini workload and bookmark session refresh — 2026-09-27 04:44 UTC

Read-only Nomad status on Mini showed the Sep 26 20:10 America/Chicago Field
Harbor PM sync child with one complete allocation and zero failed allocations.
The owner-local bookmark progress marker advanced to
`lastIncrementalSyncAt=2026-09-27T01:21:30.256Z` and **41,150 bookmarks** (57
more than the earlier check). The Sep 26 21:17 America/Chicago Like Trove
daily-capture child likewise had one complete allocation and zero failures.

A fresh `Bookmarks` GraphQL GET with `count=1` returned HTTP 200, a data
object, and zero GraphQL errors. The session values were loaded only on Mini
from the same mode-`0600` owner file, and neither values nor bookmark content
were copied into this record. This proves the bookmark session worked at the
refresh instant. Job completion and the progress marker do not by themselves
prove every downstream enrichment stage or future credential validity.

## Read-only refresh — 2026-09-27 10:38 UTC

The X Developer Console for the account listing `trovely` showed **$9.79
remaining prepaid X API credit**, **$6.37 current spend** in the Sep 19–Oct 19
billing cycle, an **Unlimited** cycle cap, and **Auto Recharge Off**. Credit is
available now; the balance has fallen $0.21 since the earlier console check.
The console does not attribute that change to Like Trove.

Mini's `like-trove` service and daily-capture periodic parent were running.
The latest retained daily-capture child, launched Sep 27 at 03:17
America/Chicago, had a complete allocation and exit code 0. The Field Harbor
AM/PM periodic parents were running; the PM parent listed its next launch for
Sep 27 at 20:10 America/Chicago. The bookmark marker remained at
`lastIncrementalSyncAt=2026-09-27T01:21:30.256Z` and **41,150 bookmarks**;
there had been no later scheduled AM/PM sync at this check.

A fresh read-only X `Bookmarks` GraphQL GET with `count=1` returned HTTP 200,
data present, and zero GraphQL errors. The probe loaded session values inside
Mini from the owner mode-`0600` file and emitted only status flags. This proves
the stored session was valid for this read at the check instant. It does not
guarantee the next sync or the future credit balance.

## Read-only refresh — 2026-09-27 11:33 UTC

The signed-in X Developer Console account listing `trovely` showed **$9.79
remaining prepaid X API credit**, **$6.37 current spend** for Sep 19–Oct 19,
2026, an **Unlimited** cycle cap, and **Auto Recharge Off**. These values have
not changed from the 10:38 UTC check. The console does not attribute spend to
Like Trove or guarantee the next scheduled call.

Mini's latest retained Like Trove daily-capture child was submitted at 08:17
UTC and had one complete allocation; its `daily-capture` task terminated with
exit code 0. The task log was unavailable through this read-only Nomad query.
A separate attempted read-only query of the capture job's declared PostgreSQL
URL from the Mini host failed at name resolution, so no poll-row counts were
claimed from this refresh. This does not change the allocation result or
establish a database failure inside the allocation.

The Field Harbor AM/PM sync parents were running, with no retained AM/PM child
in the current job listing. The owner-local bookmark marker remained at
`lastIncrementalSyncAt=2026-09-27T01:21:30.256Z` and **41,150 bookmarks**;
the next AM sync was not yet due at this check. A fresh read-only `Bookmarks`
GraphQL GET with `count=1` returned HTTP 200, data present, and zero GraphQL
errors. The owner-only cookie file remained mode `0600`; no cookie value or
bookmark content was copied. This proves the stored session worked at that
instant, not that the next ingest or credit-funded capture will complete.
