# M2 database binding implementation handoff

Status: pure resolver/transition contract implemented locally in
`v2/api/database` (see implementation status); no parser, runtime, snapshot or
restore consumer uses it yet. Not qualified.
Parent: [ADR 0003](adrs/0003-profiles-and-database-bindings.md).

## Current integration hazards

- `config.Config.Profile` already selects development/production hardening.
  Do not overload it with Mini/Fleet topology or silently weaken production
  admission when selecting a local deployment. Topology and security policy
  need independent configuration and validation.
- `model.PostgresInfra.Database` names a database, not a server identity.
  `pipeline.postgresDatabase` validates that name, while snapshot/restore
  commands invoke `pg_dump`/`pg_restore -d <name>` using ambient libpq routing.
  A new runtime binding alone would leave recovery pointed at another server.
- Snapshot filenames and lookup are keyed by database name. Two services with
  the same database name must not share a snapshot namespace after bindings
  are introduced. Preserve existing Mini snapshots through an explicit legacy
  namespace mapping, not an implicit search across every service.
- The control database URL is not an application database default. Sharing
  a physical PostgreSQL server locally does not grant applications access to
  control credentials or make control upgrades app database migrations.

## Required first implementation unit

Propose and review versioned resource syntax before altering Fleet v1 parsing.
Implement a pure resolver with stable service/binding identity, engine,
database/user identity, connection generation, secret/TLS references and
declared capabilities. Its public inspection projection contains no credentials.
Legacy PostgreSQL declarations resolve through one explicit configured legacy
service. Missing or ambiguous defaults fail before side effects.

Pass the same resolved identity to runtime rendering, migration, snapshot,
restore and health checks. Bind accepted mutable work to its generation so a
queued operation cannot silently switch targets after a cutover. Resolve actual
credentials at execution through a private adapter; do not persist DSNs in
operation payloads, exported topology, command arguments or diagnostic errors.

Snapshot metadata must bind service ID, binding ID, generation, engine and
database identity. Restore validates target compatibility explicitly; selecting
a similarly named database is not proof of intended target. Cross-service
restore requires an explicit migration mapping, not a filename match.

## Evidence needed

Tests must exercise two PostgreSQL services with identical database names;
all consumers must select the same intended target. Include legacy Mini
resolution, stale-generation rejection, credential rotation without identity
change, TLS validation, missing secret references and secret-canary diagnostics.
PG and MySQL adapters require their own real-engine backup/restore tests.
Reject CockroachDB recovery capabilities until independently implemented.

This unit does not provision a database, perform a live migration, establish
Fleet etcd support or complete M2. Writer fencing and replication cutover remain
the separate migration-authority workstream.
