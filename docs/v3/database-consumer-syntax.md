# Database consumer syntax proposal and call-site inventory

Status, 2026-09-22:
- §2 (batch three) was accepted as a local compatibility checkpoint.
- The catalog document and the `norn.app/v2` InfraSpec were approved locally in
  [the named-consumers handoff](claude-m2-named-consumers-handoff.md). Batch
  four implements them.
- §6 records batch four with its tests. It awaits independent review and is
  not M2 completion.
- Fleet v1 documents are unchanged.
- §4 (cross-target restore intent) is still only a proposal.

Parents: [ADR 0003](adrs/0003-profiles-and-database-bindings.md),
[planning contracts](planning-contracts.md) (`DatabaseResolver`,
`DatabaseService`, `DatabaseBinding`, `DeploymentProfile`),
[database-binding handoff](database-binding-handoff.md),
[M2 integration handoff](claude-m2-integration-handoff.md).

## 1. Current call-site inventory (source, 2026-09-22)

Every application-database consumer today selects its target by a database
**name** from `infrastructure.postgres.database` and ambient libpq routing
(the API process's `PG*` environment, default socket and `.pgpass`). None
carries a server, role or generation identity.

| # | Consumer | Location | Target selection today | Hazard |
| --- | --- | --- | --- | --- |
| C1 | Runtime app connection | `pipeline/submit.go` → `secrets.Manager.EnvMap(app)` | App-owned SOPS secret (usually `DATABASE_URL`); Norn renders nothing | Norn cannot prove runtime and backup use the same server |
| C2 | Deploy migration step | `pipeline/migrate.go` (`sh -c spec.Migrations`, `cmd.Env` unset) | Whatever the command reads; inherits the **entire API environment**, including `NORN_DATABASE_URL` (control DSN), `NORN_API_TOKEN` and signing keys | Control credentials are exposed to trusted app code. That does not by itself mean the control DSN is selected as the app target: the command picks its target itself, usually from its own variables or ambient libpq |
| C3 | `app.migrate` operation | `pipeline/data_operations.go` → `p.migrate` | Same as C2 | Same as C2 |
| C4 | Deploy pre-deploy snapshot | `pipeline/snapshot.go` → `createDataSnapshot` → `pg_dump -d <name>` | Name + ambient libpq | Two servers with one database name share a namespace and the wrong server can be dumped |
| C5 | `app.snapshot` | `pipeline/data_operations.go` | Same as C4 | Same as C4 |
| C6 | `app.snapshot-prune` | `pipeline/data_operations.go` → `pruneDataSnapshots` | Filename prefix `<name>_` in `snapshots/` | Prunes another target's files with the same name |
| C7 | `app.snapshot-restore` (durable) | `pipeline/data_operations.go` → `pg_restore -d <name>` | Filename + ambient libpq | Restores any same-named dump into any same-named database |
| C8 | Direct restore (legacy HTTP) | `handler/snapshots.go` `RestoreSnapshot` | Same as C7, synchronous, outside the operation queue | Bypasses acceptance fencing entirely |
| C9 | Direct snapshot / import / export | `handler/snapshots.go` `createSnapshotForSpec`, `ImportSnapshot`, `ExportSnapshot` | Name + ambient libpq / S3 key by app | As C4; imports bind no target |
| C10 | Snapshot inventory and readiness | `handler/snapshots.go` `listSnapshotsForSpec`, `handler/production_readiness.go`, `handler/ops_platform.go` | `snapshots/<name>_*` | Reports another target's backups as this app's |
| C11 | Operator hint | `handler/operator_release.go` (text only) | Reads `DATABASE_URL` from a Nomad alloc for `psql` | Advisory text; prints no secret itself |
| C12 | Health | none for app databases | None | No probe proves which server an app uses |

The control store (`config.DatabaseURL`, `NORN_DATABASE_URL`) is a separate
consumer and is never an application target.

## 2. Local plumbing without new syntax (in progress, awaiting review)

Each item names the tests that exercise it. Anything not covered by a test
listed here is not claimed.

- **Durable catalog** (migration 5, writer contract 5). `database_catalog_revisions`
  holds numbered, digest-verified catalog revisions. Activation is a
  compare-and-set on the expected current revision under an advisory lock and
  runs `ValidateTransition` from the stored catalog.
  `database_catalog_retirements` records each retired service and binding ID
  permanently, in the revision's transaction. Activation requires every
  durable tombstone to stay listed and refuses any live reuse of a retired ID.
  The recovery registry exports the revision metadata but not the catalog
  bytes. Tests: `store` `TestDatabaseCatalogRevisionsAreCompareAndSetAndPersistRetirements`
  (two-phase retirement, recreate refused, move without bump refused, durable
  tombstone enforced, 4 concurrent activations → 1 winner, tampered bytes
  refused); schema/registry pins in `store`, `controlrecovery`, `startup`.
- **Endpoint is catalog identity**. `DatabaseService.endpoint` (`host` is a
  DNS name, IP or absolute socket directory; `port` 1–65535) is part of the
  service definition. Changing it without a service generation bump is an
  unsafe transition, and with a bump accepted work becomes stale. The secret
  holds only the password, so credential rotation cannot repoint work. Tests:
  `database` `TestEndpointIsCatalogIdentityAndRotationKeepsTarget` and the
  `endpoint *` cases in `TestValidateTransitionGenerations`.
- **Private connection material** (`database/material.go`). Secrets resolve
  through a `SecretSource`. The only production source is
  `DirectorySecretSource`: an absolute, owner-only directory owned by the
  process user, read through `os.Root`. Files must be regular, `O_NOFOLLOW`,
  owner-only and at most 64 KiB. Each secret is strict JSON `{"password": "…"}`
  (optional field), with unknown and duplicate keys rejected and nothing after
  the object. `OpenSession` writes a libpq service file and an empty passfile
  (0700 directory, 0600 files). Commands get a closed environment: `PATH`,
  `HOME`, `LANG`, `LC_ALL`, `TMPDIR`, `PGSERVICEFILE`, `PGSERVICE`. Their
  arguments carry only `service=norn_target`. The in-process pgx config
  neutralises inherited defaults (service, password, passfile, TLS files,
  options, timeout, fallbacks). `Probe` verifies `current_database()`/
  `current_user`. `%v`, `%+v` and `%#v` output is redacted. Only PostgreSQL is
  supported; MySQL fails as unsupported. Tests: `database`
  `TestSameNamedDatabasesOnTwoServersAreSelectedByDeclaredTarget`,
  `TestDumpFromOneServerRestoresIntoTheSameNamedDatabaseOnTheOther`,
  `TestSessionRejectsUnsupportedTargetsAndMalformedSecrets`,
  `TestDirectorySecretSourceIsPrivateAndConfined` and the reviewer's
  `TestReviewMaterial*`.
- **Two servers**. `internal/pgtest` starts a scoped second PostgreSQL
  (`initdb`/`pg_ctl` under `/tmp`, Unix socket only, no TCP listener, trust
  auth for the OS user, stopped and deleted at cleanup). The database tests
  create a same-named database on it with the same role name as the inherited
  server. Every ambient `PG*`, `DATABASE_URL` and `NORN_DATABASE_URL` points at
  the inherited server (password, service, options, certificate and timeout
  canaries included).
- **Target identity at acceptance**. With `NORN_DATABASE_PROFILE` set, every
  database-consuming kind (`app.deploy`, `app.snapshot`, `app.snapshot-prune`,
  `app.snapshot-restore`, `app.migrate`) for an app that declares postgres is
  handled as follows:
  - It resolves `infrastructure.postgres.database` through the profile's
    explicit legacy mapping, then records `norn.database-target/v1`
    (profile, catalog revision, full `TargetIdentity`) in the payload before
    the request fingerprint. It is stored as an exact JSON string, so uint64
    generations never pass through float64.
  - An identical retry after a catalog change returns the original receipt
    with its original target. A changed request under the same key conflicts.
  - Execution re-resolves with the recorded identity as `Expected`, opens the
    session and probes it before any side effect. Stale, unbound or
    wrong-profile work is refused, and nothing falls back to ambient routing.

  Tests: `pipeline` `TestDatabaseTargetsBindAcceptanceAndDriveSnapshotRestoreMigration`,
  `TestDatabaseTargetsRejectStaleGenerationAndUnboundWork`,
  `TestDatabaseTargetsReplayAfterCatalogChangeReturnsOriginalReceipt`,
  `TestDatabaseTargetsExactLargeGenerationsSurviveAcceptanceAndExecution`
  (2^53+1 and 2^63+3 through acceptance, persistence, claim, execution and
  sidecar), the reviewer's `TestReviewDatabaseTargetLargeGeneration`.
- **Consumers wired to the recorded target**:
  - C3 `app.migrate` uses `MigrationEnvironment`, the closed environment plus
    the private libpq service. Batch three's password-free
    `DATABASE_URL=postgresql:///?service=norn_target` was replaced in batch four
    by a real connection URL (and an optional URL file), because Node migration
    clients do not read libpq service files (§3.2 rule 4, §6). Migration code
    is trusted app code and can still deliberately open any connection it
    likes. The closed environment only removes inherited credentials and
    ambient routing.
  - C5 snapshot, C6 prune and C7 durable restore (including its pre-restore
    safety snapshot) use the session.
  - C2/C4 in `app.deploy` use the same helpers through `databaseForState`.
    They are wired but **not** exercised by an end-to-end deploy test, which
    would need build/Nomad fakes that do not exist yet.
  - An identity probe runs before each bound operation. It is not an
    application health check (C12 remains open).
  - C8 direct restore and the C9 direct retention and import endpoints return
    409 `database_targets_active` while a profile is configured
    (`handler` `TestDirectSnapshotMutationsAreRefusedWhileDatabaseTargetsAreConfigured`).
  - Startup fails closed on a security-profile name, a non-private secret
    directory, a missing catalog or an undefined profile (`main`
    `TestDatabaseTargetsConfigurationFailsClosed`).
  - Pipeline tests on two real servers:
    `TestDatabaseTargetsRouteToDeclaredServerNotAmbientOne` sends snapshot,
    restore and migration to the scoped server. The inherited server holds
    the same database, schema and role names and all ambient settings, and
    its data is shown untouched.
- **Snapshot provenance and publication**:
  - A bound dump is published sidecar-first. Its `<file>.target.json` holds
    the target, catalog revision, SHA-256 and size and is written with
    `O_EXCL` and fsynced. The dump is then hard-linked without overwrite, and
    a failed link withdraws the sidecar.
  - Replay reuses an existing dump only when its sidecar names this target
    and its bytes verify. A dump without a sidecar fails closed and is never
    adopted. A foreign or orphaned sidecar keeps its name and the new dump
    takes the next one.
  - Restore verifies the digest before `pg_restore`.

  Tests: reviewer `TestReviewSnapshotReuseCannotAdoptUnprovenDump`;
  `TestDatabaseTargetSnapshotPublicationSurvivesInterruption` (orphan
  sidecar, verified reuse with no new files, tampered reuse, foreign
  same-second dump, unproven dump, unbound racer).
- **Snapshot namespaces**:
  - Named bindings use `snapshots/targets/<hash(service, binding, engine,
    database)>/`. This is wired, but no named-binding syntax can reach it yet.
  - The legacy mapping keeps the flat `snapshots/<name>_…` namespace under a
    unique recorded owner. On first bound use, `snapshots/.norn-legacy/<name>.json`
    is published by exclusive link and names the profile, mapping and service,
    plus an inventory (name, SHA-256, size) of the sidecar-less dumps present
    then.
  - Only inventoried dumps are listed and restorable, and only after digest
    verification. Unbound dumps written later are ignored. Another profile,
    mapping or service is refused (`errLegacyNamespaceOwned`).
  - The inventory is bound to the exact `TargetIdentity` recorded at
    adoption. After any generation change, adopted dumps are foreign: they
    are neither restored without the §4 intent nor pruned.

  Tests: `TestLegacySnapshotNamespaceHasOneOwnerAndAdoptionInventory` and the
  reviewer's `TestReviewLegacySnapshotAdoptionCannotCrossGeneration`. Moving
  the legacy mapping to a different service therefore needs an explicit
  re-adoption operation, which does not exist yet.
- **Legacy behaviour preserved**: with no `NORN_DATABASE_PROFILE`, fingerprints,
  snapshot names, ambient `pg_dump -d <name>`/`pg_restore`, the handler
  endpoints and the inherited migration environment are as in v2 (existing
  `pipeline`/`handler` snapshot tests unchanged). That inherited migration
  environment is still the C2 exposure; it is closed only for bound targets.

## 3. Proposed syntax (awaiting root review)

### 3.1 Catalog document (`norn.database/v1alpha1`)

This is a separately versioned document owned by infrastructure intent (the
Fleet runner or the Mini operator), not by application InfraSpec. It is the
JSON form of `database.Catalog`, and every resource carries its own
`apiVersion`. It would be activated through a future authenticated control
operation (`database.catalog-activate`, idempotency key required, expected
revision as a precondition), and the store half of that already exists.
Credentials appear only as references; endpoints are identity.

The authoritative, executable example is
[`v2/api/database/testdata/catalog-example.json`](../../v2/api/database/testdata/catalog-example.json).
`TestCatalogExampleDecodesStrictlyAndResolves` decodes it strictly, validates
it, and resolves both `primary` and a legacy declaration. It is shown here for
reading:

```json
{
  "apiVersion": "norn.database/v1alpha1",
  "services": [{
    "apiVersion": "norn.database/v1alpha1",
    "id": "mini-app-pg", "generation": 3, "purpose": "application",
    "engine": "postgresql", "engineVersion": "16.4",
    "providerRef": "local:mini-postgres",
    "endpoint": {"host": "/var/run/postgresql", "port": 5432},
    "topology": {"mode": "local-shared", "availabilityClass": "single-host"},
    "tls": {"minimumMode": "disabled"},
    "recovery": {"capabilities": ["runtime", "migration", "snapshot", "restore", "health"]}
  }],
  "bindings": [{
    "apiVersion": "norn.database/v1alpha1",
    "id": "shop-primary", "serviceId": "mini-app-pg",
    "database": "shop", "role": "shop_app", "generation": 2,
    "credentialRef": "secret:apps/shop/primary", "tls": {"mode": "disabled"}
  }],
  "profiles": [{
    "apiVersion": "norn.database/v1alpha1",
    "id": "mini", "topology": "local", "availabilityClass": "single-host",
    "databaseBindings": {"primary": "shop-primary"},
    "legacyPostgres": {
      "mappingId": "mini-legacy-pg", "serviceId": "mini-app-pg",
      "role": "legacy_apps", "generation": 1,
      "credentialRef": "secret:apps/legacy", "tls": {"mode": "disabled"}
    }
  }]
}
```

`secret:<path>` resolves under `NORN_DATABASE_SECRET_DIR`. The file is strict
JSON `{"password": "…"}` and may carry nothing else. Host, port, database and
user come from the catalog. TLS references resolve to PEM files. A YAML
authoring form would be a later convenience over this JSON; it is not
proposed here.

### 3.2 Application InfraSpec `norn.app/v2` (implemented in batch four)

Fleet v1 documents are unchanged. The app's name stays the existing `name:`
key (`model.InfraSpec.App`). The executable example is
[`v2/api/model/testdata/app-v2-example.yaml`](../../v2/api/model/testdata/app-v2-example.yaml).
It pairs with the catalog example: its logical name `primary` maps to
`shop-primary`. `TestAppV2ExampleDecodesStrictlyAndValidates` checks it.

```yaml
schemaVersion: norn.app/v2
name: shop
databases:
  - name: primary                  # profile.databaseBindings key
    purpose: application
    capabilities: [runtime, migration, snapshot, restore, health]
    runtime:
      env: DATABASE_URL            # receives the connection URL itself
      fileEnv: DATABASE_URL_FILE   # receives only a path to a file holding it
migrationDatabase: primary         # optional with exactly one database
migrations: npm run migrate
```

Rules (implemented in `model/database_requirements.go`; tests in
`model/database_requirements_test.go`):

1. Without `schemaVersion` a spec is v1, and `infrastructure.postgres` resolves
   through the profile's explicit legacy mapping. `databases` or
   `migrationDatabase` without `schemaVersion: norn.app/v2` is an error, and so
   is v2 combined with `infrastructure.postgres`. Any other `schemaVersion` is
   an error.
2. A v2 document is always decoded strictly, even by lenient v1 discovery, so
   a misspelled field fails instead of vanishing.
3. Logical names match `^[a-z][a-z0-9-]{0,62}$` and are unique. Purpose must
   be `application`. Capabilities come from a closed set, and `restore`
   requires `snapshot`. A `runtime` block and the `runtime` capability must be
   declared together.
4. **Value versus path.** `runtime.env` is a connection-valued variable: its
   value is a standard `postgresql://user:password@host:port/db?sslmode=…`
   URL, which node-postgres, Prisma, pgx and libpq accept. `runtime.fileEnv`
   is a path-valued variable: its value is
   `${NOMAD_SECRETS_DIR}/norn-databases/<name>.url`, a 0400 file containing
   that URL. A path is never presented as a URL and vice versa, and one
   variable cannot be both.
   - User and password are percent-encoded outside the RFC 3986 unreserved
     set, so the value is a single env-file-safe token.
   - Socket endpoints use a `localhost` authority plus `?host=<dir>&port=`,
     because WHATWG parsers reject credentials with an empty host.
   - IPv6 endpoints are bracketed.
5. Norn owns these variables in every job type. The same name in `env`,
   `secrets` or any process's `env` is a validation error. Before side
   effects, the deploy, rollback, cron and function paths also refuse the name
   coming from the decrypted secrets or from provisioned-service variables.
   Names starting with `NORN_` or `NOMAD_` are reserved.
6. `migrationDatabase` must be declared and have the `migration` capability.
   It is required when migrations run and several databases are declared.

### 3.3 Deployment profile selection

`NORN_DATABASE_PROFILE=<profile id>` selects the profile. It is independent of
`NORN_PROFILE` (the development/production security profile), and it may not
be `development` or `production` (implemented). When it is unset, v1 apps keep
legacy v2 behaviour. v2 apps are refused at acceptance and at execution
rather than routed ambiently.

## 4. Cross-target restore intent (proposed)

`app.snapshot-restore` gains an optional `sourceTarget` block naming the
snapshot's recorded `TargetIdentity`, plus `acknowledgeCrossTarget: true`.
It is accepted only when the snapshot sidecar names that exact identity and
the current target differs. Both identities enter the signed fingerprint.
Without it, a sidecar mismatch is refused (implemented and tested).

## 5. Remaining consumer wiring and blockers (after the batch-four correction pass)

- **Nomad agent behaviour is unqualified.** The work verified the vendored
  Nomad API contract (`Variables` CAS via `?cas=`, checked delete, `Template`
  fields) and exercised the HTTP contract against a local fake. It did **not**
  run a Nomad agent or allocation. Unproven:
  - workload-identity access to `nomad/jobs/<jobID>`, including whether
    periodic children can read the parent's variable, and that other jobs
    cannot;
  - the go-envparse handling of the rendered env file;
  - that unchanged template output does not restart a task, and that changed
    output restarts it;
  - that a template referencing a missing item fails the task
    (`error_on_missing_key`);
  - that restart and scale re-render the same revision.

  A `nomad` binary exists locally, but running it needs operator approval,
  which this session did not have.
- **MySQL is required but unqualified.** There is no adapter. OpenSession, the
  health probe and runtime delivery all report it as unsupported. A local
  DBngin `mysqld` 8.0.27 exists outside the checkout, but running it needs
  operator approval.
- **Moving a running app's database target is refused, not supported.** The
  guard (§7) keeps ordinary deploys from splitting writers. Actual online
  cutover (writer fencing, catch-up, handoff evidence) is the M6 lane and is
  not implemented. Promotion of a delivery variable is **not** a writer fence.
- **The stale-claim window is narrowed, not closed.** Nomad variable writes,
  promotion and job registration re-check the operation claim by the database
  clock immediately beforehand, and stale revisions are refused by the
  delivery fence. No transaction spans PostgreSQL and Nomad, so an executor
  that loses its claim between the check and the write can still perform that
  one write. Catalog activation (PostgreSQL only) is fully claim-fenced.
- **Job ID collisions are refused only among discovered apps.** An app added
  later, or a job registered outside Norn, is not seen. A colliding v1 app
  without delivery would still overwrite the other app's job, a pre-existing
  v2 hazard. Function job IDs are checked against app names only.
- **TLS runtime delivery is refused.** `RuntimeConnectionURL` rejects TLS
  targets because CA and client files are not yet placed in allocations. TLS
  itself is qualified for Norn's own tools and probe (§6).
- **Cross-target restore/import (§4) and legacy namespace re-adoption** still
  require explicit source/target identity. Both are refused rather than
  implemented.
- **There is no CLI** for catalog activation or inspection. The v1 API is the
  supported interface, and CLI output only renders the API's redacted
  projection.
- **The SOPS secret-name conflict check is untested.** Refusing a decrypted
  secret that shadows a delivered variable (at deploy acceptance and before
  migration) is wired, but no test exercises it, because there is no SOPS
  fixture. The spec-level `secrets`/`env` conflicts are tested.
- **Legacy migration compatibility.** Legacy (no-profile) migrations no longer
  inherit any API environment (§7). A Mini migration that relied on inherited
  `PGHOST`/`PGUSER`/`PGPASSWORD`/`DATABASE_URL` must carry its own connection
  configuration or move to a named binding. This is a behaviour change for
  that lane and has not been exercised against a live Mini.

## 6. Batch four: named consumers and runtime delivery (local, awaiting review)

Where this section and §7 differ, §7 (the correction pass) is current.

- **Syntax.** Covered in §3.2. Tests: `model/database_requirements_test.go`
  (strict v2 decoding even in discovery, the executable example, 22 rejection
  cases, secret/env conflicts).
- **Named resolution at acceptance and execution** (`pipeline/database_targets.go`):
  - A v2 app's operation records `norn.database-targets/v1`: profile, one
    catalog revision, and each consumed logical database with its exact
    `TargetIdentity`, stored as an exact JSON string.
  - Deploy consumes every declared database; `app.migrate` consumes the
    migration database; snapshot, prune and restore consume the selected
    `database` (optional when there is only one).
  - Capabilities the app declares are required of the service. `restore` is
    checked against the resolved service, because the resolver only accepts it
    as a requirement together with an expected identity.
  - Execution re-resolves each target with `Expected`, then opens and probes
    it.
  - Identical retries replay the recorded set.
  - Without a profile, named apps are refused at acceptance, execution,
    inventory, export and health.
- **Migrations** (`pipeline/migrate.go`) receive the migration database under
  its declared value and file names, or `DATABASE_URL` as a real URL for
  legacy-mapped apps. libpq tools also get a private service file. The
  environment is otherwise closed, and output is redacted for both raw and
  percent-encoded password forms.
- **Runtime delivery** (`nomad/database_delivery.go`,
  `pipeline/database_delivery.go`):
  - Values never enter job specs. They go into the Nomad Variable
    `nomad/jobs/<jobID>`, where Nomad's implicit workload-identity policy
    scopes reads to that job.
  - Web and worker tasks, periodic jobs and function jobs all get 0400
    templates with `change_mode=restart` and `error_on_missing_key`.
  - A deploy stages values under revision-qualified items, fenced by the
    catalog revision its targets were re-resolved against. Its job versions
    read only those items. The current items move only after rollout
    readiness: `healthy`, or canary promotion when canaries exist.
  - Promotion keeps the previous revision for allocations still draining and
    prunes older ones.
  - Stale revisions are refused, and a same-revision value change is a
    conflict rather than a silent rotation. Unfenced writes never replace
    different values.
  - Each function invocation gets a copy of the current delivery under its own
    job ID. The copy is deleted once the job terminates.
- **Inventory, export and readiness** (`pipeline/snapshot_inventory.go`,
  `handler/snapshots.go`):
  - `ListSnapshots`, `ListAppSnapshotsV1`, `ExportSnapshot`, production
    readiness, operator snapshot readiness, the platform ops summary, metrics
    and the contextdb view all go through the layer restore uses. They are
    read-only: they open no session and never adopt a namespace.
  - Only dumps whose provenance names the current target are shown.
  - Export verifies the digest before upload and keys by logical database.
- **Health** (`GET /api/v1/apps/{id}/databases/health`, api:read) probes each
  declared database at its current target, independently of Consul.
  Unsupported engines report `unsupported`, and failures carry only a
  SQLSTATE.
- **Catalog activation** (`POST /api/v1/database/catalog/activations`, requires
  `platform:operate`, needs `Idempotency-Key` and the mutation-audit receipt):
  - Accepted as the durable operation `database.catalog-activate` after
    validating the catalog and its transition.
  - Executed by the worker, whose lock falls back to `kind:ref` for app-less
    operations.
  - Execution is one store transaction that holds the catalog lock, verifies
    the live claim against `clock_timestamp()` (not the transaction start
    time) with the row locked, applies the compare-and-set, and writes the
    operation's succeeded record. Routing therefore never changes without the
    claim, and never without its receipt.
  - A later claim never "adopts" an equal-looking revision that another
    writer produced. A refusal changes nothing and is recorded through the
    claim.
- **Catalog inspection** (`GET /api/v1/database/catalog`, api:read) is
  redacted: no secret or secret reference is shown.
- **Acceptance errors.** Refusals from the database resolver map to 409
  `database_target_rejected`.
- **Test tooling** (`internal/pgtest`):
  - SCRAM password roles set up in-process.
  - A TLS mode listening on loopback TCP only, with Go-generated certificates
    and plaintext TCP rejected.
  - A dependency-free Node PostgreSQL client (SCRAM/MD5/cleartext) that reads
    `DATABASE_URL`/`DATABASE_URL_FILE` with node-postgres URL semantics. It
    is **not** node-postgres, which is not installed and may not be.

Tests (real PostgreSQL; two scoped `pgtest` servers where noted):

- `worker` `TestNamedDatabaseDeployThroughAcceptanceAndWorkerUsesDeclaredServer`
  goes through signed `Run` acceptance and real worker passes. Two scoped
  servers have identical database, role and password, and every ambient
  `PG*`/`DATABASE_URL` points at the wrong one. It checks:
  - a Node migration writes through both the value and the file on the
    declared server only;
  - the safety snapshot lands in that target's namespace;
  - no submitted job contains a credential;
  - web and cron tasks, rendered from the submitted templates and the
    delivered variable, reach the declared server with the Node client;
  - an identical retry replays;
  - a generation bump after acceptance fences the deploy with no snapshot,
    migration, variable write or job submission;
  - a fresh deploy publishes revision 2;
  - no credential is persisted in operations, saga events or deployments.
- `pipeline` `TestNamedDatabasesSnapshotRestoreInventoryExportAndHealthAreTargetBound`
  uses the same database name on two servers within one app:
  - an ambiguous selection is refused;
  - snapshot and restore stay per database, and cross-namespace restore is
    refused;
  - inventory groups are correct;
  - export is keyed by logical database and refuses tampered bytes;
  - health reports ok, `unsupported` for MySQL, and a 28P01 failure for a
    wrong credential.

  `TestNamedDatabasesRequireADatabaseProfile` covers the no-profile refusals.
- `database`:
  - `TestRuntimeConnectionValueWorksForOrdinaryClients`: SCRAM with a
    special-character password through the Node client (value and file) and
    pgx; a path is refused as a URL; a wrong password gives 28P01; redaction.
  - `TestTLSVerifyFullAgainstScopedServer`: pgx and `psql` over verify-full,
    SCRAM over TLS; wrong CA, host/name mismatch and plaintext are refused;
    TLS runtime delivery is refused.
  - `TestConnectionURLRoundTripsIdentityAndSecret` and
    `TestEndpointHostsAreCanonical`: IP literals are canonical, and a numeric
    non-IP name is rejected because `inet_aton` could read it as octal.
- `nomad`:
  - `TestDatabaseDeliveryTemplatesOnEveryTranslationPath`
  - `TestDatabaseVariableWritesAreCheckedIdempotentAndRedacted`: staging,
    promotion, staleness, pruning, a CAS race, function copies, redaction.
- `handler`:
  - `TestDatabaseCatalogActivationUsesDurableAuditedAcceptance`: 401/403,
    refusals before acceptance, replay, key conflict, audit receipt, an
    atomic finish (the same claim cannot execute again), a CAS-refused second
    activation, redacted inspection.
  - `TestSnapshotInventoryViewsAreTargetAware`
- `store` `TestClaimedCatalogActivationRequiresTheLiveClaim`: a lapsed lease
  is refused; a live claim activates and finishes the operation in one
  commit; finished or superseded claims are refused.
- Reviewer tests, all retained and passing:
  - `database/runtime_url_review_test.go`
  - `nomad/database_delivery_review_test.go`: both the repoint and the
    candidate cut-over cases.
  - `pipeline/catalog_activation_review_test.go`: activation without a claim,
    adoption of another writer's revision, and a lease that expires during
    the catalog-lock wait.

## 7. Batch-four correction pass (local, awaiting review; not M2 completion)

This pass addresses [named-runtime-review-checklist.md](named-runtime-review-checklist.md).
Each item says what is source-tested, what uses real clients, and what would
need an actual Nomad allocation (none was run).

1. **Target-change guard** (`pipeline/database_guard.go`):
   - The running target set of an app is the one recorded by its latest
     succeeded `app.deploy`.
   - A deploy whose target for an already-used logical database differs in
     any way (service, binding, generation, engine, database, role) is
     refused, first at acceptance and again at execution before the first
     database step (snapshot, migration, delivery and registration all come
     after it). The refusal names the M6 cutover lane.
   - Allowed: credential-only rotation (identical identity), unchanged-target
     rollouts, and databases new to the app.
   - Test: the worker test `TestNamedDatabaseDeployThroughAcceptanceAndWorkerUsesDeclaredServer`,
     against two scoped servers with identical names:
     - a credential rotation deploys and promotes revision 2 on the same
       target;
     - a deploy accepted before a move to the other server is stale;
     - a deploy after the move is refused at acceptance;
     - a deploy accepted while history was hidden is refused at execution;
     - in all three refusals, the Nomad variable writes, job registrations
       and migrations on either server stay at zero, and no job references
       the new-target revision;
     - the running tasks still reach the old server.
2. **No mutable revision-zero fallback, no target substitution:**
   - Templates always read one explicit staged revision
     (`norn_rev<R>_db_url_<name>`). Revision 0 names items that are never
     written, so a revision-less translation fails closed at render.
   - Each staged revision also stores the non-secret target identity per
     database.
   - `RunningDeliveryRevision` reads the promoted revision and requires every
     runtime database's staged URL and target identity to equal the running
     target set.
   - Rollback (`pipeline/rollback.go`), cron resume and schedule update
     (`handler/cron.go`) and function invocation (`handler/function.go`)
     reference only that revalidated revision.
   - A function gets a create-only, invocation-owned copy of exactly that
     revision, and cleanup deletes only that exact material (checked delete).
   - Deploy-group children go through the same `Run` acceptance.
   - Restart and scale keep the job version, and so the same revision, which
     promotion keeps until two promotions later.

   Tests: `nomad` `TestDatabaseDeliveryTemplatesOnEveryTranslationPath`
   (every path uses an explicit revision; the revision-less wrappers fail
   closed; no template reads current items) and
   `TestDatabaseVariableWritesAreCheckedIdempotentAndRedacted` (staging leaves
   older revisions byte-identical; exact revision parsing, so 1 is not 10;
   target items; copy and exact-owned delete). The worker test also covers
   running-revision revalidation for the service and cron jobs.
3. **Job ID collisions.** Delivery refuses when any of the app's service or
   periodic job IDs equals another discovered app's
   (`TestDeliveryRefusesCollidingJobIDs`). A function job ID equal to an app
   name is refused.
4. **Stale claims.** `store.CheckOperationClaim` is checked by the database
   clock immediately before delivery, promotion, job registration and
   rollback registration (`TestCheckOperationClaimUsesTheWallClock`). This
   narrows the window without closing it (§5).
5. **Redacted operation reads** (`model/operation_projection.go`):
   - Every JSON rendering of an operation goes through `ReadProjection`: get,
     list, active, cancel, replay and accept responses, terminal receipts,
     and CLI output of those.
   - For `database.catalog-activate` the only visible payload fields are
     `expectedRevision`, `catalogDigest` and `requestedBy`, plus
     `redacted: true`.
   - Stored payloads and signed request material are not built from this
     encoding and are unchanged. No event or saga path carries operation
     payloads.
   - Test: `handler` `TestCatalogActivationOperationReadsAreRedacted` covers
     endpoint-host and secret-reference canaries across activation, replay,
     GET (following Location as an `api:read` reader), list, active and the
     terminal receipt, and checks that the stored payload and signed canonical
     request still contain the catalog.
6. **Export keeps the verified identity; provenance travels with the bytes**
   (`pipeline/snapshot_inventory.go`):
   - Export opens the dump once with `O_NOFOLLOW`, copies that descriptor into
     a private 0600 file while hashing, and uploads only that copy once it
     matches the sidecar or adoption digest.
   - It then uploads `<key>.manifest.json` with the full target tuple,
     catalog revision, digest, size and provenance.
   - Import downloads to a fresh private directory in the namespace (object
     stores create destinations exclusively). It then:
     - requires the manifest's app, database and exact current target;
     - verifies the bytes;
     - publishes sidecar-first with the original catalog revision;
     - is idempotent for identical bytes.
   - Import is the handler's import path when a profile is configured.
   - Test: `pipeline`
     `TestNamedDatabasesSnapshotRestoreInventoryExportAndHealthAreTargetBound`:
     - the namespace file is replaced during upload, and the uploaded bytes
       still equal the verified ones;
     - a tampered file uploads nothing, and neither does a symlinked file;
     - the stored manifest carries the full tuple;
     - after local loss, a foreign-target manifest and wrong bytes are both
       refused;
     - a successful import is followed by a durable restore that recovers the
       data on the declared server only.
7. **Legacy migrations** (`pipeline/migrate.go`) get process basics plus
   `PGDATABASE` naming the declared database, and nothing else from the API
   process: no `PG*` routing or password, no `DATABASE_URL`, and no
   `NORN_`/`NOMAD_`/`CONSUL_`/`SOPS_`/`AWS_`/`GITHUB_` variables. The
   compatibility impact is in §5. Test:
   `TestLegacyMigrationDoesNotInheritControlCredentials`.

Reviewer tests added during this pass, retained and passing:
- `pipeline/migration_environment_review_test.go`: no `DATABASE_URL`,
  `PGSERVICE` or `PGPASSWORD` for a legacy migration. This led to the strict
  allowlist.
- `pipeline/snapshot_import_review_test.go`: exclusive-create object download.
  This led to downloading into a private directory.

## 8. Target-guard follow-up (local, awaiting review; not M2 completion)

This supersedes §7.1's history rule, which trusted "latest succeeded deploy"
alone.

- **Writer evidence** (`pipeline/database_guard.go` `writerHistory`).
  History is walked newest-first (at most 500 operations) back to the latest
  baseline, which is either a succeeded named `app.deploy` or a succeeded
  `app.database-baseline`. Later deploys that may have left writers are
  included as possible writers: running deploys, and deploys that failed or
  were cancelled after `snapshot` or without step evidence. Queued deploys are
  not writers yet. A deploy that failed at `clone`, `admission`, `build`,
  `artifact-admission`, `test` or `snapshot`, or was cancelled without ever
  running, is writer-free.
- **Refusal (`Ambiguous`).** The guard refuses when:
  - a possible writer has no recorded named targets (legacy or unbound);
  - the latest succeeded deploy is legacy;
  - there are 500 operations with no baseline;
  - there is no baseline and Nomad reports any of the app's jobs registered
    in any region;
  - there is no baseline and the runtime cannot be checked (no Nomad client
    or an error). Absence of a successful row, writer-free failures and queued
    intents prove nothing about unrecorded jobs.

  An unknown writer does not stop the walk; the known targets gathered back
  to the baseline are always compared first. A conflict with a known writer
  is therefore a definite (non-`Ambiguous`) refusal. A baseline, which may
  resolve ambiguity, cannot override it.
- **Comparison** against every possible writer's targets:
  - a logical name may not change target;
  - an added name is an independent addition only when no running name was
    removed, or when it reuses a target a running writer already uses (a pure
    rename);
  - a removed name plus an added name on a different target is a replacement
    and is refused. This fixes the reviewer's rename bypass.
- **Explicit baseline for legacy-to-named** (`POST
  /api/v1/apps/{id}/databases/baseline`, requires `platform:operate`,
  `Idempotency-Key` and `confirm`). It is a durable, audited
  `app.database-baseline` operation that:
  - records the current targets of the databases that have writers (runtime
    or migration capability);
  - is executed by the worker, which probes each target's identity;
  - may resolve ambiguity, but a baseline that contradicts recorded targets
    is refused at acceptance and at execution.

  It is an operator attestation plus a probe. Norn does not observe which
  database a legacy allocation actually connects to.
- **Enforcement points.** The guard runs at deploy acceptance, before the
  snapshot and migration steps, and before delivery and registration.
  Rollback, cron and function revalidation (`RunningDeliveryRevision`) uses
  the same evidence and refuses when a database has more than one possible
  live target.
- **Import hardening.** Downloaded manifests and dumps are opened with
  `O_NOFOLLOW|O_NONBLOCK` and must be regular files. Manifests are capped at
  64 KiB. Dump verification reads at most size+1 bytes from the same
  descriptor. Completion is proven by the digest match against the manifest,
  but **the manifest itself is not authenticated**: a party able to replace
  both objects consistently could substitute bytes for the same target. This
  remains open.

Tests:
- reviewer `TestReviewTargetGuardRejectsRenamedReplacement`;
- reviewer `TestReviewWriterFreeHistoryStillRequiresRuntimeEvidence`
  (queued, failed);
- reviewer `TestReviewBaselineCannotHideKnownConflictBehindAmbiguousHistory`;
- `TestTargetGuardDistinguishesAdditionFromReplacementAndFailsClosedOnAmbiguity`,
  covering:
  - an unchanged rollout, a pure rename and an independent addition (allowed);
  - a same-name move, a rename onto a new target and a replacement hidden
    beside an addition (refused);
  - a partial rollout to another target (refused), a writer-free failure
    (allowed) and a running deploy (refused);
  - a failure without step or targets (ambiguous), while a known move or a
    known partial rollout behind that unknown history is a definite refusal;
  - legacy history (ambiguous), then allowed after a baseline, then a move
    (refused);
  - no history without Nomad (ambiguous), a fresh app with no registered job
    (allowed) and an unrecorded registered job (ambiguous);
- `TestDatabaseBaselineResolvesLegacyToNamedTransition` (real scoped
  servers, probed execution, contradicting baseline refused);
- `TestDatabaseBaselineRequiresPlatformScopeAndProfile`;
- `TestImportReadsAreBoundedAndRefuseNonRegularFiles`;
- the worker deploy test. Its fake Nomad now reports registered jobs. It
  proves the execution-time guard with a realistic race: a same-target deploy
  accepted, then a failed-after-submit candidate on another target recorded,
  gives a refusal with zero variable writes, registrations or migrations.

Still open:
- Allocations that outlive a newer job version (for example a stuck canary)
  are inferred from operation history, not observed from Nomad allocations.
- Jobs registered outside Norn under other IDs are not seen.
- There is no actual M6 cutover.
