# M1 function invocation: private request and durable Nomad effects

Status: PostgreSQL and etcd private-material acceptance foundations exist.
The normal `/invoke` route now selects signed PostgreSQL admission and a
claimed worker only when the full private runtime is configured; otherwise it
returns 503. The legacy inline HTTP-to-Nomad handler is no longer routed.
Production can configure the same complete PostgreSQL bundle, but release
qualification and etcd invocation parity remain open. Migrations 18–22
implement the private envelope, effect attempts, cleanup and archive contract.
A private Mini copy has only been rehearsed through migration 19; mixed-version
and rollback gates remain before deployment.

## Required contract

`POST /apps/{id}/invoke` must retain the current request fields (`process`,
`body`, `method`, `path`) and the function's `NORN_REQUEST_*` environment
semantics. The body can contain private data. Neither plaintext body/method/path
nor resolved app/database secrets may enter an operation payload, acceptance
envelope, effect reservation, audit event, log, or archive object. Nomad's job
description must contain only variable references, not request or secret
values. Enforce the existing control JSON size limit before encryption.

Introduce a **private invocation aggregate** implemented by both PostgreSQL
and etcd. Its `AcceptInvocation` transaction must create the signed
`app.function-invoke` operation, request identity, audit/outbox intent, and an
encrypted request-material record atomically. The signed payload binds app,
process, immutable InfraSpec digest, image digest/reference, database delivery
target and revision, deterministic operation-derived Nomad job ID, private
record ID, ciphertext digest, and encryption key ID. The record holds the
request fields and no app secrets. An idempotency replay resolves the original
operation and compares the submitted plaintext request to the decrypted
original in constant time; it never stores a plaintext comparison digest in
the public operation. A mismatch is an acceptance conflict. A failed private
record write must leave no accepted operation, and a failed signature must
leave no private record.

Use a dedicated versioned encryption key ring, distinct from the audit
signing keys and SOPS app-secret files. Generate a fresh data key and nonce per
invocation; encrypt the record with authenticated encryption and bind the
control authority, operation ID, app, process, and record schema as associated
data. Wrap the data key under the active key-encryption key and retain its key
ID with the record. Rotation makes new records use the new key while pending
old records remain decryptable. A backup/restore manifest must enumerate the
required key IDs; startup and restore fail closed if any key needed by an
unresolved invocation is missing. Rewrap old records only through a verified,
audited migration. Pruning a key requires proof that no retained operation,
replay identity, recovery record, or archive reference needs it.

## Claimed worker flow

1. `OperationWorker` claims `app.function-invoke`. It checks the operation
   claim and signed acceptance, loads the pinned spec/deployment identity, and
   decrypts the private record only in worker memory. It resolves current app
   secrets and validates the pinned database target and delivery revision. A
   changed target or spec fails before any Nomad mutation; credential-only
   rotation can supply fresh private values for the same target identity.
2. Reserve `app.function-invoke.private-variable` under the live claim and a
   resource lock for the operation-derived Nomad variable path. The reservation
   contains only path, revision, ciphertext digest, and operation identity.
   Create the variable with a create-only/CAS contract and an owner marker.
   On a lost response, read that exact path and compare its owner and private
   content in worker memory. A mismatched existing variable stops for operator
   review. The variable carries request fields and any per-invocation database
   material needed by the allocation; no such bytes enter the effect record.
3. Reserve `app.function-invoke.nomad-job` under the same claim and an app/job
   resource lock. Submit a pinned, deterministic batch job with a create-only
   Nomad modify-index guard. Its task uses a private `0400` allocation template
   sourced from the operation variable to populate `NORN_REQUEST_*`; the job
   JSON contains no private values. Record the job version/modify index,
   evaluation, allocation IDs, and an exact nonsecret job-spec digest as
   recovery evidence. If submit returns ambiguously, query the deterministic
   job and reconcile only an exact digest/owner match. If the job cannot be
   found or its history is incomplete, leave the effect unresolved; never
   blindly resubmit a one-shot function.
4. Poll the exact allocation/task to a terminal state without purging the
   job. Verify exit status and bounded output against the reserved job
   identity. Under the current claim, atomically write the function-execution
   projection, terminal operation receipt, and cleanup outbox intent. An
   expired claim cannot publish a result. A successor claim first recovers
   either reserved effect, then completes the same operation without launching
   another job.
5. A separately durable cleanup consumer removes the private variable only
   after terminal receipt and required evidence are preserved. Deletion checks
   the owner marker and revision. Purge the Nomad job only after its terminal
   evidence is archived; cleanup retries independently of the user operation.
   Unresolved effects retain their private record and variable. Retention must
   count these bytes in the M2 reserve and prevent key/record pruning.

Admission fails with `503` if the aggregate, key ring, effect store, Nomad
variable ACL, or worker capability is absent. The handler must not fall back
to the current inline submit. No new invocation should be acknowledged until
all mandatory private material and signed intent are committed.

## Acceptance evidence

- Shared PostgreSQL/etcd aggregate tests: atomic signed acceptance with
  encrypted material; same-key replay and mismatch; no plaintext in control
  rows, signed/archive bytes, errors, or job JSON; key rotation, missing-key
  restore rejection, and rewrap without changing request identity.
- Disposable Nomad allocation test: exact `NORN_REQUEST_*` bytes including
  multiline and empty values, private template mode, database revision, and
  absence of private values from job inspect/output.
- Two API/worker replicas and duplicate HTTP requests: one accepted operation,
  one variable owner, one job/evaluation launch, one terminal receipt.
- Kill or disconnect at acceptance commit, variable CAS, job submit, allocation
  completion, receipt commit, and cleanup. Lost Nomad responses must reconcile
  exact remote identity or remain unresolved with zero automatic resubmits.
- Old inline `func_executions` rows and jobs get an explicit compatibility
  disposition. A v3 worker must not infer a signed intent for them or delete
  their variables. Test mixed-version rollout and rollback against a private
  Mini copy before enabling the new route.

This work is an M1/M2 dependency for a release that keeps function invocation
available. A passing handler unit test or a successful batch-job submission
alone does not close it.

## Implementation order and hardening gates

Keep each transition independently testable before adding Nomad I/O:

1. Pure identity and reconciliation decisions for the private variable, job,
   and terminal receipt. Each decision takes durable effect stage plus an
   observed remote state and returns one explicit action. An attempted write
   followed by absence or ambiguous history remains unresolved.
2. Thin adapters for exact Nomad variable and job reads, create-only writes,
   and allocation observation. Persist the write-attempt stage before each
   remote call. Exercise the adapters against disposable Nomad, including
   lost responses and duplicate workers.
3. Connect the claimed worker, signed acceptance, private record, effect
   reservations, and terminal projection. Admit the HTTP route only after the
   startup capability check proves that entire path is available.

The release gate then includes key-ring backup/restore and pruning rules,
private-variable cleanup after archived evidence, bounded log/output handling,
two-replica crash tests, and Mini mixed-version/rollback rehearsal. These
remain required qualifications; a pure decision test does not imply that the
external-effect path is safe to enable.

## Current foundation evidence

On 2026-09-25, `go test ./store -run '^TestPrivateInvocation' -count=1 -v`
passed with `NORN_TEST_DATABASE_URL` targeting a disposable PostgreSQL 17.7
container. It covered atomic acceptance/rollback, two-connection same-key
acceptance race, private material replay/mismatch, no plaintext in the tested
control rows, key absence, rotation, AAD and ciphertext tampering.

The etcd adapter uses one compare-and-put transaction for signed acceptance,
operation, kind index, and encrypted private record. Focused tests against a
disposable real etcd member covered same-key replay, mismatches, no plaintext
in stored records, and rejected-transaction cleanup; the etcd race suite also
passed. The PostgreSQL backend now connects this seam only when
`NORN_PRIVATE_INVOCATION_ENABLED=true` and
`NORN_FUNCTION_V3_PREVIEW_ENABLED=true`. The latter environment name is
retained for compatibility, but production uses the same complete PostgreSQL
runtime bundle. Startup preflights required keys and fails before serving if
the pipeline, Nomad client, durable stores, or claimed-worker dependencies are
incomplete. Etcd routing, real Nomad crash, and restore qualification remain
open.

The private variable path is `nomad/jobs/<function-job-id>/invoke`, which
matches Nomad's implicit task-group variable read scope and stays separate
from the job-level database delivery variable. The function variable adapter
has HTTP-level tests for exact reads,
create-only Nomad CAS, private-byte round trips, and redacted errors. Its
writer now uses padded base64 for Nomad's strict template decoder; the reader
also accepts older unpadded values for recovery. The worker rejects an
oversized private variable before recording a durable effect attempt.
PostgreSQL and etcd
effect-attempt stores record immutable public targets under live claims and
authorize only the first caller to cross each remote call boundary. Real
backend races, stale claims, and successor claims passed. Narrow variable and
job worker steps use those attempt stages to reconcile lost responses without
another remote create. Their fake concurrent callers issued one create each.

The closed function-job dialect derives its digest from the validated public
Nomad job. A disposable Nomad 2.0.7 server passed zero-index create, duplicate
conflict, digest read-back, and found-job observation with exact version,
evaluations, allocations, and a stable second read. Dialect v4 uses a private
`env=true` template to decode padded base64 JSON and inject
`NORN_REQUEST_BODY`, `NORN_REQUEST_METHOD`, `NORN_REQUEST_PATH`, and a validated
private environment map with JSON quoting. A pure worker encoder merges app,
secret, and process environment values while rejecting malformed and reserved
keys. A disposable Docker-enabled Nomad 2.0.7 allocation produced the
expected digest for a multiline body and secret, quotes, backslashes, Unicode
path, and empty values; the job JSON contained no private values. The builder
and steps now connect to the claimed operation executor in the PostgreSQL
preview. Named database delivery still needs ACL-enabled allocation proof for
the implicit group path; cleanup remains open. Requalify on the release Nomad version
before enabling submission.

The next pure/read-only slice now projects a terminal result only from one
exact recovered Nomad allocation, job version, evaluation, and task, with an
explicit exit code. Lost or widened lineage remains unresolved. PostgreSQL
atomically inserts the legacy function-history projection and operation
receipt under the live claim, and rejects stale claims and private receipt
metadata. These seams now connect to the claimed worker in the PostgreSQL
preview; etcd terminal projection exists but is not routed. Cleanup remains
incomplete.

For a spec with no runtime database, the public effect identity uses the paired
`databaseTarget=none` and `databaseRevision=none` sentinel. A mixed pair is
rejected before a Nomad effect. The eventual executor must compare that claim
against the pinned spec and reject a database-bearing spec with the sentinel.
Named database invocations need an exact accepted target identity and revision
recheck; a mutable current delivery revision alone is insufficient.

The public database-binding contract now encodes sorted logical target
identities and the promoted delivery revision. Its pure recheck compares the
accepted spec digest, function process, target generations, and delivery
revision before a private copy is attempted. Database values stay out of the
public binding. A composed remote step now orders claim-fenced variable
recovery, one-shot job recovery, and exact terminal observation, deferring
unknown states without resubmitting. Terminal observations include the task's
start time for the execution projection. A disposable etcd 3.5.17 member
passed the new atomic receipt, conflict, private-metadata, and lost-app-lock
tests; the etcd receipt additionally compares the app-lock fence in its
transaction. The shared resolver, dedicated claimed worker, and PostgreSQL
preview route now connect these seams. Production activation, cleanup, and
full external crash qualification remain open.

## Preview composition and remaining release gates

`NORN_FUNCTION_V3_PREVIEW_ENABLED` connects one public-only admission handler
and one dedicated claimed worker to a shared runtime resolver. The compatibility
name does not limit the runtime to non-production: it requires the private
invocation key ring, PostgreSQL operation store, pipeline, Nomad, and evidence
archive in every profile, and refuses startup if the normal operation worker is
skipped. If this complete capability is not enabled, `/invoke` returns an
explicit 503 and never falls back to the legacy HTTP-to-Nomad submitter. The
legacy function-history read remains available. The resolver matches the current spec digest to the latest successful
deployment in this environment, then selects that deployment's digest image
and the promoted database revision. Migration 22 adds the deployment digest;
new successful deployments record it with their image and status. Legacy
deployments and rollbacks without proven spec provenance refuse function
admission until a new qualified deployment succeeds. A rollback whose source
has a recorded digest now carries that digest through signed acceptance and
completion, and runs only when the current spec still matches it. A changed
historical spec must be restored and rehearsed before that rollback can run;
the digest alone cannot reconstruct it. Failed attempts do not
displace the previous successful deployment; a newer nonterminal attempt
temporarily blocks admission because it may already have changed the running
job. The claimed worker repeats the
public checks before opening private input, uses the closed variable/job
reconciliation path, and publishes a redacted terminal receipt. A pre-effect
failure finishes under the claim and app lock. An ambiguous remote effect
stays pending.

The opt-in PostgreSQL runtime also reads Nomad's regional service jobs and
their active allocation job snapshots. It requires the exact deployment image,
one healthy running allocation per desired replica, and a stable job revision
across the read. Function-only apps have no service allocation and rely on the
recorded successful deployment plus the digest-pinned one-shot job. A transient
Nomad mismatch defers a claimed invocation before private material is opened.
The function-only deployment path no longer submits an empty service job.
The opt-in `TestVerifyRunningAppImageInNomad` passed on 2026-09-25 against a
disposable local Nomad 2.0.7 dev agent with Docker and the locally pinned
`alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc`
image. It observed a real healthy allocation and refused a wrong image. The
test owns and purges its uniquely named job; the agent was stopped afterward.
This is a single-node local read-back qualification, not release topology or
process-crash evidence.

The private variable cleanup consumer now supports exact owner and revision
checked deletion, and PostgreSQL migration 20 stores lease-fenced public
cleanup intents. Claim eligibility requires terminal receipt, attempted
variable creation, and verified operation archive evidence. Migration 21
raises the reader and writer contracts so private function acceptance always
reserves that archive subject and older readers cannot encounter v2 bundles.
The archive now seals the signed public operation, terminal
execution, and public effect-attempt rows as a v2 bundle; old v1 bundles remain
readable. The cleanup consumer now runs with the enabled PostgreSQL runtime and
requires an evidence archiver at startup. Private envelope retirement still
needs a replay/key-retention policy before production
activation. A Nomad job purge policy, etcd cleanup parity,
ACL-enabled allocation with database files, literal crash/two-replica tests,
immutable spec recovery for changed-spec rollback, live release allocation and crash/race
qualification, and Mini migration 22 mixed-version/rollback
rehearsal also remain required. Migration 22 raises the minimum writer to 19,
so deploying it retires older writable binaries and needs a roll-forward
recovery rehearsal before release.
The receipt dispatcher refuses a claim-only terminal write on lease-fenced
backends when the app-lock-aware receipt interface is unavailable.

## Claimed runtime qualification — 2026-09-25

`TestClaimedFunctionV3HTTPNomadPostgres` passed against disposable local Nomad
2.0.7 and PostgreSQL. It exercised signed HTTP admission and same-key replay,
one claimed worker, an actual private Nomad variable containing the exact
request body, the deterministic job, terminal receipt, public archive source
redaction, archive-gated cleanup, and owner-checked variable deletion. Two
runtime defects found by this test were fixed: evaluation lineage uses
Nomad's submitted `JobModifyIndex` rather than the later mutable job index,
and cleanup checks the exact `norn.function-invoke/<operation-id>` owner
marker written to the variable. The test agent and database were stopped.

This is one local success path. Literal process crashes and two-worker races,
named database allocation files, Mini migration-22 rollback, key-retention
restore, and etcd parity still need release evidence.
