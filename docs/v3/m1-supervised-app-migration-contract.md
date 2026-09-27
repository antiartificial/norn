# M1 supervised application migration contract

Status: implementation boundary, 2026-09-27. This is not M1 sign-off.

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

## Current behavior and risk

Migrations without a reviewed postcondition still execute with host `sh -c`
when supervised migration mode is off. When the operator enables supervised
mode, an unreviewed migration fails before launching a command.
The deployment engine records `migrate` as a mutable step before the command.
An expired deployment migration claim still fails to manual recovery even if
its effect exists; replay of earlier deployment steps has not been qualified.
An unrecorded standalone migration likewise fails to manual recovery.
Neither path may infer a repeat-safe failure from a nonzero command exit.

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
