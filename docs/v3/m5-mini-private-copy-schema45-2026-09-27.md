# Mini private-copy rehearsal through schema 45 — 2026-09-27

At approximately 12:31 UTC, the read-only `mini-private-copy-rehearsal` script
ran on Mini against Norn PR #77 candidate
`b24723f5946df97ffa2401832d7a860784c36684`. The disposable Darwin/arm64
candidate embedded that exact version and had SHA-256
`482c9954a3389d49119b2436e1368f43df6cf2d26b269ee1945223fba965ddd9`.
Its startup contract declared catalog migration 45 and reader/writer floor
5/31.

The script forced the owner-local `norn_v2` dump session read-only and restored
the private copy into a disposable PostgreSQL 17.7 cluster reachable only by
Unix socket, with TCP disabled. It found 28 original public tables and 257,489
rows. After migrations 1–45, every original table retained its row count,
ordered primary-key fingerprint, and original-column full-row fingerprint.
A second migrate-only pass and passive/check health, version, and schema
checks passed with operation recovery, worker, and Nomad watcher disabled.
The live base Nomad job ID/version fingerprint and cloudflared config byte
fingerprint matched before and after the rehearsal.

The script reported its private database, dump, logs, and transferred candidate
removed. Independent follow-up confirmed the candidate and its transfer
directory were absent, and removed the local disposable build. A read-only
query of the live `norn_v2` database still found no
`public.norn_schema_migrations` table. No live schema, API, workload, job, or
route mutation was performed.

This proves source-copy schema and passive-startup compatibility for this
candidate. It is not a protected production-key backup, off-host restore,
RPO/RTO proof, installed-binary rollback, live traffic continuity test, or
M0/M5 sign-off. The new attestation byte cap is unset; archive retirement and
total control-store growth remain M2 gates.
