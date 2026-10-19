package etcdstore

// There is deliberately no HTTP route or production observer for this grant.
// A runner-supplied receipt is not independent observation. Until a trusted
// observer is wired here, issuance always returns ErrEffectProofMissing.

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

var ErrOfflineEffectGrantConflict = errors.New("offline effect grant changed or fence is stale")

// This private interface permits deterministic store tests. A future live
// implementation must independently observe all fields; callers cannot pass
// their own proof over an API.
type offlineEffectReobserver interface {
	reobserveEffect(context.Context, offline.Binding, offline.Attempt) (offline.EffectObservation, error)
}

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
		head.ID != issued.ID || head.AttemptID != issued.AttemptID ||
		head.BindingSHA256 != issued.BindingSHA256 || head.FenceGeneration != issued.FenceGeneration ||
		head.AuthorityEpoch != issued.AuthorityEpoch || !offlineEvidenceDigest.MatchString(head.PrechangeSHA256) ||
		!offlineEvidenceDigest.MatchString(head.IndependentlyObservedSHA256) ||
		head.IssuedAt.IsZero() || !head.ExpiresAt.After(head.IssuedAt) || head.ExpiresAt.Sub(head.IssuedAt) > time.Minute ||
		issued.Phase != "issued" || issued.ConsumedAt != nil ||
		!offlineEvidenceDigest.MatchString(head.BindingSHA256) || !offlineEvidenceDigest.MatchString(head.SavedPlanSHA256) ||
		!offlineEvidenceDigest.MatchString(head.EffectSHA256) || head.Backend.RawSHA256 == "" {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	if len(consumedRaw) == 0 {
		if head.Phase != "issued" || head.ConsumedAt != nil || !bytes.Equal(headRaw, issuedRaw) {
			return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
		}
	} else if head.Phase != "consumed" || head.ConsumedAt == nil || head.ConsumedAt.Before(head.IssuedAt) ||
		!head.ConsumedAt.Before(head.ExpiresAt) || !bytes.Equal(headRaw, consumedRaw) {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	base := head
	base.Phase, base.ConsumedAt = "issued", nil
	if !reflect.DeepEqual(base, issued) {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	return head, nil
}

// issueOfflineEffectGrant is a non-public CAS transition. It rechecks the
// checkpoint, owner consumption, registry, held fence, epoch, absence of a
// competing GitHub lane, and independent effect-time observation atomically.
func (s *V3OperationStore) issueOfflineEffectGrant(ctx context.Context, planID, attemptID, principal string, now time.Time, observer offlineEffectReobserver) (offline.EffectGrant, error) {
	if s == nil || observer == nil {
		return offline.EffectGrant{}, offline.ErrEffectProofMissing
	}
	if uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || principal == "" {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	rows, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineDispatchKey(planID)), clientv3.OpGet(s.offlineAttemptKey(planID)),
		clientv3.OpGet(s.offlineCheckpointKey(planID)), clientv3.OpGet(s.offlineCheckpointEventKey(planID, 1)),
	).Commit()
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if len(rows.Responses) != 4 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	// Keep revision-bearing KVs for the final compare, not just their values.
	b := rows.Responses[0].GetResponseRange().Kvs
	a := rows.Responses[1].GetResponseRange().Kvs
	c := rows.Responses[2].GetResponseRange().Kvs
	e := rows.Responses[3].GetResponseRange().Kvs
	if len(b) != 1 || len(a) != 1 || len(c) != 1 || len(e) != 1 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	binding, err := offline.DecodeBinding(b[0].Value)
	if err != nil || binding.SchemaVersion != offline.BindingSchemaV2 || binding.PlanID != planID ||
		now.Before(binding.CreatedAt) || !now.Before(binding.ExpiresAt) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	plan, planRevision, err := s.load(ctx, planID)
	var typed fleet.CapacityPlan
	if err != nil || !validOfflineContractionPlan(plan.Operation, planID, &typed) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	attempt, err := decodeOfflineAttempt(a[0].Value, planID)
	digest := sha256.Sum256(b[0].Value)
	if err != nil || attempt.ID != attemptID || attempt.PrincipalSubject != principal ||
		attempt.BindingSHA256 != hex.EncodeToString(digest[:]) || attempt.TargetID != binding.TargetID ||
		attempt.FenceGeneration != binding.FenceGeneration {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	checkpoint, err := decodeOfflineCheckpointPair(c[0].Value, e[0].Value, planID)
	if err != nil || checkpoint.AttemptID != attemptID || checkpoint.Prechange == nil ||
		!checkpoint.Prechange.ProviderTopologyVerified || checkpoint.Prechange.BindingSHA256 != attempt.BindingSHA256 ||
		checkpoint.FenceGeneration != binding.FenceGeneration || !now.Before(checkpoint.Prechange.ExpiresAt) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	consumptionKey := s.offlineOwnerConsumptionKey(binding.OwnerAuthorizationID)
	consumption, err := s.kv.Get(ctx, consumptionKey)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if len(consumption.Kvs) != 1 || string(consumption.Kvs[0].Value) != planID+":"+binding.OwnerEnvelopeSHA256 {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, planID); err != nil {
		return offline.EffectGrant{}, err
	} else if abandoned {
		return offline.EffectGrant{}, fleetHolderAbandonedError()
	}
	fenceContext, err := s.loadFleetTargetFenceContext(ctx, "tk-staging-nyc3", "staging", true)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	fence := fenceContext.Fence
	if fenceContext.TargetID != binding.TargetID || !fence.Held || fence.HolderPlanID != planID ||
		fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 || uint64(fence.Generation) != binding.FenceGeneration ||
		fence.AuthorityEpoch != fenceContext.Epoch || checkpoint.AuthorityEpoch != fenceContext.Epoch {
		return offline.EffectGrant{}, ErrOfflineEffectGrantConflict
	}
	observation, err := observer.reobserveEffect(ctx, binding, attempt)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if err := offline.ValidateEffectObservation(observation, binding, attempt, fenceContext.Epoch, now); err != nil {
		return offline.EffectGrant{}, err
	}
	expires := now.Add(time.Minute)
	if binding.ExpiresAt.Before(expires) {
		expires = binding.ExpiresAt
	}
	if checkpoint.Prechange.ExpiresAt.Before(expires) {
		expires = checkpoint.Prechange.ExpiresAt
	}
	grant := offline.EffectGrant{SchemaVersion: offline.EffectGrantSchema, ID: uuid.NewString(), PlanID: planID,
		AttemptID: attemptID, BindingSHA256: attempt.BindingSHA256, TargetID: binding.TargetID,
		FenceGeneration: binding.FenceGeneration, AuthorityEpoch: fenceContext.Epoch,
		SourceCommit: binding.SourceCommit, SavedPlanSHA256: binding.SavedPlanSHA256, EffectSHA256: binding.EffectSHA256,
		Backend: binding.Backend, ProviderAccountUUID: binding.ProviderAccountUUID, NodeName: binding.NodeName,
		DropletID: binding.DropletID, PrechangeSHA256: checkpoint.EvidenceSHA256,
		IndependentlyObservedSHA256: observation.EvidenceSHA256, Phase: "issued", IssuedAt: now.UTC(), ExpiresAt: expires.UTC()}
	encoded, err := json.Marshal(grant)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	committed, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.offlineDispatchKey(planID)), "=", b[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(planID)), "=", planRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineAttemptKey(planID)), "=", a[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineCheckpointKey(planID)), "=", c[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineCheckpointEventKey(planID, 1)), "=", e[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(consumptionKey), "=", consumption.Kvs[0].ModRevision),
		s.registryCompare(fenceContext),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(binding.TargetID)), "=", fenceContext.FenceRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", fenceContext.EpochRevision),
		s.abandonedCompare(planID),
		clientv3.Compare(clientv3.CreateRevision(s.fleetGitHubDispatchPreparationKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptPrefix(planID)).WithPrefix(), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectGrantKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectGrantEventKey(planID, 1)), "=", 0),
	).Then(clientv3.OpPut(s.offlineEffectGrantKey(planID), string(encoded)),
		clientv3.OpPut(s.offlineEffectGrantEventKey(planID, 1), string(encoded))).Commit()
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if !committed.Succeeded {
		return offline.EffectGrant{}, ErrOfflineEffectGrantConflict
	}
	return grant, nil
}

// consumeOfflineEffectGrant is deliberately private and has no provider
// credential side effect. Its transaction records the irrevocable one-use
// transition before a future executor may attempt an external effect. If the
// response is lost, recovery must read event 2 and must never retry apply.
func (s *V3OperationStore) consumeOfflineEffectGrant(ctx context.Context, planID, attemptID, grantID, principal string, now time.Time) (offline.EffectGrant, error) {
	if s == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || uuid.Validate(grantID) != nil || principal == "" {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(s.offlineEffectGrantKey(planID)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 1)), clientv3.OpGet(s.offlineAttemptKey(planID)),
		clientv3.OpGet(s.offlineDispatchKey(planID)), clientv3.OpGet(s.offlineCheckpointKey(planID)),
		clientv3.OpGet(s.offlineCheckpointEventKey(planID, 1))).Commit()
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if len(response.Responses) != 6 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	g := response.Responses[0].GetResponseRange().Kvs
	e := response.Responses[1].GetResponseRange().Kvs
	a := response.Responses[2].GetResponseRange().Kvs
	b := response.Responses[3].GetResponseRange().Kvs
	c := response.Responses[4].GetResponseRange().Kvs
	ce := response.Responses[5].GetResponseRange().Kvs
	if len(g) != 1 || len(e) != 1 || len(a) != 1 || len(b) != 1 || len(c) != 1 || len(ce) != 1 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	var grant, issued offline.EffectGrant
	if decodeV3Record(g[0].Value, &grant) != nil || decodeV3Record(e[0].Value, &issued) != nil ||
		grant != issued || grant.SchemaVersion != offline.EffectGrantSchema || grant.Phase != "issued" ||
		grant.ID != grantID || grant.PlanID != planID || grant.AttemptID != attemptID ||
		grant.ConsumedAt != nil || now.Before(grant.IssuedAt) || !now.Before(grant.ExpiresAt) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	attempt, err := decodeOfflineAttempt(a[0].Value, planID)
	if err != nil || attempt.ID != attemptID || attempt.PrincipalSubject != principal || attempt.BindingSHA256 != grant.BindingSHA256 {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	binding, err := offline.DecodeBinding(b[0].Value)
	if err != nil || binding.SchemaVersion != offline.BindingSchemaV2 || binding.PlanID != planID ||
		binding.TargetID != grant.TargetID || binding.FenceGeneration != grant.FenceGeneration ||
		binding.SourceCommit != grant.SourceCommit || binding.SavedPlanSHA256 != grant.SavedPlanSHA256 ||
		binding.EffectSHA256 != grant.EffectSHA256 || binding.Backend != grant.Backend ||
		binding.ProviderAccountUUID != grant.ProviderAccountUUID || binding.NodeName != grant.NodeName ||
		binding.DropletID != grant.DropletID || !now.Before(binding.ExpiresAt) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	plan, planRevision, err := s.load(ctx, planID)
	var typed fleet.CapacityPlan
	if err != nil || !validOfflineContractionPlan(plan.Operation, planID, &typed) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	if digest := sha256.Sum256(b[0].Value); hex.EncodeToString(digest[:]) != grant.BindingSHA256 {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	checkpoint, err := decodeOfflineCheckpointPair(c[0].Value, ce[0].Value, planID)
	if err != nil || checkpoint.AttemptID != attemptID || checkpoint.EvidenceSHA256 != grant.PrechangeSHA256 ||
		checkpoint.Prechange == nil || !now.Before(checkpoint.Prechange.ExpiresAt) ||
		checkpoint.AuthorityEpoch != grant.AuthorityEpoch || checkpoint.FenceGeneration != grant.FenceGeneration {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	consumptionKey := s.offlineOwnerConsumptionKey(binding.OwnerAuthorizationID)
	owner, err := s.kv.Get(ctx, consumptionKey)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if len(owner.Kvs) != 1 || string(owner.Kvs[0].Value) != planID+":"+binding.OwnerEnvelopeSHA256 {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, planID); err != nil {
		return offline.EffectGrant{}, err
	} else if abandoned {
		return offline.EffectGrant{}, fleetHolderAbandonedError()
	}
	fenceContext, err := s.loadFleetTargetFenceContext(ctx, "tk-staging-nyc3", "staging", true)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	fence := fenceContext.Fence
	if fenceContext.TargetID != binding.TargetID || !fence.Held || fence.HolderPlanID != planID ||
		fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 || uint64(fence.Generation) != binding.FenceGeneration ||
		fence.AuthorityEpoch != fenceContext.Epoch || grant.AuthorityEpoch != fenceContext.Epoch {
		return offline.EffectGrant{}, ErrOfflineEffectGrantConflict
	}
	consumed := now.UTC()
	grant.Phase, grant.ConsumedAt = "consumed", &consumed
	encoded, err := json.Marshal(grant)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	committed, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.offlineEffectGrantKey(planID)), "=", g[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineEffectGrantEventKey(planID, 1)), "=", e[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectGrantEventKey(planID, 2)), "=", 0),
		clientv3.Compare(clientv3.ModRevision(s.offlineAttemptKey(planID)), "=", a[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineDispatchKey(planID)), "=", b[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(planID)), "=", planRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineCheckpointKey(planID)), "=", c[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.offlineCheckpointEventKey(planID, 1)), "=", ce[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(consumptionKey), "=", owner.Kvs[0].ModRevision),
		s.registryCompare(fenceContext),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(binding.TargetID)), "=", fenceContext.FenceRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", fenceContext.EpochRevision),
		s.abandonedCompare(planID),
		clientv3.Compare(clientv3.CreateRevision(s.fleetGitHubDispatchPreparationKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptPrefix(planID)).WithPrefix(), "=", 0),
	).Then(clientv3.OpPut(s.offlineEffectGrantKey(planID), string(encoded)),
		clientv3.OpPut(s.offlineEffectGrantEventKey(planID, 2), string(encoded))).Commit()
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if !committed.Succeeded {
		return offline.EffectGrant{}, ErrOfflineEffectGrantConflict
	}
	return grant, nil
}
