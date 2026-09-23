package store

import (
	"context"
	"time"
)

// MutationAuditStore is the backend-neutral evidence boundary for Norn v3
// (roadmap M1 / P5, "evidence references"): the durable, signed record of every
// audited control mutation, written in two phases (reserve before the mutation,
// finish after it with the outcome and record digest), plus review incidents
// and bounded retention pruning.
//
// Callers depend on this interface rather than a concrete *DB so an etcd/object
// adapter can be introduced later. The reserve-then-finish contract and the
// "finish only a started record once" guard are exactly the evidence
// invariants the roadmap requires to survive a store migration; the shared
// conformance suite in mutation_audit_store_conformance_test.go pins them. Note
// that pruning targets diagnostics-grade age: only finished records are
// removable, never a reservation whose mutation outcome was never recorded.
type MutationAuditStore interface {
	// ReserveMutationAudit persists a started record before the mutation runs.
	ReserveMutationAudit(ctx context.Context, event *MutationAuditEvent) error
	// FinishMutationAudit records the outcome and digest of a started record.
	// It affects a record exactly once; finishing an already-finished (or
	// unknown) record returns an error.
	FinishMutationAudit(ctx context.Context, id, path string, status int, outcome string, finishedAt time.Time, durationMs int64, digest string) error
	ListMutationAudits(ctx context.Context, limit int) ([]MutationAuditEvent, error)
	GetMutationAudit(ctx context.Context, id string) (*MutationAuditEvent, error)

	InsertMutationAuditIncident(ctx context.Context, incident *MutationAuditIncident) error
	ListMutationAuditIncidents(ctx context.Context, eventIDs []string) (map[string]MutationAuditIncident, error)

	// CountStaleMutationAudits counts reservations left unfinished before a cutoff.
	CountStaleMutationAudits(ctx context.Context, before time.Time) (int, error)
	// PruneMutationAudits removes only finished records older than a cutoff.
	PruneMutationAudits(ctx context.Context, before time.Time) (int64, error)
}

// Compile-time proof that the PostgreSQL adapter satisfies the evidence
// boundary. A future etcd/object adapter adds its own assertion here.
var _ MutationAuditStore = (*DB)(nil)
