# ADR 0007: The auth aggregate and cross-boundary atomic revocation

Status: Proposed. Date: 2026-09-23. Owner: Norn.

## Context

The v3 backend abstraction (ADR 0002) extracted per-boundary storage interfaces so a fresh Fleet can run on etcd with no control PostgreSQL. Five core control boundaries — operations, deployments, events, Fleet attempts and mutation-audit evidence — now have interface + PostgreSQL adapter + etcd adapter, all passing one shared conformance suite (`store/storetest`), verified against a live etcd cluster.

Extracting the identity/revocation and exec-session boundaries surfaced a constraint that changes the design, established from the code rather than assumed:

- `IdentityStore.RotateAccessToken`, `RevokeAccessToken` and `RevokeAccessDevice` do not only mutate a credential. In one PostgreSQL transaction they also **cancel the exec sessions that credential authorized** and return their ids. Revocation and session cancellation are atomic today.
- `step_up_challenges` and `exec_sessions` carry **foreign keys to `access_devices`**. The exec-session boundary cannot even be exercised without an identity device; its conformance suite must be handed a `registerDevice` callback.

Identity and exec-sessions are therefore not two independently portable boundaries. They are **one aggregate** — call it the auth aggregate — sharing referential integrity and an atomic revocation-plus-cancellation guarantee. A naive etcd `IdentityStore` that revoked a token but returned an empty session list would pass a boundary-local test yet leave a live session authorized by a revoked credential: a latent security gap. This is the exact fencing/reconciliation problem ADR 0002 flagged, now made concrete.

## Recommended decision

Treat identity and exec-sessions as a single **AuthStore** aggregate with one atomicity boundary, not two composable interfaces. A backend adapter (PostgreSQL or etcd) implements the whole aggregate or none of it.

Preserve the current invariant on any backend: **revoking a credential and cancelling its authorized sessions is a single atomic, fenced operation**, and the set of cancelled session ids is observable to the caller. On PostgreSQL this remains one transaction. On etcd it is one multi-key compare-and-swap transaction over the credential record and the affected session records (etcd transactions are all-or-nothing over an explicit key set), scoped to a single etcd cluster so both live in the same store.

Do not split the auth aggregate across two backends. The control plane already selects one backend for all five core boundaries via `controlstore`; the auth aggregate selects the same single backend. A configuration that put identity on etcd and sessions on PostgreSQL (or vice versa) is rejected, because no cross-store transaction can make revocation atomic.

## Alternatives and consequences

- **Two independent adapters with best-effort cascade.** Revoke in one store, then cancel sessions in the other outside a transaction. Rejected: a crash or partition between the two steps leaves an authorized session under a revoked credential — fails open on auth, which ADR 0002 forbids.
- **Saga / outbox across the two stores.** Durable revocation intent, asynchronously reconciled into session cancellation. Viable for eventual consistency but leaves a window where a revoked credential still authorizes execution. Acceptable only if paired with an execution-boundary fence (below); not acceptable as the sole mechanism.
- **Fencing generations at the execution boundary.** Independent of storage atomicity: every exec mutation re-checks a monotonic credential/authority generation at use, so a session whose credential was revoked is refused at its next fenced action even if its record was not yet cancelled. Recommended as **defense in depth alongside** single-store atomicity, and as the *primary* guarantee if a future design ever does span stores. A pre-call check alone cannot fence a paused process that later resumes.
- **Keep them coupled only on PostgreSQL, never port to etcd.** Lowest effort; the Mini keeps working. Rejected for the Fleet: the auth aggregate is on the request path, so a Fleet that cannot run auth on etcd is not control-PG-free.

## Invariants and failure behavior

Revocation is atomic with session cancellation and returns the cancelled ids. Session creation is gated on a verified, unconsumed step-up challenge and a bounded per-device active count; the gate and the insert are atomic (challenge consumption and session creation cannot diverge). Connect refuses a revoked or unknown device via a consistent read. OIDC assertion reservation is a single-use replay tombstone with an expiry; concurrent exchanges race safely and exactly one wins.

On etcd: the credential record and each candidate session are read at a revision, and the cancel-plus-revoke is committed as one transaction guarded on those revisions; a lost race retries against the new revisions. Referential integrity (`step_up_challenges`/`exec_sessions` → device) becomes an explicit existence check in the transaction rather than a foreign key. Ambiguous transport failures return outcome-unknown and resolve through stable credential/session ids, never automatic re-authorization.

## Migration and acceptance

The PostgreSQL adapter and its conformance suite (`RunIdentityStoreConformance`, `RunExecSessionStoreConformance`) are the contract. A second-backend (etcd) auth adapter must pass both suites plus aggregate-level tests the boundary-local suites cannot express: revoke-token-cancels-its-sessions, revoke-device-cancels-all-its-sessions, and a crash injected between credential mutation and session cancellation leaving no authorized-but-revoked session. The `registerDevice` seam becomes the aggregate's own device creation once the suites are combined.

Acceptance: identical PG/etcd auth invariants; revocation race under concurrency; partition/crash at each revocation checkpoint; replayed OIDC assertion rejected; no session survives its credential's revocation under any interleaving.

## Open decisions

Whether to add the execution-boundary fencing generation now (defense in depth) or defer it until a cross-store design is actually needed. Whether the combined AuthStore interface supersedes the separate `IdentityStore`/`ExecSessionStore` interfaces or composes them. Exact etcd key layout and transaction shape for the revoke-plus-cancel commit, to be settled in the auth-adapter spike. This ADR does not authorize implementation; it records that identity and exec-sessions are one aggregate and that their revocation atomicity is a release-gating invariant.
