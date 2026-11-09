package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

var ErrWorkloadReleasePolicyNotFound = errors.New("workload release policy not found")
var ErrWorkloadReleasePolicyStale = errors.New("workload release policy is stale")

func validateWorkloadReleasePolicy(policy store.WorkloadReleasePolicy) error {
	_, approvedAtOffset := policy.ApprovedAt.Zone()
	if policy.SchemaVersion != store.WorkloadReleasePolicySchema || policy.App == "" || policy.ControlEnvironment == "" ||
		policy.FleetTargetGeneration == 0 || uuid.Validate(policy.ApprovalID) != nil ||
		strings.TrimSpace(policy.ApprovedBy) == "" || len(policy.ApprovedBy) > 256 || policy.ApprovedBy != strings.TrimSpace(policy.ApprovedBy) || strings.ContainsAny(policy.ApprovedBy, "\x00\r\n\t") ||
		policy.ApprovedAt.IsZero() || approvedAtOffset != 0 || policy.Mode != store.WorkloadReleasePolicyStatelessOverlap ||
		policy.MinReplicas < 1 || policy.MaxReplicas < policy.MinReplicas || policy.SurgeLimit < 1 ||
		policy.MaxReplicas > int(^uint(0)>>1)-policy.SurgeLimit || len(policy.RouteSteps) < 3 || len(policy.RouteSteps) > 16 {
		return fmt.Errorf("workload release policy is incomplete")
	}
	first, last := policy.RouteSteps[0], policy.RouteSteps[len(policy.RouteSteps)-1]
	if first.OldWeight != 100 || first.TargetWeight != 0 || last.OldWeight != 0 || last.TargetWeight != 100 {
		return fmt.Errorf("workload release policy route must begin old-only and end target-only")
	}
	mixed, previous := false, -1
	for index, step := range policy.RouteSteps {
		if step.OldWeight < 0 || step.TargetWeight < 0 || step.OldWeight+step.TargetWeight != 100 ||
			(index > 0 && step.TargetWeight <= previous) {
			return fmt.Errorf("workload release policy route steps are invalid")
		}
		mixed = mixed || (step.OldWeight > 0 && step.TargetWeight > 0)
		previous = step.TargetWeight
	}
	if !mixed {
		return fmt.Errorf("workload release policy requires an explicit mixed-version step")
	}
	return nil
}

func (s *V3OperationStore) workloadReleasePolicyKey(app, environment string) string {
	sum := sha256.Sum256([]byte(app + "\x00" + environment))
	return s.prefix + "/v3/workload-release-policies/" + hex.EncodeToString(sum[:])
}

// CurrentWorkloadReleasePolicy returns the latest approved policy only while
// it is bound to the current placement generation. A placement change requires
// the operator to approve the overlap envelope again.
func (s *V3OperationStore) CurrentWorkloadReleasePolicy(ctx context.Context, app, environment string) (store.WorkloadReleasePolicy, int64, error) {
	if s == nil || s.kv == nil || app == "" || environment == "" {
		return store.WorkloadReleasePolicy{}, 0, fmt.Errorf("workload release policy reader is unavailable")
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, app, environment)
	if err != nil {
		return store.WorkloadReleasePolicy{}, 0, err
	}
	key := s.workloadReleasePolicyKey(app, environment)
	response, err := s.kv.Get(ctx, key)
	if err != nil {
		return store.WorkloadReleasePolicy{}, 0, fmt.Errorf("load workload release policy: %w", err)
	}
	if len(response.Kvs) == 0 {
		return store.WorkloadReleasePolicy{}, 0, ErrWorkloadReleasePolicyNotFound
	}
	if len(response.Kvs) != 1 {
		return store.WorkloadReleasePolicy{}, 0, fmt.Errorf("workload release policy key is not unique")
	}
	var policy store.WorkloadReleasePolicy
	if decodeV3Record(response.Kvs[0].Value, &policy) != nil || validateWorkloadReleasePolicy(policy) != nil ||
		policy.App != app || policy.ControlEnvironment != environment || policy.FleetTargetGeneration != target.Generation {
		return store.WorkloadReleasePolicy{}, 0, ErrWorkloadReleasePolicyStale
	}
	stable, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(app, environment)), "=", targetRevision),
		clientv3.Compare(clientv3.ModRevision(key), "=", response.Kvs[0].ModRevision),
	).Then(clientv3.OpGet(key)).Commit()
	if err != nil || !stable.Succeeded {
		return store.WorkloadReleasePolicy{}, 0, fmt.Errorf("workload release policy changed during verification")
	}
	return policy, response.Kvs[0].ModRevision, nil
}

// ConfigureWorkloadReleasePolicy stores an owner-approved overlap envelope
// under a separate CAS key, so policy changes do not rewrite Fleet placement
// identity or invalidate the currently serving route snapshot.
func (s *V3OperationStore) ConfigureWorkloadReleasePolicy(ctx context.Context, policy store.WorkloadReleasePolicy, expectedRevision int64) (int64, error) {
	if s == nil || s.kv == nil || expectedRevision < 0 || validateWorkloadReleasePolicy(policy) != nil {
		return 0, fmt.Errorf("workload release policy configuration is invalid")
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, policy.App, policy.ControlEnvironment)
	if err != nil || policy.FleetTargetGeneration != target.Generation {
		return 0, fmt.Errorf("workload release policy does not match current Fleet placement")
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return 0, err
	}
	key := s.workloadReleasePolicyKey(policy.App, policy.ControlEnvironment)
	txn, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(policy.App, policy.ControlEnvironment)), "=", targetRevision),
		clientv3.Compare(clientv3.ModRevision(key), "=", expectedRevision),
	).Then(clientv3.OpPut(key, string(encoded))).Commit()
	if err != nil {
		return 0, err
	}
	if !txn.Succeeded {
		return 0, fmt.Errorf("workload release policy or Fleet placement changed during configuration")
	}
	return txn.Header.Revision, nil
}
