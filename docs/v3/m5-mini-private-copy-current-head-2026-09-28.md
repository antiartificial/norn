# Mini private-copy compatibility refresh — 2026-09-28

The read-only `mini-private-copy-rehearsal` passed on Mini against exact Norn
PR #77 head `2e674384858d0ca0c849b803d1f6f791cd4116af`. The disposable
Darwin/arm64 candidate embedded that SHA and had binary SHA-256
`06141d534cf86c84a8312f3d0823cc1aa91bfe01217860f490e78dbcc705af4d`.
The transferred rehearsal script had SHA-256
`59a8bf607635e80afbfb322cc3cff643050aa3edf3dfebd0b4914f7d89995036`.

The source was Mini's owner-local `norn_v2` PostgreSQL Unix socket. `pg_dump`
ran with a read-only source transaction. The dump restored into a disposable
Postgres.app 17.7 cluster with TCP disabled. Its **28 original public tables
and 260,831 rows** retained all original primary-key and full-row fingerprints
after migrations 1–45. A second migrate-only pass succeeded. The candidate's
passive/check API returned healthy version and schema responses with reader
floor 5 and writer floor 31, and with operation recovery, workers, and the
Nomad watcher disabled.

Before/after fingerprints of live Nomad base-job IDs and modify indexes and
the cloudflared configuration matched. The script reported cleanup of its
disposable database, dump, and logs. Independent follow-up found the live
API still on `v2.20.0-platform-30-ga5da8ef` and no
`public.norn_schema_migrations` table in the source database. The transferred
candidate and script and the local build were removed afterward.

This is current-head schema and passive-startup compatibility evidence. It is
not a production-key control backup, off-host restore, scheduled maintenance
transition, installed-binary rollback, live traffic continuity result, or M5
sign-off.
