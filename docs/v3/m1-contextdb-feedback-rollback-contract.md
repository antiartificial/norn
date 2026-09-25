# ContextDB feedback rollback: cross-service M1 gate

The Norn v2 handler at `v2/api/handler/ops_contextdb.go` currently sends an
inline `POST /v1/namespaces/{namespace}/feedback/events/{event}/rollback`.
It does not accept a signed operation, reserve an effect, send a remote
idempotency identity, or reconcile an ambiguous response. An API process
crash after ContextDB applies the rollback but before Norn returns a receipt
leaves Norn unable to distinguish success from no effect. Retrying the POST
could execute the rollback twice.

At the checked ContextDB source revision `23c7e49` (`main`),
`internal/server/rest.go` registers `GET /v1/namespaces/{ns}/feedback/events`
but no rollback POST or rollback lookup route. Its REST reference likewise
lists feedback events without a rollback mutation. The behavior of a deployed
ContextDB instance therefore cannot be inferred from this source. Norn cannot
safely invent retry semantics around the existing POST.

Before changing the Norn handler, establish and test this ContextDB contract:

1. The rollback POST accepts a stable idempotency key bound to namespace,
   event ID, mode, reason, and owner. Replaying the same key returns the
   original receipt; changing any bound field yields a conflict. The receipt
   includes a stable rollback ID and the original event ID.
2. ContextDB atomically persists that key and the rollback effect, including
   the no-op/already-rolled-back outcome. A read endpoint can resolve the key
   after an ambiguous POST response or process crash.
3. Norn accepts `contextdb.feedback-rollback` through its signed operation
   store, preserving the exact namespace/event/mode/reason/owner in the
   fingerprint. The worker reserves one fenced HTTP effect before dispatch,
   sends the stable remote key, and reconciles by key before any retry. It
   stores the ContextDB receipt before completing the claimed operation.
4. Qualification covers duplicate Norn admissions, changed request conflict,
   two workers racing, ContextDB success with lost response, worker restart,
   and rollback lookup failure. The last case remains pending and must not
   issue another POST.

Keep this mutation outside the v3 release gate until the ContextDB side of
the contract and the Norn worker path are verified together. This note does
not assert that any live ContextDB deployment lacks the endpoint; it records
the missing source contract required for safe integration.
