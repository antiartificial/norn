package etcdstore

// An offline attempt is a durable admission record on the already-held Fleet
// target fence. This file creates no runner token, provider grant, or effect
// route. It is intentionally separate from the GitHub Actions attempt keys.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/offline"
)

var ErrOfflineAttemptConflict = errors.New("offline attempt admission changed or is already occupied")
var ErrOfflineAttemptCorrupt = errors.New("offline attempt record is invalid")
var ErrOfflineCheckpointConflict = errors.New("offline checkpoint changed or fence is stale")
var ErrOfflineCheckpointInvalid = errors.New("offline checkpoint phase or evidence is invalid")

var offlineEvidenceDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *V3OperationStore) offlineAttemptKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-attempts/" + planID
}

func (s *V3OperationStore) offlineCheckpointKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-checkpoints/" + planID
}

func (s *V3OperationStore) offlineCheckpointEventKey(planID string, sequence uint64) string {
	return fmt.Sprintf("%s/v3/fleet-offline-checkpoint-events/%s/%020d", s.prefix, planID, sequence)
}

func (s *V3OperationStore) GetOfflineCheckpoint(ctx context.Context, planID string) (offline.Checkpoint, error) {
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineCheckpointKey(planID)),
		clientv3.OpGet(s.offlineCheckpointEventKey(planID, 1)),
	).Commit()
	if err != nil {
		return offline.Checkpoint{}, err
	}
	if len(response.Responses) != 2 || response.Responses[0].GetResponseRange() == nil || response.Responses[1].GetResponseRange() == nil {
		return offline.Checkpoint{}, ErrOfflineAttemptCorrupt
	}
	head := response.Responses[0].GetResponseRange().Kvs
	event := response.Responses[1].GetResponseRange().Kvs
	var headRaw, eventRaw []byte
	if len(head) == 1 {
		headRaw = head[0].Value
	}
	if len(event) == 1 {
		eventRaw = event[0].Value
	}
	return decodeOfflineCheckpointPair(headRaw, eventRaw, planID)
}

func decodeOfflineCheckpointPair(headRaw, eventRaw []byte, planID string) (offline.Checkpoint, error) {
	if headRaw == nil && eventRaw == nil {
		return offline.Checkpoint{}, ErrNotFound
	}
	if headRaw == nil || eventRaw == nil || !bytes.Equal(headRaw, eventRaw) {
		return offline.Checkpoint{}, ErrOfflineAttemptCorrupt
	}
	var checkpoint offline.Checkpoint
	if err := decodeV3Record(headRaw, &checkpoint); err != nil ||
		checkpoint.SchemaVersion != offline.CheckpointSchema || checkpoint.PlanID != planID ||
		uuid.Validate(checkpoint.AttemptID) != nil || checkpoint.Sequence != 1 || checkpoint.Phase != "prechange_observed" ||
		!offlineEvidenceDigest.MatchString(checkpoint.EvidenceSHA256) || checkpoint.AuthorityEpoch < 1 ||
		checkpoint.FenceGeneration == 0 || checkpoint.RecordedAt.IsZero() {
		return offline.Checkpoint{}, ErrOfflineAttemptCorrupt
	}
	return checkpoint, nil
}

// RecordOfflineCheckpoint admits only an observation phase. The binding,
// attempt, owner consumption, registry, target fence, authority epoch and
// protected-lane absence are compared in one transaction. The event is
// append-only; the head is a CAS cursor. A lost response must be reconciled
// by reading the exact event, never by retrying a changed request.
func (s *V3OperationStore) RecordOfflineCheckpoint(ctx context.Context, planID, attemptID, authenticatedPrincipal, phase, evidenceSHA256 string, expectedSequence uint64, now time.Time) (offline.Checkpoint, error) {
	if s == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || authenticatedPrincipal == "" ||
		phase != "prechange_observed" || !offlineEvidenceDigest.MatchString(evidenceSHA256) || expectedSequence != 0 {
		return offline.Checkpoint{}, ErrOfflineCheckpointInvalid
	}
	attemptResponse, err := s.kv.Get(ctx, s.offlineAttemptKey(planID))
	if err != nil {
		return offline.Checkpoint{}, err
	}
	if len(attemptResponse.Kvs) != 1 {
		return offline.Checkpoint{}, ErrNotFound
	}
	attempt, err := decodeOfflineAttempt(attemptResponse.Kvs[0].Value, planID)
	if err != nil {
		return offline.Checkpoint{}, err
	}
	if attempt.ID != attemptID || attempt.PrincipalSubject != authenticatedPrincipal {
		return offline.Checkpoint{}, offline.ErrAttemptMismatch
	}
	bindingResponse, err := s.kv.Get(ctx, s.offlineDispatchKey(planID))
	if err != nil {
		return offline.Checkpoint{}, err
	}
	if len(bindingResponse.Kvs) != 1 {
		return offline.Checkpoint{}, ErrNotFound
	}
	binding, err := offline.DecodeBinding(bindingResponse.Kvs[0].Value)
	digest := sha256.Sum256(bindingResponse.Kvs[0].Value)
	if err != nil || binding.PlanID != planID || attempt.BindingSHA256 != hex.EncodeToString(digest[:]) ||
		binding.RunnerPrincipalSubject != authenticatedPrincipal || now.Before(binding.CreatedAt) || !now.Before(binding.ExpiresAt) {
		return offline.Checkpoint{}, offline.ErrAttemptMismatch
	}
	consumptionKey := s.offlineOwnerConsumptionKey(binding.OwnerAuthorizationID)
	consumption, err := s.kv.Get(ctx, consumptionKey)
	if err != nil {
		return offline.Checkpoint{}, err
	}
	if len(consumption.Kvs) != 1 || string(consumption.Kvs[0].Value) != planID+":"+binding.OwnerEnvelopeSHA256 {
		return offline.Checkpoint{}, offline.ErrAttemptMismatch
	}
	if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, planID); err != nil {
		return offline.Checkpoint{}, err
	} else if abandoned {
		return offline.Checkpoint{}, fleetHolderAbandonedError()
	}
	fenceContext, err := s.loadFleetTargetFenceContext(ctx, "tk-staging-nyc3", "staging", true)
	if err != nil {
		return offline.Checkpoint{}, err
	}
	fence := fenceContext.Fence
	if fenceContext.TargetID != binding.TargetID || !fence.Held || fence.HolderPlanID != planID ||
		fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 || uint64(fence.Generation) != binding.FenceGeneration ||
		fence.AuthorityEpoch != fenceContext.Epoch {
		return offline.Checkpoint{}, ErrOfflineCheckpointConflict
	}
	checkpoint := offline.Checkpoint{SchemaVersion: offline.CheckpointSchema, PlanID: planID, AttemptID: attemptID,
		Sequence: 1, Phase: phase, EvidenceSHA256: evidenceSHA256, AuthorityEpoch: fenceContext.Epoch,
		FenceGeneration: binding.FenceGeneration, RecordedAt: now.UTC()}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return offline.Checkpoint{}, err
	}
	committed, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.offlineAttemptKey(planID)), "=", attemptResponse.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineDispatchKey(planID)), "=", bindingResponse.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(consumptionKey), "=", consumption.Kvs[0].ModRevision),
		s.registryCompare(fenceContext),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(binding.TargetID)), "=", fenceContext.FenceRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", fenceContext.EpochRevision),
		s.abandonedCompare(planID),
		clientv3.Compare(clientv3.CreateRevision(s.fleetGitHubDispatchPreparationKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptPrefix(planID)).WithPrefix(), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineCheckpointKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineCheckpointEventKey(planID, 1)), "=", 0),
	).Then(clientv3.OpPut(s.offlineCheckpointKey(planID), string(encoded)),
		clientv3.OpPut(s.offlineCheckpointEventKey(planID, 1), string(encoded))).Commit()
	if err != nil {
		return offline.Checkpoint{}, err
	}
	if !committed.Succeeded {
		return offline.Checkpoint{}, ErrOfflineCheckpointConflict
	}
	return checkpoint, nil
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
