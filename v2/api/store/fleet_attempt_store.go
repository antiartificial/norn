package store

import (
	"context"

	"norn/v2/api/fleet"
)

// FleetAttemptStore is the backend-neutral control boundary for Fleet runner
// attempts: the record of each CI-driven provider apply for a plan, with
// server-owned attempt numbering and root-attempt lineage, revision-based
// optimistic concurrency, and heartbeat-lease fencing (a live attempt whose
// lease expires is abandoned on the next read).
//
// It is a domain seam for Norn v3 (roadmap M1 / P5, "Fleet attempts"). Callers
// depend on this interface rather than a concrete *DB so an etcd-backed adapter
// can be introduced later. The revision CAS and lease-expiry behavior are the
// same fencing guarantees the etcd adapter must reproduce with its own
// compare-and-swap transactions and leases; the shared conformance suite in
// fleet_attempt_store_conformance_test.go pins them.
type FleetAttemptStore interface {
	ListFleetRunnerAttempts(ctx context.Context, planID string) ([]fleet.RunnerAttempt, error)
	GetFleetRunnerAttempt(ctx context.Context, planID, attemptID string) (*fleet.RunnerAttempt, error)
	CreateFleetRunnerAttempt(ctx context.Context, item fleet.RunnerAttempt) (*fleet.RunnerAttempt, error)
	// UpdateFleetRunnerAttempt applies action ("heartbeat", "advance",
	// "cancel") only when revision matches the persisted row, then bumps the
	// revision. A stale revision matches no row and returns an error.
	UpdateFleetRunnerAttempt(ctx context.Context, planID, id string, revision int64, action string, values ...interface{}) (*fleet.RunnerAttempt, error)
}

// Compile-time proof that the PostgreSQL adapter satisfies the Fleet-attempt
// boundary. A future etcd adapter adds its own assertion here.
var _ FleetAttemptStore = (*DB)(nil)
