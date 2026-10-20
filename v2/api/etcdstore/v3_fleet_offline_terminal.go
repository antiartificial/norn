package etcdstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/offline"
)

var ErrOfflineTerminalConflict = errors.New("offline terminal evidence changed or fence is stale")

// offlineTerminalReobserver is private by design. A future production client
// must independently read provider, versioned state and every workload
// namespace; runner-supplied JSON cannot implement this contract over HTTP.
type offlineTerminalReobserver interface {
	reobserveTerminal(context.Context, offline.Binding, offline.Attempt, offline.EffectGrant) (offline.TerminalObservation, error)
}

// OfflineTerminalObserver is implemented only by server-owned read clients.
// HTTP callers cannot provide an observation or terminal proof.
type OfflineTerminalObserver interface {
	ObserveOfflineTerminal(context.Context, offline.Binding, offline.Attempt, offline.EffectGrant) (offline.TerminalObservation, error)
}

type offlineTerminalObserverAdapter struct{ OfflineTerminalObserver }

func (a offlineTerminalObserverAdapter) reobserveTerminal(ctx context.Context, b offline.Binding, attempt offline.Attempt, grant offline.EffectGrant) (offline.TerminalObservation, error) {
	return a.ObserveOfflineTerminal(ctx, b, attempt, grant)
}

// RecordOfflineTerminal records one terminal receipt from server-owned reads.
// It does not release the target fence.
func (s *V3OperationStore) RecordOfflineTerminal(ctx context.Context, planID, attemptID, grantID string, now time.Time, observer OfflineTerminalObserver) (offline.TerminalEvidence, error) {
	if observer == nil {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	return s.recordOfflineTerminal(ctx, planID, attemptID, grantID, now, offlineTerminalObserverAdapter{observer})
}

func (s *V3OperationStore) offlineTerminalKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-terminals/" + planID
}
func (s *V3OperationStore) offlineTerminalEventKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-terminal-events/" + planID + "/00000000000000000001"
}

func decodeOfflineTerminalPair(head, event []byte, planID string) (offline.TerminalEvidence, error) {
	if len(head) == 0 && len(event) == 0 {
		return offline.TerminalEvidence{}, ErrNotFound
	}
	if len(head) == 0 || len(event) == 0 || !bytes.Equal(head, event) {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	var receipt offline.TerminalEvidence
	if decodeV3Record(head, &receipt) != nil || receipt.SchemaVersion != offline.TerminalSchema ||
		receipt.PlanID != planID || uuid.Validate(receipt.AttemptID) != nil || uuid.Validate(receipt.GrantID) != nil ||
		receipt.TargetID == "" || receipt.FenceGeneration == 0 || receipt.AuthorityEpoch < 1 ||
		!offlineEvidenceDigest.MatchString(receipt.BindingSHA256) || !offlineEvidenceDigest.MatchString(receipt.EvidenceSHA256) ||
		!offlineEvidenceDigest.MatchString(receipt.ProviderEvidenceSHA256) || !offlineEvidenceDigest.MatchString(receipt.BackendEvidenceSHA256) ||
		!offlineEvidenceDigest.MatchString(receipt.WorkloadEvidenceSHA256) || receipt.ObservedAt.IsZero() ||
		receipt.RecordedAt.Before(receipt.ObservedAt) || receipt.Backend.VersionID == "" ||
		receipt.Backend.Serial == 0 || !offlineEvidenceDigest.MatchString(receipt.Backend.RawSHA256) {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	return receipt, nil
}

// GetOfflineTerminal derives lookup keys from a probe, then validates every
// authoritative row in one etcd read revision. The probe is never evidence:
// a concurrent rebind either appears in the snapshot and fails validation or
// is outside the snapshot and is not asserted by this read.
func (s *V3OperationStore) GetOfflineTerminal(ctx context.Context, planID string) (offline.TerminalEvidence, error) {
	if s == nil || uuid.Validate(planID) != nil {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	probe, err := s.kv.Txn(ctx).Then(clientv3.OpGet(s.offlineTerminalKey(planID)), clientv3.OpGet(s.offlineTerminalEventKey(planID)),
		clientv3.OpGet(s.offlineDispatchKey(planID))).Commit()
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	if len(probe.Responses) != 3 {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	terminalProbe := probe.Responses[0].GetResponseRange().Kvs
	if len(terminalProbe) == 0 {
		if len(probe.Responses[1].GetResponseRange().Kvs) != 0 {
			return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
		}
		return offline.TerminalEvidence{}, ErrNotFound
	}
	bindingProbe := probe.Responses[2].GetResponseRange().Kvs
	if len(terminalProbe) != 1 || len(bindingProbe) != 1 {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	var candidate offline.TerminalEvidence
	if decodeV3Record(terminalProbe[0].Value, &candidate) != nil || candidate.TargetID == "" || candidate.PlanID != planID {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	candidateBinding, err := offline.DecodeBinding(bindingProbe[0].Value)
	if err != nil || candidateBinding.PlanID != planID || candidateBinding.OwnerAuthorizationID == "" {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineTerminalKey(planID)), clientv3.OpGet(s.offlineTerminalEventKey(planID)),
		clientv3.OpGet(s.offlineEffectGrantKey(planID)), clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 1)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 2)), clientv3.OpGet(s.offlineDispatchKey(planID)),
		clientv3.OpGet(s.offlineAttemptKey(planID)), clientv3.OpGet(s.offlineOwnerConsumptionKey(candidateBinding.OwnerAuthorizationID)),
		clientv3.OpGet(s.fleetTargetFenceKey(candidate.TargetID)), clientv3.OpGet(s.fleetAuthorityEpochKey()),
		clientv3.OpGet(s.offlineCheckpointKey(planID)), clientv3.OpGet(s.offlineCheckpointEventKey(planID, 1)),
		clientv3.OpGet(s.fleetTargetRegistryKey()),
		clientv3.OpGet(s.fleetTargetKey(candidate.TargetID)),
		clientv3.OpGet(s.fleetTargetAliasKey("cluster:tk-staging-nyc3")),
		clientv3.OpGet(s.fleetTargetAliasKey("environment:staging")),
	).Commit()
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	if len(response.Responses) != 16 {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	var raw [16][]byte
	for i := range raw {
		rows := response.Responses[i].GetResponseRange().Kvs
		if len(rows) == 1 {
			raw[i] = rows[0].Value
		}
	}
	terminal, err := decodeOfflineTerminalPair(raw[0], raw[1], planID)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	grant, err := decodeOfflineEffectGrantPair(raw[2], raw[3], raw[4], planID)
	if err != nil || grant.Phase != "consumed" || grant.ConsumedAt == nil ||
		terminal.GrantID != grant.ID || terminal.AttemptID != grant.AttemptID ||
		terminal.BindingSHA256 != grant.BindingSHA256 || terminal.TargetID != grant.TargetID ||
		terminal.FenceGeneration != grant.FenceGeneration || terminal.AuthorityEpoch != grant.AuthorityEpoch ||
		terminal.Backend.Bucket != grant.Backend.Bucket || terminal.Backend.Key != grant.Backend.Key ||
		terminal.Backend.Lineage != grant.Backend.Lineage || terminal.Backend.Serial <= grant.Backend.Serial ||
		terminal.Backend.VersionID == grant.Backend.VersionID || !terminal.ObservedAt.After(*grant.ConsumedAt) {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	binding, err := offline.DecodeBinding(raw[5])
	digest := sha256.Sum256(raw[5])
	if err != nil || binding.SchemaVersion != offline.BindingSchemaV2 || binding.PlanID != planID ||
		binding.OwnerAuthorizationID != candidateBinding.OwnerAuthorizationID ||
		binding.TargetID != candidate.TargetID || binding.TargetID != grant.TargetID ||
		binding.FenceGeneration != grant.FenceGeneration || binding.DispatchNonceSHA256 == "" ||
		hex.EncodeToString(digest[:]) != grant.BindingSHA256 || binding.Backend != grant.Backend ||
		binding.ProviderAccountUUID != grant.ProviderAccountUUID || binding.NodeName != grant.NodeName ||
		binding.DropletID != grant.DropletID {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	attempt, err := decodeOfflineAttempt(raw[6], planID)
	if err != nil || uuid.Validate(attempt.ID) != nil || attempt.ID != terminal.AttemptID ||
		attempt.BindingSHA256 != terminal.BindingSHA256 || attempt.PrincipalSubject != binding.RunnerPrincipalSubject ||
		attempt.TargetID != binding.TargetID || attempt.FenceGeneration != binding.FenceGeneration {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	if string(raw[7]) != planID+":"+binding.OwnerEnvelopeSHA256 {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	var fence v3FleetTargetFence
	if decodeV3Record(raw[8], &fence) != nil || !fence.Held || fence.HolderPlanID != planID ||
		fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 || fence.Generation <= 0 ||
		uint64(fence.Generation) != terminal.FenceGeneration || fence.AuthorityEpoch != terminal.AuthorityEpoch {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	var registry v3FleetTargetRegistry
	if decodeV3Record(raw[12], &registry) != nil || registry.Generation < 1 {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	var target FleetTarget
	if decodeV3Record(raw[13], &target) != nil || target.TargetID != binding.TargetID ||
		!slices.Contains(target.Aliases, "cluster:tk-staging-nyc3") ||
		!slices.Contains(target.Aliases, "environment:staging") ||
		string(raw[14]) != binding.TargetID || string(raw[15]) != binding.TargetID {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	_, canonicalTargetID, err := lifecycle.CanonicalTarget(lifecycle.TargetIdentity{
		Provider: target.Provider, ProviderAccount: target.ProviderAccount, StateBackend: target.StateBackend,
	})
	if err != nil || canonicalTargetID != binding.TargetID {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(string(raw[9])), 10, 64)
	if err != nil || epoch < 1 || epoch != terminal.AuthorityEpoch {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	checkpoint, err := decodeOfflineCheckpointPair(raw[10], raw[11], planID)
	if err != nil || checkpoint.AttemptID != terminal.AttemptID || checkpoint.EvidenceSHA256 != grant.PrechangeSHA256 ||
		checkpoint.FenceGeneration != terminal.FenceGeneration || checkpoint.AuthorityEpoch != terminal.AuthorityEpoch {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	return terminal, nil
}

// recordOfflineTerminal is an internal, single-write CAS after a consumed
// grant. It deliberately does not release the fence; target release still
// needs its own independently qualified transaction and recovery policy.
func (s *V3OperationStore) recordOfflineTerminal(ctx context.Context, planID, attemptID, grantID string, now time.Time, observer offlineTerminalReobserver) (offline.TerminalEvidence, error) {
	if s == nil || observer == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || uuid.Validate(grantID) != nil {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineEffectGrantKey(planID)), clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 1)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 2)), clientv3.OpGet(s.offlineAttemptKey(planID)),
		clientv3.OpGet(s.offlineDispatchKey(planID)), clientv3.OpGet(s.offlineCheckpointKey(planID)),
		clientv3.OpGet(s.offlineCheckpointEventKey(planID, 1)),
	).Commit()
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	if len(response.Responses) != 7 {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	var rows [7]*mvccpb.KeyValue
	for i := range rows {
		kvs := response.Responses[i].GetResponseRange().Kvs
		if len(kvs) != 1 {
			return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
		}
		rows[i] = kvs[0]
	}
	grant, err := decodeOfflineEffectGrantPair(rows[0].Value, rows[1].Value, rows[2].Value, planID)
	if err != nil || grant.Phase != "consumed" || grant.AttemptID != attemptID || grant.ID != grantID {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	binding, err := offline.DecodeBinding(rows[4].Value)
	digest := sha256.Sum256(rows[4].Value)
	if err != nil || binding.SchemaVersion != offline.BindingSchemaV2 || binding.PlanID != planID ||
		binding.TargetID != grant.TargetID || binding.FenceGeneration != grant.FenceGeneration ||
		hex.EncodeToString(digest[:]) != grant.BindingSHA256 || binding.Backend != grant.Backend {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	attempt, err := decodeOfflineAttempt(rows[3].Value, planID)
	if err != nil || uuid.Validate(attempt.ID) != nil || attempt.ID != attemptID || attempt.BindingSHA256 != grant.BindingSHA256 ||
		attempt.PrincipalSubject != binding.RunnerPrincipalSubject || attempt.TargetID != grant.TargetID ||
		attempt.FenceGeneration != grant.FenceGeneration {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	checkpoint, err := decodeOfflineCheckpointPair(rows[5].Value, rows[6].Value, planID)
	if err != nil || checkpoint.AttemptID != attemptID || checkpoint.EvidenceSHA256 != grant.PrechangeSHA256 ||
		checkpoint.AuthorityEpoch != grant.AuthorityEpoch || checkpoint.FenceGeneration != grant.FenceGeneration {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	plan, planRevision, err := s.load(ctx, planID)
	var typed fleet.CapacityPlan
	if err != nil || !validOfflineContractionPlan(plan.Operation, planID, &typed) {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	ownerKey := s.offlineOwnerConsumptionKey(binding.OwnerAuthorizationID)
	owner, err := s.kv.Get(ctx, ownerKey)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	if len(owner.Kvs) != 1 || string(owner.Kvs[0].Value) != planID+":"+binding.OwnerEnvelopeSHA256 {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, planID); err != nil {
		return offline.TerminalEvidence{}, err
	} else if abandoned {
		return offline.TerminalEvidence{}, fleetHolderAbandonedError()
	}
	fenceContext, err := s.loadFleetTargetFenceContext(ctx, "tk-staging-nyc3", "staging", true)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	fence := fenceContext.Fence
	if fenceContext.TargetID != binding.TargetID || !fence.Held || fence.HolderPlanID != planID ||
		fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 || uint64(fence.Generation) != binding.FenceGeneration ||
		fence.AuthorityEpoch != fenceContext.Epoch || grant.AuthorityEpoch != fenceContext.Epoch {
		return offline.TerminalEvidence{}, ErrOfflineTerminalConflict
	}
	observed, err := observer.reobserveTerminal(ctx, binding, attempt, grant)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	if completed := time.Now().UTC(); completed.After(now) {
		now = completed
	}
	if err := offline.ValidateTerminalObservation(observed, grant, now); err != nil {
		return offline.TerminalEvidence{}, err
	}
	receipt := offline.TerminalEvidence{SchemaVersion: offline.TerminalSchema, PlanID: planID,
		AttemptID: attemptID, GrantID: grantID, BindingSHA256: grant.BindingSHA256,
		TargetID: grant.TargetID, FenceGeneration: grant.FenceGeneration, AuthorityEpoch: grant.AuthorityEpoch,
		ObservedAt: observed.ObservedAt.UTC(), RecordedAt: now.UTC(), EvidenceSHA256: observed.EvidenceSHA256,
		ProviderEvidenceSHA256: observed.ProviderEvidenceSHA256, BackendEvidenceSHA256: observed.BackendEvidenceSHA256,
		WorkloadEvidenceSHA256: observed.WorkloadEvidenceSHA256, Backend: observed.Backend}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	committed, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.offlineEffectGrantKey(planID)), "=", rows[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineEffectGrantEventKey(planID, 1)), "=", rows[1].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineEffectGrantEventKey(planID, 2)), "=", rows[2].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineAttemptKey(planID)), "=", rows[3].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineDispatchKey(planID)), "=", rows[4].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineCheckpointKey(planID)), "=", rows[5].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineCheckpointEventKey(planID, 1)), "=", rows[6].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(planID)), "=", planRevision),
		clientv3.Compare(clientv3.ModRevision(ownerKey), "=", owner.Kvs[0].ModRevision),
		s.registryCompare(fenceContext),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(binding.TargetID)), "=", fenceContext.FenceRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", fenceContext.EpochRevision),
		s.abandonedCompare(planID),
		clientv3.Compare(clientv3.CreateRevision(s.fleetGitHubDispatchPreparationKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptPrefix(planID)).WithPrefix(), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineTerminalKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineTerminalEventKey(planID)), "=", 0),
	).Then(clientv3.OpPut(s.offlineTerminalKey(planID), string(encoded)),
		clientv3.OpPut(s.offlineTerminalEventKey(planID), string(encoded))).Commit()
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	if !committed.Succeeded {
		return offline.TerminalEvidence{}, ErrOfflineTerminalConflict
	}
	return receipt, nil
}
