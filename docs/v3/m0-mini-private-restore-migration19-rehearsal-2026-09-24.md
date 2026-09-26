# Mini private-data schema rehearsal through migration 19 — 2026-09-24

Status: schema-copy rehearsal passed; upgrade, mixed-version operation, and
rollback remain open.

A read-only PostgreSQL custom dump of Mini's `norn_v2` control database was
restored into a disposable PostgreSQL 17.7 container with no network. A
purpose-built binary imported the candidate `store.NewControlSchemaMigrator`;
no API, worker, Nomad client, or external effect ran against the copy.

The 28 original public tables contained 246,171 rows. For those original
tables, per-table row counts and ordered primary-key fingerprints matched
before and after migration. Versions 1–19 applied on the first call; a second
call applied zero versions. The resulting schema contract was reader 3,
writer 16. The private dump, temporary binary, and disposable container were
removed after the check.

This evidence covers migration preservation and idempotence for the copied
Mini data at the time of the dump. It does not establish compatibility with
the installed Mini binary, active workers, app traffic, or rollback after the
new writer contract. Those remain release gates.
