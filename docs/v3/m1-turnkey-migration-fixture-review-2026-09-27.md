# Turnkey Offer Intake as a reviewed migration fixture — 2026-09-27

Read-only Mini source and database review. No migration, job, database, secret,
or source file was changed. The source checkout was clean at
`550c145b934037efad8ffe588e53c07112da265a`; it has no configured Git
remote, so this note does not claim a hosted source review or signed artifact.

Turnkey's `server/migrate.ts` creates `app_migrations`, then executes each
sorted SQL file inside a transaction that also inserts its filename into the
ledger. Twelve `server/migrations/*.sql` files existed in the inspected source,
numbered 001–012. A read-only query through the application's owner-local
SOPS environment returned 12 ledger rows. A second scalar query that required
exactly those twelve filenames and no extra ledger rows returned `ready` on
the current database.
No URL, role, password, row data, or SOPS value was emitted. The result proves
only that the current target ledger contains these filenames. It does not
prove the SQL contents are immutable, that a protected backup exists, or that
a future supervised execution and crash recovery are safe.

This is a promising first **schema postcondition review** because the ledger
entry commits with each migration file. The reviewed query should require the
accepted source's exact filename set, not only the maximum filename or a total
count. The v3 checker can compare its single `ready`/`missing` result to the
reviewed expected value `ready` on the accepted target. The full query and
file manifest must be pinned with the candidate revision and re-reviewed when
a migration file is added or changed.

The candidate query exercised read-only on Mini was:

```sql
SELECT CASE WHEN
  (SELECT count(*) FROM app_migrations) = 12
  AND (SELECT count(*) FROM app_migrations WHERE filename IN (
    '001_initial.sql', '002_model_runs.sql', '003_artifact_single_use.sql',
    '004_durable_review.sql', '005_pipeline_fingerprint.sql',
    '006_job_claim_token.sql', '007_model_run_pricing_snapshot.sql',
    '008_model_run_diagnostics.sql', '009_job_budget_reservations.sql',
    '010_delivery_outbox.sql', '011_processing_work_items.sql',
    '012_evaluation_runs.sql'
  )) = 12
THEN 'ready' ELSE 'missing' END
```

Its filename set still needs app-owner review against the accepted revision.
A read-only negative control substituting a nonexistent 012 filename returned
`missing` on the same target.

## Conversion boundary before supervised mode

The current InfraSpec is implicit v1. It declares
`infrastructure.postgres`, lists `DATABASE_URL` as an application secret, and
wraps `node pilot-dist/migrate.js` in `sops exec-env`. The v3 supervised path
requires `schemaVersion: norn.app/v2`, a named database with `migration` and
`runtime` capabilities, a selected `migrationDatabase`, and a reviewed
`migrationPostcondition`. Its database URL must come from that accepted Norn
binding. The SOPS wrapper and application secret must not override the
runner-owned migration target; the runtime `DATABASE_URL` source must change
with the binding. A catalog/profile binding for this app was not proved by
this check.

A scratch conversion of the complete current InfraSpec to `norn.app/v2`
replaced the SOPS migration wrapper with `node pilot-dist/migrate.js`, removed
the app-secret `DATABASE_URL` source and v1 `infrastructure.postgres`, and
declared a named `primary` PostgreSQL runtime/migration binding with the
exact-file postcondition above. The current v3 `ParseInfraSpecDocument` and
`ValidateSpec` accepted that candidate: named target `primary`, no errors,
and one existing warning that its public endpoint needs cloudflared/forge
routing in local network mode. This proves declaration syntax only; the
scratch file was not placed in the source checkout or used for deployment.
A fresh read-only Mini control query found `database_catalog_revisions`
absent, as expected before the v3 upgrade. No live named catalog/profile
binding was created or tested.

Before any PR that changes the app declaration is merged or used on Mini:

1. Name and prove the v3 database catalog/profile binding to the existing
   application database and dedicated nonprivileged role; verify generation,
   backup, and rollback ownership.
2. Move the source to a reviewable remote or otherwise establish an approved
   exact-source provenance path. Convert the v1 declaration and SOPS command
   to v2 named delivery in an isolated candidate, then validate it against the
   exact Norn candidate and inspect the rendered Nomad job without exposing a
   URL.
3. Approve the exact 12-file postcondition and data invariants with the app
   owner. Test it against a disposable source copy, including missing-file and
   wrong-target rejection. Re-review it for any source revision that adds or
   edits migration SQL.
4. Prove the protected supervisor's one-command ownership and ambiguous
   result handling before enabling it for this app. The current live job and
   source remain on the legacy path.

This advances fixture selection and source-to-target review; M1/M2 and Mini
upgrade gates remain open.
