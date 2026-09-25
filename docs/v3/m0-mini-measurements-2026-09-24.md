# Mini M0 control-store measurements — 2026-09-24

Read-only snapshot at 2026-09-24 15:59 UTC. This extends the [API inventory](m0-mini-baseline-2026-09-23.md); it is one measurement, not an upgrade fixture, growth rate, restore proof, or M0 sign-off.

## Source identity

The running `norn-api` process (PID 93746 at measurement time and again at 16:30 UTC) resolved to `/Users/0xadb/go/bin/norn-api`. Its SHA-256 was `2fdc974ec8b7234c9f73ec9f2156abf1eee06bd63e3d2853572c6f93f79e6e31`. On 2026-09-24, the installed binary and `bin/norn-api` in the current exact-SHA release directory had that same hash. The release manifest binds `bin/norn-api` to that hash and names source SHA `a5da8ef15d12e9eca7561e90b90d96f6dc652a21`. The local manifest verifier returned `signed`, and Mini's configured external release signature hook accepted the retained release. The authenticated API reported that directory as current and reported `v2.20.0-platform-30-ga5da8ef`. Together these observations bind the running API binary to the signed release source SHA. They do not establish the live database's migration ledger or schema compatibility with v3.

`go version -m` identifies the binary as `norn/v2/api`, built with Go 1.26.6 for Darwin/arm64 with `-trimpath=true`; it does not embed a VCS revision. The signed release manifest supplies the source binding instead. Mini's working checkout was separately at `42a397a3773f1f91572abfe9d17944938fcd9246` on a deployment branch, so its `HEAD` must not be used as the running binary's source identity.

The `com.norn.api` user LaunchAgent was running as PID 93746. Its plist invokes only `/Users/0xadb/bin/norn-api-sops-launcher`, with `RunAtLoad` and `KeepAlive` enabled and no plist environment variables. The plist SHA-256 was `dd306d224f2b55fa131de2ec09dca51c6f439dc1bc8746d3b4c6b0d4c920dec8` (owner `0xadb`, mode `0644`); the owner-only launcher SHA-256 was `e6e6e11410389ec4e920f9cadbfd79fc70bd18f028ff3ef83de366fdd60f84ef` (mode `0700`). These hashes pin the launch entrypoint without copying its private configuration into this repository.

Postgres.app 17.7 served local database `norn_v2` as user `0xadb`. This database had 28 public base tables and 483 `operations` rows. It contained operation `8744a975-9c35-48fd-ac4b-4145ee3350fa`, which the 2026-09-23 Norn API inventory also reported. The current v2 database has no `schema_migrations` or `control_schema_migrations` table. A schema-only `pg_dump --no-owner --no-privileges` had SHA-256 `6895808a4e773d85f724f58c5db4b8a77ffbec5418c77af0cec49549428c10c5`; this hash fingerprints the dump output, not a migration ledger.

## Size and connections

All byte values come from PostgreSQL size functions. `pg_stat_user_tables.n_live_tup` is an estimate.

| Measurement | Value |
| --- | ---: |
| Database size | 240,323,731 bytes |
| User-table total size | 231,309,312 bytes |
| User-table heap size | 143,974,400 bytes |
| User-table index size | 86,269,952 bytes |
| Connections to `norn_v2` | 4 (1 active measurement query, 3 idle) |

| Largest table | Total bytes | Heap bytes | Index bytes | Estimated live rows |
| --- | ---: | ---: | ---: | ---: |
| `beacon_events` | 109,051,904 | 56,754,176 | 52,248,576 | 88,843 |
| `control_events` | 79,273,984 | 68,182,016 | 10,960,896 | 80,335 |
| `mutation_audit_events` | 30,130,176 | 12,967,936 | 17,121,280 | 54,320 |
| `saga_events` | 9,076,736 | 4,628,480 | 4,096,000 | 16,819 |
| `deployment_steps` | 1,531,904 | 802,816 | 688,128 | 2,599 |

Measurements used read-only `psql` queries against `pg_database_size`, `pg_stat_user_tables`, and `pg_stat_activity`; `pg_dump` output was piped directly to `shasum`. No rows, credentials, app definitions, or dump contents were copied into the repository. Repeat the same measurement after an agreed interval to establish growth and derive retention and restore budgets. The schema migration/version mapping, launcher's decrypted runtime inputs, sanitized fixture, private restore, and owner map remain open M0 evidence.

A second read-only sample at 2026-09-24 16:30 UTC measured database size 240,422,035 bytes and user-table total 231,407,616 bytes, each 98,304 bytes above the 15:59 UTC sample. Heap size was 144,031,744 bytes, index size 86,310,912 bytes, and the connection snapshot was again one active measurement query plus three idle sessions. This 31-minute delta is a short observation, not a retention or capacity growth rate.

A third read-only sample at 2026-09-24 18:02 UTC measured database size 240,741,523 bytes and user-table total 231,727,104 bytes, each 417,792 bytes above the 15:59 UTC sample. Heap size was 144,236,544 bytes, index size 86,425,600 bytes, and four database connections were observed. The largest tables were `beacon_events` (109,223,936 bytes), `control_events` (79,413,248), `mutation_audit_events` (30,236,672), `saga_events` (9,076,736), `deployment_steps` (1,531,904), and `operations` (761,856). This roughly two-hour observation remains too short to set a retention growth budget or restore capacity target.

A fourth read-only sample at 2026-09-24 23:57 UTC measured database size
241,888,403 bytes and user-table total 232,873,984 bytes. These are 1,564,672
bytes above the 15:59 UTC sample, over about eight hours. Heap size was
144,949,248 bytes, index size 86,859,776 bytes, and four connections were
observed. The largest tables were `beacon_events` (109,748,224 bytes),
`control_events` (79,831,040), `mutation_audit_events` (30,441,472),
`saga_events` (9,076,736), `deployment_steps` (1,531,904), and `operations`
(761,856). The running API binary SHA-256 remained
`2fdc974ec8b7234c9f73ec9f2156abf1eee06bd63e3d2853572c6f93f79e6e31`
and `/api/version` still reported `v2.20.0-platform-30-ga5da8ef`. A fresh
read-only Norn inventory succeeded and reported 27 app records. No Mini
database write, release change, or app mutation was made. This same-day
observation is useful for detecting a gross growth error, but it does not
capture workload cycles or establish a numeric reserve or retention budget.

A fifth read-only sample at 2026-09-25 01:07 UTC measured database size
242,076,819 bytes and user-table total 233,062,400 bytes. Both are 1,753,088
bytes above the 15:59 UTC baseline, over about nine hours. Heap size was
145,055,744 bytes and indexes were 86,941,696 bytes. The largest tables were
`beacon_events` (109,838,336 bytes), `control_events` (79,896,576),
`mutation_audit_events` (30,474,240), `saga_events` (9,076,736),
`deployment_steps` (1,531,904), and `operations` (761,856). This SSH query
used PostgreSQL aggregate size functions only; it read no application rows or
credentials. The interval remains too short to set a representative growth
or restore reserve.

A sixth read-only PostgreSQL aggregate sample at 2026-09-25 01:46 UTC measured
database size 242,166,931 bytes and user-table total 233,152,512 bytes,
1,843,200 bytes above the 15:59 UTC baseline over about ten hours. Heap size
was 145,121,280 bytes and indexes were 86,966,272 bytes. This still does not
cover a representative workload cycle or establish the retention and restore
budgets.

A seventh read-only aggregate sample at 2026-09-25 08:27 UTC measured database
size 243,305,619 bytes and user-table total 234,291,200 bytes. Each is
2,981,888 bytes above the 15:59 UTC baseline over about 16.5 hours. Heap size
was 145,809,408 bytes, indexes were 87,416,832 bytes, and four connections
were observed. The authenticated Mini inventory at 08:23 UTC still reported
27 app records, zero active operations, 13 active incidents, host status `ok`,
and production readiness `blocked`. This longer observation still does not
include a full representative workload cycle or establish a numeric retention,
backup-space, or restore-time budget.

An eighth read-only aggregate sample at 2026-09-25 09:58 UTC measured database
size 243,559,571 bytes and user-table total 234,545,152 bytes. Each is
3,235,840 bytes above the 2026-09-24 15:59 UTC baseline over about 18 hours.
Heap size was 145,973,248 bytes and indexes were 87,506,944 bytes. An
authenticated, read-only inventory at the same time reported API version
`v2.20.0-platform-30-ga5da8ef`, healthy control services, 27 app records,
zero active operations, host status `ok`, production readiness `blocked`, and
`fleet_node_pools.configured=false`. This remains an observation rather than
a representative workload-cycle rate or a numeric release budget.

A ninth read-only aggregate sample at 2026-09-25 13:55 UTC measured database
size 244,206,739 bytes and user-table total 235,192,320 bytes. Each is
3,883,008 bytes above the 2026-09-24 15:59 UTC baseline over about 22 hours.
Heap size was 146,374,656 bytes and indexes were 87,752,704 bytes. A fresh
authenticated inventory at 13:54 UTC still reported the v2.20.0 API, 27 app
records (including the duplicate Watchtower source), zero active operations,
and production readiness `blocked`. It also reported snapshot-retention
warnings for `field-harbor` and `turnkey-offer-intake`. This observation still
does not establish a representative workload cycle or a reviewed retention,
backup-space, or restore-time budget. No Mini mutation was made.

An additional read-only aggregate sample at 2026-09-25 18:41:32 UTC measured
database size **244,993,171 bytes**, user-table total **235,978,752 bytes**,
heap **146,866,176 bytes**, and indexes **88,047,616 bytes**. Database size
grew **4,669,440 bytes** from the 2026-09-24 15:59 UTC baseline over about
26.71 hours, a linearized **4.00 MiB per day** for this one observed interval.
The largest tables were `beacon_events` (111,280,128 bytes),
`control_events` (81,084,416), and `mutation_audit_events` (30,760,960).
The prior and current 24-hour windows contained 1,501 and 1,500 Beacon
events, 1,549 and 1,500 control events, and 1,691 and 1,694 mutation audit
events respectively. These are aggregate counts, not application rows.

At 18:41 UTC, the authenticated read-only inventory still reported signed v2
API version `v2.20.0-platform-30-ga5da8ef`, 27 app records, zero active
operations, host status `ok`, and production readiness `blocked`. Mini's data
volume reported 36 GiB available. No Mini database, app, job, release, or
provider state was changed.

For planning only, doubling the observed daily byte growth gives an **8 MiB
per day** control-database growth allowance. Over 30 days that adds about
240 MiB to the current 234 MiB database; a **512 MiB control-database size
review threshold** rounds that envelope upward. This is a provisional sizing
trigger, not a retention policy, backup reserve, RPO, RTO, or M0 exit budget.
The interval covers one calendar day at this workload, but no workload-cycle
classification or timed current-head backup/restore evidence establishes those
other budgets. Recheck the trend after a representative cycle and review the
numeric objectives before release approval.

At 2026-09-25 18:44 UTC, a read-only PostgreSQL 17.7 custom-format
`pg_dump --no-owner --no-privileges` of `norn_v2` streamed directly to a byte
count on Mini. It emitted **13,163,661 bytes** and completed in **0.82 seconds**
wall time (`user 0.69`, `sys 0.02`). The shell used `pipefail`; the dump
process and byte count both exited successfully. No archive bytes or rows were
copied to this checkout, and the temporary timing text on Mini was removed.
This measures backup extraction under the observed load. It does not measure
durable protected-backup publication, retention cost, restore time, validation,
or application recovery, so it is not an RPO or RTO result.
