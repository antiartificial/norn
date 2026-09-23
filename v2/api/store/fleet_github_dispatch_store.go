package store

import "context"

// FleetGitHubDispatchStore is the control boundary for durable GitHub Actions
// dispatch state in the fleet workflow, keyed by plan id. FinishFleetGitHubDispatch
// only matches a dispatch whose stored nonce hash equals the supplied one — a
// replay/fencing guard. It is logically adjacent to FleetAttemptStore (both keyed
// by the fleet plan lifecycle) but has no shared table, so it is a backend-neutral
// seam of its own for Norn v3 (roadmap M3 / P5).
type FleetGitHubDispatchStore interface {
	GetFleetGitHubDispatch(ctx context.Context, planID string) (*FleetGitHubDispatch, error)
	CreateFleetGitHubDispatch(ctx context.Context, item FleetGitHubDispatch) (*FleetGitHubDispatch, error)
	FinishFleetGitHubDispatch(ctx context.Context, planID, nonceHash string, runID int64, workflowURL string) (*FleetGitHubDispatch, error)
}

// Compile-time proof that the PostgreSQL adapter satisfies the boundary.
var _ FleetGitHubDispatchStore = (*DB)(nil)
