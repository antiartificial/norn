# MySQL private restore runner: supervision slice

The private `MySQLRestoreRunner` consumes an already accepted, prepared
`database.mysql-restore` operation. It commits the one-way `executing` intent
before starting `mysql`, passes the already verified SQL artifact descriptor as
stdin, creates TLS and password files only in the session's owner-only
directory, and terminalizes the intent with its operation receipt. Failure
after the one-way boundary records `needs-inspection`; a worker crash leaves
`executing` and cannot be retried as SQL.

The client executable is checksum-verified through an opened descriptor before
execution and its inode is compared again afterward. The process still executes
the configured path because macOS clients may load libraries relative to that
installed path. This leaves a pathname-resolution race that this slice cannot
eliminate portably. A detected path or inode change is an uncertain restore and
is retained for inspection. Deploy the tool only from a protected installation
root; do not treat the checksum check as a complete executable-attestation
system.

The runner has no HTTP route or enabled public capability. A v2 artifact binds
source-derived SHA-256 schema and ordered-row fingerprints into its signed
intent. The runner independently inspects the restored target before recording
success. It renews its operation claim throughout import and cancels the SQL
client on known renewal loss. A disposable PostgreSQL 17.7 plus MySQL 8.4 run
passed with a 120 ms lease and a 350 ms delayed client; a changed-row target
check was rejected. A second disposable run stole the claim during a delayed
client: cancellation returned promptly, no success receipt was written, the
intent became `needs-inspection`, and successor replay was rejected. The
private runner bounds pipe draining after cancellation so a wrapper child
cannot delay that containment indefinitely. This is still a private
qualification, not a complete restore lane.

Expired executing intents now atomically become `needs-inspection` with a
failed, manual-recovery operation. A read-scoped inspection endpoint returns
verified signed acceptance, catalog, target, and artifact identities without
the private artifact path or profile selector. It cannot retry or acknowledge
an ambiguous restore. A `prepared` intent whose claim expired before SQL is
atomically failed and its target reservation released; it cannot be confused
with an executing import. The disposable PostgreSQL recovery test passed both
cases.

The private request now signs a source-quiescence evidence reference and
persists a maintenance fence from preparation through execution. Catalog
activation refuses while any such fence is held. Migration 27 retains the
fence even after a successful import because runtime authentication remains
locked pending a separately authorized resume. An expired prepared intent
releases it only if no account-lock intent exists; every uncertain lock or SQL
outcome retains it. The signed evidence reference
records what an operator asserted; it does not independently prove quiescence.

A disposable PostgreSQL/MySQL rehearsal killed the separate restore worker and
its mysql child with OS SIGKILL after the target had received the first table.
Fresh control-store recovery retained `needs-inspection`, a failed one-attempt
operation, signed redacted inspection, and no automatic SQL replay. This proves
the crash classification at that boundary, not safe application write isolation
or a complete rollback procedure.

Migration 26 adds a private runtime-launch reservation gate. Reservation and
restore-fence acquisition serialize on the catalog lock and exclude by physical
MySQL service generation and database, including aliases through another
binding. Both the artifact source and destination are checked. A launched or
ambiguous reservation remains blocking until an explicit stop receipt; a
reserved launch needs a recorded no-start proof before release. Disposable
PostgreSQL concurrency tests passed both acquisition orders. A private,
opt-in WordPress verified-TLS cold-start path reserves before Nomad submit,
requires an absent job and zero active allocations, and binds the observed
allocation to the returned evaluation ID. It does not cover rolling deploy,
rollback, cron, function, canary, host assurance, or direct Nomad starts.

A separate private MySQL 8.4 primitive can lock one dedicated runtime account,
terminate its existing sessions, verify two zero-session observations, and
unlock only through an explicit call. A disposable MySQL 8.4 test passed with
an existing session, rejected new authentication, and a later explicit unlock.
The fence authenticates as a distinct, exact MySQL account and uses the target
TLS policy. It currently needs `SELECT` on `mysql.user` to prove username
uniqueness, which needs narrower provider-specific provisioning before release.
The immutable catalog and accepted restore request now bind distinct runtime,
restore, and fence credentials. The restore client verifies its exact account
and uses the restore credential for import and post-import checks, even while
runtime authentication is locked. Migration 27 records `lock-intended` before
the runner alters the exact runtime account and `verified-lock` after session
drain. Begin requires that verified checkpoint. A lost claim or uncertain lock
cannot auto-unlock or begin SQL. This covers the destination account; the
artifact source has a distinct writer identity and still lacks a proved lock.

Migration 28 adds a global, owner-and-epoch-bound runtime mutation fence. The
private restore runner acquires it before account locking and leaves it held
after import or uncertainty. It serializes acquisition with operation claims
and holds queued deploy, rollback, restart, scale, canary, cron, and function
invocation operations until exact release. Acquisition rejects an already
running claimed operation of these kinds. Host assurance, direct Nomad
actions, and existing allocations are not yet gated, so this is an admission
boundary rather than complete
write isolation.

## Still required before a usable restore lane

A private, read-only completed-restore recovery assessment now checks the
verified signed acceptance against the completed intent, destination account
lock checkpoint, source receipt identity, exact claim generation, active
catalog revision, and still-held global fence. A disposable PostgreSQL test
rejects an unfinished restore and a replaced fence. This assessment does not
inspect the live MySQL account or target contents, authorize an unlock, or
release the fence. It is one prerequisite for a future separately signed,
operator-observed resume.

The private live assessment now also observes the destination's exact MySQL
runtime account using the catalog-bound fence credential, confirms it remains
locked with no sessions in two read-only observations, and recomputes the
restored target schema and data fingerprints using the restore credential. It
rechecks the control-plane assessment afterward. A disposable MySQL 8.0 test
exercised locked and unlocked account states; the PostgreSQL/MySQL restore
rehearsal accepted the completed target and rejected a changed row. These
observations are not atomic with a later resume. Source-account state, direct
Nomad starts, and external writers still need independent qualification.

A private recovery admission now signs a one-attempt operator operation derived
from the completed restore's acceptance digest, catalog revision, exact source
receipt, target identity, and held fence epoch/owner. Exact request-key replay
returns the original signed operation; a new admission fails if the fence has
been replaced. A disposable PostgreSQL test passed both cases. This operation
currently has no executor, external-effect checkpoint, account unlock, or fence-release
path, so accepting it does not resume application writes.

Migration 36 adds a private recovery intent. A claimed signed recovery can now
commit an exact operation, restore, catalog, fence epoch, owner, and claim
generation before any external effect. Preparation holds the catalog gate and
fence row, permits only an identical claim retry, and rejects a successor
claim. Disposable PostgreSQL migration and recovery tests passed. Execution
checkpoints, execution-time source reobservation, unlock, and fence release remain open.

Recovery now has a read-only source reobservation path. It verifies the signed
source staging receipt and acceptance against the durable stop and account-lock
checkpoints, then asks Nomad for the exact post-CAS stopped revision and all
terminal signed allocations. Nomad unit cases reject a restarted or changed
job, a live or missing signed allocation, and changed deployment provenance;
the PostgreSQL recovery test verifies the signed job identity is passed to the
observer. This stopped-source-only assessment is not yet wired into an unlock
executor and does not inspect the live source MySQL account.

A further private live assessment resolves the signed source binding from the
active catalog, checks its exact MySQL runtime account remains locked and
session-free through the fence credential, and reobserves the stopped Nomad
job afterward. The disposable PostgreSQL/MySQL restore rehearsal accepted the
locked account and rejected it after an explicit test unlock. These are
read-only observations and still need to be repeated under a durable unlock
effect intent; the test Nomad observer validates plumbing, while the separate
Nomad tests exercise the concrete client behavior.

Migration 37 adds a one-way destination `target-unlock-intended` checkpoint.
The private recovery runner renews its signed claim, reobserves the stopped
and locked source plus the locked destination and target fingerprints, commits
the checkpoint, then unlocks the exact destination MySQL account. A separate
read-only proof requires that account to be unlocked and session-free, the
target fingerprints unchanged, the source still stopped and locked, and the
original claim/fence still held before recording `target-unlock-proved`. The
disposable PostgreSQL/MySQL rehearsal passed the effect and rejected replay.
Any uncertain effect keeps the intent and global fence held. The source account
remains locked. This still has no fence-release/resume step and is private.

- Qualify bounded memory/disk behavior on representative large data.
- Wire the launch and mutation gates into every application write and resume
  path, and add signed source-account quiescence before staging the dump.
  Prove this on the actual managed WordPress/MySQL runtime, including direct
  Nomad starts, periodic children, restart policy, and host assurance.
- Bind source quiescence and retained artifact storage to acceptance; the
  current artifact path remains host-local and ephemeral.
- Define evidence-bound operator reconciliation after inspection. Current
  signed identity and target fingerprints cannot prove whether a partial SQL
  prefix was applied, so no acknowledgement mutation is exposed.

These gaps keep MySQL restore non-deployable and the public capability closed.
