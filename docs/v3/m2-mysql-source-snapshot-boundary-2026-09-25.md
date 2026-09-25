# Private MySQL source snapshot boundary

Migration 29 adds a signed `database.mysql-source-snapshot` preparation path.
Its accepted request exists before the artifact and binds the source catalog
revision, full target and maintenance identities, exact WordPress Nomad job
revision and allocation IDs, and dump-tool digest. Preparation serializes with
catalog changes and runtime launches, then permanently reserves the physical
source database in `quiesce-intended`. An identical claimed replay is allowed;
a different request or operation cannot reuse that source. The row also blocks
catalog activation and new runtime launch reservations.

The catalog can bind a distinct snapshot account. The private staging path
verifies its exact MySQL `CURRENT_USER()`, endpoint, and TLS policy while
retaining the application source identity in the artifact. A disposable MySQL
test covers a locked runtime account and a successful snapshot with the
separate reader credential.

Migration 30 adds a private claimed Nomad stop step. It checkpoints
`stop-intended` before sending a guarded stop for the signed job revision and
allocation IDs. It records `stop-proved` only after Nomad reports `Stop=true`
and the exact allocations are terminal or absent. An uncertain result stays
fenced and cannot automatically repeat the stop. A disposable PostgreSQL
integration test covers checkpoint order, exact request, and replay refusal;
Nomad unit tests cover stale revisions, competing allocations, and a job that
is still running despite terminal allocations. This is not a source write
lock or a snapshot receipt.

The private request requires the signed job app and job ID to equal the
accepted operation's app. Release admission still needs a live deployment or
manifest proof that this exact job owns the selected source binding; naming
equality alone cannot establish database ownership.

`quiesce-intended` is a reservation, **not** a write-stop proof. Before a
snapshot can be accepted for restore, the signed stop and account-lock proofs,
staged artifact, and service-signed receipt must be bound to a separate signed
restore decision. The existing restore request still accepts an operator-supplied
source-quiescence reference; that private prototype must require the signed
snapshot receipt. Source unlock/restart needs a separately signed recovery
operation. No public snapshot or restore capability is enabled.

### Source account lock checkpoint (private)

The private source operation now binds a durable global runtime mutation fence
before the signed Nomad CAS stop. A second checkpoint stores `lock-intended`
before the exact catalog-bound MySQL fence credential locks the runtime account,
terminates sessions, and proves no sessions remain. Only a successful proof
records `lock-proved`. An uncertain response, lost claim, or failed proof leaves
both reservations held. Generic runtime-fence release rejects a bound source
operation. There is no automatic retry, public route, unlock, or resume.

### Artifact staging receipt (private)

Migration 32 adds `stage-intended` before the dump tool runs and `stage-proved`
only after the bounded, owner-only SQL file verifies against its measured size,
SHA-256, source identity, and source-derived expectation. The production path
uses the catalog-derived snapshot account and the dump-tool digest from the
accepted request. It rechecks the live claim, source reservation, catalog
revision, and global runtime fence before storing the receipt. Any ambiguous
stage result remains fenced and cannot automatically run the dump again.

The service signs canonical receipt bytes with the acceptance signer. The
receipt binds the signed acceptance intent and its digest, source identity,
catalog revision, dump-tool digest, local artifact path, and artifact metadata.
Loading the receipt verifies the exact stored bytes and signature. This is a
service attestation of staging, not a separately accepted restore decision.
The local SQL file is not durably retained or replicated by this step; a
restore must verify its bytes again and must cite the receipt digest in a
separately signed restore acceptance. The current restore prototype has not
been wired to require that receipt.

This is still a private implementation step, not a qualified source snapshot
workflow. The private quiescence runner renews its claim before and throughout
the Nomad stop and account lock. The separate artifact staging step checks the
claim at its boundaries but does not yet renew it throughout the dump. Release
qualification needs a supervised staging claim, retained artifact storage and
availability proof, explicit recovery after an ambiguous effect, and a signed
resume/unlock decision. Direct host mutations also need qualification against
the global fence at their actual effect point.
