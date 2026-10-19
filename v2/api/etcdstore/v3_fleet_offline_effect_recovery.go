package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/fleet/offline"
)

// RecoverOfflineEffectGrant reads the whole authority decision in one etcd
// revision. It never issues or consumes a grant. The runner's credential,
// binding, owner consumption, registered target, held fence and epoch must
// still describe the exact attempt; a partial restore is not recovery.
func (s *V3OperationStore) RecoverOfflineEffectGrant(ctx context.Context, planID, attemptID string, identity OfflineRunnerIdentity) (offline.EffectGrant, error) {
	if s == nil || !identity.valid() {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	const (
		head = iota
		issued
		consumed
		attemptKey
		bindingKey
		owners
		registry
		clusterAlias
		environmentAlias
		targets
		fences
		epoch
		abandoned
		githubPrep
		runnerDispatch
		runnerAttempts
	)
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineEffectGrantKey(planID)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 1)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 2)),
		clientv3.OpGet(s.offlineAttemptKey(planID)),
		clientv3.OpGet(s.offlineDispatchKey(planID)),
		clientv3.OpGet(s.prefix+"/v3/fleet-offline-owner-consumptions/", clientv3.WithPrefix()),
		clientv3.OpGet(s.fleetTargetRegistryKey()),
		clientv3.OpGet(s.fleetTargetAliasKey("cluster:tk-staging-nyc3")),
		clientv3.OpGet(s.fleetTargetAliasKey("environment:staging")),
		clientv3.OpGet(s.prefix+"/v3/fleet-targets/", clientv3.WithPrefix()),
		clientv3.OpGet(s.prefix+"/v3/fleet-target-fences/", clientv3.WithPrefix()),
		clientv3.OpGet(s.fleetAuthorityEpochKey()),
		clientv3.OpGet(s.fleetTargetAbandonedKey(planID)),
		clientv3.OpGet(s.fleetGitHubDispatchPreparationKey(planID)),
		clientv3.OpGet(s.fleetRunnerDispatchKey(planID)),
		clientv3.OpGet(s.fleetRunnerAttemptPrefix(planID), clientv3.WithPrefix()),
	).Commit()
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if len(response.Responses) != 16 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	rows := func(i int) [][]byte {
		rangeResult := response.Responses[i].GetResponseRange()
		if rangeResult == nil {
			return nil
		}
		out := make([][]byte, 0, len(rangeResult.Kvs))
		for _, kv := range rangeResult.Kvs {
			out = append(out, kv.Value)
		}
		return out
	}
	if len(rows(head)) != 1 || len(rows(issued)) != 1 || len(rows(attemptKey)) != 1 || len(rows(bindingKey)) != 1 ||
		len(rows(registry)) != 1 || len(rows(clusterAlias)) != 1 || len(rows(epoch)) != 1 ||
		len(rows(abandoned)) != 0 || len(rows(githubPrep)) != 0 || len(rows(runnerDispatch)) != 0 || len(rows(runnerAttempts)) != 0 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	var consumedRaw []byte
	if len(rows(consumed)) == 1 {
		consumedRaw = rows(consumed)[0]
	} else if len(rows(consumed)) != 0 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	grant, err := decodeOfflineEffectGrantPair(rows(head)[0], rows(issued)[0], consumedRaw, planID)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	attempt, err := decodeOfflineAttempt(rows(attemptKey)[0], planID)
	if err != nil || grant.AttemptID != attemptID || attempt.ID != attemptID || attempt.PrincipalSubject != identity.Subject ||
		attempt.PrincipalTokenID != identity.TokenID || attempt.PrincipalDeviceID != identity.DeviceID ||
		attempt.BindingSHA256 != grant.BindingSHA256 || attempt.TargetID != grant.TargetID || attempt.FenceGeneration != grant.FenceGeneration {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	binding, err := offline.DecodeBinding(rows(bindingKey)[0])
	digest := sha256.Sum256(rows(bindingKey)[0])
	if err != nil || binding.SchemaVersion != offline.BindingSchemaV2 || binding.PlanID != planID ||
		binding.RunnerPrincipalSubject != identity.Subject || binding.TargetID != grant.TargetID ||
		binding.FenceGeneration != grant.FenceGeneration || hex.EncodeToString(digest[:]) != grant.BindingSHA256 ||
		binding.SourceCommit != grant.SourceCommit || binding.SavedPlanSHA256 != grant.SavedPlanSHA256 ||
		binding.EffectSHA256 != grant.EffectSHA256 || binding.Backend != grant.Backend ||
		binding.ProviderAccountUUID != grant.ProviderAccountUUID || binding.NodeName != grant.NodeName || binding.DropletID != grant.DropletID {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	ownerKey := s.offlineOwnerConsumptionKey(binding.OwnerAuthorizationID)
	ownerFound := false
	for _, kv := range response.Responses[owners].GetResponseRange().Kvs {
		if string(kv.Key) == ownerKey {
			if ownerFound || string(kv.Value) != planID+":"+binding.OwnerEnvelopeSHA256 {
				return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
			}
			ownerFound = true
		}
	}
	if !ownerFound {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	var reg v3FleetTargetRegistry
	if decodeV3Record(rows(registry)[0], &reg) != nil || reg.Generation < 1 || string(rows(clusterAlias)[0]) != grant.TargetID ||
		(len(rows(environmentAlias)) != 0 && (len(rows(environmentAlias)) != 1 || string(rows(environmentAlias)[0]) != grant.TargetID)) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	targetFound := false
	for _, kv := range response.Responses[targets].GetResponseRange().Kvs {
		if string(kv.Key) == s.fleetTargetKey(grant.TargetID) {
			var target FleetTarget
			if targetFound || decodeV3Record(kv.Value, &target) != nil || target.TargetID != grant.TargetID || target.ProviderAccount != grant.ProviderAccountUUID {
				return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
			}
			targetFound = true
		}
	}
	if !targetFound {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	epochNumber, err := strconv.ParseInt(strings.TrimSpace(string(rows(epoch)[0])), 10, 64)
	if err != nil || epochNumber < 1 || epochNumber != grant.AuthorityEpoch {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	fenceFound := false
	for _, kv := range response.Responses[fences].GetResponseRange().Kvs {
		if string(kv.Key) == s.fleetTargetFenceKey(grant.TargetID) {
			var fence v3FleetTargetFence
			if fenceFound || decodeV3Record(kv.Value, &fence) != nil || !fence.Held || fence.HolderPlanID != planID ||
				fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 || uint64(fence.Generation) != grant.FenceGeneration ||
				fence.AuthorityEpoch != grant.AuthorityEpoch {
				return offline.EffectGrant{}, offline.ErrEffectProofMismatch
			}
			fenceFound = true
		}
	}
	if !fenceFound {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	return grant, nil
}
