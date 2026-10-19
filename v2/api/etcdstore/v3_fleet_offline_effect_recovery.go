package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/fleet/lifecycle"
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
	// This probe names exact keys only. It is not evidence: the following
	// transaction reads every row at this same revision, and fails closed if
	// the revision has been compacted before the read.
	probe, err := s.kv.Get(ctx, s.offlineDispatchKey(planID))
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if len(probe.Kvs) != 1 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	candidate, err := offline.DecodeBinding(probe.Kvs[0].Value)
	if err != nil || candidate.PlanID != planID || candidate.OwnerAuthorizationID == "" || candidate.TargetID == "" {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	revision := probe.Header.Revision
	if revision < 1 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	return s.recoverOfflineEffectGrantAtRevision(ctx, planID, attemptID, identity, candidate, revision)
}

func (s *V3OperationStore) recoverOfflineEffectGrantAtRevision(ctx context.Context, planID, attemptID string, identity OfflineRunnerIdentity, candidate offline.Binding, revision int64) (offline.EffectGrant, error) {
	if s == nil || !identity.valid() || revision < 1 {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	getAt := func(key string, options ...clientv3.OpOption) clientv3.Op {
		return clientv3.OpGet(key, append(options, clientv3.WithRev(revision))...)
	}
	const (
		head = iota
		issued
		consumed
		attemptKey
		bindingKey
		owner
		registry
		clusterAlias
		environmentAlias
		target
		fence
		epoch
		abandoned
		githubPrep
		runnerDispatch
		runnerAttempts
	)
	response, err := s.kv.Txn(ctx).Then(
		getAt(s.offlineEffectGrantKey(planID)),
		getAt(s.offlineEffectGrantEventKey(planID, 1)),
		getAt(s.offlineEffectGrantEventKey(planID, 2)),
		getAt(s.offlineAttemptKey(planID)),
		getAt(s.offlineDispatchKey(planID)),
		getAt(s.offlineOwnerConsumptionKey(candidate.OwnerAuthorizationID)),
		getAt(s.fleetTargetRegistryKey()),
		getAt(s.fleetTargetAliasKey("cluster:tk-staging-nyc3")),
		getAt(s.fleetTargetAliasKey("environment:staging")),
		getAt(s.fleetTargetKey(candidate.TargetID)),
		getAt(s.fleetTargetFenceKey(candidate.TargetID)),
		getAt(s.fleetAuthorityEpochKey()),
		getAt(s.fleetTargetAbandonedKey(planID)),
		getAt(s.fleetGitHubDispatchPreparationKey(planID)),
		getAt(s.fleetRunnerDispatchKey(planID)),
		getAt(s.fleetRunnerAttemptPrefix(planID), clientv3.WithPrefix()),
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
		len(rows(owner)) != 1 || len(rows(registry)) != 1 || len(rows(clusterAlias)) != 1 || len(rows(environmentAlias)) != 1 ||
		len(rows(target)) != 1 || len(rows(fence)) != 1 || len(rows(epoch)) != 1 ||
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
	if binding.OwnerAuthorizationID != candidate.OwnerAuthorizationID || binding.TargetID != candidate.TargetID ||
		string(rows(owner)[0]) != planID+":"+binding.OwnerEnvelopeSHA256 {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	var reg v3FleetTargetRegistry
	if decodeV3Record(rows(registry)[0], &reg) != nil || reg.Generation < 1 || string(rows(clusterAlias)[0]) != grant.TargetID ||
		string(rows(environmentAlias)[0]) != grant.TargetID {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	var targetRecord FleetTarget
	if decodeV3Record(rows(target)[0], &targetRecord) != nil || targetRecord.TargetID != grant.TargetID ||
		targetRecord.Provider != "digitalocean" || targetRecord.ProviderAccount != grant.ProviderAccountUUID ||
		targetRecord.StateBackend != "s3://"+binding.Backend.Bucket+"/"+binding.Backend.Key ||
		!slices.Contains(targetRecord.Aliases, "cluster:tk-staging-nyc3") ||
		!slices.Contains(targetRecord.Aliases, "environment:staging") {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	_, canonicalTargetID, err := lifecycle.CanonicalTarget(lifecycle.TargetIdentity{
		Provider: targetRecord.Provider, ProviderAccount: targetRecord.ProviderAccount, StateBackend: targetRecord.StateBackend,
	})
	if err != nil || canonicalTargetID != grant.TargetID {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	epochNumber, err := strconv.ParseInt(strings.TrimSpace(string(rows(epoch)[0])), 10, 64)
	if err != nil || epochNumber < 1 || epochNumber != grant.AuthorityEpoch {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	var fenceRecord v3FleetTargetFence
	if decodeV3Record(rows(fence)[0], &fenceRecord) != nil || !fenceRecord.Held || fenceRecord.HolderPlanID != planID ||
		fenceRecord.HolderNonceSHA256 != binding.DispatchNonceSHA256 || uint64(fenceRecord.Generation) != grant.FenceGeneration ||
		fenceRecord.AuthorityEpoch != grant.AuthorityEpoch {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	return grant, nil
}
