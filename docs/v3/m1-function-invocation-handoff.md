# M1 function invocation: private request and durable Nomad effects

Status: PostgreSQL and etcd private-material acceptance foundations implemented
and tested against disposable PostgreSQL 17.7 and etcd; route and worker are
not wired.
`InvokeFunction` still submits a batch job in the HTTP process. Its current
execution-row check and request-independent completion watcher reduce two
failure windows but do not provide signed acceptance, claim fencing, or crash
recovery. The new explicit encryption key ring, migration 18, and
`AcceptPrivateInvocation` are dormant pending key configuration and the effect
runner. Migration 19 adds the public pre-call effect-attempt fence and raises
the writer contract to 16. A private Mini copy has now been rehearsed through
migration 19; the mixed-version and rollback gates remain before deployment.

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
passed. Neither backend is connected to the function route or startup key
preflight. Real Nomad, process-crash, and restore qualification remain open.

The function variable adapter has HTTP-level tests for exact reads,
create-only Nomad CAS, private-byte round trips, and redacted errors. Its
writer now uses padded base64 for Nomad's strict template decoder; the reader
also accepts older unpadded values for recovery. PostgreSQL and etcd
effect-attempt stores record immutable public targets under live claims and
authorize only the first caller to cross each remote call boundary. Real
backend races, stale claims, and successor claims passed. Narrow variable and
job worker steps use those attempt stages to reconcile lost responses without
another remote create. Their fake concurrent callers issued one create each.

The closed function-job dialect derives its digest from the validated public
Nomad job. A disposable Nomad 2.0.7 server passed zero-index create, duplicate
conflict, digest read-back, and found-job observation with exact version,
evaluations, allocations, and a stable second read. Dialect v2 uses a private
`env=true` template to decode padded base64 JSON and inject
`NORN_REQUEST_BODY`, `NORN_REQUEST_METHOD`, and `NORN_REQUEST_PATH` with JSON
quoting. A disposable Docker-enabled Nomad 2.0.7 allocation produced the
expected digest for a multiline body, quotes, backslashes, Unicode path, and
empty method; the job JSON contained no request values. The builder and steps
are still disconnected from the claimed operation executor. App/database
secret delivery, the allocation's ACL for this custom variable path, terminal
receipt, and cleanup remain open. Requalify on the release Nomad version
before enabling submission.
