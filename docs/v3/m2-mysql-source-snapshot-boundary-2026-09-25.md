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

`quiesce-intended` is a reservation, **not** a write-stop proof. Before a
snapshot can be accepted for restore, the remaining private runner must
durably checkpoint the source account-lock intent and verified session drain,
stage the artifact
through the snapshot credential, and sign a receipt that binds those proofs to
the artifact. The existing restore request still accepts an operator-supplied
source-quiescence reference; that private prototype must be replaced by the
signed snapshot receipt. Source unlock/restart needs a separately signed
recovery operation. No public snapshot or restore capability is enabled.

### Source account lock checkpoint (private)

The private source operation now binds a durable global runtime mutation fence
before the signed Nomad CAS stop. A second checkpoint stores `lock-intended`
before the exact catalog-bound MySQL fence credential locks the runtime account,
terminates sessions, and proves no sessions remain. Only a successful proof
records `lock-proved`. An uncertain response, lost claim, or failed proof leaves
both reservations held. Generic runtime-fence release rejects a bound source
operation. There is no automatic retry, public route, unlock, or resume.

This is still a private implementation step, not a qualified source snapshot
workflow. The operation claim is checked around each checkpoint but not renewed
across the external Nomad/MySQL calls. A production runner needs lease renewal
and cancellation, signed artifact staging/receipt, explicit recovery after an
ambiguous effect, and a signed resume/unlock decision. Direct host mutations
also need qualification against the global fence at their actual effect point.
