package store

// AuthStore is the combined control boundary for the auth aggregate: identity
// and revocation (IdentityStore) together with interactive execution sessions
// and their step-up challenges (ExecSessionStore).
//
// These are one aggregate, not two independently portable boundaries, because
// they share referential integrity (step_up_challenges and exec_sessions
// reference access_devices) and an atomic guarantee: RotateAccessToken,
// RevokeAccessToken and RevokeAccessDevice cancel the exec sessions the revoked
// credential authorized, in the same operation, and return their ids. A backend
// adapter implements the whole aggregate or none of it; splitting identity onto
// one backend and sessions onto another cannot make revocation atomic. See
// docs/v3/adrs/0007-auth-aggregate-and-revocation.md (Accepted).
//
// The disposition on ADR 0007 is "compose, do not supersede": AuthStore composes
// the two already-verified interfaces under one atomicity boundary rather than
// replacing them, so their conformance suites remain the contract unchanged.
type AuthStore interface {
	IdentityStore
	ExecSessionStore
}

// Compile-time proof that the PostgreSQL adapter satisfies the whole aggregate.
// A second backend (etcd) adds its own assertion; on PostgreSQL the atomicity is
// one transaction, on etcd one multi-key compare-and-swap over the credential
// and the affected session records within a single cluster.
var _ AuthStore = (*DB)(nil)
