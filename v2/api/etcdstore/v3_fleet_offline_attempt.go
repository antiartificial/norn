package etcdstore

// An offline attempt is a durable admission record on the already-held Fleet
// target fence. This file creates no runner token, provider grant, or effect
// route. It is intentionally separate from the GitHub Actions attempt keys.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/offline"
)

var ErrOfflineAttemptConflict = errors.New("offline attempt admission changed or is already occupied")
var ErrOfflineAttemptCorrupt = errors.New("offline attempt record is invalid")

func (s *V3OperationStore) offlineAttemptKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-attempts/" + planID
}

func (s *V3OperationStore) GetOfflineAttempt(ctx context.Context, planID string) (offline.Attempt, error) {
	response, err := s.kv.Get(ctx, s.offlineAttemptKey(planID))
	if err != nil {
		return offline.Attempt{}, err
	}
	if len(response.Kvs) != 1 {
		return offline.Attempt{}, ErrNotFound
	}
	return decodeOfflineAttempt(response.Kvs[0].Value, planID)
}

func decodeOfflineAttempt(raw []byte, planID string) (offline.Attempt, error) {
	var attempt offline.Attempt
	if err := decodeV3Record(raw, &attempt); err != nil ||
		attempt.SchemaVersion != offline.AttemptSchema || attempt.PlanID != planID ||
		attempt.ID == "" || attempt.Status != "queued" || attempt.CurrentPhase != "prechange_verified" {
		return offline.Attempt{}, ErrOfflineAttemptCorrupt
	}
	return attempt, nil
}

// AdmitOfflineAttempt requires an authenticated offline principal supplied by
// the future HTTP boundary; a caller-supplied claim cannot choose that value.
// It binds one queued attempt to the immutable reservation and its current
// shared fence in one etcd Txn. No retry/recovery or effect capability exists.
func (s *V3OperationStore) AdmitOfflineAttempt(ctx context.Context, claim offline.AttemptClaim,
	authenticatedPrincipal, attemptID string, now time.Time) (offline.Attempt, error) {
	if s == nil || uuid.Validate(attemptID) != nil || authenticatedPrincipal == "" ||
		claim.RunnerPrincipalSubject != authenticatedPrincipal {
		return offline.Attempt{}, offline.ErrAttemptMismatch
	}
	bindingResponse, err := s.kv.Get(ctx, s.offlineDispatchKey(claim.PlanID))
	if err != nil {
		return offline.Attempt{}, err
	}
	if len(bindingResponse.Kvs) != 1 {
		return offline.Attempt{}, ErrNotFound
	}
	binding, err := offline.DecodeBinding(bindingResponse.Kvs[0].Value)
	if err != nil || binding.MatchAttempt(claim, now) != nil {
		return offline.Attempt{}, offline.ErrAttemptMismatch
	}
	consumptionKey := s.offlineOwnerConsumptionKey(binding.OwnerAuthorizationID)
	consumption, err := s.kv.Get(ctx, consumptionKey)
	if err != nil {
		return offline.Attempt{}, err
	}
	if len(consumption.Kvs) != 1 || string(consumption.Kvs[0].Value) != binding.PlanID+":"+binding.OwnerEnvelopeSHA256 {
		return offline.Attempt{}, offline.ErrAttemptMismatch
	}
	plan, planRevision, err := s.load(ctx, binding.PlanID)
	var typed fleet.CapacityPlan
	if err != nil || !validOfflineContractionPlan(plan.Operation, binding.PlanID, &typed) {
		return offline.Attempt{}, offline.ErrAttemptMismatch
	}
	if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, binding.PlanID); err != nil {
		return offline.Attempt{}, err
	} else if abandoned {
		return offline.Attempt{}, fleetHolderAbandonedError()
	}
	fenceContext, err := s.loadFleetTargetFenceContext(ctx, "tk-staging-nyc3", "staging", true)
	if err != nil {
		return offline.Attempt{}, err
	}
	fence := fenceContext.Fence
	if fenceContext.TargetID != binding.TargetID || !fence.Held ||
		fence.HolderPlanID != binding.PlanID || fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 ||
		uint64(fence.Generation) != binding.FenceGeneration || fence.AuthorityEpoch != fenceContext.Epoch {
		return offline.Attempt{}, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetExecutionOccupied}
	}
	digest := sha256.Sum256(bindingResponse.Kvs[0].Value)
	attempt := offline.Attempt{SchemaVersion: offline.AttemptSchema, ID: attemptID, PlanID: binding.PlanID,
		BindingSHA256: hex.EncodeToString(digest[:]), PrincipalSubject: authenticatedPrincipal,
		TargetID: binding.TargetID, FenceGeneration: binding.FenceGeneration,
		Status: "queued", CurrentPhase: "prechange_verified", CreatedAt: now.UTC()}
	encoded, err := json.Marshal(attempt)
	if err != nil {
		return offline.Attempt{}, err
	}
	committed, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.offlineDispatchKey(binding.PlanID)), "=", bindingResponse.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(consumptionKey), "=", consumption.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(binding.PlanID)), "=", planRevision),
		s.registryCompare(fenceContext),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(binding.TargetID)), "=", fenceContext.FenceRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", fenceContext.EpochRevision),
		s.abandonedCompare(binding.PlanID),
		clientv3.Compare(clientv3.CreateRevision(s.offlineAttemptKey(binding.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetGitHubDispatchPreparationKey(binding.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(binding.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptPrefix(binding.PlanID)).WithPrefix(), "=", 0),
	).Then(clientv3.OpPut(s.offlineAttemptKey(binding.PlanID), string(encoded))).Commit()
	if err != nil {
		return offline.Attempt{}, err
	} // indeterminate: GET, never retry blindly
	if !committed.Succeeded {
		return offline.Attempt{}, ErrOfflineAttemptConflict
	}
	return attempt, nil
}
