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

`quiesce-intended` is a reservation, **not** a write-stop proof. Before a
snapshot can be accepted for restore, the remaining private runner must stop
the exact Nomad job revision, prove allocations absent, durably checkpoint the
source account-lock intent and verified session drain, stage the artifact
through the snapshot credential, and sign a receipt that binds those proofs to
the artifact. The existing restore request still accepts an operator-supplied
source-quiescence reference; that private prototype must be replaced by the
signed snapshot receipt. Source unlock/restart needs a separately signed
recovery operation. No public snapshot or restore capability is enabled.
