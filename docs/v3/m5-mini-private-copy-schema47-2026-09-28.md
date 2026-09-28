# Mini private-copy rehearsal through schema 47 — 2026-09-28

The checked `mini-private-copy-rehearsal` passed on Mini against draft PR #77
candidate `37581228c2bfdb460307bdf7301b8d49a4028597`. The disposable
Darwin/arm64 API embedded that exact SHA and had SHA-256
`a8a09d31839285caed5b8c767b3ee1edd17a078c07f59b5eca24054cb04a7636`.
The transferred rehearsal script had SHA-256
`59a8bf607635e80afbfb322cc3cff643050aa3edf3dfebd0b4914f7d89995036`.

Mini's owner-local `norn_v2` PostgreSQL 17.7 Unix socket supplied a fresh
read-only `pg_dump`. The script restored the dump into a disposable,
socket-only PostgreSQL cluster with TCP disabled. Its **28 original public
tables and 261,547 rows** retained every original primary-key and full-row
fingerprint after migrations 1–47. A second migrate-only pass passed. The
candidate's passive health, version and schema checks passed with reader and
writer floors 5 and 31 and with operation recovery, workers and the Nomad
watcher disabled.

The live Nomad base-job IDs and modify indexes and cloudflared configuration
had matching before/after fingerprints. The rehearsal reported no source
mutations and verified cleanup of its disposable cluster, dump and logs. A
separate read-only query confirmed the live `norn_v2` database still has no
`public.norn_schema_migrations` table. The transferred candidate and script
directory and local disposable build were removed and verified absent.

This qualifies private-copy schema and passive-startup compatibility for this
exact candidate. It does not prove production-key protected backup, off-host
restore, live Mini upgrade, rollback, traffic continuity or M5 sign-off.
