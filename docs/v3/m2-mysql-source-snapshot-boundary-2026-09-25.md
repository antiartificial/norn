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
accepted operation's app. Private admission now proves the selected source
against a successful signed deployment's exact database target, observes its
provenance-stamped Nomad job and live allocations, and accepts that derived
request as one signed operation. It refuses deployments with multiple regions
until every writer can be stopped together. At execution, the guarded stop
rechecks the signed metadata, version, modify index, and allocation set before
mutation, then verifies the exact stopped revision. The stop passed against
disposable Nomad 2.0.7; full source-to-restore qualification remains open.

`quiesce-intended` is a reservation, **not** a write-stop proof. Before a
snapshot can be accepted for restore, the signed stop and account-lock proofs,
staged artifact, and service-signed receipt must be bound to a separate signed
restore decision. The private restore request now requires the signed snapshot
receipt digest and source operation identity. Source unlock/restart needs a
separately signed recovery operation. No public snapshot or restore capability
is enabled.

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
separately signed restore acceptance. Migration 33 now makes that citation
mandatory for the private restore request. Prepare and Begin verify the
persisted service signature, source acceptance lineage, exact source and
artifact identity, physical source reservation, receipt digest, and current
owner-only file bytes. Legacy operator-entered `source_quiescence` rows remain
for audit but cannot qualify a new restore. Provider aliases of the same
physical MySQL database are rejected as self-restores.

Migration 34 transfers the active global runtime fence from the stage-proved
source to the separately accepted restore inside one PostgreSQL transaction.
It keeps the same epoch and never clears the claim gate. The transfer records
the exact source receipt and restore claim binding, permits only an exact
same-claim retry, and blocks a successor claim from silently resuming. The
private restore runner must transfer before intending the destination account
lock; its later checkpoints verify the transferred owner and epoch. Generic
fence release refuses both the old source owner and the transferred restore
owner. No source or destination account is automatically unlocked.

This is still a private implementation step, not a qualified source snapshot
workflow. The quiescence and artifact-staging runners renew the claim during
their external effects; renewal loss cancels the dump and forbids a new signed
receipt. Release qualification still needs retained artifact storage and
cross-node availability proof, explicit recovery after an ambiguous effect,
and a signed resume/unlock decision. Direct host mutations also need
qualification against the global fence at their actual effect point.

### Retained artifact implementation gate

`v2/api/artifactstore` now defines a separate streaming, content-addressed
64 GiB artifact contract and a bounded private local adapter. It does not use
the evidence archive's whole-`[]byte` API. Reads authenticate the declared
size and SHA-256 through EOF; closing early reports an unverified stream.
The local adapter proves the interface and crash-durable publication on one
host, but cannot restore after that host or disk is lost.

The existing Garage-backed evidence archive cannot be treated as an immutable
large-artifact backend merely by switching to multipart upload. Garage's
published [S3 compatibility list](https://garagehq.deuxfleurs.fr/documentation/reference-manual/s3-compatibility/)
supports multipart while listing bucket versioning and bucket policies as
unavailable. An off-host adapter must prove
its own overwrite/delete and retention guarantees, or use a backend with
server-enforced object retention. Before restore is enabled, the source
receipt must bind a remotely retained object identity, and a separate node
must materialize and verify that object after the publisher's local file is
removed. Interrupted publish and restore checkpoints need explicit recovery.
