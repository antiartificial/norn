package etcdstore

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/offline"
)

var ErrOfflineDeleteCommitmentConflict = errors.New("offline delete commitment changed or authority is stale")

func (s *V3OperationStore) offlineDeleteCommitmentKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-delete-commitments/" + planID
}

func (s *V3OperationStore) offlineDeleteCommitmentEventKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-delete-commitment-events/" + planID + "/00000000000000000001"
}

type offlineDeleteLedgerRows struct {
	raw        [][]byte
	revisions  []int64
	binding    offline.Binding
	attempt    offline.Attempt
	grant      offline.EffectGrant
	checkpoint offline.Checkpoint
}

// readOfflineDeleteLedger reads the commitment and all referenced authority
// rows at one etcd revision. A missing event or a changed source is corrupt,
// never an invitation to issue a second provider request.
func (s *V3OperationStore) readOfflineDeleteLedger(ctx context.Context, planID string) (offlineDeleteLedgerRows, error) {
	var out offlineDeleteLedgerRows
	keys := []string{s.offlineDeleteCommitmentKey(planID), s.offlineDeleteCommitmentEventKey(planID),
		s.offlineDispatchKey(planID), s.offlineAttemptKey(planID), s.offlineEffectGrantKey(planID),
		s.offlineEffectGrantEventKey(planID, 1), s.offlineEffectGrantEventKey(planID, 2),
		s.offlineCheckpointKey(planID), s.offlineCheckpointEventKey(planID, 1)}
	ops := make([]clientv3.Op, 0, len(keys))
	for _, key := range keys {
		ops = append(ops, clientv3.OpGet(key))
	}
	response, err := s.kv.Txn(ctx).Then(ops...).Commit()
	if err != nil {
		return out, err
	}
	if len(response.Responses) != len(keys) {
		return out, ErrOfflineAttemptCorrupt
	}
	out.raw = make([][]byte, len(keys))
	out.revisions = make([]int64, len(keys))
	for i, item := range response.Responses {
		rows := item.GetResponseRange().Kvs
		if len(rows) > 1 {
			return out, ErrOfflineAttemptCorrupt
		}
		if len(rows) == 1 {
			out.raw[i], out.revisions[i] = rows[0].Value, rows[0].ModRevision
		}
	}
	if (len(out.raw[0]) == 0) != (len(out.raw[1]) == 0) {
		return out, ErrOfflineAttemptCorrupt
	}
	if out.binding, err = offline.DecodeBinding(out.raw[2]); err != nil || out.binding.SchemaVersion != offline.BindingSchemaV2 || out.binding.PlanID != planID {
		return out, ErrOfflineAttemptCorrupt
	}
	if out.attempt, err = decodeOfflineAttempt(out.raw[3], planID); err != nil {
		return out, ErrOfflineAttemptCorrupt
	}
	if out.grant, err = decodeOfflineEffectGrantPair(out.raw[4], out.raw[5], out.raw[6], planID); err != nil || out.grant.Phase != "consumed" {
		return out, ErrOfflineAttemptCorrupt
	}
	if out.checkpoint, err = decodeOfflineCheckpointPair(out.raw[7], out.raw[8], planID); err != nil {
		return out, ErrOfflineAttemptCorrupt
	}
	if out.attempt.ID != out.grant.AttemptID || out.checkpoint.AttemptID != out.attempt.ID ||
		out.attempt.BindingSHA256 != offline.DeleteCommitmentSHA256(out.raw[2]) ||
		out.grant.BindingSHA256 != out.attempt.BindingSHA256 ||
		out.checkpoint.EvidenceSHA256 != out.grant.PrechangeSHA256 ||
		out.checkpoint.FenceGeneration != out.grant.FenceGeneration || out.checkpoint.AuthorityEpoch != out.grant.AuthorityEpoch {
		return out, ErrOfflineAttemptCorrupt
	}
	return out, nil
}

// GetOfflineDeleteCommitment recovers the exact historical bytes after a lost
// response. Reads require the event and every referenced authority row; they
// never reconstruct a new commitment from a later clock or runtime identity.
// Recovery is for audit/reconciliation only; it authorizes no provider call.
func (s *V3OperationStore) GetOfflineDeleteCommitment(ctx context.Context, planID string) (offline.DeleteCommitment, []byte, error) {
	if s == nil || uuid.Validate(planID) != nil {
		return offline.DeleteCommitment{}, nil, ErrOfflineAttemptCorrupt
	}
	rows, err := s.readOfflineDeleteLedger(ctx, planID)
	if err != nil {
		return offline.DeleteCommitment{}, nil, err
	}
	if len(rows.raw[0]) == 0 {
		return offline.DeleteCommitment{}, nil, ErrNotFound
	}
	if !bytes.Equal(rows.raw[0], rows.raw[1]) {
		return offline.DeleteCommitment{}, nil, ErrOfflineAttemptCorrupt
	}
	c, err := offline.DecodeDeleteCommitment(rows.raw[0])
	if err != nil || c.PlanID != planID || c.AttemptID != rows.attempt.ID || c.GrantID != rows.grant.ID ||
		c.BindingSHA256 != offline.DeleteCommitmentSHA256(rows.raw[2]) ||
		c.AttemptSHA256 != offline.DeleteCommitmentSHA256(rows.raw[3]) ||
		c.EffectGrantSHA256 != offline.DeleteCommitmentSHA256(rows.raw[4]) ||
		c.AuthorityEpoch != rows.grant.AuthorityEpoch || c.FenceGeneration != rows.grant.FenceGeneration ||
		c.RunnerPrincipalSubject != rows.attempt.PrincipalSubject || c.RunnerTokenID != rows.attempt.PrincipalTokenID ||
		c.RunnerDeviceID != rows.attempt.PrincipalDeviceID || c.TargetID != rows.binding.TargetID {
		return offline.DeleteCommitment{}, nil, ErrOfflineAttemptCorrupt
	}
	_, reconstructed, err := offline.NewDeleteCommitment(rows.binding, rows.attempt, rows.grant,
		c.SupervisorRootID, c.SupervisorExecutionID, c.RuntimeInstanceID, c.CommittedAt)
	if err != nil || !bytes.Equal(reconstructed, rows.raw[0]) {
		return offline.DeleteCommitment{}, nil, ErrOfflineAttemptCorrupt
	}
	return c, rows.raw[0], nil
}

// commitOfflineDelete is deliberately internal and has no live route. The
// caller must supervise the identified runtime before using its bytes. The
// CAS consumes no additional provider capability and never releases a fence.
// Caller-supplied time cannot extend the one-minute grant. The local pre-CAS
// clock read cannot prove freshness at the networked etcd commit instant.
// Future executor admission must independently recheck the exact commitment,
// grant validity, held fence, epoch and supervised runtime before DELETE. If
// any current check is unavailable or ambiguous, it must fail closed. A
// commitment never grants retry permission after response loss.
func (s *V3OperationStore) commitOfflineDelete(ctx context.Context, planID, attemptID, grantID, rootID, executionID, runtimeID string) (offline.DeleteCommitment, []byte, error) {
	return s.commitOfflineDeleteWithClock(ctx, planID, attemptID, grantID, rootID, executionID, runtimeID, time.Now)
}

func (s *V3OperationStore) commitOfflineDeleteWithClock(ctx context.Context, planID, attemptID, grantID, rootID, executionID, runtimeID string, clock func() time.Time) (offline.DeleteCommitment, []byte, error) {
	if s == nil || uuid.Validate(planID) != nil {
		return offline.DeleteCommitment{}, nil, offline.ErrDeleteCommitment
	}
	rows, err := s.readOfflineDeleteLedger(ctx, planID)
	if err != nil {
		return offline.DeleteCommitment{}, nil, err
	}
	if len(rows.raw[0]) != 0 {
		return offline.DeleteCommitment{}, nil, ErrOfflineDeleteCommitmentConflict
	}
	if rows.attempt.ID != attemptID || rows.grant.ID != grantID {
		return offline.DeleteCommitment{}, nil, offline.ErrDeleteCommitment
	}
	if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, planID); err != nil {
		return offline.DeleteCommitment{}, nil, err
	} else if abandoned {
		return offline.DeleteCommitment{}, nil, fleetHolderAbandonedError()
	}
	fenceContext, err := s.loadFleetTargetFenceContext(ctx, "tk-staging-nyc3", "staging", true)
	if err != nil {
		return offline.DeleteCommitment{}, nil, err
	}
	fence := fenceContext.Fence
	if fenceContext.TargetID != rows.binding.TargetID || !fence.Held || fence.HolderPlanID != planID ||
		fence.HolderNonceSHA256 != rows.binding.DispatchNonceSHA256 || uint64(fence.Generation) != rows.grant.FenceGeneration ||
		fence.AuthorityEpoch != rows.grant.AuthorityEpoch || fenceContext.Epoch != rows.grant.AuthorityEpoch {
		return offline.DeleteCommitment{}, nil, ErrOfflineDeleteCommitmentConflict
	}
	ownerKey := s.offlineOwnerConsumptionKey(rows.binding.OwnerAuthorizationID)
	owner, err := s.kv.Get(ctx, ownerKey)
	if err != nil {
		return offline.DeleteCommitment{}, nil, err
	}
	if len(owner.Kvs) != 1 || string(owner.Kvs[0].Value) != planID+":"+rows.binding.OwnerEnvelopeSHA256 {
		return offline.DeleteCommitment{}, nil, ErrOfflineAttemptCorrupt
	}
	plan, planRevision, err := s.load(ctx, planID)
	var typed fleet.CapacityPlan
	if err != nil || !validOfflineContractionPlan(plan.Operation, planID, &typed) {
		return offline.DeleteCommitment{}, nil, ErrOfflineAttemptCorrupt
	}
	if clock == nil {
		return offline.DeleteCommitment{}, nil, offline.ErrDeleteCommitment
	}
	c, encoded, err := offline.NewDeleteCommitment(rows.binding, rows.attempt, rows.grant, rootID, executionID, runtimeID, clock().UTC())
	if err != nil {
		return offline.DeleteCommitment{}, nil, err
	}
	// Refresh time before the authority CAS to reject known expiry. This is
	// deliberately only a local preflight: etcd has no transaction-time clock
	// predicate, so expiry may cross during the networked Commit call.
	decisionTime := clock().UTC()
	if decisionTime.Before(c.CommittedAt) || !decisionTime.Before(rows.grant.ExpiresAt) || !decisionTime.Before(rows.binding.ExpiresAt) {
		return offline.DeleteCommitment{}, nil, offline.ErrDeleteCommitment
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.CreateRevision(s.offlineDeleteCommitmentKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineDeleteCommitmentEventKey(planID)), "=", 0),
		clientv3.Compare(clientv3.ModRevision(s.opKey(planID)), "=", planRevision),
		clientv3.Compare(clientv3.ModRevision(ownerKey), "=", owner.Kvs[0].ModRevision),
		s.registryCompare(fenceContext),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(rows.binding.TargetID)), "=", fenceContext.FenceRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", fenceContext.EpochRevision),
		s.abandonedCompare(planID),
		clientv3.Compare(clientv3.CreateRevision(s.offlineTerminalKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetGitHubDispatchPreparationKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptPrefix(planID)).WithPrefix(), "=", 0),
	}
	keys := []string{s.offlineDeleteCommitmentKey(planID), s.offlineDeleteCommitmentEventKey(planID), s.offlineDispatchKey(planID),
		s.offlineAttemptKey(planID), s.offlineEffectGrantKey(planID), s.offlineEffectGrantEventKey(planID, 1),
		s.offlineEffectGrantEventKey(planID, 2), s.offlineCheckpointKey(planID), s.offlineCheckpointEventKey(planID, 1)}
	for i := 2; i < len(keys); i++ {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(keys[i]), "=", rows.revisions[i]))
	}
	result, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(keys[0], string(encoded)), clientv3.OpPut(keys[1], string(encoded))).Commit()
	if err != nil {
		return offline.DeleteCommitment{}, nil, err
	}
	if !result.Succeeded {
		return offline.DeleteCommitment{}, nil, ErrOfflineDeleteCommitmentConflict
	}
	return c, encoded, nil
}
