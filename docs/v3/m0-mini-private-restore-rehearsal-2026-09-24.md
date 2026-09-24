# Mini private-data restore rehearsal — 2026-09-24

This is an isolated M0 data-fidelity checkpoint for v3 integration commit `e5387ba03909babd7d77b017841fe6ae576a1b4d`. It does not authorize or simulate a live Mini cutover. The Mini database was only read; its API, jobs, routes, and credentials were not changed.

## Source and isolation

The live Mini API binary hash checked after extraction was `2fdc974ec8b7234c9f73ec9f2156abf1eee06bd63e3d2853572c6f93f79e6e31`, matching the [signed source inventory](m0-mini-measurements-2026-09-24.md) for `a5da8ef15d12e9eca7561e90b90d96f6dc652a21`. PostgreSQL 17.7 `pg_dump` produced a custom-format, owner/privilege-free copy of `norn_v2`. The private dump was about 13 MB; it contained data and was never committed or printed.

The target was a disposable `postgres:16` container with image digest `sha256:a3b7f434b2dc57ce85a67e171163eb8ab1a1ebcb39d27484661f26b1dfbe30d6`. Docker inspection reported `network=none` and no published ports. The procedure started PostgreSQL and a purpose-built schema migrator in that container; it did not start a Norn API or worker. The dump and logs lived in an owner-only local temporary directory, and the dump file had mode `0600`. The host used SSH to read the Mini source, so the network isolation claim applies to the target container.

PostgreSQL 16's `pg_restore` cannot read the PostgreSQL 17 custom archive directly. The PostgreSQL 17 restore tool rendered SQL from the archive; the exact `SET transaction_timeout = 0;` line was removed because PostgreSQL 16 does not recognize that PostgreSQL 17 session setting. The first attempted restore stopped on that setting before table creation. Its disposable database was recreated before the filtered restore. No other dump content was changed.

## Observed result

The data restore completed with 28 legacy public tables. The candidate's `store.NewControlSchemaMigrator` then adopted the unversioned schema and applied migrations 1–16 successfully. Its ledger reported current migration 16, minimum reader 2, and minimum writer 13. A second migration run applied zero versions. The resulting schema had 44 public tables: all 28 legacy tables remained and 16 new tables were added.

Every legacy table retained its row count across migration. Seven representative tables also had matching before/after hashes of selected stable identity and content fields:

| Table | Stable fields checked |
| --- | --- |
| `operations` | ID, kind, status, payload, metadata |
| `control_events` | ID, type, payload |
| `saga_events` | ID, saga ID, metadata |
| `beacon_events` | ID, type, severity, metadata |
| `deployments` | ID, app, status, image tag |
| `cron_states` | App, process, paused state, schedule |
| `access_tokens` | Token ID, device ID, scopes |

The selected field hashes and the counts of all 28 legacy tables matched exactly before and after migration. The hash query and raw results were not retained, so the hash comparison cannot be independently reproduced; it is a limited observation, not a retained acceptance fixture. Row counts do not establish field equality, and these selected hashes do not establish sequence, constraint, or index behavior. The container and owner-only dump directory were removed after verification; neither remains available as a fixture.

## Sanitized execution record

| Check | Observed result |
| --- | --- |
| Candidate source | `e5387ba03909babd7d77b017841fe6ae576a1b4d` |
| Source tool | Mini PostgreSQL 17.7 `pg_dump --format=custom --no-owner --no-privileges --dbname=norn_v2` |
| Target isolation | Docker inspect: network `none`, published ports `{}` |
| Restore compatibility | PostgreSQL 17 `pg_restore -f -` SQL replay into PostgreSQL 16; removed only `SET transaction_timeout = 0;` |
| Pre-migration | 28 public legacy tables, no version ledger |
| First migrator run | Version 16, 16 versions applied, minimum reader 2, minimum writer 13 |
| Second migrator run | Version 16, zero versions applied |
| Post-migration | 44 public tables, 16 migration-ledger rows, 28/28 legacy table counts matched |
| Cleanup | Target container absent and temporary dump directory absent after deletion |

This record summarizes the observations. It is not a preserved command transcript, database fixture, or full content digest. A future acceptance rehearsal must retain reproducible queries and sanitized output.

## Remaining M0/M5 gates

The copied data was not run with the old or candidate API, so old-worker coexistence, auth/session operation, jobs, routes, volumes, and app database behavior remain unverified. Rollback after writer contract 13 was not attempted. The restore used a disposable database rather than the production platform upgrade script, and the SQL replay was not one transaction. A retained, sanitized CI fixture and an exact private upgrade/rollback rehearsal still need the operator-owned app/job/route/database map, disabled external effects, versioned binary run, and explicit rollback conditions. M0 and M5 remain open.
