# Retention implementation review

Initial source review of migration 6; implementation is in progress, not accepted.

## Independent checkpoint, 2026-09-22 evening

Root full API race suite PASSes with both scoped PostgreSQL URLs, excluding
only the known Darwin metrics sampler test: pipeline 28.572s, retention 19.500s,
startup 25.793s, store 14.625s, worker 20.284s, recovery CLI 19.905s,
handler 15.121s, logcollect 5.139s, archive 4.510s. This is an integrated local
checkpoint; outstanding semantic findings below and full M2 requirements
remain open despite the green suite.

Recovery CLI emulator race correction independently verified: both archive CLI
tests PASS (10.222s, race, real scoped PG and emulated S3). Fault switches now
use synchronized Configure calls. This closes the reproduced test data race,
not the separate read-only-open/probe-write finding.

Historical log handler independently PASSes (handler 4.305s, race, scoped PG):
app/scope authorization, source labels, query bounds, visible diagnostic loss,
and separation from exhausted evidence reserve. The test directly invokes the
handler with a populated spool; runtime job discovery and real routed HTTP
collection are not established by it. Fleet reads remain node-local unless
cross-replica routing/shared retrieval is implemented and qualified.

Initial collector tests independently PASS (logcollect 4.049s, race): labelled
streams, restart deduplication/gaps, bounded segment storage with persisted loss
counters, torn-tail recovery, exclusive spool opening, and Nomad client against
fake HTTP. Not real Nomad qualification. Review overall metadata/directory
growth as allocations churn (segment quota alone is insufficient), terminal
stream error-vs-EOF handling, and follower fairness beyond MaxFollowers.

Stream review corrections independently PASS (nomad 1.883s, race): both
ordinary departure and cancellation of a non-reading client terminate upstream
requests. The fixture now lists the wrong group first, proving selection uses
the allocation's own TaskGroup; initial allocation queries carry context.
This closes those scoped review findings, not multi-allocation collection or
real Nomad-agent qualification.

Payload inventory review: the inventory test checks every registry table is
classified and listed bulky columns exist; it does not detect new unlisted
payload columns. Keep that claim narrow or add explicit column classification.
An archive index with unbounded event_ids and catalog revision lineage still
grow with history despite being labelled current-state; include their byte
budgets/compaction contracts in sustained-growth qualification.

Reserve HTTP slice independently PASSes (handler 2.578s, race, scoped PG).
It verifies refusal/auditing at a pending-count limit, restoration after
archival, local headroom exhaustion and read/revocation exemptions. The test
explicitly clears a two-second process-local cache before each request, so it
does NOT establish atomic admission or cross-replica capacity reservation.
Audit retry behavior too: an already-accepted idempotent replay must not be
mistaken for new evidence admission when reserve is exhausted.

Evidence reserve in-flight source review: pending saga-bundle count/age is a
backlog health signal, not by itself a bound on total retained bytes or already
accepted operations. Integrate admission atomically with capacity reservation
and include queued/running work plus non-saga bulky evidence. Test many concurrent
admissions when no operation has terminalized yet, and a single oversized saga.
Do not label a pending-count threshold as full bounded control-store growth.
Use authoritative database time for durable age decisions across replicas.

Recovery CLI source review: `archive-verify` opens the same object adapter
as the writer, which always performs conditional probe PUTs. Verification
should work with read-only archive credentials and perform no remote writes;
split read-only opening from writer capability qualification. The emulator
currently identifies the credential as archive-reader but does not prove a
Get/List-only policy. Test an endpoint that rejects every PUT and counts attempts.
Likewise, make the trust distinction explicit for reindex without recovered
signing keys; checksum-only verification is not authenticated recovery evidence.

Independent CLI race run: local archive verify/reindex test PASS (5.57s);
object-profile test FAIL (whole package 8.314s) with a DATA RACE between
`emulator.go:190` reading CorruptReads and `archive_e2e_test.go:197` writing
it. Synchronize emulator fault configuration (including all similar booleans
and quota settings) and rerun before claiming this integration race-clean.

Object integration checkpoint: root independently PASSed
`TestEvidenceArchiveLifecycleOverObjectAdapter` (3.129s, race, no skips).
Scoped PostgreSQL plus S3 emulator covers outage preservation, publication,
verification/pruning, full saga history, archive-only index rebuild and tamper
refusal. Main startup's passive branch returns before archive configuration;
active archive opening now has a 30-second deadline. Real service qualification
and complete retained-payload coverage remain separate gates.

Fleet object adapter initial tests independently PASS (archive 2.211s, race):
conditional immutable writes, duplicate/conflict handling, bounded reads,
corruption, outage/quota errors and refusing a non-conditional store. This uses
the local S3 emulator, not DigitalOcean Spaces or real credential policy.
Source uses a persistent probe key and conditional writes; bounded retry counts
are not wall-clock deadlines. Review pagination/list memory and caller deadlines
as integration proceeds; startup performs a probe write and must document that.

Local archive hardening independently PASSes (archive 2.051s, race), including
two separately opened instances contending for a 100-byte quota with 40
concurrent 30-byte writes: exactly three succeed. Source adds whole-archive
flock and parent-directory fsync. This is not a multi-process crash test;
blocking mutex/flock acquisition currently does not observe context cancellation.

Further correction checkpoint: full retention package independently PASSes
(9.030s, race, scoped pgtest servers, verbose no skips), including root's
hold-during-verification regression, running-old-reader retirement, effect
creation during verification and signed-acceptance operation binding. Pruning
now verifies externally before a short locking transaction and rechecks holds.
This closes the reproduced UPDATE race; it does not establish all phantom
insertion/new-session races or production reader identification assumptions.

Concurrent hold is now reproduced, not only a source concern:
`TestReviewPruneHonorsHoldCreatedDuringVerification` FAILS (0.932s, real PG,
race). Its verification callback commits manualRecoveryRequired on the owning
operation using another connection; pruning then deletes both events anyway.
Preserve the test and coordinate hold writers with deletion transactionally.
The test permits serialization that prevents the hold committing mid-prune;
it forbids deleting after that hold has already committed. Cover effect and
deployment holds as well, including phantom inserts, not only this UPDATE.

Database-guard correction independently verified: renamed replacement,
mixed known/ambiguous baseline conflict, queued/failed writer-free history,
addition-versus-replacement and legacy-to-named baseline tests all PASS
(pipeline 6.634s, real PostgreSQL, race, no skips). History now preserves known
target sets alongside ambiguity and checks definite conflicts first. The
sanitized upgrade-contract probe also PASSes (startup 1.892s). These close
those reproduced failures, not the remaining runtime/retention qualifications.

Redaction correction verified: both root offset-sweep regressions now PASS
(database 1.476s, race), covering raw and percent-encoded passwords at both
head/tail cuts. Source now replaces complete secrets before trimming possible
partial boundary fragments. The concurrent baseline regression rerun still
FAILS (pipeline 2.506s); Claude has begun that correction next.

Compatibility correction source review: migration 6 now raises reader contract
to 2 and pruning checks the persisted floor. This is necessary but not enough:
`configureEvidenceArchive` still returns the hot-only store when archive directory
is unset, even for the new contract-2 binary. After pruning, restarting without
that configuration must fail startup or return explicit unavailable history,
never silently expose incomplete hot-only history. Also prove already-running
contract-1 processes are retired; a startup floor alone cannot evict them.

Independent race rerun during that correction: retention PASS 1.965s and
controlrecovery PASS 4.718s. Startup FAIL 27.246s at
`TestPlatformUpgradeStartupContractProbeUsesSanitizedEnvironment`: probe fixture
still advertises readerVersion/catalogMinimumReaderVersion 1. Synchronize the
upgrade-script contract fixtures and rerun; do not treat this in-flight run as
startup acceptance.

Root ran Nomad stream cancellation and explicit task log-rotation tests with
race detection: both pass. Stream test uses a fake Nomad HTTP server and checks
upstream cancellation after consuming stdout/stderr, then closing the reader
or cancelling context. This is not actual allocation qualification, and does
not test cancellation while the pipe writer is blocked on a non-reading client.
Source review: initial allocation queries still use no supplied context; task
selection still takes the first job task group rather than matching allocation
TaskGroup. Multi-allocation, task/node/stream-labelled collection remains open.

Output capture review: root buffer/high-volume/migration tests pass (capture
4.228s, pipeline 3.196s); maintenance bounded-output and original redaction tests
also pass (worker 3.827s, database 1.745s). Added
`database/TestReviewCaptureRedactionDoesNotCreateNewSecretCuts` to cover a
different boundary: trimming a password-length margin *before* redaction can
cut a formerly complete password and leave a fragment at the newly created
boundary. Preserve this offset-sweep regression and fix both head and tail
boundary handling, including encoded password forms.

New concrete regression: `TestReviewPruneCannotLeaveLegacyReadersAdmitted`
FAILS against real PostgreSQL with race detection (0.686s). A shadow-published
saga is pruned by ModePrune while norn_schema_compatibility still admits
reader contract 1, whose history reads only consult hot data. Preserve the test.
Pruning must refuse until a durable compatible transition, or atomically enforce
an archive-aware reader floor, with active old-reader handling and rollback
qualification. A process-local mode flag is insufficient.

Root independently ran startup archive configuration and handler historical
authorization/corruption tests: PASS (main 2.284s, handler 2.067s, no skips).
These verify the tested HTTP scope/app boundaries, not pruning compatibility.

Root reran the retention lifecycle, outage/crash/hold integration tests and
`TestReviewReadBackBindsWholeArchiveSubject` with real scoped PostgreSQL and
race detection: all PASS (1.860s, no skips). This verifies the local-file saga
archive slice, including supplementary events and archive-index rebuilding;
it does not qualify Fleet object storage, concurrent hold creation, reader-floor
enforcement, or complete bounded control storage.

In the same independent run,
`TestReviewBaselineCannotHideKnownConflictBehindAmbiguousHistory` still FAILS
(pipeline 2.974s): baseline accepts a conflicting target hidden behind ambiguous
history. Database batch acceptance remains open; preserve this regression.

## Outbox/prune source review while integration is in flight

Root `TestReviewReadBackBindsWholeArchiveSubject` FAILS (0.309s, race):
a valid-checksum bundle at the expected key with matching saga ID/sequence
but another app and operation is acknowledged. Bind the complete subject,
expected object key and original operation/acceptance identity in read-back,
orphan adoption and restored-index paths. Signature verification alone must
also bind signed content to this operation, not just prove some bytes were
signed. Preserve retention/archiver_review_test.go.

`ProcessPendingEvidenceIntent` currently loads mutable hot evidence and calls
external publication inside its transaction before storing the cutoff. Crash
after immutable publication but before DB commit leaves an object at the same
sequence key and a pending intent with no frozen payload. Retry must recover
the exact published bytes/cutoff, not rebuild with new SealedAt or late events
and hit permanent immutable conflict. Add a real upload-success/ack-loss test.

`PruneVerifiedEvidence` locks the intent, but its hold queries do not lock
operation/deployment/effect rows. Verify transaction-level coordination with
writers that can create recovery holds during object verification; a row lock
on the archive intent alone does not stabilize those other tables. Reader-floor
compatibility must also be checked in the deletion transaction, not only in
worker configuration. These are source findings, not yet runtime reproductions.

## Reproduced local archive boundary failure

Correction checkpoint: independent archive package race suite PASS (1.702s,
verbose), including root parent-symlink regression, immutable duplicates,
capacity/read bounds, FIFO rejection and bundle tamper checks. Adapter now
uses os.Root confinement. This closes the reproduced outside-root write, not
multi-process capacity serialization or full ancestor-directory crash durability.

Root `archive/store_review_test.go` reproduces parent-symlink traversal:
`TestReviewLocalArchiveRejectsSymlinkedParent` FAILS (0.648s, race). A private
archive root containing `evidence -> another temporary directory` permits
PutImmutable(evidence/item.json) outside its root. O_NOFOLLOW on the leaf alone
does not protect ancestor components. Preserve the test, anchor all operations
to a checked root/directory chain and reject unsafe ancestor traversal for
publication, duplicate verification and reads. Also review capacity locking
across multiple LocalStore instances/processes and fsync of newly created
parent directories, not only the leaf directory.

Independent initial evidence: generic schema migration/compatibility tests pass
with real PostgreSQL and race detection (1.667s); these use synthetic migration
definitions and do not qualify archive behavior. The actual control-schema
legacy adoption/evidence preservation test also passes (1.487s, no skips).
Recovery registry now includes evidence_archive_intents in source; archive
restore and prune compatibility still need end-to-end tests.

- Migration 6 leaves minimum reader 1 and writer 5, relying on shadow mode
  until older readers are gone. Enabling pruning needs enforceable durable
  compatibility evidence, not only an operator flag or process-local setting.
  Preserve archive-aware reads across the rollback window and test an older
  reader/startup against already-pruned state. Once deletion happens, disabling
  pruning alone does not restore compatibility.
- Update recovery inspection/export registries and startup schema pins together
  with the new archive index. Indexes are rebuildable, but recovery must know
  which immutable objects to discover and verify without a historical PG copy.
- Saga-only archive outbox rows are a vertical slice, not full bounded control
  storage. Identify all bulky payload destinations and holds from the retention
  handoff before claiming M2 retention complete.
- A terminal operation does not close all later saga publications. Test late
  events, supplementary bundle sequencing, competing archivers, crash after
  upload/verification and before acknowledgement, and exact-ID prune retry.
- Original signed bytes must remain original bytes. Archive public redacted
  operation projections must not accidentally replace recovery evidence.
- Apply app/tenant authorization before historical lookup and never accept an
  arbitrary filesystem/object key as authority. Corrupt or missing archived
  pages must fail visibly rather than silently returning partial history.
- Archive failure preserves evidence. Bound diagnostic spools separately;
  audited mutation admission must react to exhausted evidence reserve without
  treating diagnostic loss as permission to discard authoritative evidence.

Independent verification will distinguish local files, emulated object APIs,
real clients and actual Fleet runtime. No live retention deletion is authorized.
