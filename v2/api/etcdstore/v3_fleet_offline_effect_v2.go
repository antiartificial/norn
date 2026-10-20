package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet/offline"
)

var ErrOfflineEffectV2Conflict = errors.New("offline effect v2 changed or its authority is stale")

func (s *V3OperationStore) offlineEffectV2Key(planID string) string {
	return s.prefix + "/v3/fleet-offline-effect-v2/" + planID
}

func (s *V3OperationStore) offlineEffectV2EventKey(planID string, sequence uint64) string {
	return fmt.Sprintf("%s/v3/fleet-offline-effect-v2-events/%s/%020d", s.prefix, planID, sequence)
}

type offlineEffectV2Read struct {
	grant          offline.EffectGrantV2
	revision       int64
	eventRevisions [4]int64
}

func (s *V3OperationStore) readOfflineEffectV2(ctx context.Context, planID string) (offlineEffectV2Read, error) {
	var out offlineEffectV2Read
	if uuid.Validate(planID) != nil {
		return out, offline.ErrEffectProofV2
	}
	ops := []clientv3.Op{clientv3.OpGet(s.offlineEffectV2Key(planID))}
	for i := uint64(1); i <= 4; i++ {
		ops = append(ops, clientv3.OpGet(s.offlineEffectV2EventKey(planID, i)))
	}
	response, err := s.kv.Txn(ctx).Then(ops...).Commit()
	if err != nil {
		return out, err
	}
	if len(response.Responses) != 5 {
		return out, ErrOfflineAttemptCorrupt
	}
	var raw [5][]byte
	for i := range raw {
		rows := response.Responses[i].GetResponseRange().Kvs
		if len(rows) > 1 {
			return out, ErrOfflineAttemptCorrupt
		}
		if len(rows) == 1 {
			raw[i] = rows[0].Value
			if i == 0 {
				out.revision = rows[0].ModRevision
			} else {
				out.eventRevisions[i-1] = rows[0].ModRevision
			}
		}
	}
	if len(raw[0]) == 0 && len(raw[1]) == 0 {
		return out, ErrNotFound
	}
	if len(raw[0]) == 0 || len(raw[1]) == 0 {
		return out, ErrOfflineAttemptCorrupt
	}
	var states []offline.EffectGrantV2
	for i := 1; i < len(raw); i++ {
		if len(raw[i]) == 0 {
			continue
		}
		if i > 1 && len(raw[i-1]) == 0 {
			return out, ErrOfflineAttemptCorrupt
		}
		var value offline.EffectGrantV2
		if decodeV3Record(raw[i], &value) != nil || value.Validate() != nil || value.PlanID != planID {
			return out, ErrOfflineAttemptCorrupt
		}
		states = append(states, value)
	}
	if len(states) == 0 || decodeV3Record(raw[0], &out.grant) != nil || out.grant.Validate() != nil || !reflect.DeepEqual(out.grant, states[len(states)-1]) {
		return offlineEffectV2Read{}, ErrOfflineAttemptCorrupt
	}
	phases := []string{"issued", "delete_committed", "delete_dispatched", "terminal_verified"}
	for i, state := range states {
		if state.Phase != phases[i] || state.ID != states[0].ID || state.AttemptID != states[0].AttemptID ||
			state.BindingSHA256 != states[0].BindingSHA256 || state.WriterFence != states[0].WriterFence ||
			state.TargetFenceGeneration != states[0].TargetFenceGeneration || state.AuthorityEpoch != states[0].AuthorityEpoch {
			return offlineEffectV2Read{}, ErrOfflineAttemptCorrupt
		}
		issued := state
		issued.Phase, issued.Commitment, issued.Dispatch, issued.Terminal = "issued", nil, nil, nil
		if !reflect.DeepEqual(issued, states[0]) ||
			(i >= 2 && !reflect.DeepEqual(state.Commitment, states[1].Commitment)) ||
			(i >= 3 && !reflect.DeepEqual(state.Dispatch, states[2].Dispatch)) {
			return offlineEffectV2Read{}, ErrOfflineAttemptCorrupt
		}
	}
	return out, nil
}

func (s *V3OperationStore) GetOfflineEffectGrantV2(ctx context.Context, planID string) (offline.EffectGrantV2, error) {
	read, err := s.readOfflineEffectV2(ctx, planID)
	return read.grant, err
}

func (s *V3OperationStore) IssueOfflineEffectGrantV2(ctx context.Context, planID, attemptID string, runner OfflineRunnerProof,
	observation offline.EffectObservationV2, now time.Time) (offline.EffectGrantV2, error) {
	state, err := s.loadOfflineGrantState(ctx, planID, attemptID, runner, now)
	if err != nil {
		return offline.EffectGrantV2{}, err
	}
	if state.binding.SchemaVersion != offline.BindingSchemaV2 {
		return offline.EffectGrantV2{}, offline.ErrEffectProofV2
	}
	verified, err := offline.VerifyEffectObservationV2(observation, state.binding, state.attempt, state.fence.Epoch, now)
	if err != nil {
		return offline.EffectGrantV2{}, offline.ErrEffectProofV2
	}
	observation, _ = verified.Match(state.binding, state.attempt, state.fence.Epoch, now)
	expires := now.Add(30 * time.Second)
	for _, limit := range []time.Time{state.binding.ExpiresAt, runner.TokenExpiresAt, observation.ValidUntil} {
		if limit.Before(expires) {
			expires = limit
		}
	}
	if !expires.After(now) {
		return offline.EffectGrantV2{}, offline.ErrEffectProofV2
	}
	grant := offline.EffectGrantV2{SchemaVersion: offline.EffectGrantSchemaV2, ID: uuid.NewString(), PlanID: planID,
		AttemptID: attemptID, BindingSHA256: state.attempt.BindingSHA256, TargetID: state.binding.TargetID,
		TargetFenceGeneration: state.binding.FenceGeneration, AuthorityEpoch: state.fence.Epoch,
		OwnerAuthorizationID: state.binding.OwnerAuthorizationID, RunnerSubject: runner.Subject,
		SourceCommit: state.binding.SourceCommit, SavedPlanSHA256: state.binding.SavedPlanSHA256,
		EffectSHA256: state.binding.EffectSHA256, Backend: state.binding.Backend,
		ProviderAccountUUID: state.binding.ProviderAccountUUID, NodeName: state.binding.NodeName, DropletID: state.binding.DropletID,
		RunnerTokenID: runner.TokenID, RunnerDeviceID: runner.DeviceID, WriterFence: observation.WriterFence,
		EffectEvidence:      offline.EffectEvidenceV2{SHA256: observation.EvidenceSHA256, ObservedAt: observation.ObservedAt.UTC(), ValidUntil: observation.ValidUntil.UTC()},
		DeleteRequestSHA256: offline.ExactDeleteRequestSHA256V2(), Phase: "issued", IssuedAt: now.UTC(), ExpiresAt: expires.UTC()}
	if grant.Validate() != nil {
		return offline.EffectGrantV2{}, offline.ErrEffectProofV2
	}
	raw, err := json.Marshal(grant)
	if err != nil {
		return offline.EffectGrantV2{}, err
	}
	compares := append(s.offlineGrantStateCompares(state, runner),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectV2Key(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectV2EventKey(planID, 1)), "=", 0))
	result, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(s.offlineEffectV2Key(planID), string(raw)),
		clientv3.OpPut(s.offlineEffectV2EventKey(planID, 1), string(raw))).Commit()
	if err != nil {
		return offline.EffectGrantV2{}, err
	}
	if !result.Succeeded {
		return offline.EffectGrantV2{}, ErrOfflineEffectV2Conflict
	}
	return grant, nil
}

func (s *V3OperationStore) loadCurrentEffectV2(ctx context.Context, planID, attemptID, grantID string, runner OfflineRunnerProof,
	now time.Time, requireFresh bool) (offlineGrantState, offlineEffectV2Read, error) {
	state, err := s.loadOfflineGrantState(ctx, planID, attemptID, runner, now)
	if err != nil {
		return state, offlineEffectV2Read{}, err
	}
	read, err := s.readOfflineEffectV2(ctx, planID)
	if err != nil {
		return state, read, err
	}
	g := read.grant
	if state.binding.SchemaVersion != offline.BindingSchemaV2 || g.ID != grantID || g.AttemptID != attemptID ||
		g.BindingSHA256 != state.attempt.BindingSHA256 || g.TargetID != state.binding.TargetID ||
		g.TargetFenceGeneration != state.binding.FenceGeneration || g.AuthorityEpoch != state.fence.Epoch ||
		g.OwnerAuthorizationID != state.binding.OwnerAuthorizationID || g.RunnerSubject != runner.Subject ||
		g.SourceCommit != state.binding.SourceCommit || g.SavedPlanSHA256 != state.binding.SavedPlanSHA256 ||
		g.EffectSHA256 != state.binding.EffectSHA256 || g.Backend != state.binding.Backend ||
		g.ProviderAccountUUID != state.binding.ProviderAccountUUID || g.NodeName != state.binding.NodeName || g.DropletID != state.binding.DropletID ||
		g.RunnerTokenID != runner.TokenID || g.RunnerDeviceID != runner.DeviceID || (requireFresh && !now.Before(g.ExpiresAt)) {
		return state, read, offline.ErrEffectProofV2
	}
	return state, read, nil
}

func (s *V3OperationStore) effectV2Transition(ctx context.Context, state offlineGrantState, current offlineEffectV2Read,
	runner OfflineRunnerProof, sequence uint64, next offline.EffectGrantV2) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	compares := append(s.offlineGrantStateCompares(state, runner),
		clientv3.Compare(clientv3.ModRevision(s.offlineEffectV2Key(next.PlanID)), "=", current.revision),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectV2EventKey(next.PlanID, sequence)), "=", 0))
	for i := uint64(1); i < sequence; i++ {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.offlineEffectV2EventKey(next.PlanID, i)), "=", current.eventRevisions[i-1]))
	}
	result, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(s.offlineEffectV2Key(next.PlanID), string(raw)),
		clientv3.OpPut(s.offlineEffectV2EventKey(next.PlanID, sequence), string(raw))).Commit()
	if err != nil {
		return err
	}
	if !result.Succeeded {
		return ErrOfflineEffectV2Conflict
	}
	return nil
}

func (s *V3OperationStore) CommitOfflineDeleteV2(ctx context.Context, planID, attemptID, grantID string, runner OfflineRunnerProof,
	commitmentSHA256, deleteRequestSHA256 string, now time.Time) (offline.EffectGrantV2, error) {
	state, read, err := s.loadCurrentEffectV2(ctx, planID, attemptID, grantID, runner, now, true)
	if err != nil {
		return offline.EffectGrantV2{}, err
	}
	if read.grant.Phase != "issued" || !offlineDigest(commitmentSHA256) || deleteRequestSHA256 != read.grant.DeleteRequestSHA256 {
		return offline.EffectGrantV2{}, offline.ErrDeleteCommitmentV2
	}
	next := read.grant
	next.Phase = "delete_committed"
	next.Commitment = &offline.DeleteCommitmentV2{SchemaVersion: offline.DeleteCommitmentSchemaV2, SHA256: commitmentSHA256,
		DeleteRequestSHA256: deleteRequestSHA256, CommittedAt: now.UTC()}
	if next.Validate() != nil {
		return offline.EffectGrantV2{}, offline.ErrDeleteCommitmentV2
	}
	if err := s.effectV2Transition(ctx, state, read, runner, 2, next); err != nil {
		return offline.EffectGrantV2{}, err
	}
	return next, nil
}

func offlineDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *V3OperationStore) DispatchOfflineDeleteV2(ctx context.Context, planID, attemptID, grantID string, runner OfflineRunnerProof,
	observation offline.EffectObservationV2, now time.Time) (offline.DeleteDispatchV2, error) {
	state, read, err := s.loadCurrentEffectV2(ctx, planID, attemptID, grantID, runner, now, true)
	if err != nil {
		return offline.DeleteDispatchV2{}, err
	}
	if read.grant.Phase != "delete_committed" || read.grant.Commitment == nil {
		return offline.DeleteDispatchV2{}, offline.ErrDeleteCommitmentV2
	}
	verified, verifyErr := offline.VerifyEffectObservationV2(observation, state.binding, state.attempt, state.fence.Epoch, now)
	if verifyErr != nil {
		return offline.DeleteDispatchV2{}, offline.ErrEffectProofV2
	}
	observation, _ = verified.Match(state.binding, state.attempt, state.fence.Epoch, now)
	if observation.WriterFence != read.grant.WriterFence {
		return offline.DeleteDispatchV2{}, offline.ErrEffectProofV2
	}
	dispatch := offline.DeleteDispatchV2{SchemaVersion: offline.DeleteDispatchSchemaV2, PlanID: planID, AttemptID: attemptID,
		GrantID: grantID, CommitmentSHA256: read.grant.Commitment.SHA256, TargetID: read.grant.TargetID,
		TargetFenceGeneration: read.grant.TargetFenceGeneration, WriterFence: read.grant.WriterFence,
		AuthorityEpoch: read.grant.AuthorityEpoch, RetryPolicy: offline.DeleteRetryPolicyV2, AdmittedAt: now.UTC()}
	next := read.grant
	next.Phase = "delete_dispatched"
	next.Dispatch = &dispatch
	if next.Validate() != nil {
		return offline.DeleteDispatchV2{}, offline.ErrEffectProofV2
	}
	if err := s.effectV2Transition(ctx, state, read, runner, 3, next); err != nil {
		return offline.DeleteDispatchV2{}, err
	}
	return dispatch, nil
}

func (s *V3OperationStore) RecordOfflineTerminalV2(ctx context.Context, planID, attemptID, grantID string, runner OfflineRunnerProof,
	observation offline.TerminalObservationV2, now time.Time) (offline.TerminalEvidenceV2, error) {
	state, read, err := s.loadCurrentEffectV2(ctx, planID, attemptID, grantID, runner, now, false)
	if err != nil {
		return offline.TerminalEvidenceV2{}, err
	}
	verified, verifyErr := offline.VerifyTerminalObservationV2(observation, read.grant, now)
	if verifyErr != nil {
		return offline.TerminalEvidenceV2{}, offline.ErrTerminalProofV2
	}
	observation, _ = verified.Match(read.grant, now)
	receipt := offline.TerminalEvidenceV2{SchemaVersion: offline.TerminalEvidenceSchemaV2, PlanID: planID,
		AttemptID: attemptID, GrantID: grantID, BindingSHA256: read.grant.BindingSHA256, TargetID: read.grant.TargetID,
		TargetFenceGeneration: read.grant.TargetFenceGeneration, WriterFence: read.grant.WriterFence,
		AuthorityEpoch: read.grant.AuthorityEpoch, ObservedAt: observation.ObservedAt.UTC(), RecordedAt: now.UTC(),
		EvidenceSHA256: observation.EvidenceSHA256, ProviderEvidenceSHA256: observation.ProviderEvidenceSHA256,
		BackendEvidenceSHA256: observation.BackendEvidenceSHA256, WorkloadEvidenceSHA256: observation.WorkloadEvidenceSHA256}
	next := read.grant
	next.Phase = "terminal_verified"
	next.Terminal = &receipt
	if next.Validate() != nil {
		return offline.TerminalEvidenceV2{}, offline.ErrTerminalProofV2
	}
	if err := s.effectV2Transition(ctx, state, read, runner, 4, next); err != nil {
		return offline.TerminalEvidenceV2{}, err
	}
	return receipt, nil
}

// RecoverOfflineEffectGrantV2 is read-only and never returns effect authority.
func (s *V3OperationStore) RecoverOfflineEffectGrantV2(ctx context.Context, planID, attemptID string, runner OfflineRunnerProof,
	now time.Time) (offline.EffectGrantV2, error) {
	state, err := s.loadOfflineGrantState(ctx, planID, attemptID, runner, now)
	if err != nil {
		return offline.EffectGrantV2{}, err
	}
	read, err := s.readOfflineEffectV2(ctx, planID)
	if err != nil {
		return offline.EffectGrantV2{}, err
	}
	response, err := s.kv.Txn(ctx).If(append(s.offlineGrantStateCompares(state, runner),
		clientv3.Compare(clientv3.ModRevision(s.offlineEffectV2Key(planID)), "=", read.revision))...).Then(
		clientv3.OpGet(s.offlineEffectV2Key(planID))).Commit()
	if err != nil {
		return offline.EffectGrantV2{}, err
	}
	if !response.Succeeded {
		return offline.EffectGrantV2{}, ErrOfflineEffectV2Conflict
	}
	if read.grant.AttemptID != attemptID || read.grant.BindingSHA256 != state.attempt.BindingSHA256 {
		return offline.EffectGrantV2{}, offline.ErrEffectProofV2
	}
	return read.grant, nil
}
