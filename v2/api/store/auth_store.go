package store

// AuthStore is the atomic aggregate for credential lifecycle and interactive
// execution. A backend implements the complete aggregate because revoking or
// rotating a credential must cancel its pending and running exec sessions in
// the same durable mutation. PostgreSQL provides that guarantee with one
// transaction; another backend must provide an equivalent atomic primitive.
type AuthStore interface {
	IdentityStore
	ExecSessionStore
}

var _ AuthStore = (*DB)(nil)
