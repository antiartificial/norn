# Mini private-data schema rehearsal through migration 23 — 2026-09-25

Status: copied-schema preservation, idempotence, and read-only historical
reader refusal passed. A live Mini upgrade, writable mixed-version operation,
backup restore, and rollback remain release gates.

A fresh read-only PostgreSQL custom-format dump of Mini's control database was
restored into a disposable PostgreSQL 17.7 container. The container had
`network=none` and no published ports. Only PostgreSQL and candidate binaries
ran against that copy: no Norn API listener, worker, Nomad client, webhook,
cron, provider client, or app runtime started. Mini's database was never
migrated or written.

The restored copy held 28 original public tables and 247,131 rows across
those tables. Before migration, the rehearsal recorded each original table's
row count and a digest of ordered primary-key values. The candidate executed
`NORN_SCHEMA_MODE=migrate-only` twice. It applied versions 1–23 on its first
run and zero versions on the second. The result reported current migration
23, minimum reader 4, and minimum writer 19. All original row counts and
primary-key fingerprints matched after the migration. The expanded schema had
48 public tables.

This rehearsal also caught and corrected a catalog-immutability defect before
the final run. A historical binary with the migration-21 catalog originally
failed only because the candidate had altered migration 21's checksum. The
candidate now preserves its published checksum and appends migration 23 for
the reader contract. That historical binary's read-only schema check now
refuses explicitly because reader version 3 is below required version 4.
This is a compatibility refusal, not a rollback proof.

The private dump, temporary binaries, scratch source, SQL/fingerprint files,
and disposable container were removed after the check. The evidence above
does not establish compatibility with the installed Mini binary, active
workers, application traffic, a writable older binary, or rollback after the
new writer contract. Those remain required before a Mini deployment.
