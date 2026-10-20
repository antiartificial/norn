package etcdstore

import (
	"context"
	"strconv"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
)

// ValidateFleetRunnerFenceBinding reads the attempt, target fence, epoch and
// protected dispatch at one linearizable etcd revision. The HTTP caller must
// first prove ownership of this exact attempt's GitHub workload identity.
func (s *V3OperationStore) ValidateFleetRunnerFenceBinding(ctx context.Context, planID, attemptID string, expected *fleet.RunnerFenceBinding) (*fleet.RunnerFenceBinding, error) {
	if expected == nil || expected.TargetID == "" || expected.Generation <= 0 || expected.AuthorityEpoch <= 0 {
		return nil, &lifecycle.FenceError{Code: "fleet_runner_attempt_stale", Reason: "runner attempt has no target fence binding"}
	}
	keys := []string{s.fleetRunnerAttemptKey(planID, attemptID), s.fleetTargetFenceKey(expected.TargetID), s.fleetAuthorityEpochKey(), s.fleetRunnerDispatchKey(planID)}
	ops := make([]clientv3.Op, 0, len(keys))
	for _, key := range keys {
		ops = append(ops, clientv3.OpGet(key))
	}
	response, err := s.kv.Txn(ctx).Then(ops...).Commit()
	if err != nil {
		return nil, err
	}
	for _, result := range response.Responses {
		if len(result.GetResponseRange().Kvs) != 1 {
			return nil, &lifecycle.FenceError{Code: "fleet_runner_attempt_stale", Reason: "runner attempt fence evidence is missing"}
		}
	}
	var attempt fleet.RunnerAttempt
	if err := decodeV3Record(response.Responses[0].GetResponseRange().Kvs[0].Value, &attempt); err != nil {
		return nil, err
	}
	if attempt.PlanID != planID || attempt.ID != attemptID || attempt.FenceBinding == nil || *attempt.FenceBinding != *expected || attempt.Status != "queued" && attempt.Status != "running" || !attempt.HeartbeatExpiresAt.After(time.Now().UTC()) {
		return nil, &lifecycle.FenceError{Code: "fleet_runner_attempt_stale", Reason: "runner attempt fence binding is not live"}
	}
	var fence v3FleetTargetFence
	if err := decodeV3Record(response.Responses[1].GetResponseRange().Kvs[0].Value, &fence); err != nil {
		return nil, err
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(string(response.Responses[2].GetResponseRange().Kvs[0].Value)), 10, 64)
	if err != nil {
		return nil, err
	}
	var dispatch v3FleetRunnerDispatch
	if err := decodeV3Record(response.Responses[3].GetResponseRange().Kvs[0].Value, &dispatch); err != nil {
		return nil, err
	}
	if !fence.Held || fence.HolderPlanID != planID || fence.HolderNonceSHA256 != dispatch.DispatchNonceSHA256 || fence.Generation != expected.Generation {
		return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetExecutionOccupied, Reason: "runner attempt no longer holds its exact target fence"}
	}
	if epoch != expected.AuthorityEpoch || fence.AuthorityEpoch != expected.AuthorityEpoch {
		return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetAuthoritySuperseded, Reason: "runner attempt authority epoch was superseded"}
	}
	return expected, nil
}
