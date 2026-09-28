# Mini private-copy rehearsal through schema 46 — 2026-09-28

The read-only `mini-private-copy-rehearsal` passed on Mini against Norn draft
PR #77 candidate `dea73e04ceb801c24bd6822819fe30fe29f8e999`. The
disposable Darwin/arm64 API embedded that exact SHA. Its SHA-256 was
`d52e5f0427d7561474bce73d41cc170bc8d291daf3b81f835403939af4d532c4`;
the transferred rehearsal script's SHA-256 was
`59a8bf607635e80afbfb322cc3cff643050aa3edf3dfebd0b4914f7d89995036`.

Mini's owner-local `norn_v2` PostgreSQL 17.7 Unix socket supplied the source.
The script forced `pg_dump` into a read-only transaction and restored the dump
into a disposable, socket-only PostgreSQL cluster. Its **28 original public
tables and 261,259 rows** retained all original primary-key and full-row
fingerprints after migrations 1–46. A second migrate-only pass passed. The
candidate's passive health, version, and schema checks passed with reader and
writer floors 5 and 31 and with recovery, workers, and the Nomad watcher
disabled.

Before/after fingerprints of live Nomad base-job IDs and modify indexes and
the cloudflared config were unchanged. The script reported no source mutations
and verified cleanup of its disposable database, dump, and logs. An independent
read-only query found no `public.norn_schema_migrations` table in live
`norn_v2`. The transferred candidate and script and local build were removed.

This qualifies private-copy schema and passive-startup compatibility for this
candidate. It does **not** prove a production-key backup, off-host restore,
protected Mini upgrade, rollback, traffic continuity, or M5 sign-off.
