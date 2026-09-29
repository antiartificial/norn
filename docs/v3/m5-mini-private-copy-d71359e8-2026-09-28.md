# M5 Mini private-copy refresh at Norn PR #77 head d71359e8

The read-only `mini-private-copy-rehearsal` passed on Mini against exact draft
PR #77 head `d71359e8e4443e2cd71dc9cf761e7be31e60f84c`. The disposable
Darwin/arm64 candidate embedded that SHA and had SHA-256
`a36a1a7feb13c04aa47307bdfe05bee840b592bc31394a8dbd0ffc7b447bd412`.
The transferred rehearsal script had SHA-256
`59a8bf607635e80afbfb322cc3cff643050aa3edf3dfebd0b4914f7d89995036`.
Both transferred hashes matched their local source files before execution.

Mini's owner-local `norn_v2` PostgreSQL 17.7 Unix socket supplied a fresh
read-only `pg_dump`. The script restored it into a disposable socket-only
PostgreSQL cluster with TCP disabled. All **28 original public tables and
262,660 rows** retained their primary-key and full-row fingerprints through
migrations 1–47. A second migrate-only pass succeeded. The passive candidate
passed health, version and schema checks with reader floor 5 and writer floor
31; operation recovery, workers and the Nomad watcher were disabled.

Live Nomad base-job IDs/modify indexes and cloudflared configuration had
matching before/after fingerprints. The script reported no source mutation and
verified cleanup of its disposable cluster, dump and logs. Independent
follow-up found no rehearsal scratch directory and confirmed the live
`norn_v2` database still has no `public.norn_schema_migrations` table. The
transferred candidate and script were removed after the run.

This qualifies private-copy schema and passive-startup compatibility for
this exact candidate. It does not prove a protected production-key backup,
off-host restore, installed-binary rollback, live traffic continuity or M5
sign-off.
