# M1 supervised application migration contract

Status: implementation boundary, 2026-09-27. This is not M1 sign-off.

The [execution contract](execution-milestones.md) defines M1 as shared
control semantics and a working PostgreSQL adapter. MySQL migration is a
separate engine capability: it remains disabled in the resolver and is not
an M1 exit criterion. Initial v3 still requires the declared MySQL binding
and later selected-engine backup/restore and cutover qualification under M2
and M6; PostgreSQL evidence cannot be used to advertise MySQL migration.

The implementation now has an authenticated, secret-free migration intent,
durable supervisor preparation and launch, a Linux private-pipe/cgroup
backend, and signed command status observation. The helper discards command
output, owns supported connection files and scrubs them before terminal
status. A dead-helper cleanup preserves the signed running state and runs only
after the backend proves the cgroup empty. The generic `build.test` launcher,
query and verifier reject migration evidence. The database adapter derives the
target digest from the full accepted `TargetIdentity`, including service and
binding generations. Deployment `migrate` and standalone `app.migrate` now
route a reviewed PostgreSQL migration through this effect when the operator
sets `NORN_MIGRATION_EXECUTION=supervised`; the setting is off by default.
A resolved PostgreSQL session produces runner-owned service,
passfile, URL and TLS file material for private launch, including URL path
remapping. A migration-specific verifier now requires contained zero exit and
an independent original-target postcondition result; nonzero, timed-out and
ambiguous command states remain unresolved. A concrete PostgreSQL/MySQL
checker now binds one reviewed scalar SQL assertion to the accepted target
digest, reconnects to that target, verifies database and role in a read-only
transaction, and requires exactly one non-null result matching the expected
value. PostgreSQL and MySQL paths passed disposable database tests. The v2
InfraSpec can now declare `migrationPostcondition.query` and
`migrationPostcondition.expectedValue`; validation requires a migration
command and selected named database, and the pipeline derives its digest with
the accepted engine. The effect executor now has a migration-specific private
launch, observation, revocation and empty-result adapter. A durably registered
but never launched migration can be tombstoned and verified without reading
the database; a launched stopped migration remains unresolved. The pipeline
requires a claim, a pinned source checkpoint and an InfraSpec in the prepared
checkout that agrees with the accepted command, database selection and
postcondition. An expired standalone migration with both a source checkpoint
and durable migration effect can be requeued to observe that original effect;
legacy or unrecorded migrations still require manual recovery. Protected
runtime and process-crash qualification remain required before activation.

The migration command timeout now remains armed after the shell leader exits
until its dedicated Linux cgroup is empty. If a background descendant outlives
the timeout, the runner kills the command cgroup and records a timed-out
failure rather than allowing a later successful observation. An opt-in real
Linux cgroup test passed both a descendant that drained before timeout and a
background `sleep` that required timeout termination in a disposable privileged
Alpine container on 2026-09-27. The general API suite and protected Mini/Fleet
runtime qualification remain separate gates.

Observation now also checks the helper's direct cgroup membership when a
signed migration status remains running and the execution cgroup is populated.
If the helper is gone, it kills the dedicated command child cgroup. The later
empty-cgroup observation scrubs private connection material and stays unknown
for manual review; it never converts the interrupted command into success or
repeat-safe failure. The opt-in real Linux cgroup test now uses disposable
PostgreSQL 16: it commits row 1, kills the helper while the command sleeps,
then proves the cgroup emptied, private material was scrubbed, the later row 2
write never ran, and even a satisfied postcondition cannot approve the unknown
execution. `v2/scripts/test-snapshot-cgroup-linux.sh` runs this alongside the
snapshot and descendant-timeout tests with host-compiled binaries and tmpfs
PostgreSQL; it passed on 2026-09-27 without installing packages in Docker.
The protected-runtime crash and database-write reconciliation rehearsal is
still required for M1 qualification.

The disposable Linux harness now also kills the API test process after a
PostgreSQL migration write commits while its command cgroup remains active.
A newly started process reopens the supervisor journal, receives the same
runtime identity without launching a second command, observes contained zero
exit, and finds exactly one committed row. This qualifies the local runner
process boundary; it does not exercise the full deployment claim/store path
or replace the protected Mini/Fleet rehearsal.

A PostgreSQL-backed integration fixture now crosses signed deployment
acceptance, named database snapshot publication, source/build checkpoints,
an expired migration-step claim, the original reserved migration effect,
and a replacement claim. After changing the application database, the
successor reuses both target-bound pre-migration dump/sidecar pairs byte for
byte and reserves the original effect; deleting one original dump makes the
snapshot stage fail closed. Both claims run the actual deployment pipeline
through its snapshot stage; the first pass records the source/build
checkpoints and migration-step start. The focused fixture and full pipeline
package passed locally against disposable PostgreSQL. The test seeds the
original migration effect and stops before launching the migration command,
so the full deployment process-crash and protected-runtime proof remains open.

The 2026-09-27 full-pipeline Linux harness probe found an execution constraint:
the existing cgroup test runs as root, while `newNamedFixture` starts its own
PostgreSQL servers through `pgtest` and therefore needs a non-root test
process. In a disposable privileged `postgres:16` container with a private
cgroup namespace, giving `postgres` ownership of a new cgroup subtree and its
`cgroup.procs` file still produced `Permission denied` when that user tried to
join the child cgroup. Do not treat the current supervisor-only process-crash
test as full-pipeline proof. The pipeline fixture therefore starts its
PostgreSQL servers outside the test process as `postgres`, then runs the
pipeline/API crash case as root against those disposable servers and the real
cgroup backend.
That fixture is now available through
`v2/scripts/test-pipeline-cgroup-linux.sh`: it starts three socket-only
PostgreSQL servers as `postgres` in container tmpfs, then runs the pipeline
tests as root. The joined replay test still seeds its migration effect. A
local rerun at code head `f0350929` passed all six harness cases, including
remote snapshot readback and the changed-snapshot refusal. The v3 CI workflow
now has a dedicated opt-in harness job; the ordinary `go test ./...` job skips
these cgroup cases. At exact head `d95691b9`, the
[hosted M1 job](https://github.com/antiartificial/norn/actions/runs/36416503304/job/108908803963)
ran all six pipeline cases and the supervisor containment, descendant-timeout,
helper-death and API-exit cases with no skips. This is disposable Linux
evidence, not protected Mini runtime qualification. A second test now
executes the accepted deployment through real source/build
checkpoints, named snapshots, a PostgreSQL migration command and the real
cgroup backend. The command commits one row and remains in its command cgroup
when the first API test process exits. An expired claim is requeued; the
successor observes the same runtime instance, records one completed effect,
finds exactly one row, and sees the command cgroup empty. A third isolated
Linux scenario exits the API while the
original PostgreSQL transaction is still open: the row is not yet visible,
`pg_stat_activity` shows the original writer active, and the successor waits
for that contained command to commit before completing the same effect. It
also records one row, one completed migration step and no surviving writer.
The Linux harness now also exits a second API process while it is observing
the still-running original command. The guarded recovery path grants this
deployment one additional attempt (up to three total) only while the original
migration step and required predecessor/effect evidence are present. A third
claim completes that same effect and records one database write; the four
Linux harness cases pass. A PostgreSQL store negative control removes the
original effect after the second claim and confirms recovery fails to manual
review instead of using the extra attempt to launch new work.
An additional PostgreSQL store test covers the boundary before effect
reservation: even with source/build checkpoints and a completed snapshot, an
expired claim whose `migrate` step is running but has no effect remains failed
for manual recovery and cannot be claimed by a successor. That test passed
against a disposable PostgreSQL server; it does not assert an actual command
was launched.
Other PostgreSQL crash windows and protected-runtime qualification remain
open for M1. MySQL migration is separately unqualified under the later
database capability gates; this local test signs neither boundary.

## MySQL supervised-launch boundary

Source review at head `95b76873` confirms that the SQL postcondition checker
supports MySQL, but `runSupervisedMigration` rejects a MySQL target before
effect reservation. `Session.WithMigrationLaunchMaterial` is deliberately
PostgreSQL-only: it builds a libpq service file, passfile, connection URL, and
remapped TLS files for the private runner. A MySQL session instead exposes
structured host/user/password/database values and separately verified TLS
material. Dropping the engine check would neither supply the runner's private
MySQL connection format nor prove credential cleanup after API death.

The MySQL implementation needs an accepted-target-bound private material
adapter for both disabled and verified TLS, explicit declared migration
environment names, runner-owned files that survive the API process, a
MySQL-specific command and postcondition fixture, and the same crash/replay
negative cases as PostgreSQL. Keep the current fail-closed engine check until
that path is implemented and qualified. The PostgreSQL crash evidence does
not establish MySQL migration safety.

## Current behavior and risk

The SQL migration verifier now rereads the active catalog before and after
its original-target postcondition query. A changed catalog revision or target
leaves the effect unresolved even if the command exited zero and the SQL
assertion matched. Disposable PostgreSQL and MySQL checker tests and the full
database and pipeline package suites passed locally. This closes a stale
catalog approval path; it does not provide the missing MySQL private launch
or protected process-crash qualification.

Migrations without a reviewed postcondition still execute with host `sh -c`
when supervised migration mode is off. When the operator enables supervised
mode, an unreviewed migration fails before launching a command.
The deployment engine records `migrate` as a mutable step before the command.
An expired deployment migration claim now requeues only with a recorded
source, content-addressed build, completed snapshot step and original
migration effect, with no later mutable step. Its replacement claim must
reuse the verified original target-bound snapshot rather than create a new
one. Missing evidence still fails to manual recovery. This branch has local
store, snapshot, and disposable Linux process-crash tests, but no
protected-runtime process-crash qualification yet.
An unrecorded standalone migration likewise fails to manual recovery.
Neither path may infer a repeat-safe failure from a nonzero command exit.

The effect reservation itself is stable across claims when source, command,
target and postcondition identity are unchanged: `Reserve` returns the existing
record, and the supervisor execution ID is derived from the operation and
input digest. A replacement claim restarts the ordered deploy steps,
including the pre-migration snapshot. The guarded replay path now refuses an
absent or invalid original snapshot; it does not silently create a new dump
of a post-migration database. When export is declared, the snapshot stage
re-enters the claimed create-only publication path, rereads the remote dump
and manifest, and verifies both against the pinned source before advancing.
The separate export-crash and migration-crash fixtures exercise those paths.
The joined disposable Linux/PostgreSQL harness covers their combined boundary for
two named databases with remote export enabled. After the migration commits
and the first API exits, successor replay keeps the same four remote dump and
manifest objects byte for byte and completes the original effect with one
database write. A negative run changes one remote dump before replay; the
successor fails at the snapshot stage and does not complete the migration
step. All six harness cases passed locally. This is not protected Mini/Fleet
runtime qualification or a MySQL recovery proof.

The generic effect runner is currently limited to `build.test`. Its descriptor
omits environment values, but its verifier interprets a contained command's
exit as a final result. A schema migration can partially commit before a
nonzero exit, so that verdict alone is insufficient to approve a successor.
The snapshot private launch protocol is specific to `pg_dump` and its artifact.
Neither protocol should be silently reused for application migrations.

## Required durable identity

Reserve one migration effect before launching a command. Bind the record to:

- authority, app, deployment/operation, claim generation and supervisor
  execution ID;
- pinned source digest, migration command digest and declared working tree;
- accepted database binding ID, target identity and binding generation;
- migration epoch or explicit operator revision, so a legitimate new migration
  cannot be confused with recovery of the old one.

The durable descriptor contains only these nonsecret identifiers, hashes and
a MAC. Database URLs, service files, passwords, command output and private
connection material never enter the control database or API logs. Credentials
travel through a private launch channel to an owner-only runner context. A
replacement claim may query the original execution without needing the old
credential and must never launch a second command under the same identity.
The runner must own any URL, service, passfile and TLS files for the command's
lifetime; it cannot depend on API-session paths that disappear when the API
process or claim exits. Scrub those files after confirmed command containment.

## Reconciliation rule

The runner must establish a stable execution identity before command launch,
retain a launch intent across process death, and report authenticated terminal
evidence only after its command and all descendants are contained or stopped.
On Linux this requires an execution cgroup; process-group exit alone does not
prove descendant stop. Missing, corrupt or conflicting supervisor history is
unknown state and holds the app migration gate.

An authenticated zero exit can complete the effect only after an independent
postcondition check on the original database target. The postcondition must
be migration specific: expected schema/version and any declared data
invariants, not simply a process exit code. Legacy arbitrary shell migrations
with no declared, checkable postcondition stay outside automatic successor
admission; their owner must review the original target. A nonzero exit, timeout,
containment failure, lost response or unknown database state requires manual
review unless the adapter can prove no write began. A successor must not run
until that review records a new authorized migration epoch or a verified
repeat-safe outcome. Do not interpret a partially committed migration as a
repeat-safe failure.

## Implementation slices

1. Add a private migration descriptor and runner protocol, separate from
   `build.test` and snapshot. Authenticate the accepted target and command
   identity; add secret-canary tests over every durable record, status and log.
2. Add a migration-specific verifier that combines contained execution
   evidence with an original-target postcondition. Keep failure and ambiguous
   outcomes pending/manual; add a reviewed operator resolution path.
3. Route deployment `migrate` and standalone `app.migrate` through one
   reservation and recovery path. A replacement claim queries the old
   execution; it does not create a new effect from a fresh checkout.
4. Qualify process crash before launch, during a write, after commit before
   acknowledgement, and during successor recovery. Prove one migration owner,
   no duplicate writes, no surviving writer after accepted termination, and
   no credential in persisted evidence. Run against disposable PostgreSQL and
   the supported MySQL binding; repeat on the protected Mini upgrade fixture.

Until these checks pass, keep interrupted migrations in manual recovery and
leave the M1 gate open. A green general API suite or a completed Nomad job is
not migration reconciliation evidence.
