package etcdstore

// Atomic, non-authorizing reservation for an offline work-staging contraction.
// This is deliberately not exposed as an HTTP route or runner credential
// grant. A later server admission must verify the owner signature, source,
// plan and backend evidence before calling this method, and add an attempt
// protocol before any effect can run. The reservation itself holds the same
// durable target fence as protected GitHub dispatch and executor attempts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/model"
)

var ErrOfflineDispatchReservationConflict = errors.New("offline dispatch reservation lost its atomic target-fence commit")

func (s *V3OperationStore) offlineDispatchKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-dispatches/" + planID
}

// GetOfflineDispatchReservation reads a previously committed reservation. It
// does not prove that the current target fence still belongs to the plan.
func (s *V3OperationStore) GetOfflineDispatchReservation(ctx context.Context, planID string) (offline.Binding, error) {
	response, err := s.kv.Get(ctx, s.offlineDispatchKey(planID))
	if err != nil {
		return offline.Binding{}, err
	}
	if len(response.Kvs) != 1 {
		return offline.Binding{}, ErrNotFound
	}
	return offline.DecodeBinding(response.Kvs[0].Value)
}

// ReserveOfflineDispatch atomically persists the exact binding and acquires
// the existing target fence. A lost etcd commit response is indeterminate:
// callers must read this key and the fence, never issue a changed reservation.
// The method does not consume owner authorization or permit an attempt.
func (s *V3OperationStore) ReserveOfflineDispatch(ctx context.Context, binding offline.Binding, now time.Time) (offline.Binding, error) {
	if s == nil || binding.Validate() != nil || now.Before(binding.CreatedAt) || !now.Before(binding.ExpiresAt) {
		return offline.Binding{}, offline.ErrInvalidBinding
	}
	if existing, err := s.GetOfflineDispatchReservation(ctx, binding.PlanID); err == nil {
		if sameOfflineBinding(existing, binding) {
			abandoned, abandonedErr := s.checkFleetTargetPlanAbandoned(ctx, binding.PlanID)
			fence, _, fenceErr := s.GetFleetTargetFence(ctx, binding.TargetID)
			epoch, _, epochErr := s.fleetAuthorityEpochWithRevision(ctx)
			if abandonedErr != nil || abandoned || fenceErr != nil || !fence.Held || fence.HolderPlanID != binding.PlanID ||
				fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 ||
				uint64(fence.Generation) != binding.FenceGeneration || epochErr != nil ||
				fence.AuthorityEpoch != epoch {
				return offline.Binding{}, ErrOfflineDispatchReservationConflict
			}
			return existing, nil
		}
		return offline.Binding{}, ErrOfflineDispatchReservationConflict
	} else if !errors.Is(err, ErrNotFound) {
		return offline.Binding{}, err
	}
	planRecord, planRevision, err := s.load(ctx, binding.PlanID)
	if err != nil {
		return offline.Binding{}, err
	}
	op := planRecord.Operation
	if op.Kind != "fleet.capacity-plan" || op.Status != model.OperationSucceeded {
		return offline.Binding{}, fmt.Errorf("offline reservation requires a succeeded Fleet capacity plan")
	}
	var plan fleet.CapacityPlan
	encodedPayload, err := json.Marshal(op.Payload)
	if err != nil || json.Unmarshal(encodedPayload, &plan) != nil ||
		plan.ID != binding.PlanID || plan.Cluster != "tk-staging-nyc3" || plan.Pool != "app" ||
		plan.Action != "scale" || plan.Current.Desired != 3 || plan.Proposed.Desired != 2 {
		return offline.Binding{}, fmt.Errorf("offline reservation requires the exact work-staging 3-to-2 app plan")
	}
	if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, binding.PlanID); err != nil {
		return offline.Binding{}, err
	} else if abandoned {
		return offline.Binding{}, fleetHolderAbandonedError()
	}
	fenceContext, err := s.loadFleetTargetFenceContext(ctx, plan.Cluster, "staging", true)
	if err != nil {
		return offline.Binding{}, err
	}
	if fenceContext.TargetID == "" || fenceContext.TargetID != binding.TargetID {
		return offline.Binding{}, fmt.Errorf("offline reservation requires the registered target")
	}
	target, err := s.GetFleetTarget(ctx, binding.TargetID)
	if err != nil {
		return offline.Binding{}, err
	}
	if target.Provider != "digitalocean" || target.ProviderAccount != binding.ProviderAccountUUID ||
		target.StateBackend != "s3://"+binding.Backend.Bucket+"/"+binding.Backend.Key {
		return offline.Binding{}, fmt.Errorf("offline reservation provider account or backend differs from registered target")
	}
	next, err := lifecycle.DecideAcquire(fenceContext.Fence, binding.PlanID,
		binding.DispatchNonceSHA256, op.StartedAt, fenceContext.Epoch)
	if err != nil {
		return offline.Binding{}, err
	}
	if uint64(next.Generation) != binding.FenceGeneration {
		return offline.Binding{}, fmt.Errorf("offline reservation fence generation differs")
	}
	bound, err := json.Marshal(binding)
	if err != nil {
		return offline.Binding{}, err
	}
	fence, err := json.Marshal(fleetTargetFenceRecord(next))
	if err != nil {
		return offline.Binding{}, err
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.opKey(binding.PlanID)), "=", planRevision),
		s.registryCompare(fenceContext),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(binding.TargetID)), "=", fenceContext.FenceRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", fenceContext.EpochRevision),
		s.abandonedCompare(binding.PlanID),
		clientv3.Compare(clientv3.CreateRevision(s.offlineDispatchKey(binding.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetGitHubDispatchPreparationKey(binding.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(binding.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptPrefix(binding.PlanID)).WithPrefix(), "=", 0),
	}
	committed, err := s.kv.Txn(ctx).If(compares...).Then(
		clientv3.OpPut(s.offlineDispatchKey(binding.PlanID), string(bound)),
		clientv3.OpPut(s.fleetTargetFenceKey(binding.TargetID), string(fence)),
	).Commit()
	if err != nil {
		return offline.Binding{}, err // uncertain: caller must read exact state
	}
	if !committed.Succeeded {
		return offline.Binding{}, ErrOfflineDispatchReservationConflict
	}
	return binding, nil
}

func sameOfflineBinding(a, b offline.Binding) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && string(left) == string(right)
}
