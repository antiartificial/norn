package etcdstore

// This file records an inert, one-use offline grant lifecycle. It has no
// provider client or credential and neither transition authorizes an effect.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/offline"
)

var ErrOfflineEffectGrantConflict = errors.New("offline effect grant changed or its authority is stale")

func (s *V3OperationStore) offlineEffectGrantKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-effect-grants/" + planID
}

func (s *V3OperationStore) offlineEffectGrantEventKey(planID string, sequence uint64) string {
	return fmt.Sprintf("%s/v3/fleet-offline-effect-grant-events/%s/%020d", s.prefix, planID, sequence)
}

func decodeOfflineEffectGrantPair(headRaw, issuedRaw, consumedRaw []byte, planID string) (offline.EffectGrant, error) {
	if len(headRaw) == 0 && len(issuedRaw) == 0 && len(consumedRaw) == 0 {
		return offline.EffectGrant{}, ErrNotFound
	}
	if len(headRaw) == 0 || len(issuedRaw) == 0 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	var head, issued offline.EffectGrant
	if decodeV3Record(headRaw, &head) != nil || decodeV3Record(issuedRaw, &issued) != nil ||
		head.SchemaVersion != offline.EffectGrantSchema || issued.SchemaVersion != offline.EffectGrantSchema ||
		head.PlanID != planID || issued.PlanID != planID || uuid.Validate(head.ID) != nil ||
		head.ID != issued.ID || head.AttemptID != issued.AttemptID || head.BindingSHA256 != issued.BindingSHA256 ||
		head.RunnerSubject != issued.RunnerSubject || head.RunnerTokenID != issued.RunnerTokenID ||
		head.RunnerDeviceID != issued.RunnerDeviceID || head.AuthorizesEffect || issued.AuthorizesEffect ||
		head.IssuedAt.IsZero() || !head.ExpiresAt.After(head.IssuedAt) ||
		head.ExpiresAt.Sub(head.IssuedAt) > time.Minute {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	if len(consumedRaw) == 0 {
		if head.Phase != "issued" || head.ConsumedAt != nil || !bytes.Equal(headRaw, issuedRaw) {
			return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
		}
	} else if head.Phase != "consumed" || head.ConsumedAt == nil ||
		head.ConsumedAt.Before(head.IssuedAt) || !head.ConsumedAt.Before(head.ExpiresAt) ||
		!bytes.Equal(headRaw, consumedRaw) {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	base := head
	base.Phase, base.ConsumedAt = "issued", nil
	if !reflect.DeepEqual(base, issued) {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	return head, nil
}

func (s *V3OperationStore) GetOfflineEffectGrant(ctx context.Context, planID string) (offline.EffectGrant, error) {
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineEffectGrantKey(planID)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 1)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 2)),
	).Commit()
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if len(response.Responses) != 3 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	var raw [3][]byte
	for i := range raw {
		rows := response.Responses[i].GetResponseRange().Kvs
		if len(rows) == 1 {
			raw[i] = rows[0].Value
		} else if len(rows) > 1 {
			return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
		}
	}
	return decodeOfflineEffectGrantPair(raw[0], raw[1], raw[2], planID)
}

type offlineGrantState struct {
	binding                                         offline.Binding
	attempt                                         offline.Attempt
	bindingRevision, attemptRevision, ownerRevision int64
	planRevision                                    int64
	fence                                           fleetTargetFenceContext
}

func (s *V3OperationStore) loadOfflineGrantState(ctx context.Context, planID, attemptID string,
	runner OfflineRunnerProof, now time.Time) (offlineGrantState, error) {
	var state offlineGrantState
	if s == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil ||
		!validOfflineRunnerProof(runner, now) {
		return state, offline.ErrEffectGrantMismatch
	}
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineDispatchKey(planID)),
		clientv3.OpGet(s.offlineAttemptKey(planID)),
	).Commit()
	if err != nil {
		return state, err
	}
	if len(response.Responses) != 2 {
		return state, ErrOfflineAttemptCorrupt
	}
	bindings := response.Responses[0].GetResponseRange().Kvs
	attempts := response.Responses[1].GetResponseRange().Kvs
	if len(bindings) != 1 || len(attempts) != 1 {
		return state, ErrOfflineAttemptCorrupt
	}
	binding, err := offline.DecodeBinding(bindings[0].Value)
	if err != nil || binding.PlanID != planID || binding.RunnerPrincipalSubject != runner.Subject ||
		now.Before(binding.CreatedAt) || !now.Before(binding.ExpiresAt) {
		return state, offline.ErrEffectGrantMismatch
	}
	attempt, err := decodeOfflineAttempt(attempts[0].Value, planID)
	digest := sha256.Sum256(bindings[0].Value)
	if err != nil || attempt.ID != attemptID || attempt.PrincipalSubject != runner.Subject ||
		attempt.PrincipalTokenID != runner.TokenID || attempt.PrincipalDeviceID != runner.DeviceID ||
		attempt.BindingSHA256 != hex.EncodeToString(digest[:]) || attempt.TargetID != binding.TargetID ||
		attempt.FenceGeneration != binding.FenceGeneration {
		return state, offline.ErrEffectGrantMismatch
	}
	owner, err := s.kv.Get(ctx, s.offlineOwnerConsumptionKey(binding.OwnerAuthorizationID))
	if err != nil {
		return state, err
	}
	if len(owner.Kvs) != 1 || string(owner.Kvs[0].Value) != planID+":"+binding.OwnerEnvelopeSHA256 {
		return state, offline.ErrEffectGrantMismatch
	}
	plan, planRevision, err := s.load(ctx, planID)
	var typed fleet.CapacityPlan
	if err != nil || !validOfflineContractionPlan(plan.Operation, planID, &typed) {
		return state, offline.ErrEffectGrantMismatch
	}
	if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, planID); err != nil {
		return state, err
	} else if abandoned {
		return state, fleetHolderAbandonedError()
	}
	fence, err := s.loadFleetTargetFenceContext(ctx, "tk-staging-nyc3", "staging", true)
	if err != nil {
		return state, err
	}
	if fence.TargetID != binding.TargetID || !fence.Fence.Held || fence.Fence.HolderPlanID != planID ||
		fence.Fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 ||
		uint64(fence.Fence.Generation) != binding.FenceGeneration ||
		fence.Fence.AuthorityEpoch != fence.Epoch {
		return state, ErrOfflineEffectGrantConflict
	}
	state = offlineGrantState{binding: binding, attempt: attempt, bindingRevision: bindings[0].ModRevision,
		attemptRevision: attempts[0].ModRevision, ownerRevision: owner.Kvs[0].ModRevision,
		planRevision: planRevision, fence: fence}
	return state, nil
}

func (s *V3OperationStore) offlineGrantStateCompares(state offlineGrantState, runner OfflineRunnerProof) []clientv3.Cmp {
	planID := state.binding.PlanID
	return []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.offlineDispatchKey(planID)), "=", state.bindingRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineAttemptKey(planID)), "=", state.attemptRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineOwnerConsumptionKey(state.binding.OwnerAuthorizationID)), "=", state.ownerRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(planID)), "=", state.planRevision),
		s.registryCompare(state.fence),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(state.binding.TargetID)), "=", state.fence.FenceRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", state.fence.EpochRevision),
		s.abandonedCompare(planID),
		clientv3.Compare(clientv3.CreateRevision(s.fleetGitHubDispatchPreparationKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptPrefix(planID)).WithPrefix(), "=", 0),
		clientv3.Compare(clientv3.ModRevision(s.prefix+"/auth/device/"+runner.DeviceID), "=", runner.DeviceRevision),
		clientv3.Compare(clientv3.ModRevision(s.prefix+"/auth/token/"+runner.TokenID), "=", runner.TokenRevision),
	}
}

// IssueOfflineEffectGrant persists an inert issue event. A future effect lane
// must add its own independent effect-time evidence and credential boundary.
func (s *V3OperationStore) IssueOfflineEffectGrant(ctx context.Context, planID, attemptID string,
	runner OfflineRunnerProof, now time.Time) (offline.EffectGrant, error) {
	state, err := s.loadOfflineGrantState(ctx, planID, attemptID, runner, now)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	expires := now.Add(time.Minute)
	if state.binding.ExpiresAt.Before(expires) {
		expires = state.binding.ExpiresAt
	}
	if runner.TokenExpiresAt.Before(expires) {
		expires = runner.TokenExpiresAt
	}
	if !expires.After(now) {
		return offline.EffectGrant{}, offline.ErrEffectGrantMismatch
	}
	b := state.binding
	grant := offline.EffectGrant{SchemaVersion: offline.EffectGrantSchema, ID: uuid.NewString(),
		PlanID: planID, AttemptID: attemptID, BindingSHA256: state.attempt.BindingSHA256,
		TargetID: b.TargetID, FenceGeneration: b.FenceGeneration, AuthorityEpoch: state.fence.Epoch,
		OwnerAuthorizationID: b.OwnerAuthorizationID, SourceCommit: b.SourceCommit,
		SavedPlanSHA256: b.SavedPlanSHA256, EffectSHA256: b.EffectSHA256, Backend: b.Backend,
		ProviderAccountUUID: b.ProviderAccountUUID, NodeName: b.NodeName, DropletID: b.DropletID,
		RunnerSubject: runner.Subject, RunnerTokenID: runner.TokenID, RunnerDeviceID: runner.DeviceID,
		Phase: "issued", AuthorizesEffect: false, IssuedAt: now.UTC(), ExpiresAt: expires.UTC()}
	raw, err := json.Marshal(grant)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	compares := append(s.offlineGrantStateCompares(state, runner),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectGrantKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectGrantEventKey(planID, 1)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectGrantEventKey(planID, 2)), "=", 0))
	committed, err := s.kv.Txn(ctx).If(compares...).Then(
		clientv3.OpPut(s.offlineEffectGrantKey(planID), string(raw)),
		clientv3.OpPut(s.offlineEffectGrantEventKey(planID, 1), string(raw)),
	).Commit()
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if !committed.Succeeded {
		return offline.EffectGrant{}, ErrOfflineEffectGrantConflict
	}
	return grant, nil
}

// ConsumeOfflineEffectGrant records the one-use transition before any future
// effect implementation. It performs no provider call and returns no secret.
func (s *V3OperationStore) ConsumeOfflineEffectGrant(ctx context.Context, planID, attemptID, grantID string,
	runner OfflineRunnerProof, now time.Time) (offline.EffectGrant, error) {
	if uuid.Validate(grantID) != nil {
		return offline.EffectGrant{}, offline.ErrEffectGrantMismatch
	}
	state, err := s.loadOfflineGrantState(ctx, planID, attemptID, runner, now)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineEffectGrantKey(planID)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 1)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 2)),
	).Commit()
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if len(response.Responses) != 3 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	var raw [3][]byte
	for i := range raw {
		rows := response.Responses[i].GetResponseRange().Kvs
		if len(rows) == 1 {
			raw[i] = rows[0].Value
		}
	}
	grant, err := decodeOfflineEffectGrantPair(raw[0], raw[1], raw[2], planID)
	if err != nil || grant.Phase != "issued" || grant.ID != grantID || grant.AttemptID != attemptID ||
		grant.BindingSHA256 != state.attempt.BindingSHA256 || grant.RunnerSubject != runner.Subject ||
		grant.RunnerTokenID != runner.TokenID || grant.RunnerDeviceID != runner.DeviceID ||
		grant.AuthorityEpoch != state.fence.Epoch || grant.AuthorizesEffect ||
		now.Before(grant.IssuedAt) || !now.Before(grant.ExpiresAt) {
		return offline.EffectGrant{}, offline.ErrEffectGrantMismatch
	}
	consumed := now.UTC()
	grant.Phase, grant.ConsumedAt = "consumed", &consumed
	encoded, err := json.Marshal(grant)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	compares := append(s.offlineGrantStateCompares(state, runner),
		clientv3.Compare(clientv3.ModRevision(s.offlineEffectGrantKey(planID)), "=", response.Responses[0].GetResponseRange().Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineEffectGrantEventKey(planID, 1)), "=", response.Responses[1].GetResponseRange().Kvs[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectGrantEventKey(planID, 2)), "=", 0))
	committed, err := s.kv.Txn(ctx).If(compares...).Then(
		clientv3.OpPut(s.offlineEffectGrantKey(planID), string(encoded)),
		clientv3.OpPut(s.offlineEffectGrantEventKey(planID, 2), string(encoded)),
	).Commit()
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if !committed.Succeeded {
		return offline.EffectGrant{}, ErrOfflineEffectGrantConflict
	}
	return grant, nil
}

// RecoverOfflineEffectGrant is read-only. It returns a transition only while
// the same credential, owner reservation and shared target fence remain live.
func (s *V3OperationStore) RecoverOfflineEffectGrant(ctx context.Context, planID, attemptID string,
	runner OfflineRunnerProof, now time.Time) (offline.EffectGrant, error) {
	state, err := s.loadOfflineGrantState(ctx, planID, attemptID, runner, now)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	grant, err := s.GetOfflineEffectGrant(ctx, planID)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if grant.AttemptID != attemptID || grant.BindingSHA256 != state.attempt.BindingSHA256 ||
		grant.TargetID != state.binding.TargetID || grant.FenceGeneration != state.binding.FenceGeneration ||
		grant.AuthorityEpoch != state.fence.Epoch || grant.OwnerAuthorizationID != state.binding.OwnerAuthorizationID ||
		grant.SourceCommit != state.binding.SourceCommit || grant.SavedPlanSHA256 != state.binding.SavedPlanSHA256 ||
		grant.EffectSHA256 != state.binding.EffectSHA256 || grant.Backend != state.binding.Backend ||
		grant.ProviderAccountUUID != state.binding.ProviderAccountUUID || grant.NodeName != state.binding.NodeName ||
		grant.DropletID != state.binding.DropletID || grant.RunnerSubject != runner.Subject ||
		grant.RunnerTokenID != runner.TokenID || grant.RunnerDeviceID != runner.DeviceID || grant.AuthorizesEffect {
		return offline.EffectGrant{}, offline.ErrEffectGrantMismatch
	}
	return grant, nil
}
