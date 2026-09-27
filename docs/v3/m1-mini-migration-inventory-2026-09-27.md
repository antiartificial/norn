# Mini migration declarations for the v3 release boundary — 2026-09-27

Read-only inspection of top-level `infraspec.yaml`/`infraspec.yml` files under
Mini's `/Users/0xadb/projects` found 29 InfraSpecs. Five declare a nonempty
top-level `migrations` command; none of those five declares a top-level
`migrationPostcondition`. No command text, credentials, connection strings, or
application data were copied. Current exact Nomad job allocation status was
queried separately. Neither inspection launched a migration or changed a job.

| Source | Exact job allocation status | Reviewed postcondition |
| --- | --- | --- |
| `field-harbor` | 1 running | Absent |
| `like-trove` | 1 running | Absent |
| `motifgarden` | No exact job | Absent |
| `signal-sideband` | 1 running | Absent |
| `turnkey-offer-intake` | 2 running | Absent |

This is a source-file inventory, not proof that the deployed revision used the
same file or that a migration will run on the next deploy. It also does not
prove the other 24 apps have no migration behavior outside InfraSpec. The
`migrations`/`migrationPostcondition` fields were identified by top-level YAML
keys, not by executing or approving the declared commands.

The [supervised migration contract](m1-supervised-app-migration-contract.md)
requires a reviewed target-specific SQL postcondition when
`NORN_MIGRATION_EXECUTION=supervised`; the five declarations above would be
rejected by that release mode as written. Leaving supervision off permits the
legacy direct shell path and does not qualify M1. Before enabling supervised
mode for a protected release, review each active app's deployed revision,
selected database binding, migration semantics and read-only postcondition;
either add and test an accepted postcondition or explicitly hold that app's
migration/deploy. Treat `motifgarden` separately because it has no exact job.
Do not infer a safe postcondition from command exit or a schema version name
without checking the original database target and data invariants.

## Like Trove runtime migration bypass

Source review of Mini's `like-trove` checkout at
`ae41270478831a6a7dc1b96017ff50e3435968f1` found the declared
migration command invokes `go run ./cmd/like-trove migrate`. The installed
Nomad job has a PostgreSQL-form `DATABASE_URL` in its web task (the value was
not retained). The same source calls `db.Migrate(ctx)` from `serve` and capture
paths, so starting or running the app can issue schema DDL and a data backfill
outside Norn's migration effect. The helper runs `CREATE TABLE/INDEX IF NOT
EXISTS`, adds missing columns, then backfills media hashes. Those operations
are intended to be idempotent but can still write after a supervisor claim
expires or while another migration owner runs.

Before Like Trove can qualify for a v3 supervised migration gate, its owner
must choose and test one schema writer boundary: disable runtime migrations
when Norn owns the reviewed migration step, or explicitly keep Like Trove's
own migration mechanism and exclude its deploy from the automated migration
claim. Verify the deployed revision and all capture/worker entry points before
asserting single ownership. The Mini checkout had a pre-existing modification
to `secrets.enc.yaml`; it was not edited or copied during this inspection.
