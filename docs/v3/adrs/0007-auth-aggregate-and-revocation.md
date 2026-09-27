# ADR 0007: Atomic auth aggregate and exec-session revocation

Status: Accepted 2026-09-23 on `feature/durable-app-recovery-ui`; reconciled into the v3 integration branch 2026-09-24. Owner: Norn.

## Decision

Credential/device identity and exec sessions form one `AuthStore` aggregate on one selected backend. `AuthStore` composes the `IdentityStore` and `ExecSessionStore` interfaces; an adapter must implement both. Rotating or revoking a credential and cancelling its authorized pending/running exec sessions is one atomic backend mutation that returns the cancelled session IDs. Splitting those records across PostgreSQL and etcd is invalid.

Every exec mutation must also recheck a monotonic credential/authority generation at the execution boundary. A stale session status or an earlier successful connect check does not authorize continued execution after device revocation. The PostgreSQL adapter uses a transaction; the etcd adapter uses a multi-key compare-and-swap guarded by record revisions. Ambiguous transport outcomes must resolve through stable credential and session identities, never by automatically restoring authorization.

## Evidence and remaining qualification

`store.AuthStore` composes the two interfaces; PostgreSQL `store.DB` and etcd `etcdstore.AuthStore` implement it. Shared conformance and etcd revocation/connect race tests cover atomic cancellation and session admission. This code integration is not live Fleet qualification. Two-process partition/crash tests, old-record compatibility, and the decided execution-boundary generation remain M1/M3 exit work. `ExecSessionAuthorized` currently rechecks session and device state at use; it is not evidence of a monotonic generation token spanning a paused external exec call.

The prior durable-branch ADR included both a proposed single replacement interface and an accepted disposition that composed the existing interfaces. This reconciled ADR adopts the accepted composed interface and removes that contradiction. It retains the atomicity and execution-boundary requirements as release gates.
