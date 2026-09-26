# Mini private-data schema rehearsal through migration 18 — 2026-09-24

This repeats the data-preservation portion of the earlier
[migration-17 rehearsal](m0-mini-private-restore-migration17-rehearsal-2026-09-24.md)
for the current candidate catalog. It does not qualify a Mini upgrade or
rollback.

The Mini's running API binary SHA-256 was still
`2fdc974ec8b7234c9f73ec9f2156abf1eee06bd63e3d2853572c6f93f79e6e31`,
the signed-source identity recorded in the earlier measurement. A read-only
PostgreSQL 17.7 custom-format dump of `norn_v2` was streamed into a local
owner-only temporary directory. Its file mode was `0600`. The dump was
restored into a disposable `postgres:17.7` container with `network=none` and
no published ports. Only PostgreSQL and a purpose-built migration binary ran
there; no Norn API, worker, webhook, cron, provider client, or app runtime
started against the copy. The Mini database was never migrated or written.

The copy had 28 original public tables and 246,055 rows across them. For each
original table, the rehearsal compared row count and a digest of ordered
primary-key values before and after `store.NewControlSchemaMigrator.Migrate`.
All 28 counts and primary-key fingerprints matched. The migrator applied
versions 1–18, reported minimum reader 3 and writer 15, then applied zero
versions on a second invocation. This verifies schema adoption, legacy-key
preservation, and migration idempotence for this copy. The private dump,
container, and temporary files were removed after verification.

This run did not start either API binary against the migrated copy, test the
old writer against contract 15, replay function invocations, exercise
rollback, or verify application jobs/routes. It also does not replace the M5
upgrade rehearsal. Those gates remain open before Mini deployment.
