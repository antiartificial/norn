# Named runtime integration review

## Import and environment correction checkpoint

Independent post-hardening rerun: named database snapshot/restore/inventory/
export/health roundtrip and root exclusive-download import regression both
PASS (6.462s, race, both URLs, verbose non-skip). Manifest local-file reads
now reject nonregular/symlink inputs and enforce a 64 KiB bound. Still review
transport download bounds and `copyVerified`, which currently uses a blocking
open and unbounded io.Copy before checking size; local manifest checks alone
do not establish fully bounded archive input handling.

Root independently reran both regressions plus the implementation's legacy
credential and job-ID collision tests with race detection, both disposable
URLs and verbose output: all four PASS, no skips (4.152s). Import now downloads
to an absent path inside a private same-filesystem staging directory. Legacy
migrations receive basic process variables and only the explicitly declared
PGDATABASE, not inherited DATABASE_URL/PG* credentials. This proves the tested
environment boundary, not filesystem isolation from HOME-based client defaults
or qualification of every legacy application's migration configuration.
Historical failures below are retained as the correction record.

Broader independent rerun PASS: pipeline 14.077s, nomad 1.800s, handler
3.207s, worker 14.289s (`-race`, both URLs, `-count=1`, excluding only
`^TestSampleDarwinHostMetrics$`). Remaining source-review findings and actual
runtime qualification are not waived by these package results.

## Target guard review during correction pass

Baseline endpoint scope/profile unit check independently PASS (1.759s,
race): api:write alone is forbidden and missing configured profile is refused.
This test does not cover successful authenticated HTTP acceptance, audit,
idempotent replay, concurrent mutations or the mixed-history conflict below.

Baseline conflict laundering now independently reproduced:
`TestReviewBaselineCannotHideKnownConflictBehindAmbiguousHistory` FAILS
(2.517s, real PG, race, verbose non-skip). Older succeeded deploy records
primary on pg-a; a newer failed operation has unknown targets; current catalog
maps primary to pg-b. Baseline acceptance ignores the early Ambiguous error
and accepts pg-b without examining the known pg-a writer. Preserve uncertainty
and known target sets together; baseline attestation must never erase a known
conflicting writer merely because another row is ambiguous. Test acceptance
and execution-time history changes, not only an isolated known baseline.

Correction evidence: root rename plus queued/failed writer-free-history
regressions now all PASS independently (2.862s, race, real PG, no skips).
Broader target-history/addition/replacement cases and the legacy-to-named
baseline pipeline test also PASS (3.869s, both URLs, verbose). No-baseline
history now requires runtime registration checks regardless of harmless rows.
The newly added baseline endpoint and its interaction with ambiguous-plus-known
writer history still require independent authorization/recovery review; this
is not blanket acceptance of the new attestation path.

Root `TestReviewWriterFreeHistoryStillRequiresRuntimeEvidence` now FAILS
for both queued and failed-at-build history (0.640s, race, real PG, no skips).
With no Nomad client and no successful target baseline, those harmless history
rows suppress runtime observation and allow a new target. No-history itself
is refused correctly; adding a non-writer row must not turn unknown runtime
into safe admission. Preserve both subcases in database_guard_review_test.go.

Correction: root rename regression now PASS (1.500s, race, real PG,
verbose non-skip). The in-flight writerHistory implementation adds historical
writer sets and a baseline path. Review two boundaries before accepting:
`sawWriterWork` becomes true even for queued or writer-free failed deploys, so
those rows must not suppress the no-history runtime check; an existing legacy
job remains ambiguous despite an unrelated failed build. Also a baseline must
not discard known conflicting targets merely because a different historical
row is ambiguous. Probing candidate targets proves their identity, not that
old writers are fenced. Baseline authorization, explicit attestation, accepted
payload identity and concurrent deploy serialization require tests.

Rename bypass is now reproduced, not merely a source finding:
`TestReviewTargetGuardRejectsRenamedReplacement` FAILS against disposable PG
(0.497s, race, verbose non-skip). A succeeded deployment records primary on
pg-old; replacing its sole logical resource with replacement on pg-new is
accepted by requireRunningTargetsUnchanged. Preserve the regression. A
removed-old/added-new resource pairing cannot establish independent database
addition while old allocations may still write; require explicit transition
evidence rather than treating nonintersecting names as automatically safe.

Independent correction evidence: focused Nomad delivery/template regression
suite PASS (1.472s); real-PG
`TestNamedDatabaseDeployThroughAcceptanceAndWorkerUsesDeclaredServer` PASS
(9.048s, verbose non-skip, race). This proves unchanged-target credential
rotation and refusal of same-logical-name target moves at acceptance and
execution with zero migration/variable/job side effects. Nomad HTTP and task
template rendering are simulated; actual Nomad allocation behavior remains
unqualified. The test temporarily hides successful history to enqueue a move,
then restores it before execution, so it does NOT close the absent/ambiguous
history finding below or the logical-name replacement finding.

`database_guard.go` now compares named targets with the latest succeeded
app.deploy. Before acceptance, qualify these boundaries:

- Renaming logical `primary` to `replacement` while preserving DATABASE_URL
  must not bypass the writer-cutover guard. The current name intersection
  ignores removed/new names, permitting old and candidate processes to write
  different targets under the same runtime contract. Adding an independent
  database is distinct; test the rename/rebinding case explicitly.
- Latest succeeded operation is not necessarily all running writers: a failed
  partial rollout or active canary can leave candidate allocations. Refuse
  ambiguous runtime/history rather than infer absence from no successful row.
- Direct legacy-to-named conversion with no recorded target history needs
  explicit baseline verification, not a nil-history exemption.
- Check a rejected move before migration as well as before job registration:
  migration code can write to the candidate database before candidates start.

These are source-review findings on the in-flight guard, not completed test
results. Preserve ordinary unchanged-target rollout and explicit new-database
addition without implying the M6 cutover lane already exists.

## Import transport contract review

Now reproduced after callers compiled: independent race-enabled
`TestReviewSnapshotImportSupportsExclusiveObjectDownload` FAILS (2.627s,
verbose non-skip). Export succeeds, then import fails with `file exists` at
the pre-created `.norn-import-*.tmp` destination. Preserve this test and use
the real storage client's exclusive destination semantics in implementation
tests, rather than an overwriting fake that masks this incompatibility.

New root `TestReviewSnapshotImportSupportsExclusiveObjectDownload` matches
`storage.Client.GetObject` no-replace destination semantics. The current import
creates `temporaryPath` before calling GetObject, whereas the real client
publishes via `os.Link(tempPath, destPath)` and refuses existing destinations.
Use an absent filename inside a private same-filesystem staging directory.
Initial test execution is NOT a reproduced runtime failure: the package is
temporarily unbuildable while named_database_integration_test.go callers are
being migrated to the new export signature. Rerun after that in-flight edit.
Also bound manifest downloads and reject FIFO/nonregular files before blocking
reads; do not infer archive completion merely from a mutable manifest key.

## Read projection correction checkpoint

Independent `TestCatalogActivationOperationReadsAreRedacted` passes against
disposable PostgreSQL with race detection (1.613s, verbose, no skips). It
checks activation/replay/get/list/active/terminal responses for endpoint and
secret-reference canaries, and verifies persisted payload plus signed request
bytes are unchanged. `model/operation_projection.go` now projects Operation
JSON; storage marshals payload maps separately. Continue checking event paths
and internal struct serialization before treating every read surface as covered.

## Legacy migration environment regression

Recheck after `legacyMigrationEnvironment` was introduced still FAILS
(0.791s). Dropping NORN_* is insufficient: allowlisting inherited DATABASE_URL
and every PG* value still forwards the API's control-database routing/password.
There is no evidence that an API-process value is application-scoped merely
because it has a conventional database variable name. Use explicitly supplied
app material (or refuse an unconfigured database-consuming migration), while
non-database migration commands get only a neutral process environment. Do not
weaken the root regression to permit the control DSN or password.

`TestReviewLegacyMigrationDoesNotInheritControlEnvironment` now independently
reproduces inherited API/control-database environment in the no-profile lane
(race-enabled, 0.719s, no database fixture or skips). It uses test-only values
and a shell assertion that never prints them. Preserve this regression while
building the explicit application-scoped migration environment; empty or absent
app configuration must not silently reuse API DATABASE_URL/PGSERVICE/PGPASSWORD
or NORN_API_TOKEN. This is part of the active correction batch.

## Catalog execution fencing regression

Correction checkpoint: all three root catalog regressions PASS independently
against both disposable URLs (`go test -race ./pipeline -run
'^TestReviewCatalogActivation' -v -count=1 -timeout=30s`, 3.254s, no skips).
The implementation now atomically commits activation and the operation's
terminal success, removes equal-content recovery inference, and checks fresh
clock time at activation completion. This supersedes the reproduced failures
below, not the remaining broader runtime/export/redaction requirements.

Broader independent race verification also passes store (11.532s), pipeline
(13.961s) and worker (15.727s). Handler initially fails only the known
`TestSampleDarwinHostMetrics` CPU sampler (`signal: killed`); rerunning handler
with only that exact test excluded passes (3.767s). Both disposable database
URLs were supplied. This is local integration evidence, not Nomad allocation
or Linux containment qualification.

Root also independently ran the scoped-server TLS and runtime URL tests:
`TestTLSVerifyFullAgainstScopedServer` and
`TestRuntimeConnectionValueWorksForOrdinaryClients` PASS without skips
(race-enabled package run 4.313s). Evidence covers pgx/libpq verify-full,
wrong-CA and plaintext rejection, plus pgx and the test-owned Node SCRAM
client consuming value/file URLs. It does not cover installed node-postgres,
Nomad template execution, or TLS CA delivery into allocations (still refused).

Provenance is now independently reproduced too:
`TestReviewCatalogActivationCannotAdoptAnotherWritersRevision` FAILS. An
accepted/claimed operation at expected revision 0 reports success after a
different writer activates identical bytes at revision 1. Catalog equality is
not an operation receipt. The complete reviewer run (both URLs, verbose,
race-enabled) takes 2.140s: empty claim PASS; foreign-writer receipt FAIL;
lease-expired-during-lock-wait FAIL. Bind revision provenance to the accepted
operation identity and recover that exact historical revision, including after
later valid catalog activations.

Follow-up after claimed activation was added: the empty-claim regression now
passes, but `TestReviewCatalogActivationRejectsLeaseExpiredDuringLockWait`
FAILS with real PostgreSQL (1.866s package run, both URLs, verbose non-skip).
The test holds the catalog advisory lock on an independent connection, observes
the activation blocked, waits for lease expiry using database clock time, then
releases the lock. Activation still changes durable routing. `now()` retains
transaction-start time across the wait. Lock the operation row first as needed
and validate expiry with fresh database clock time after all relevant waits.
Retain both reviewer tests. The initial single-URL run skipped this fixture and
is not evidence; the final two-URL run above is the relevant result.

Independent recheck with both disposable PostgreSQL URLs:
`go test -race ./pipeline ./handler -run
'TestReviewCatalogActivationRequiresCurrentClaim|TestNamed|TestDatabaseCatalog'
-count=1 -timeout=120s` still fails the empty-claim regression
(pipeline 4.894s); selected handler tests pass (1.817s). Named integration
coverage does not supersede this failed authority invariant. The new export
test checks the callback pathname/key but does not read exported bytes or
round-trip their provenance, so it does not yet qualify archive recovery.

TestReviewCatalogActivationRequiresCurrentClaim FAILS against real disposable
PostgreSQL (0.766s): executeCatalogActivation activates accepted catalog bytes
with an empty OperationClaim, before any worker claims the operation. The
activation store method has no claim argument/check. Validate authority, owner,
generation, operation identity and expiry under transaction locks together with
the catalog CAS; read DB time after acquiring locks. Preserve the root regression
and add expired/stolen claim and lock-wait cases. Checking only terminalization
does not undo a stale owner's durable routing change.

Also bind activation provenance to operation ID for crash recovery: comparing
only catalog contents at expected+1 cannot prove this operation performed it,
and looking only at the latest revision loses recovery after later activations.

## Correction checkpoint

Independent `go test -race ./nomad -run '^TestReview.*Delivery' -count=1`
passes (1.926s) after revision-specific staged items were introduced. This
addresses the two reproduced helper-level mutations. Still audit all consumers:
revision-zero templates read mutable promoted items, so no long-running cron,
rollback or service allocation may silently select that fallback. Function
copies must remain invocation-owned and immutable. Runtime tests must show that
changing unrelated staged items does not restart old allocations and that
promotion cannot authorize simultaneous independent-cluster writers.

Pipeline-specific qualification: submit.go registers candidates referencing the
new staged revision before promoteDatabases. Candidates can write as soon as
they start; promotion of a variable is not a database writer fence. When a
running app's actual target tuple changes, ordinary deploy must reject it until
the explicit database cutover lane supplies writer-fencing/catch-up evidence.
Credential-only rotation and unchanged-target app rollout are different cases.
Add a two-target test asserting no new-target job is registered while old-target
writers remain authorized. This guard preserves the planned M6 migration lane;
it must not be advertised as completed online cutover support.

## Initial delivery-source findings

Confirmed by independent regression
TestReviewDatabaseDeliveryCannotRepointLiveJob (FAIL, 0.739s): publishing newer
material and then replaying an old delivery overwrites the material referenced
by the running job. Keep database_delivery_review_test.go; adapt it to explicit
generation-aware APIs as needed, preserving the no-repoint assertion.

Follow-up: PutDatabaseVariable now refuses replacement, but the new
DeliverDatabaseVariable updates the same watched path when the catalog revision
increases. TestReviewCandidateDeliveryDoesNotRepointCurrentAllocations FAILS
(0.711s): preparing revision 2 rewrites material referenced by revision 1
allocations. Catalog ordering is not rollout/cutover authorization. Immutable
material and per-allocation target references (or an explicitly proven controlled
handoff) remain necessary. Do not fix only the old helper while preserving the
same unsafe mutation through a newly named method.

`nomad/database_delivery.go` currently publishes to nomad/jobs/<jobID> and
templates use change_mode=restart. PutDatabaseVariable reads the latest index
and then CAS-updates any changed URL. This is NOT operation/target fencing:
an older accepted operation arriving later can read the newer index and replace
the newer target. Worse, publishing a candidate target immediately restarts
existing allocations against it before candidate registration/health/traffic
cutover. Use immutable generation-bound material and allocation references or
another proven fenced handoff. CAS against an index just read is insufficient.

CopyDatabaseVariable similarly takes the current mutable service value rather
than a function invocation's accepted target. Bind invocation identity and
cleanup to exact owned material. Audit app/periodic job naming collisions
(app a-b versus process b of app a) before treating jobID as a private namespace.
Keep default ACL workload-identity behavior distinct from proven authorization
under a scoped runtime test.

## Reproduced URL failure

TestReviewRuntimeURLPreservesIPv6Authority fails against the initial URL builder:
an accepted IPv6 endpoint becomes a percent-escaped host without brackets and
url.Parse rejects it. Use correct authority construction (including IPv6 brackets)
while separately escaping username/password/path/query. Preserve
database/runtime_url_review_test.go. Its password is a test-only literal.

Review against the actual submitted job and allocation client, not only rendered
YAML or fake command calls. This supplements the batch-four handoff.

- Bind private variable paths to app authorization, target identity and generation.
  Updating one shared mutable variable must not silently repoint already running
  allocations or split old/new writers during a target cutover. Credential-only
  rotation needs an explicit template/restart contract distinct from cutover.
- Inspect web, worker, cron and function translation paths. Per-process values,
  app environment, secret keys and multiple database declarations must not
  override each other or the resolved target. Reject collisions before variable
  publication/job registration, not after an allocation starts.
- Confirm workload identity can read only its private material. A generic
  namespace-wide read ACL or an app-supplied arbitrary variable path must not
  expose other applications. Submitted job specs contain references and templates,
  not passwords. Test job/API/error/inspection serialization with canaries.
- Template syntax and escaping must handle quotes, newlines, backslashes,
  dollar signs and URL-reserved characters in passwords without injection or
  corruption. A file path is not a connection URL; test conventional clients.
- Runtime readiness must fail closed if variable publication/template rendering
  fails. Do not declare a deployment healthy merely because registration returned
  a job ID. Preserve the old healthy deployment when candidate material fails.
- Retry variable publication and job registration using durable generation-bound
  identity. A stale worker must not overwrite material belonging to a newer
  target/operation; clean up only exact resources no live allocation references.
- Every accepted database use must record its target, not just migrationDatabase.
  A service with several databases must not run one stale secondary binding while
  checking only the primary. Explicitly define which binding snapshot APIs select.
- Audit every workload producer beyond app.deploy: rollback, deploy-group child
  operations, restart/scale and cron/function registration must preserve or
  explicitly revalidate the accepted target set. The initial
  databaseConsumingKinds map excludes app.rollback; do not let that path submit
  named-runtime material using an unbound current catalog or ambient fallback.
- Catalog mutation uses existing authentication, scopes, durable idempotency,
  audit and revision preconditions. Identical retry returns original receipt;
  conflicting request, unauthorized actor and unavailable authority fail closed.
- Redact catalog payload consistently on generic operation GET/list, replay,
  events and CLI output, not only ActivateDatabaseCatalog's immediate response.
  Accepted catalog bytes contain private endpoint and secret-reference metadata;
  compare access policy against redacted GetDatabaseCatalog. Test an API-read
  principal following Location after activation and listing the same operation.
  Source recheck confirms `handler/operations.go` GetOperation, ListOperations
  and ActiveOperations serialize stored operations after AttachReceipt only;
  `model.Operation.AttachReceipt` does not redact payloads. These paths therefore
  need an explicit read projection, without mutating the signed persisted bytes.
- Snapshot inventory/export/readiness and health share the resolver and access
  control with restore. Foreign-generation dumps are not current restore proof.
  Do not allow arbitrary filesystem paths, symlink traversal or cross-app export.
- ExportTargetSnapshot currently verifies by pathname and later passes that path
  to upload for another open. Preserve one verified file identity through upload
  (or revalidate a private immutable copy), including replacement/symlink races.
  Export recoverable target metadata with dump bytes: a target-aware object key
  alone does not preserve the full target tuple, digest and generation needed by
  restore. Test export/import round trips rather than upload success alone.

Record what is source-tested, what uses real clients, and what runs under an
actual scoped Nomad allocation. Missing runtime evidence remains unqualified.
