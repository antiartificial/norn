# Norn v3 implementation status

Status date: 2026-09-22.

This status applies only to the local development branch `codex/v3-foundations`, based on Norn baseline commit `7304f39`. It records branch-local progress; it does not change the proposed status of the v3 ADRs or authorize a runtime mutation.

## Current stage

**Paused at the user's request, 2026-09-22.** See [resume handoff](RESUME.md).
The implementation process was interrupted and checked stopped. Sections below
record implementation history; references to an active pass are historical
until the user resumes. No full M0–M9 exit gate is declared complete.

The active implementation goal covers the complete M0–M9 plan, not only the
first patch. Implementation is now performed directly by Claude in this
worktree, batch by batch, with independent root review and independent
PostgreSQL race verification before each batch is accepted. Local
implementation does not authorize paid provisioning, live cutovers, commits
or deployment.

| Batch | State | Evidence owner |
| --- | --- | --- |
| Claude batch one: M2 database resolver, control-recovery verifier/CLI | Accepted as a local implementation checkpoint (not M1/M2 completion) | Root re-ran the changed packages with both disposable URLs: store 8.322s, controlrecovery 4.593s, recovery CLI 7.342s, database 2.219s, handler 4.401s |
| Claude batch two: supervised build.test effect recovery (below) | Corrections accepted as a local checkpoint after the first review ([claude-batch-two-review.md](claude-batch-two-review.md)) | Root ran the full API race suite with both disposable URLs, excluding only `TestSampleDarwinHostMetrics` |
| Claude batch three: M2 database consumer plumbing (below) | Accepted as a local compatibility integration checkpoint, not M2 completion | Root full API race suite passed; independent two-server adapter run passed (4.411s); final legacy-generation regression passed after correction (1.430s). See [review](database-consumer-syntax-review.md) |
| Claude batch four: named consumers and runtime delivery (below) | First correction reviewed; guard follow-up implemented and awaiting review; not accepted and not M2 completion | Independent catalog, redaction, exclusive import and migration-environment tests pass. Follow-up evidence is in the section below. See [review checklist](named-runtime-review-checklist.md) |
| Claude M2 retention slices R1–R4 (below) | Implemented locally, awaiting review; not M2 retention completion | Implementation-session evidence only (below) |
| Claude M2 retention pass R5–R11 (below) | Implemented locally, awaiting review; not M2 retention completion | Implementation-session evidence only (below); emulators and fake Nomad qualify no provider or agent |

Full M0–M9 remains incomplete. The retention vertical follows this database
integration. Everything from "Historical orchestration record" onward
predates Claude's batches and remains as the record of earlier slices.

The reproduced logical-name replacement and ambiguous-history guard failures
are now independently verified fixed. Root also independently passed the full
API race suite with both scoped PostgreSQL URLs, excluding only the known
`TestSampleDarwinHostMetrics` failure: pipeline 21.025s, startup 26.139s,
store 9.987s, retention 2.715s, worker 17.097s. The redaction offset sweeps,
archive subject check and reader-floor regression are retained and passing.
This is a local implementation checkpoint, not full batch or milestone acceptance.

The next Claude pass is active on Fleet object archival, archive recovery,
evidence-reserve admission, complete historical reads and bounded labelled log
collection. Remaining concurrency and active-reader retirement qualifications
are tracked in `retention-review-checklist.md`. Live runtime and provider
qualification remain separate gates; no deployment is authorized here.

## Claude retention pass R5–R11 (awaiting review)

This pass follows [retention-review-checklist.md](retention-review-checklist.md)
and the [handoff](retention-implementation-handoff.md). It is not M2
completion. Everything object-store related was tested against the in-process
emulator `internal/s3emulator`, and everything Nomad related against fakes. That
qualifies no real provider (Garage, MinIO, S3, a Fleet object service) and no
Nomad agent.

- **R5: signed acceptance is bound to its content.**
  `store.VerifyArchivedAcceptance` checks both digests, then strictly decodes
  the signed envelope and the canonical request. They must name this bundle's
  operation, saga, intent, request identity, receipt and signing key, and
  match the archived operation's kind, app, ref, risk, source, attempts and
  semantic payload. This runs on read-back, adoption, `RestoreIndex` and
  offline verification, with or without a signer. A validly signed acceptance
  from another operation is refused.
  - Test: `TestArchivedAcceptanceIsBoundToItsOperationNotJustSigned` (swapped
    acceptance, rewritten payload, altered canonical bytes, forged bundle at
    index recovery).
- **R6: prune holds under concurrency, and running old readers.**
  - **Deletion transaction.** Pruning checks holds without locks, verifies
    the object (external I/O, no locks held), then runs a short deletion
    transaction. That transaction locks the intent, the saga's operation
    rows (`FOR UPDATE`, which also blocks new effect rows through their
    foreign key), the saga's deployments and the app's deployed rows. It
    re-checks every hold, including the reader floor, before deleting. A
    hold committed during verification is therefore seen.
  - **Running old readers.** Migration 7's floor stops old binaries from
    starting; it cannot retire running ones. Every Norn control-store pool
    now declares its reader contract in `application_name`
    (`norn/reader=2/<component>`), including the recovery CLI. Pruning is
    held (`unretired-readers-connected`) while any session of the control
    role on the control database declares less than contract 2 or nothing
    at all. That covers pre-archive binaries and contract-1 binaries.
  - Pruning tests now use private scoped PostgreSQL servers (`pgtest`),
    because this hold is database-wide.
  - Tests: reviewer `TestReviewPruneHonorsHoldCreatedDuringVerification`,
    `TestPruneHonorsEffectCreatedDuringVerification` and
    `TestRunningOldReadersHoldPruningUntilRetired` (an undeclared session and
    a `reader=1` session each hold pruning until they disconnect).
- **R7: archive adapters.**
  - `archive.LocalStore` now serializes capacity and publication across
    instances and processes (flock on the root), and fsyncs the parent of
    every directory it creates.
  - New `archive.ObjectStore` for Fleet. It publishes by conditional create
    (`If-None-Match: *`) with Content-MD5 and records the SHA-256 in object
    metadata. A duplicate is verified byte for byte; different bytes are a
    conflict. Reads are bounded: stat first, then a size-limited read tied
    to the stat'ed ETag, then hashed. Retries are bounded.
  - `OpenObjectStore` probes conditional creation and refuses a store that
    overwrites despite `If-None-Match`.
  - The backend is explicit (`NORN_EVIDENCE_ARCHIVE_BACKEND=local|object`).
    The local profile stays the consolidated default (a directory alone
    selects local). The Fleet profile needs a dedicated access key and
    secret key read from owner-only files, an optional CA bundle, and
    credentials that differ from application storage credentials.
  - Tests:
    - `TestLocalStoreCapacityHoldsAcrossInstances`;
    - `TestObjectStoreIsImmutableVerifiedAndBoundedAgainstEmulator` and
      `TestObjectStoreRefusesUnsafeOrMisconfiguredStores`;
    - `TestEvidenceArchiveLifecycleOverObjectAdapter` (outage, publish,
      prune, archive-aware reads, index rebuild and tamper refusal over the
      object adapter);
    - `TestEvidenceArchiveFleetObjectProfileIsExplicitAndIndependent` (the
      real CA-file path against the emulator's TLS certificate).
- **R8: archive recovery CLI.** `norn-control-recovery archive-verify`
  verifies every object offline with no database: bounded reads, bundle
  integrity, keys derived from the subject, acceptance content binding, and
  signatures when the age-encrypted recovery keys are supplied. It exits
  non-zero on any rejected object. `archive-reindex` rebuilds a control
  schema's index from the archive alone and never overwrites existing rows.
  Both commands open either profile.
  - Tests: `TestArchiveRecoveryCommandsVerifyAndReindexWithoutHistoricalDatabase`
    (signed saga archived and pruned; verified with the right, wrong and no
    keys; reindexed into a fresh database whose pruned history is then
    readable; corruption fails visibly) and
    `TestArchiveVerifyOpensTheObjectProfile`.
- **R9: durable evidence-reserve admission.**
  - Migration 8 adds `evidence_reserve` and raises the minimum writer
    contract to 6, since a writer that doesn't enforce the reserve could
    keep admitting mutations. Migrations 1–7 are unchanged.
  - The policy and the archive's capacity observation are durable. The
    backlog (pending count and oldest pending age) is measured live from the
    outbox at admission, so a stopped archiver cannot leave a stale "ok".
  - The archiver records capacity each pass: a capacity refusal, or local
    headroom below `NORN_EVIDENCE_RESERVE_MIN_FREE_BYTES`.
  - `EvidenceReserveAdmissionMiddleware` refuses audited mutations with 503
    `evidence_reserve_exhausted`. The refusal itself is audited. Reads and
    token revocation stay available.
  - Enabled by any archive-configured process. Only an explicit
    `NORN_EVIDENCE_RESERVE=disabled` disables it; dropping archive
    configuration does not.
  - Diagnostic log loss is never an input.
  - Migration 14 adds an optional, narrower signed-acceptance payload budget.
    `NORN_EVIDENCE_RESERVE_MAX_SIGNED_ACCEPTANCE_BYTES` defaults to `0`
    (disabled). When configured, authoritative acceptance locks the durable
    policy row and atomically reserves the exact persisted request canonical
    bytes, signed envelope bytes, and signature bytes. Existing acceptances
    are backfilled, concurrent admissions cannot oversubscribe the limit, and
    idempotent replay does not reserve twice.
  - Signed-acceptance reservations are not released because the current
    archive/prune lifecycle retains those hot rows. This counter does not
    include duplicated domain rows, PostgreSQL heap/index/TOAST/WAL overhead,
    or later operation, effect, and event growth. It is not a total hot-store
    byte bound; queued/running growth remains an M2 requirement.
  - Tests: `TestEvidenceReserveRefusesAuditedMutationsUntilEvidenceIsArchived`
    (backlog, archive headroom, audit row, revocation, recovery after
    archiving), `TestEvidenceReservePolicyIsDurableAndOnlyExplicitlyDisabled`,
    `TestSignedAcceptanceByteReserveMigrationBackfillsExistingPayloads`,
    `TestSignedAcceptanceByteReserveRejectsOversizedPayloadAtomically`, and
    `TestSignedAcceptanceByteReserveSerializesConcurrentCapacity`.
- **R10: archive-aware listings and payload inventory.**
  `HistoryStore.ListByApp` and `ListRecent` merge pruned bundles, newest
  first, and stop only when no remaining bundle can hold a newer event. A
  needed bundle that can't be read, including on a server without an
  archive, fails with `archived_history_unavailable` instead of returning a
  hot-only answer. `retention.PayloadInventory` classifies all 35 control
  tables with bulky columns and holds, and a test ties it to the recovery
  registry.
  - Tests: `TestAppAndRecentListingsAreArchiveAwareAfterPruning` (listings
    after pruning equal the pre-prune truth) and
    `TestPayloadInventoryCoversEveryControlTable`.
- **R11: labelled log collection and hardened live streaming.**
  - `logcollect.Collector` keeps one follower per allocation, task and
    stream, labelled with app, job, node ID and name, allocation, task group,
    task and stream.
  - It resumes from the spool's own tail: a torn last line is truncated on
    recovery, and positions are keyed by Nomad log-file index and offset. A
    restart therefore neither duplicates nor silently skips output that
    Nomad still retains. Output rotated away while the collector was down
    becomes an explicit gap record.
  - Terminal allocations complete and are not re-followed. Followers of
    replaced allocations stop.
  - `logcollect.Spool` is bounded per segment, stream and total. It drops
    the oldest segments with durable loss counters, is owner-only under
    `os.Root`, and is single-writer (flock).
  - `GET /v1/apps/{id}/logs/history` requires `api:read` and enforces app
    binding. It is bounded and labelled, and reports gaps and loss counters.
  - Live `StreamLogs` now uses the request context for its initial
    allocation queries and the allocation's own task group. It also
    unblocks a writer stuck on a client that stopped reading.
  - Configured by `NORN_LOG_SPOOL_DIR` and its limits.
  - Tests:
    - `TestCollectorLabelsResumesWithoutDuplicatesAndRecordsGaps`
      (multi-node, multi-group, rotation, restart, gap, turnover);
    - `TestSpoolIsBoundedCountsLossAndRecoversTornTail`;
    - `TestCollectorOverNomadClientAgainstFakeHTTPAPI`;
    - `TestAppLogHistoryIsAuthorizedLabelledAndIndependentOfEvidenceReserve`;
    - `TestStreamLogsCancelsUpstreamWhileWriterIsBlocked`.

Pins updated for migrations 7–8:
- recovery inspection checksums and registry (35 tables);
- control-schema test (8 migrations, reader 2, writer 6);
- acceptance test (version 8, writer 6);
- startup contract test and platform-upgrade fixture (reader 2, writer 6,
  catalog 8).

Evidence (implementation session, darwin, both disposable URLs, `pgtest`
servers):
- `go vet ./...` and `GOOS=linux go vet ./...` pass.
- `go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1`
  passes all 27 packages with tests. Timings: pipeline 28.7s, startup 27.8s,
  retention 19.9s, cmd/norn-control-recovery 20.3s, handler 13.2s, store
  14.5s, worker 20.7s, logcollect 5.8s, archive 4.0s.
- All 13 reviewer `*review*_test.go` files are kept and passing.

Remaining M2 retention requirements (not complete):
- **Archived domains.** Saga events are archived and pruned. Terminal signed
  Fleet GitHub receipts without a saga are also reserved and archived as
  immutable operation bundles, including their original signed bytes, but
  their hot operation, acceptance intent and identity are deliberately not
  pruned because protected-action replay and recovery do not yet read them
  from the archive. The GitHub action still precedes final receipt acceptance:
  a reserve outage after external success is recovered by retrying the same
  protected result, rather than by a pre-dispatch reservation. Every other
  table classed `hot-evidence-archival-pending` in the inventory still stays
  hot. That includes operations, acceptance intents, effects,
  checkpoints, deployments, control events, webhook deliveries, Fleet
  attempts, exec sessions and audit incidents. Control events also need
  expired-cursor/resync behaviour before bounded replay pruning. The
  mutation-audit and beacon age-only deletions are not yet the
  archive-backed policy.
- **Reader census scope.** The census counts only sessions of the control
  role on the control database. Other roles and hosts are invisible to it.
  A pre-archive binary that connects in the milliseconds after the check is
  not blocked. Platform rollback to a pre-archive binary relies on the
  startup-contract probe. Rollback after pruning is not qualified end to
  end.
- **Admission scope.** The reserve gates HTTP audited mutations.
  Operations created by internal schedulers (cron) are not gated. There is
  no alerting integration beyond the health endpoint.
- **Emulator-only object store.** The Fleet object adapter is untested
  against Garage, MinIO or any Fleet object service. There is no
  object-lock or retention-mode integration. The object store has no
  capacity bound of its own; exhaustion is seen only as a write refusal.
- **Log collection limits.**
  - The spool is not qualified against a real Nomad agent's rotation or
    offset semantics.
  - Collection is single-region, and batch function jobs are not
    collected.
  - Historical logs are not redacted for secrets.
  - There are no host journal limits or node disk budget.
  - There is no collector for Fleet nodes, whether a system job or a host
    service.
- **Workload measurement.** Bounded hot-state growth under a sustained
  workload has not been measured.

## Claude guard follow-up and M2 retention slices (awaiting review)

None of this is M2 completion, and batch four is not accepted.

### Guard follow-up

Details are in [database-consumer-syntax.md §8](database-consumer-syntax.md).

- **Replacement vs addition.** Removing one logical database while adding
  another on a different target is refused. A pure rename onto the same
  target, and an independent addition with nothing removed, are allowed.
- **Writer evidence.** Evidence is every possible live writer back to the
  latest baseline. The baseline is a succeeded named deploy or a recorded
  `app.database-baseline`.
  - Running deploys count as possible writers.
  - Failed or canceled deploys count unless they stopped at a writer-free
    step.
  - Failures without step evidence are never assumed writer-free.
- **Ambiguity.** Unknown writer targets are ambiguous and refused. These
  include legacy succeeded deploys, failures without targets, history beyond
  500 operations, and a registered job, or unreachable Nomad, when there is
  no baseline.
- **Known conflicts first.** Known targets are always checked before
  ambiguity is reported. A known conflict is a definite refusal that a
  baseline cannot override (reviewer regression
  `TestReviewBaselineCannotHideKnownConflictBehindAmbiguousHistory`).
- **Baseline.** `POST /v1/apps/{id}/databases/baseline` requires
  `platform:operate`, a profile and `confirm`. It probes the targets and
  records them, which resolves the legacy-to-named transition. It is an
  attestation plus an identity probe, not an observation of legacy
  allocations.
- **Import hardening.** Import reads are bounded and use
  `O_NOFOLLOW|O_NONBLOCK`. The manifest is capped at 64 KiB. Downloads go to
  an exclusive private staging directory.

Tests:
- `TestTargetGuardDistinguishesAdditionFromReplacementAndFailsClosedOnAmbiguity`
- `TestDatabaseBaselineResolvesLegacyToNamedTransition`
- `TestDatabaseBaselineRequiresPlatformScopeAndProfile`
- `TestImportReadsAreBoundedAndRefuseNonRegularFiles`
- The worker realistic-race test (zero side effects).
- Reviewer tests: `TestReviewTargetGuardRejectsRenamedReplacement`,
  `TestReviewWriterFreeHistoryStillRequiresRuntimeEvidence` and
  `TestReviewBaselineCannotHideKnownConflictBehindAmbiguousHistory`.

### Retention (ADR 0001, [handoff](retention-implementation-handoff.md))

- **R1: archive contract.**
  - `archive.Store` is `PutImmutable`/`Get`/`Verify`/`List`.
  - The local adapter works over `os.Root`, with owner-only roots and
    no-replace link publication. It fsyncs both the file and the directory,
    verifies duplicates and enforces a capacity bound.
  - The bundle format is `norn.evidence-bundle/v1`. It records an explicit
    event-ID cutoff, links, the raw operation row and the byte-exact signed
    acceptance.
  - Tests: `archive` package, plus reviewer tests
    `TestReviewLocalArchiveRejectsSymlinkedParent` and
    `TestReviewReadBackBindsWholeArchiveSubject`.
- **R2: outbox and archiver.**
  - Migration 6 adds `evidence_archive_intents`. Its outbox row is written
    atomically with the terminal transition, and a backfill covers other
    terminal paths.
  - Before acknowledging a bundle, the archiver publishes it, reads it back,
    checks its subject and acceptance signature, and verifies it.
  - Late events produce supplementary sequences.
  - Shadow mode compares without deleting.
- **R3: holds, pruning and reads.**
  - Holds cover:
    - active, manual-recovery and unresolved-effect work;
    - active deployments and the two latest deployed ones (current and
      rollback);
    - the minimum age;
    - reader compatibility.
  - Pruning deletes only the recorded event IDs, under a row lock, after
    re-verifying the object.
  - `GET /v1/apps/{id}/sagas/{sagaId}` is authorized: it requires
    `api:read`, enforces the credential's app binding and checks saga
    ownership.
  - Archived history that cannot be verified is refused with 503, never
    served partially. This includes a server without an archive configured.
  - `RestoreIndex` rebuilds the index from the archive alone.
  - Migration 7 raises the minimum reader contract to 2. Contract-1 readers
    read saga history only from the hot table, so they are retired, and
    pruning re-checks this floor as a hold (reviewer regression
    `TestReviewPruneCannotLeaveLegacyReadersAdmitted`).
  - Tests: `TestEvidenceArchiveLifecycleWithHoldsPruningAndArchiveReads`,
    `TestEvidenceArchiveOutagesCrashBoundariesAndHolds`,
    `TestArchivedSagaHistoryReadsAreAuthorizedCompleteAndFailClosed` and
    `TestEvidenceArchiveConfigurationFailsClosed`.
- **R4: bounded capture and log hygiene.**
  - `capture.Buffer` keeps a head and a tail while the process runs.
    Maintenance, migration, `pg_dump` and `pg_restore` output is bounded
    during execution, not after exit. Exit status is preserved, and
    `outputTruncated`/`outputBytes` are recorded explicitly.
  - Session redaction replaces whole secrets first, then drops the bytes at
    each truncation cut that could hold a secret fragment.
  - Every Nomad task, whether service, periodic or batch, carries explicit
    `LogConfig`: 5 × 10 MB per stream, checked against the group's
    ephemeral disk.
  - `StreamLogs` binds both upstream Nomad requests to the request context
    and to reader close, removes finished channels from the select, and
    drains blocked decoders. The log route enforces app binding for
    app-bound credentials.
  - Tests: `TestBufferBoundsHighVolumeSubprocessOutput` (64 MiB, allocation
    bounded), `TestMaintenanceExecutorBoundsOutputWhileRunning`,
    `TestFailingMigrationOutputIsBoundedWhileRunning`,
    `TestRedactCapturedDropsSecretFragmentsAtCuts`, both
    `TestReviewCaptureRedaction*` reviewer tests,
    `TestTranslateSubmitsExplicitTaskLogRotation`,
    `TestStreamLogsCancelsUpstreamWhenClientLeaves` and
    `TestStreamLogsEnforcesAppBinding`.

Evidence (implementation session, darwin, both disposable URLs):
- `go vet ./...` and `GOOS=linux go vet ./...` pass.
- `go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1`
  passes every package.

Remaining retention gaps: superseded by the R5–R11 pass above. Its
"Remaining M2 retention requirements" list is current.

## Claude batch four correction pass (awaiting review)

Scope is [named-runtime-review-checklist.md](named-runtime-review-checklist.md);
details are in [database-consumer-syntax.md §7](database-consumer-syntax.md).
This pass is not a new milestone and not M2 completion. Batch four is not
accepted.

Implemented:
- A deploy guard refuses to move a running app's database target, at
  acceptance and at execution before any side effect. Moving targets is the
  M6 cutover lane, which is not implemented.
- There is no mutable revision-zero fallback. Staged revisions carry target
  identity. Rollback, cron and function paths reference only a revision
  revalidated against the running targets.
- Functions get invocation-owned, create-only copies with exact-owned cleanup.
- Job ID collisions are refused among discovered apps.
- A live-claim check runs before every Nomad write.
- Every operation JSON rendering uses a read projection, so catalog
  activation payloads are redacted. Stored and signed bytes are unchanged.
- Export uploads verified bytes from a private copy, plus a provenance
  manifest. A target-bound import is followed by durable restore.
- Legacy migrations get basics plus the declared `PGDATABASE` only.

Reviewer tests kept and passing include the three catalog regressions, the
delivery repoint/cutover tests, the IPv6 URL test, and this pass's migration
environment and exclusive-download tests.

Evidence (implementation session, darwin, both disposable URLs, scoped
`pgtest` servers):
- `go vet ./...` and `GOOS=linux go vet ./...` pass.
- `go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1` passes
  every package (see the final report for timings).

Still unqualified:
- The Nomad agent/allocation runtime: workload-identity scope, template
  render/restart/missing-key behaviour, restart and scale re-rendering.
  Running the local `nomad` binary needs operator approval.
- MySQL: running the local `mysqld` needs approval.
- node-postgres itself.
- TLS runtime delivery.
- Online target cutover (M6).
- The stale-claim window before Nomad writes is narrowed, not closed.
- Job ID collisions with undiscovered or non-Norn jobs.
- Legacy-migration compatibility on a live Mini.
- A catalog CLI.
- A SOPS conflict test.

## Claude batch four checkpoint: named consumers and runtime delivery

Scope per [claude-m2-named-consumers-handoff.md](claude-m2-named-consumers-handoff.md).
There is no new migration, and writer contract 5 is unchanged. Fleet v1
documents are unchanged. Details and the full test mapping are in
[database-consumer-syntax.md §3.2 and §6](database-consumer-syntax.md).

Implemented:
- The `norn.app/v2` InfraSpec `databases`/`migrationDatabase` fields, decoded
  strictly and validated.
- Named resolution at acceptance and execution, with an exact recorded target
  set, retry replay and generation fencing.
- Migrations receive a real connection URL and an optional URL file.
- Runtime delivery to web, worker, cron and function jobs through private
  Nomad Variables and templates. Delivery is staged per catalog revision and
  promoted after readiness, with stale and repoint writes refused.
- Target-aware inventory, export, readiness and ops views.
- The `GET /api/v1/apps/{id}/databases/health` probe.
- Catalog activation as a durable, audited, claim-fenced operation, plus
  redacted inspection.
- TLS verify-full qualified against a scoped server.
- `pgtest` gained password roles, a TLS mode and a dependency-free Node
  client.

Reviewer tests added during the batch are retained and pass:
- `database/runtime_url_review_test.go` (IPv6 authority)
- `nomad/database_delivery_review_test.go` (unfenced repoint, candidate
  cut-over before readiness)
- `pipeline/catalog_activation_review_test.go` (activation requires the live
  claim; no adoption of another writer's revision; a lease that expires
  during the lock wait)

Each caused a design correction, described in §6.

Evidence (implementation session, darwin, both inherited disposable URLs, and
scoped `pgtest` servers):
- `go vet ./...` and `GOOS=linux go vet ./...` pass.
- `gofmt -l` flags only the pre-existing
  `pipeline/acceptance_integration_test.go`.
- `go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1` passes
  every package.

Not established:
- Nomad agent behaviour: workload-identity scope, periodic-child variable
  access, env-file parsing, restart on re-render, blocking on a missing
  variable. The local `nomad` binary needs operator approval to run.
- MySQL: there is no adapter. The existing local `mysqld` needs operator
  approval to run.
- node-postgres itself (not installed; installs are not authorized).
- TLS runtime delivery.
- The legacy inherited migration environment.
- Cross-target restore and re-adoption intent.
- A catalog CLI.
- A test for the SOPS secret-name conflict.

None of this is deployed.

## Claude batch three checkpoint: M2 database consumer plumbing

Scope per [claude-m2-integration-handoff.md](claude-m2-integration-handoff.md),
[database-binding-handoff.md](database-binding-handoff.md) and the root
[syntax review](database-consumer-syntax-review.md)/[checklist](database-consumer-review-checklist.md).
No InfraSpec, Fleet v1 or parser change was made. The call-site inventory
(C1–C12), the §2 per-item test mapping and the syntax proposal are in
[database-consumer-syntax.md](database-consumer-syntax.md).

Implemented:

- Migration 5 `database-catalog-revisions` (writer contract 5; frozen
  migrations 1–4 unchanged). It adds CAS catalog revisions and permanent
  retirement tombstones (`store/database_catalog*.go`). The recovery
  inspection catalog, export registry (catalog bytes excluded) and the
  schema/startup/upgrade version pins are updated together.
- `DatabaseService.endpoint` is catalog identity, fenced by service generation.
  Connection secrets are password-only strict JSON.
- A private libpq connection-material adapter (`database/material.go`) with a
  closed tool environment, neutralised in-process pgx defaults, identity
  probe and redacted formatting. It is PostgreSQL only.
- `internal/pgtest`: a scoped, socket-only, disposable second PostgreSQL
  server for two-server tests (local `initdb`/`pg_ctl`, no install, no
  privilege).
- Acceptance-time target recording in the signed payload (exact uint64),
  replay-preserving after catalog changes. Execution re-resolves with
  `Expected` and probes, and routes migrate/snapshot/prune/restore through the
  session. Direct handler mutations are refused while a profile is
  configured. `NORN_DATABASE_PROFILE`/`NORN_DATABASE_SECRET_DIR` startup
  wiring fails closed.
- Sidecar-first snapshot publication with fail-closed reuse. A legacy flat
  namespace has a unique owner manifest with an adoption inventory bound to
  the exact adopted target.

Reviewer tests retained: `database/material_review_test.go`,
`database/review_generation_history_test.go` and
`pipeline/database_snapshot_review_test.go`. All pass. The fixture in
`material_review_test.go` was adapted when the endpoint moved into the
catalog (an endpoint added to the binding, secrets reduced to
`{"password":…}`), and its assertions are unchanged.

Evidence (implementation session, darwin, both inherited disposable URLs):

- `gofmt -l` is clean for every file changed in this batch.
  `pipeline/acceptance_integration_test.go` predates it and is unformatted.
- `go vet ./...` and `GOOS=linux go vet ./...` pass.
- `go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1`: all
  packages pass (see the final report for timings).

Not established, and remaining:

- C1 runtime rendering (per-engine private templates, Nomad variables,
  web/worker/cron/function conflict checks).
- Named-binding acceptance, which waits for syntax review.
- `app_recovery` admission through the resolver.
- Target-accurate C9 export and C10 inventory/readiness.
- An `app.deploy` end-to-end test with a bound target.
- Closing the legacy-mode migration environment.
- The C12 health API.
- The catalog activation operation/CLI/API and legacy re-adoption.
- A MySQL adapter (no local MySQL runtime).
- TLS verify modes against a TLS server.

None of this is deployed or qualified on a live host.

## Claude batch two checkpoint: supervised build.test effect recovery

Scope per [claude-next-batch-review.md](claude-next-batch-review.md) and
[effect-recovery-handoff.md](effect-recovery-handoff.md). No migration, schema
or recovery-registry change; migration 3 and its checksum are unchanged.

Implemented:

- Runner integrity (`effect/supervisor/runner_protocol.go`): output is captured
  through a bounded writer (16 MiB stored, excess drained and counted), hashed,
  fsynced and closed before the HMAC-signed terminal status is published; the
  status binds result length and SHA-256. If storage fails the terminal status
  is withheld. Requests are read bounded, strictly decoded (unknown fields and
  trailing data rejected) and errors never echo request content. Commands run
  in their own process group with a signed-material timeout; on expiry the
  whole group is killed and a final failure is recorded.
- Result authentication: `readVerifiedResult` uses one non-following,
  non-blocking descriptor and accepts output only if length and digest match
  the signed terminal status (replacement, truncation, growth, symlink, FIFO
  and oversize rejected). `RetrieveResult` and observation both use it.
- cgroup backend split: observation/revocation/result logic is portable and
  tested against a fake cgroupfs; only `Start` is Linux-only. `cgroup.events`
  parsing is strict (missing/duplicate/malformed/invalid rejected). A missing
  cgroup, or an empty cgroup without a terminal status, is `unknown` (never
  not-found). The runner log descriptor is closed exactly once by the parent
  after start; the reaper only waits.
- Registration safety (`manager.go`): the root registry records the runtime ID
  before the backend starts, alongside a durable launch-intent marker and the
  journal. Journal loss or a foreign journal fails closed, and a rolled-back
  registry cannot re-register a namespace that has launch history. Prepare
  refusal is now a deferral, not a command failure.
- Final failure semantics: a contained, self-exited (or runner-timed-out)
  failure verifies as `failed` and completes the effect, releasing the gate.
  This is a final outcome, not repeat safety: the same operation input reuses
  the recorded failure and never relaunches. A stopped/revoked execution is
  still not repeat-safe and stays gated.
- Executor: `Recover` drives an existing record (query, verify, complete or
  resolve) without launching. `PGEffectStore` gains `Authority` and
  `UnresolvedForResource`.
- Pipeline: `build.test` in deploy and preflight runs through the executor when
  configured. The input digest binds command, environment, timeout and the
  operation's source identity (not the per-claim checkout), so a later claim
  finds the same effect. The first version derived a per-claim subject for
  unpinned sources; the review corrections below replace that. A blocked step recovers the exact effect holding the app's
  `build.test` resource (any operation) and proceeds only if trusted evidence
  released it. Pending effects propagate to the worker as deferrals: the
  deployment, regions, step and saga are not failed and nothing is published;
  the checkout is kept for the still-running command.
- Startup/config: `NORN_BUILD_TEST_EXECUTION` is `legacy-unfenced` (default,
  unchanged v2 behaviour) or `supervised`. Supervised mode requires supervisor
  root, signing key, cgroup root, timeout and a verified runner (absolute,
  non-symlink, owner/root-owned, not group/world writable, optional SHA-256 pin,
  `--protocol` handshake); any gap fails startup with no fallback. The command
  environment is only `PATH` and `HOME`, never the API environment. On
  non-Linux platforms supervised mode fails startup.
- Release tool: `platform-upgrade` builds `norn-effect-runner` and installs it
  beside `norn-api` by stage-and-rename before the API restarts, including
  rollback and restore paths.

Evidence (implementation session, 2026-09-22, disposable PostgreSQL via the
inherited test URLs):

- `go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1
  -timeout=300s` passed across the API module: supervisor 12.527s, effect
  3.212s, worker 5.052s, pipeline 4.102s, store 8.469s, startup 28.754s,
  runner CLI 3.494s, controlrecovery 7.905s, api 7.334s. `go vet ./...` passed
  for darwin and `GOOS=linux` (cross-compile of the Linux `Start` path only).
- Worker/PostgreSQL end-to-end tests through the real worker, pipeline,
  PGEffectStore, supervisor Manager and verifier, with a scripted backend in
  place of cgroup containment:
  - claim 1 pending → conflicting operation blocked → the successor recovers the
    original effect → claim 2 reuses the result: exactly one external execution
    per operation and one `preflight.complete` each (`-count=3` also passed);
  - owner crash with lease-expiry recovery: the same execution resumes, a stale
    claim cannot launch or terminalize, one publication;
  - deploy pending leaves deployment/step/saga non-terminal and survives
    `RecoverExpiredOperations`; a contained test failure then fails the deploy
    with one `deploy.failed` and one launch.
- Unit/fake-FS tests: signed length/digest binding, 16 MiB bound with discard
  count, timeout kills a background descendant in the process group, malformed
  requests without echo, result replacement/truncation/growth/symlink/FIFO/
  oversize, status tamper, cgroup parser fixtures, fail-closed observation
  table, revoke wait/cancel, concurrent root initialization, registry
  delete/truncate/edit/symlink/other-key, replay after launched-journal loss,
  registry rollback, foreign journal, runner binary verification and handshake,
  startup mode/no-fallback wiring, and upgrade-script runner installation.

### Batch two corrections after independent review

The first review found three blocking issues. Corrections:

1. Timeout lifetime (`effect/supervisor/terminate.go`). The timeout
   callback no longer sends a raw negative-PID kill after `Wait`. The runner
   observes command exit without reaping (Linux `waitid(WNOWAIT)`, Darwin
   kqueue `NOTE_EXIT`; other platforms refuse to run). It then closes a
   timeout guard whose terminate runs under the same mutex, so close joins any
   in-flight kill and no callback acts afterwards. Only then does it reap. The
   process-group ID is therefore reserved (leader unreaped) for every kill. On
   Linux the backend creates a dedicated `command` child cgroup; the runner
   places the command there and kills it via `cgroup.kill` opened relative to a
   directory descriptor taken before start, so no numeric ID is involved. A
   group or cgroup kill is still not the containment proof; terminal
   acceptance still requires an empty execution cgroup.
2. No replay of preceding stages. Migration 4 (`operation-execution-checkpoints`)
   adds write-once, claim-fenced `operation_checkpoints` (outputs stored as
   exact bytes with a verified SHA-256). The claim is checked with the clock
   read after the row lock; the reviewer's lock-wait regression test is kept.
   With supervised build.test, clone records the source identity. Later claims
   check out the recorded commit and must reproduce the same tree digest. The
   ordinary Docker build records its image. Later claims reuse it
   (`build.reused`) instead of running Docker build/push, and every Docker
   invocation goes through an injectable `RunBuildCommand` boundary.
3. Operation identity for unpinned sources. The build.test subject is now the
   recorded source identity (tree digest plus provenance), identical on every
   claim. If a later claim's dirty or non-git source differs, the step fails
   with `SourceIdentityChangedError` before any build or test. The ambiguous
   original effect stays gated, and no second test is authorized.

Schema coherence: writer contract and catalog raised to 4 (migration 4
minimum writer 4), recovery inspection catalog pins the migration 4 checksum,
and the export registry classifies `operation_checkpoints` (outputs excluded
from inspection, included in encrypted bundles). Migrations 1–3 are unchanged.
The release tool's interim refusal of writer-floor increases before migration
applies to this change, as it did to migration 3.

Correction evidence (implementation session, 2026-09-22, inherited disposable
PostgreSQL URLs):

- Full `go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1
  -timeout=600s` passed (supervisor 13.843s, worker 7.760s, store 10.291s,
  pipeline 3.474s, startup 33.370s, controlrecovery 6.788s, recovery CLI
  7.427s). `go vet ./...` passed for darwin and `GOOS=linux`.
- Timeout lifecycle: manual-scheduler guard tests (fire-then-close, late
  callback after close, close joins in-flight terminate, real timer stopped),
  runContained trace ordering `started → exited → guard-closed → reaped` for
  normal exit and timeout, 30-iteration deadline race with no terminate after
  guard close, cgroup terminator bound to the opened directory (not the
  path), and sync-failure/publication-ordering tests at the helper boundary.
  Lifecycle tests passed `-count=5`.
- Worker/PostgreSQL, counted builds: an ordinary deploy build runs once
  across pending claim 1, still-running claim 2, an owner crash with lease
  recovery, and the claim that completes the original test (builds=1, test
  launches=1, three `build.reused`, one `deploy.failed`). An ordinary
  preflight completes with builds=1, launches=1 and one `preflight.complete`.
  Dirty-git and non-git sources, unchanged, reuse the original test
  (launches=1, builds=1). Changed, they fail with the checkpoint error, keep
  launches=1 and builds=1, leave the original effect launched, and publish one
  `preflight.failed`. The earlier prebuilt-image and clean-source tests are
  retained. Passed `-count=2`.
- Store: checkpoint write-once/conflict/stale-claim/expired-claim/tamper test
  and the reviewer's lock-wait expiry test (`-count=3`).

Not established: real cgroup-v2 containment on Linux (no scoped Linux
environment was used; the Linux path is compile-checked only), Darwin
containment (still fail-closed), Docker/BuildKit and registry push as fenced
effects (a crash between push completion and the build checkpoint still
rebuilds), audited operator reconciliation for stopped or unknown effects,
cleanup of checkouts kept for deferred commands, and live or multi-host
qualification. A local filesystem attacker who deletes the registry entry,
journal and launch-intent together is outside this local integrity model.
Checkpoints are enabled only with supervised build.test; legacy-unfenced mode
keeps v2 retry behaviour, including the deploy crash allowlist requeueing
interrupted `test` steps.

## Historical orchestration record

The entries below predate Claude's batches. Earlier orchestration used Astra
low with Sol high implementation.

Three additional foundation slices are frozen and accepted as local review units
after Sol implementation checks and independent primary PostgreSQL/race checks.
This is not full M1 or upgrade qualification:

| Slice | Implementation scope | Verification still required |
| --- | --- | --- |
| Operation ownership | Monotonic claims, guarded worker/pipeline transitions, cancellation on lost ownership, owner-aware deployment recovery | Downstream effect fences/proven-stop remain separate; local PG and cancellation regressions passed |
| Versioned schema | Immutable migration ledger, compatibility checks, one transactional migration owner, legacy adoption | Generic and actual legacy adoption PG/race fixtures passed; real Mini fixture/release qualification remains |
| Startup integration | Explicit schema modes, passive candidate, release-tool migration/restart path | Real passive binary and simulated rollback passed; direct upgrade ordering remains static, live supervisor/signed release/admission handoff unqualified |

The PG acceptance-store seam and maintenance HTTP/CLI acceptance lane are also
accepted local checkpoints. Primary independently passed the full API module
with PostgreSQL under `-race`, excluding only `TestSampleDarwinHostMetrics`,
after those checkpoints; CLI race verification also passed. Other producer
paths are not yet converted, so the platform-wide acceptance invariant is not
active or qualified.

The app enqueue producer conversion is source-frozen pending final independent
review. Primary PostgreSQL race checks passed for pipeline (2.100s) and handler
(2.646s), excluding only the known Darwin sampler. HTTP fixtures cover rollback
replay after the latest deployment changes, same-key concurrent promotion,
foreign-actor qualification consumption, and webhook delivery/body conflicts.
Independent source review found no new blocker in these converted paths. The
promotion regression now uses a store wrapper that forces both identity lookups
to miss, then delays the loser until the winner commits, before allowing the
loser's consumption lookup. Primary passed that test and the new signed atomic
qualification HTTP test together against PostgreSQL under race (1.962s).
Qualification is now frozen after Astra source review and primary PostgreSQL
race verification of pipeline (2.175s) and handler (2.797s), excluding only the
known Darwin sampler. Tests include current CI-intent rejection on replay,
original signature/ID retention after mutable deployment status changes,
changed-input conflict, actor isolation and deterministic concurrent resolution.
Completed qualification receipts remain nonclaimable. This is not platform-wide
producer coverage: Fleet completed-operation/dispatch producers still require
conversion. A subsequent independent full API PostgreSQL race run passed
(`go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1`), including
startup 28.902s, handler 4.218s, pipeline 3.883s and store 3.916s. The exact known
Darwin sampler remains excluded. This verifies the local integration checkpoint,
not a release gate or live upgrade.

The CLI retry/result slice is frozen after independent Astra review and primary
`go test -race ./... -count=1` (api 1.336s, cmd 2.897s) plus `go vet ./...`.
Commands preserve caller retry keys and accepted operation metadata, return
terminal replay results immediately, and poll durable operation status on every
iteration rather than depending on best-effort terminal saga events. Partial
group errors print all results and return failure. Browser caller integration
is not yet included in this accepted slice.

Fleet capacity-plan API acceptance passed bounded Astra review and primary
PostgreSQL race verification (1.608s). The fixture covers inventory-free replay,
actor/request isolation, deterministic concurrent acceptance and transaction
rollback on a missing audit receipt. Its CLI now generates/prints or accepts an
explicit key; primary full CLI race (api 2.421s, cmd 3.324s) and vet passed.
The implementation owner also passed full handler/contract race checks
(3.446s/2.052s), CLI race and diff checks; the capacity-plan unit is frozen.
Reconciliation,
runner creation and GitHub effects are not covered by this slice.

Current Sol implementation lanes are Fleet reconciliation acceptance and web
action retry/result integration. Prospective schema preflight remains bounded
as described below; it does not replace admission/quiescence or Mini upgrade
rehearsals.

The prospective **serving-API** preflight has now passed independent bounded
source review. It reads the running instance's compiled schema contract from a
direct loopback endpoint, binds its PID to launchd and the listener, and compares
an HMAC of the exact database configuration with the migration environment.
Active/rollback compatibility is checked before migration; duplicate JSON fields
are rejected. The primary passed the full API PostgreSQL race suite again
(excluding only the known Darwin sampler), plus the newly added negative
PID/database-binding fixture separately. These fixtures execute shell control
flow with supervisor/HTTP shims and a migration marker, not a real Mini upgrade
or a real PostgreSQL transition. The running host-agent is another database
writer and is not covered by this serving-API attestation. Its exclusion/handoff,
admission quiescence and full M5/M6 qualification remain required.
The final bounded restart guard therefore refuses a prospective reader/writer
floor increase before migration, even if the serving API supports that target.
Unchanged-floor compatible transitions remain executable. This refusal is an
interim safety boundary, not the required Mini v2-to-v3 transition mechanism.

The baseline proxy activation retained a worker-disabled preflight PID. The startup
slice now refuses this path until a complete activation/rollback handoff is
implemented and tested. Such refusal is an interim safety boundary, not
completion of the M6 running-upgrade requirement.

The first local implementation slice is **exec-session startup and ownership safety**, implemented by Terra at medium reasoning and reviewed by Sol at high reasoning. Final review disposition is recorded below. This is part of M1, not completion of M0 or M1.

Implemented locally:

- Additive internal owner identity/token/lease columns; migrations no longer globally fail running exec sessions.
- Atomic session claim, database-clock lease renewal and owner-checked completion; expired or incorrect ownership cannot renew or finish.
- Authoritative cancellation/revocation remains cross-replica; stale completion cannot overwrite it.
- The WebSocket closes when renewal loses ownership or returns a database error.
- Startup and session read/create paths reconcile expired owned sessions. Legacy unowned sessions survive until their hard TTL. Recovery is traffic-triggered, not a continuously running background reaper.

The default lease is five seconds, checked every 500 milliseconds with a two-second database timeout. These are implementation defaults, not qualified availability targets. A transient database failure deliberately closes the affected stream; this does not provide PTY handoff or guarantee termination of every downstream side effect.

The accepted local operation slice replaces global deployment recovery with owner-aware recovery and removes operation-ID-only completion from the claimed execution paths. Atomic request acceptance is the next implementation slice; downstream effect fencing and general concurrent API qualification are not established. The baseline defects and bounded scope are specified in [foundation review](foundation-review.md). No active-active or rolling-upgrade qualification is implied.

An old binary still contains the old startup invalidation query. Restarting or rolling back to that binary can interrupt current exec sessions. A prerequisite patch or an explicit interruption window is required; additive schema compatibility alone does not solve this.

Final review disposition: Sol high reported no remaining blocking finding in the frozen exec slice; the primary agent independently passed the required sequential API and race checks below. Accepted as a local review unit only, not a milestone or release qualification.

## Current evidence

- A read-only Mini inventory was refreshed at `2026-09-22T18:21:35Z`: reported version `v2.20.0-platform-28-g212a35c`, 27 application entries (not necessarily unique applications), and 0 active operations.
- Fleet is unconfigured.
- The baseline `go test ./...` run fails an existing `TestSampleDarwinHostMetrics` case because the CPU command is killed at one second; an isolated rerun has the same failure.
- Baseline store tests with a disposable local PostgreSQL instance passed.
- Post-change API suite with disposable PostgreSQL passed: `NORN_TEST_DATABASE_URL=<isolated-test-db> go test ./... -skip '^TestSampleDarwinHostMetrics$' -count=1`.
- CLI unit suite and CLI race suite passed unchanged.
- Post-change targeted PostgreSQL race tests passed: `NORN_TEST_DATABASE_URL=<isolated-test-db> go test -race ./store ./handler -run 'Test(Exec|Connect)' -count=1`.
- Post-change `go vet ./...`, formatting review and `git diff --check` passed.
- Subsequent generic migration framework: independent `NORN_TEST_DATABASE_URL=<isolated-test-db> go test -race ./store -run 'Test(Schema|ValidateMigration|MigrationChecksum)' -count=1` passed. This covers supplied fixture migrations, not a Mini upgrade or full integrated startup.
- Combined in-progress foundation tree: independent `NORN_TEST_DATABASE_URL=<isolated-test-db> go test ./... -skip '^TestSampleDarwinHostMetrics$' -count=1` passed across the API module. `git diff --check` and `bash -n v2/scripts/platform-upgrade` also passed. This is intermediate integration evidence; operation/startup agents had not frozen their final changes, and it does not replace passive-binary, upgrade/rollback, or HA qualification.
- A subsequent combined race run encountered an in-progress `OperationClaim` field-visibility refactor and failed compilation. After the owner corrected the call sites, independent `NORN_TEST_DATABASE_URL=<isolated-test-db> go test -race ./store ./worker ./pipeline ./startup -count=1` passed for all four packages. The earlier compile failure is resolved; final integrated acceptance still awaits frozen startup/operation review.
- The checkpoint-write finding is resolved in the frozen ownership slice: deployment execution stops when step-start persistence fails; a pipeline regression exercises that failure before execution. Retry and recovery treat unknown steps and failed mutable checkpoints conservatively. This does not make downstream effects independently fenced.
- Independent process-level verification passed `go test . ./store -run '^(TestPassiveBinaryStartupIsReadOnlyAndStatusOnly|TestControlSchemaAdoptsLegacyRowsWithoutReplacingEvidence)$' -count=1` against disposable PostgreSQL. This exercises the built passive API and actual legacy Norn rows, not merely handler mocks. Stronger external-call and database-write rejection checks remain requested before final startup acceptance.
- The concurrent retry ownership gap is fixed: `RetryClaimedOperation` now repeats ownership predicates in the final UPDATE and distinguishes ownership loss from unsafe retry. Astra source review accepted that correction. Primary independently passed `go test -race ./store -run '^TestRetryClaimCASRejectsOwnerChangeWhileUpdateWaitsOnRowLock$' -count=3` against PostgreSQL; the test holds a competing row update, observes retry waiting on the lock, commits the replacement owner, and checks its state remains intact.
- Current combined foundation verification: independent `go test -race . ./store ./worker ./pipeline ./startup -count=1` passed with disposable PostgreSQL. This includes the built passive API using read-only database sessions and zero-request Nomad/Consul probes, actual legacy-schema adoption, and an executed shell rollback fixture with simulated service management. It is not a live supervisor/release or HA rehearsal.

The next M1 store implementation is dispatched to Sol high after its ownership-slice checkpoint: [atomic operation acceptance](atomic-acceptance-handoff.md), including scoped identity, signed acceptance intent, transactional domain rows, and uncertain-commit resolution. HTTP and pipeline integration follow the agreed store contract; dispatch is not implementation completion.

Atomic-acceptance work now has initial domain types and migration SQL in the
worktree, plus HTTP authentication provenance and verified actor-context code.
The persisted authority identity replaces an ambiguous environment-name
fallback. The adapter, migration integration and endpoint acceptance tests are
still in progress. Existing token scope/legacy/step-up/managed-registry/header
regressions passed independently under the race detector after the provenance
change; this does not verify the new acceptance transaction or actor resolver.

New actor/context unit tests also passed independently under the race detector,
covering duplicate device labels, token lineage, issuer separation and CI
attempt identity. Source review then found that the real generic CI-token
rotation path drops CI/app/environment provenance; correcting that path and
testing actual authentication/rotation remains required. Unit tests that merely
replace a token ID while retaining claims are not end-to-end rotation evidence.
Maintenance acceptance also requires coordinated API/OpenAPI/CLI retry-key
handling; this is assigned with HTTP integration, not treated as an optional
post-release client fix.

Independent `go test -race ./handler -run 'Test.*(Acceptance|Rotation|Rotate)'
-count=1` with disposable PostgreSQL now passes the first real maintenance
middleware/adapter acceptance fixture and actual signed-token rotation fixture.
The maintenance fixture confirms one identity/intent/operation, original-result
replay across device credentials, changed-request conflict and reserved receipt
linkage. The rotation fixture verifies managed-token actor continuity and
fail-closed generic CI rotation. This closes the identified rotation regression;
it is not provider OIDC-exchange or complete acceptance qualification.

The first PG acceptance adapter and retained-key HMAC signer now compile.
Independent source review found acceptance-blocking issues: recursive removal
of ID-shaped keys can erase semantic request references; queued versus completed
acceptance disposition needs fingerprint binding; Resolve must verify immutable
operation semantics, not only IDs and a supplied fingerprint. Fixes and real-PG
regressions are in progress. Normal lifecycle/output updates must remain
replayable while tampered immutable intent must be rejected.

Independent acceptance verification now passes the complete targeted store
suite with PostgreSQL and `-race`, including authority, claim visibility,
concurrency, rollback, indeterminate acknowledgment, retention and integrity
tests. The full API module also passed against a fresh disposable database with
only the documented Darwin sampler excluded; the CLI race suite passed.
The pinned-release lifecycle finding is now closed: derived `SourceKind` is
replayable while accepted SHA/artifact/source-ref constraints remain bound.
Astra's bounded fingerprint/Resolve review reports no remaining blocker;
primary's final targeted PostgreSQL race run passed, including actual
`UpdateDeploymentResult` and changed-SHA rejection. The local store seam is
accepted. Remaining enqueue callers, legacy replay bridges and client parity
still prevent full atomic-acceptance/M1 completion.

Migration 2 introduces a new release-tool blocker: the direct upgrade script
checks the previous binary against the current ledger, migrates, then checks it
again. A writer-contract-1 fallback can pass the first check but fail after
minimum writer 2 is committed, leaving the old serving process active and no
validated fallback. Before M5/release acceptance, compare prospective target
schema requirements with current/fallback binary contracts **before migration**,
and enforce the admission/old-writer transition. The mode-only startup probe
and queue snapshot are insufficient. Do not use this branch for a live upgrade.

Exec regression coverage includes repeated migration preserving current/legacy rows, independent store instances, concurrent claims with one winner, live-owner preservation, expired-owner recovery, hard TTL, empty/wrong credentials, stale renewal/completion, positive completion, cancellation/revocation precedence and watcher closure on ownership loss/database error. These are PostgreSQL and unit tests; no live Nomad exec or Mini upgrade was exercised.

These observations are baseline evidence only. They are not a Mini upgrade rehearsal, Fleet bootstrap, live deployment, qualification result, or M0 completion.

## Claude batch one checkpoint: M2 resolver and recovery follow-through

Scope per [claude-implementation-handoff.md](claude-implementation-handoff.md):
the pure M2 database resolver/transition contract, then control-recovery
verifier/CLI gaps. No parser, runtime, effect/supervisor or pipeline wiring
changed. This is a reviewable local batch, not an M1/M2 gate.

Implemented:

- `database/`: `NewResolver`/`ValidateCatalog`/`Resolve`/`ValidateTransition`.
  `ClientCertificateRequired` now reports service policy. Named vs explicit
  legacy resolution never falls back; named+legacy requests, legacy names that
  collide with a named binding, and legacy names that hit a control database on
  the same provider are rejected. PG and MySQL have separate declarable
  capability sets. Cockroach/aliases are rejected, and control services must
  be PG. Application/control credentials, roles and same-provider databases are
  kept separate. Full `TargetIdentity` fencing is enforced (restore requires
  `Expected`). Deployment topology is `local`/`fleet` and rejects NORN_PROFILE
  names. Errors and redacted JSON/fmt projections never echo references or
  rejected values. Transitions: service purpose is immutable. Other service
  changes need a service generation bump. Binding/legacy target or TLS-target
  changes need a binding generation bump. Credential/certificate reference
  rotation keeps the generation. Generations are monotonic, and logical
  resources cannot be re-pointed.
- Review R1/R2 (`claude-batch-one-review.md`): removal now requires permanent
  `retired` tombstones (service/binding namespaces; legacy mapping IDs share the
  binding namespace), tombstones cannot be dropped and IDs cannot be reused.
  Legacy mappings are compared by MappingID across the whole catalog, and
  shared MappingIDs must have identical definitions. Both review regressions
  in `review_generation_history_test.go` are preserved and pass.
- `store.VerifyAcceptanceEvidence`: one pure verifier (digests, signed links,
  immutable domain, runner lineage) now used by both `PGOperationStore.Resolve`
  and restored-bundle verification. It compares payload/metadata by exact
  decimal value; the live store now loads exact payload/metadata for this
  check, so it also rejects float-equivalent integer tampering above 2^53.
  Signed envelopes and requests are decoded strictly (unknown fields rejected).
- Qualification recovery verification mirrors the handler's structural checks
  (schema, staging, IDs, SHA/content-addressed artifact, full candidate incl.
  signer workflow) and also binds the receipt to its operation row (ID, app, ref,
  status, source, metadata) and qualified deployment row.
- Producer known vectors: `handler/recovery_vectors_test.go` pins the real
  `signReleaseQualification`/`signMutationAudit`/`signMutationAuditIncident`
  output to `controlrecovery/testdata/*.json`. Recovery's independent
  implementation is tested against those files.
- Recovery: `RestoreReport.unresolvedEffects` (checked against the restored
  rows), bundle/public-input reads use non-blocking single descriptors (FIFO
  rejection), recipients/public keys are bounded regular-file reads. Private
  inputs keep single-fd `O_NOFOLLOW` owner-only checks; inspection output stays
  atomic no-replace. Key JSON and passive/inert semantics are documented in the
  CLI package comment and the control-recovery handoff.

Evidence (this session, 2026-09-22):

- Passed without PostgreSQL: `go test -race ./... -skip
  '^TestSampleDarwinHostMetrics$' -count=1 -timeout=300s` across the API module
  (startup 27.563s, controlrecovery 3.342s, database 2.275s, store 2.585s,
  handler 2.659s, cmd/norn-control-recovery 2.150s); `go vet ./...`; gofmt. This
  run skips every `NORN_TEST_DATABASE_URL`-gated test.
- Passed in that run: resolver suite (including both review regressions),
  handler producer-vector tests, recovery vector/tamper tests, store exact-JSON
  unit tests, and CLI subprocess tests for malformed/unencrypted recovery key
  documents, group-readable identities and FIFO inputs.
- The implementing session could not run any PostgreSQL-backed test: in this
  non-interactive session the permission layer required interactive approval for
  any command setting `NORN_TEST_DATABASE_URL`.
- Independent reviewer checkpoint (`claude-batch-one-review.md`): a full API
  `go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1
  -timeout=120s` passed with both disposable source and recovery-target URLs set
  (CLI recovery 7.260s, controlrecovery 8.388s, database 3.731s, store 8.525s,
  startup 29.198s). That included the real CLI encrypted-key passive restore.
  The run overlapped the final store evidence tests
  (`store/operation_acceptance_evidence_test.go`, including the PG-backed
  `TestResolveBindsIntegersAboveFloatPrecision`), so rerun the changed packages
  with both URLs set before acceptance: `go test -race ./store ./controlrecovery
  ./cmd/norn-control-recovery ./database ./handler -count=1`.

Remaining gaps: no parser/runtime consumer wiring, provider provisioning,
MySQL adapter or real-engine MySQL backup/restore; tombstones are catalog data,
not yet a persisted protocol. Legacy unsigned/keyless mutation-audit rows fail
recovery verification closed. No fenced activation exists. The next batch
(execution recovery) remains as scoped in `claude-next-batch-review.md` and
was not started.

## Gate and deployment boundary

The explicit encrypted PostgreSQL round trip now independently **passes**
(1.974s): `CreateBundle` uses real `pg_dump`, the encrypted artifact is verified,
`RestorePassive` invokes real `pg_restore` into a separate disposable database,
and the restored acceptance/audit fixture passes retained-key verification.
The report remains passive and not activation-ready. This resolves the missing
`admission` field failure below for that fixture, but deployment-bearing
acceptance, qualification/tamper coverage, failure atomicity and CLI validation
remain required; one positive round trip is not full recovery qualification.

New independent checks pass: ten web event-hook tests (30ms), executor race
tests (1.570s), and supervisor manager race tests (1.977s, mocked backend).
The recovery round-trip test initially skipped without its separate target;
an explicit rerun against a new disposable target database performed the dump
and restore but **failed** retained-signature validation because the verifier's
canonical request type omitted `admission`. This is an integration failure,
not a qualified restore. Exact schema alignment and fixture cleanup safeguards
are assigned; the supervisor tests do not prove Linux process containment.

The latest full API module race run **passes** with disposable PostgreSQL and
only the previously documented exact Darwin sampler exclusion:
`go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1 -timeout=120s`.
Representative results: startup 27.813s, store 7.935s, handler 4.485s,
controlrecovery 3.618s, hub 3.175s and worker 2.452s. This supersedes the earlier
integration failures below for this tested source snapshot. The recovery CLI
builds but has no dedicated tests; real encrypted PG restore, production effect
supervision and client replay-resync remain incomplete. A green module suite
does not close any full milestone gate.

Targeted worker/store race checks independently pass (worker 2.061s, store
2.864s) after adding unresolved-effect deferral. The worker test proves a pending
effect queues claim-guarded recovery rather than generic retry or terminal
failure. Pipeline stages do not yet propagate this contract end to end; a
claim1-pending/claim2-recovery test with one external execution and one terminal
publication remains required before integration is qualified.

Recovery's subsequent complete package race run passes against disposable
PostgreSQL (1.989s): the registry now classifies migration 3 with its exact
checksum, and the unused-import compile failure is corrected. Coverage remains
inspection, manifest validation and synthetic encrypted-bundle reads. No actual
`CreateBundle`/`pg_dump`/`RestorePassive` round-trip test exists at this checkpoint;
neither concrete retained-signature verification nor the CLI is qualified.

Latest full API race attempt passed all compiled packages, including startup
29.470s, store 7.670s, handler 4.831s, hub 2.199s and effect 1.485s. The run
still failed overall because the actively edited recovery package had an unused
`os` import in `restore.go`. Migration-3 startup/store fixture failures from
the earlier run no longer reproduced. The same exact Darwin sampler exclusion
was retained. Recovery must compile and pass before claiming a green full run.

The revised hub independently passes ten race-suite repetitions (1.536s),
including registration-before-upgrade, replay/poll deduplication and older
external/newer local delivery. Reconnect cursor behavior across out-of-order
cross-node delivery remains a separate review item; this is not multi-node
event-stream qualification. Recovery manifest validation passes independently
(3.707s), and the synthetic encrypted-bundle verify-then-read test passes
(1.359s), confirming the ciphertext descriptor stays usable after verification.
Neither test performs a real PostgreSQL dump/restore round trip.

The first three PostgreSQL effect-store race tests independently pass (1.823s):
original-reservation recovery/repeat-safe completion, refusal to erase a
recorded launch, and expired-claim refusal after a held operation lock.
The last test still needs an explicit waiter barrier to prove it exercised
the lock-wait interleaving rather than merely starting after expiry. These
tests do not establish production supervisor or worker integration.

The executor race suite independently passes again (1.587s), now including
`TestExecutorDoesNotAcceptTerminalEvidenceFromDifferentRuntime`: a launched
reservation for runtime A remains launched and unchanged when the supervisor
reports repeat-safe failure from runtime B under the same execution ID.
This verifies the in-memory executor guard, not the pending PostgreSQL adapter
or a restart-persistent supervisor.

The subsequent full API race run is **not green** after introducing migration 3.
Inspection correctly refuses the unclassified `operation_effects` table; its
future-ledger fixture also collides with version 3. Startup contract and control
authority tests still assert migration/writer version 2. These integration
updates are assigned to the owning implementation lanes; validation must remain
fail-closed. Separately, `TestHubPersistsIDsAndReplaysAfterCursor` timed out on
its live-message read under the full run, then passed ten isolated race runs
(1.318s). That isolated pass does not resolve the broad-run failure or prove
absence of a registration/broadcast ordering race.

A subsequent combined targeted race run passed against disposable PostgreSQL:
effect 1.636s, store 2.204s, handler 1.555s. Its selection covered executor,
revocation, stale completion, resource-gate, runner-attempt and dispatch-finish
tests, including the runner HTTP acceptance test. This is a targeted integration
checkpoint, not the full API suite or external runner qualification. The new
effect PostgreSQL adapter and signed encrypted recovery CLI remain in progress.

Runner-attempt acceptance's five targeted PostgreSQL race-test groups now pass
independently (1.855s), including generated-ID replay, request-option conflicts,
raw-nonce rejection, atomic recovery rollback/concurrency and pristine-then-
tampered lineage checks. Final invariant review and protected external runner
caller compatibility remain pending before the bounded unit is frozen.

The new effect executor's race tests pass independently (1.366s) using in-memory
store/supervisor adapters. The paused-before-launch test proves the interface
requires a durable execution-ID tombstone before releasing a never-launched
reservation, and a successor can execute while the old launch is rejected.
This is not yet a PostgreSQL effect store, restart-persistent host supervisor,
process containment proof, or integrated worker/build/test recovery.

The first offline control-inspection package independently passes its real-PG
race suite (2.060s): consistent repeatable-read snapshot across a concurrent
accepted-operation write, exact BIGINT lexemes above 2^53, deterministic output,
secret-canary exclusion, unknown zero-column table/column rejection, unsupported
catalog/checksum rejection, and exact-one compatibility metadata. Registry
isolation and safe diagnostic tests also pass. This is an inspection-only
library with `restorable:false`, not an encrypted backup, offline CLI, restored
control plane or M1 recovery completion. Signed encrypted same-PG recovery
remains required.

The post-reconciliation integration checkpoint independently passed the full
API module with disposable PostgreSQL and `go test -race ./... -skip
'^TestSampleDarwinHostMetrics$' -count=1` (startup 29.407s, store 6.413s,
handler 3.809s). The full CLI race suite also passed (api 1.901s, cmd 3.080s).
The exclusion is the existing host CPU sampler timeout, not a new acceptance
test. This checkpoint precedes runner-create and browser-fixture implementation.

### Independent verification checkpoint: client retries and Fleet leases

Actual in-app browser verification against the isolated built-UI fixture now
passes the three intended client flows: lost preflight response followed by
401 and terminal replay (three identical request keys); partial deploy-group
retry (two identical parent keys, original child receipt retained); and running
deploy with a 503 first poll followed by terminal success (one POST, two polls).
The final deploy exercise recorded zero unsupported API calls and browser
warning/error capture was empty. Earlier overview exploration hit deliberately
unsupported fixture routes; it does not qualify those unrelated panels.
Synthetic health and enabled-deploy metadata were corrected in the fixture,
not production behavior. Fixture isolation tests independently passed 4/4.
Temporary loopback servers and browser tab were stopped after verification.
This is actual browser-to-synthetic-API evidence, not a live Norn deployment.

The web retry/result slice passed independent verification: all 14 Vitest files
(102 tests), TypeScript compilation and the Vite production build. Rejected or
indeterminate requests retain their original idempotency key, including a lost
response followed by an authentication rejection. Accepted operation polling
survives transient errors, and partial group retries preserve member receipts.
This bounded source/test slice is frozen; real-browser verification against an
isolated synthetic backend is recorded above. No live API was used.

Five Fleet reconciliation/attempt store regression tests independently passed
against disposable PostgreSQL with the race detector (4.678s). The expiry tests
observe an actual PostgreSQL lock wait with transaction start before lease
expiry, wait for database-clock expiry, then release the lock. This distinguishes
post-lock wall-clock validation from the unsafe transaction-start timestamp.
Independent review confirms that evidence gap is closed. HTTP replay/conflict
and greater-than-100-history coverage remain in progress before freezing the
complete reconciliation unit. The subsequent greater-than-100-history store
fixture initially failed on an untyped SQL parameter; after an explicit cast
fix, all six targeted store regressions independently passed with the race
detector (3.696s). Pipeline and contract package race tests also passed; the
broader handler run hit the already documented Darwin sampler timeout and does
not count as a passing handler suite. A subsequent independent full handler
race run passed (3.107s) with only that exact test excluded. The new
`TestFleetReconciliationHTTPUsesTypedAtomicAcceptance` also passed independently
by name with verbose output (1.574s): original receipt replay after a later
phase, changed-input conflict, actor isolation and signed acceptance rows
without legacy raw-key metadata. These close the pending HTTP/history test
checks for the bounded reconciliation slice, not full Fleet acceptance.
Recovery-CI authorization still has an existing
attempt-expiration side effect in its read path; the store replay itself does
not perform dynamic admission or expiration.

The reconciliation unit is now frozen after implementation and independent
review/test checkpoints. The ineffective expiry update inside a rejected
acceptance transaction was removed; rejected admission does not pretend to
persist an expiration mutation that would roll back. The next active API unit
is compound runner-attempt acceptance: dispatch/predecessor revalidation,
atomic cancellation plus successor/receipt creation, immutable dispatch run
binding, and removal of hidden expiry writes from authorization reads. These
changes do not by themselves prove a prior external runner has stopped.

All full M0–M9 milestone gates remain pending. No milestone gate has passed. No live deployment, provider provisioning, migration, cutover, or other runtime mutation is authorized or has been performed by this branch status.

Future implementation and review must attach the specific evidence required by the execution milestones before any gate can be reported as passed. Live work requires its own explicitly named execution scope.

## Workspace handoff

Worktree: `/Users/arti/Desktop/Claude/norn-v3-foundations`, branch `codex/v3-foundations`. Changes are local and uncommitted. The original checkout's independently committed HA-lab work (`66cf0c4`) has not been merged or modified here. Review this isolated patch before integrating with that checkout.
