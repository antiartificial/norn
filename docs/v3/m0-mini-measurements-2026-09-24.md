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
