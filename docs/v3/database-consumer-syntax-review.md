# Database consumer proposal: root review

## Independent full-suite checkpoint

After the snapshot and exact-generation corrections, root's targeted pipeline
race run passed (2.525s), including both independent review regressions. Root
then ran the complete API race suite with both disposable PostgreSQL URLs and
only `TestSampleDarwinHostMetrics` excluded: all packages passed (database
7.671s, pipeline 3.239s, store 9.712s, worker 8.026s, controlrecovery 5.639s).
This verifies the current local slice, not missing named runtime consumers,
MySQL, retention, Fleet etcd or overall M2 completion. Final batch source review
and contract disposition are still required before acceptance.

## Independent adapter correction checkpoint

The four material_review_test.go regressions pass with race detection after
the endpoint/password separation and decoder/formatting corrections (1.383s).
With both disposable URLs explicitly set, the verbose race run of
TestSameNamedDatabasesOnTwoServersAreSelectedByDeclaredTarget,
TestDumpFromOneServerRestoresIntoTheSameNamedDatabaseOnTheOther, and
TestEndpointIsCatalogIdentityAndRotationKeepsTarget passed, not skipped (4.411s).
The helper starts and cleans a separate Unix-socket-only PostgreSQL instance.
This qualifies the tested adapter paths only, not runtime rendering or complete
pipeline integration. A previous run without the URLs skipped these integration
tests and is not used as evidence of their success.

## Independent catalog checkpoint

Real disposable PostgreSQL verification passed:
`go test -race ./store -run TestDatabaseCatalog -count=1 -timeout=90s`
(1.468s). The test covers expected-revision conflicts, permanent retirements,
forbidden generation reuse, four concurrent activations with one winner, and
stored-byte tamper rejection. This does not prove target acceptance atomicity,
consumer integration or cross-server routing.

## Reproduced adapter failures

Final-source regression: TestReviewLegacySnapshotAdoptionCannotCrossGeneration
FAILS (0.451s). Legacy ownership records profile/mapping/service/database but
not the complete TargetIdentity. A dump adopted at service generation 1 is
still findable and verifies successfully at generation 2, allowing a cross-target
restore without intent. Pin the complete target tuple at adoption and reject
incompatible reuse (including role/binding generation changes), with explicit
migration mapping required to transfer it. Retain this regression; the earlier
full-suite pass did not cover this newly identified case.

Snapshot integration regression: TestReviewSnapshotReuseCannotAdoptUnprovenDump
fails independently. createDataSnapshotAt with reuse=true takes an existing
nonempty dump without metadata and ensureSidecar assigns the current target to
its bytes. This fabricates provenance, including in a non-legacy namespace.
Unknown existing bytes must fail closed; recover only from a durable pre-dump
intent binding or atomically published verified dump/metadata bundle. Retain
database_snapshot_review_test.go and add interruption/retry coverage.

Independent `go test ./database -run '^TestReviewMaterial' -count=1` fails
both tests in material_review_test.go against the initial material.go:

- TestReviewMaterialIgnoresAmbientService: pgx.ParseConfig reads PGSERVICE and
  PGSERVICEFILE before later fields are cleared. A missing ambient service file
  breaks an otherwise explicit target. Build configuration with all inherited
  defaults neutralized without mutating process-global environment; test ambient
  password, certificate and timeout settings as well.
- TestReviewMaterialRejectsTrailingJSONDelimiter: Decoder.More is not an EOF
  check. A valid object followed by `]` is accepted. Require a second Decode to
  return io.EOF and reject duplicate keys where strict secrets demand uniqueness.

Retain these independent regression tests. These are local helper failures,
separate from the endpoint-fencing and real-engine acceptance gaps below.

Follow-up: explicit private service selection now fixes the ambient-service
regression. However TestReviewMaterialDoesNotInheritPassword independently fails:
an omitted optional password inherits PGPASSWORD into session.config.Password.
The target environment must explicitly neutralize every connection default,
not just host/service selection. The trailing-delimiter regression still fails.

TestReviewMaterialFormattingIsRedacted also independently fails: fmt with `%v`
prints Session's private password field. Implement redacted String/GoString (and
check value versus pointer forms) so diagnostic formatting cannot expose it.

Source review: bindDatabaseTarget JSON-unmarshals the typed target into
map[string]interface{} without UseNumber. Generations above 2^53 and catalog
revisions can be rounded before signing. Preserve exact integer values across
binding, persistence, evidence verification and execution; add a large-generation
round-trip regression rather than restricting the established uint64 contract.

Decision: do not adopt section 3 parser/runtime syntax yet. Separate versioned
catalog and app contracts, independent topology/security settings, explicit
legacy mappings and closed connection environments are the right direction.
The following require correction before acceptance.

1. Endpoint identity cannot be freely mutable credential material. The proposed
   secret contains host and port but accepted identity contains neither their
   digest nor an immutable endpoint revision. Changing that secret can repoint
   accepted work with unchanged generations. Bind endpoint identity to the
   service generation (or immutable versioned endpoint record); credential-only
   rotation may change passwords, not target routing. Database/user probes alone
   cannot distinguish two clusters with identical database and role names.
2. A `postgresql:///?service=...` URL is not a universal application connection
   contract. Node drivers, WordPress and MySQL must receive supported per-engine
   connection material through private runtime templates/files. Preserve
   non-secret inspection/job intent, but do not require all clients to implement
   libpq service files. Validate application and per-process environment conflicts
   across web, worker, cron and function renderers.
3. The catalog YAML example omits the per-resource apiVersion values required
   by current types/validation; its profile maps shop-db while its app references
   primary. Provide one coherent, executable strict-decoder fixture. Check the
   actual InfraSpec app/name convention rather than inventing a replacement.
4. Flat legacy snapshots need a unique explicit namespace owner. Two profiles
   or mappings must not both adopt one database-name namespace. Missing-sidecar
   adoption needs validated inventory/mapping, not blanket permission to restore
   any same-named dump. Dump and metadata publication/recovery must be atomic in
   effect: partial publication cannot turn a new dump into an unbound legacy one.
5. Target resolution at acceptance must preserve idempotent replay after catalog
   updates: return the original accepted receipt, then reject stale execution.
   Do not replace the client's original request semantics with newly resolved
   metadata when checking retry fingerprints.
6. The document was written before implementation. Its section 2 claims must
   remain proposed/in progress until source and tests establish them. Migration
   code inheriting control environment is a credential exposure; it does not
   by itself prove the control DSN was selected as an application default.

Two-server tests remain required. A disposable second local PG instance falls
within scoped local testing from the handoff; it is not live provisioning or a
reason to substitute two database names on one server. No package installation,
privileged runtime or external service is authorized by that test scope.

Read database-consumer-review-checklist.md for the complete consumer acceptance
checks. This review is not authorization for an external cutover or deployment.
