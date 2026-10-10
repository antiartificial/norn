package etcdstore

import (
	"context"

	"norn/v2/api/model"
)

// NewestFleetConvergePlan returns resource's newest recorded converge plan
// operation, or nil when there is none. Newest is (StartedAt, ID) descending,
// the same order the executor admission and PG use (fleetNewestConvergePlan).
// The converge watcher's etcd recorder uses it (plan-5-7 E6).
func (s *V3OperationStore) NewestFleetConvergePlan(ctx context.Context, resource string) (*model.Operation, error) {
	id, _, err := s.fleetNewestConvergePlan(ctx, resource)
	if err != nil || id == "" {
		return nil, err
	}
	return s.GetOperation(ctx, id)
}
