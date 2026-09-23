# Independent database consumer review checklist

These are acceptance checks from source inspection, not implemented capabilities.

- `pipeline/data_operations.go` currently routes dump and restore by database
  name through inherited libpq environment. Named bindings must supply a closed
  connection environment for both safety snapshots and destructive restore.
  Conflicting ambient PGHOST, PGSERVICE, PGPASSFILE and PGOPTIONS must not redirect
  commands. Test with two actual server instances containing identical names.
- `pipeline/migrate.go` runs arbitrary trusted shell migration code and returns
  combined output on failure. Provide the same target as runtime; test diagnostic
  secret canaries and never claim arbitrary application code cannot deliberately
  override its connection. Reject contradictory configured target variables.
- `nomad/translator.go` merges app and per-process environment for web, worker,
  cron and function jobs. Check every translation path and precedence, not only
  web jobs. Connection injection must not expose credentials in inspection or
  ordinary operation evidence. Define the private runtime-secret boundary.
- Snapshot inventory, pruning, safety snapshots and restore must share a target
  namespace. Existing database-name filenames cannot identify a server. Validate
  metadata and digest before accepting reuse; require explicit legacy mapping.
- `handler/app_recovery.go` currently admits only legacy Postgres declarations.
  Named-binding admission must use the same resolver as execution and bind the
  accepted target atomically with operation acceptance. Reject stale targets
  before safety snapshot, migration or restore side effects.
- Retrying an already accepted request after catalog changes must return the
  original receipt, not recompute a new target into its request fingerprint and
  falsely report an idempotency conflict. Test acceptance, catalog revision,
  identical retry and execution rejection separately. Resolution must precede
  new acceptance without replacing uncertain-commit recovery semantics.
- Credential rotation preserves identity only if endpoint/database/role and
  declared generations remain consistent. Catalog revisions must preserve
  retirement history across restart, concurrent updates and restore; stale
  writers must not erase tombstones.
- Application health is not merely Consul service health: prove the selected
  database connection independently and report unsupported probes honestly.
- Include negative cases for missing secrets, TLS mismatch, wrong server,
  stale accepted generation, same-name snapshots, secret-bearing stderr and
  unsupported engine operations. PG evidence is not MySQL qualification.

Existing Mini behavior remains a compatibility lane. Do not make named targets
fall back to its ambient connection settings or the control database DSN.
