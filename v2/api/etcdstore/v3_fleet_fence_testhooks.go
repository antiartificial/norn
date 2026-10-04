package etcdstore

// TEST HOOKS ONLY. The exported methods in this file exist solely so the
// fence conformance harness (handler/fleet_conformance_etcd_test.go,
// implementing fleettest.FenceHarness.AgeHolder and Dispatch's StartedAt
// override) can backdate storage without real waits. Go's export_test.go
// cannot cross packages, and the repo's build-tag hook pattern
// (norn_test_crash_hooks) would require -tags on the mandated go-test-strict
// commands, so they live here instead. They bypass signing and every fence,
// lineage and CAS rule: no production package may call them (verify with
// `grep -rn 'AgeFleetTargetHolder\|SetFleetCapacityPlanStartedAt\|ReleaseFleetTargetFence\|AbandonFleetTargetPlan' --include='*.go' . | grep -v _test.go`,
// which must list only this file).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"norn/v2/api/fleet/lifecycle"
)

// AgeFleetTargetHolder is a test-only fixture mutation
// (fleettest.FenceHarness.AgeHolder): it subtracts by from planID's dispatch
// preparation and binding CreatedAt and from every one of its attempts'
// HeartbeatExpiresAt, so lifecycle.AbandonMinimumAge can be exercised
// without a real wait. It must never be called from a production path.
func (s *V3OperationStore) AgeFleetTargetHolder(ctx context.Context, planID string, by time.Duration) error {
	if preparationResp, err := s.kv.Get(ctx, s.fleetGitHubDispatchPreparationKey(planID)); err != nil {
		return err
	} else if len(preparationResp.Kvs) == 1 {
		var preparation v3FleetGitHubDispatchPreparation
		if err := decodeV3Record(preparationResp.Kvs[0].Value, &preparation); err != nil {
			return err
		}
		preparation.CreatedAt = preparation.CreatedAt.Add(-by)
		encoded, err := json.Marshal(preparation)
		if err != nil {
			return err
		}
		if _, err := s.kv.Put(ctx, s.fleetGitHubDispatchPreparationKey(planID), string(encoded)); err != nil {
			return err
		}
	}
	if bindingResp, err := s.kv.Get(ctx, s.fleetRunnerDispatchKey(planID)); err != nil {
		return err
	} else if len(bindingResp.Kvs) == 1 {
		var binding v3FleetRunnerDispatch
		if err := decodeV3Record(bindingResp.Kvs[0].Value, &binding); err != nil {
			return err
		}
		binding.CreatedAt = binding.CreatedAt.Add(-by)
		encoded, err := json.Marshal(binding)
		if err != nil {
			return err
		}
		if _, err := s.kv.Put(ctx, s.fleetRunnerDispatchKey(planID), string(encoded)); err != nil {
			return err
		}
	}
	attempts, _, err := s.listFleetRunnerAttempts(ctx, planID)
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		attempt.HeartbeatExpiresAt = attempt.HeartbeatExpiresAt.Add(-by)
		encoded, err := json.Marshal(attempt)
		if err != nil {
			return err
		}
		if _, err := s.kv.Put(ctx, s.fleetRunnerAttemptKey(planID, attempt.ID), string(encoded)); err != nil {
			return err
		}
	}
	return nil
}

// SetFleetCapacityPlanStartedAt is a test-only fixture mutation
// (fleettest.FenceHarness.Dispatch): it overwrites planID's capacity-plan
// operation StartedAt, so lifecycle.DecideAcquire's revalidation check
// (M4/Q2) can be exercised against a chosen timestamp instead of a real
// wait. It must never be called from a production path.
func (s *V3OperationStore) SetFleetCapacityPlanStartedAt(ctx context.Context, planID string, startedAt time.Time) error {
	record, _, err := s.load(ctx, planID)
	if err != nil {
		return err
	}
	record.Operation.StartedAt = startedAt
	encoded, err := json.Marshal(v3Record{Operation: record.Operation})
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, s.opKey(planID), string(encoded))
	return err
}

// ReleaseFleetTargetFence is a test-only, unsigned entry into the internal
// release (planFleetTargetRelease) that the signed fleet.target.fence-release
// acceptance calls: fence harnesses use it without minting a signed
// operation. It must never be called from a production path.
func (s *V3OperationStore) ReleaseFleetTargetFence(ctx context.Context, targetID string, expectedGeneration int64, mode lifecycle.ReleaseMode, proof *lifecycle.TerminalProof, operationID string, now time.Time) (lifecycle.FenceFacts, error) {
	plan, err := s.planFleetTargetRelease(ctx, targetID, expectedGeneration, mode, proof, operationID, now)
	if err != nil {
		return lifecycle.FenceFacts{}, err
	}
	if err := s.commitFleetTargetMutationPlanForTest(ctx, plan); err != nil {
		return lifecycle.FenceFacts{}, err
	}
	fence, _, err := s.GetFleetTargetFence(ctx, targetID)
	return fence, err
}

// AbandonFleetTargetPlan is the unsigned test-only counterpart for the
// internal H7 abandon (planFleetTargetAbandon). It must never be called from
// a production path.
func (s *V3OperationStore) AbandonFleetTargetPlan(ctx context.Context, planID, nonceSHA256, cluster, environment, operationID, listingSnapshotSHA256 string, now time.Time) error {
	plan, err := s.planFleetTargetAbandon(ctx, planID, nonceSHA256, cluster, environment, operationID, listingSnapshotSHA256, now)
	if err != nil {
		return err
	}
	return s.commitFleetTargetMutationPlanForTest(ctx, plan)
}

func (s *V3OperationStore) commitFleetTargetMutationPlanForTest(ctx context.Context, plan fleetTargetMutationPlan) error {
	txn, err := s.kv.Txn(ctx).If(plan.compares...).Then(plan.puts...).Commit()
	if err != nil {
		return err
	}
	if !txn.Succeeded {
		return fmt.Errorf("fleet target mutation changed concurrently")
	}
	return nil
}
