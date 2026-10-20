package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/offline"
)

var ErrOfflineDeleteCommitmentConflict = errors.New("offline delete commitment changed or authority is stale")

const offlineDeleteDispatchSchema = "norn.fleet-offline-delete-dispatch/v1"

type offlineDeleteDispatch struct {
	SchemaVersion         string    `json:"schemaVersion"`
	PlanID                string    `json:"planId"`
	AttemptID             string    `json:"attemptId"`
	GrantID               string    `json:"grantId"`
	CommitmentSHA256      string    `json:"commitmentSHA256"`
	TargetID              string    `json:"targetId"`
	DropletID             string    `json:"dropletId"`
	FenceGeneration       uint64    `json:"fenceGeneration"`
	AuthorityEpoch        int64     `json:"authorityEpoch"`
	SupervisorRootID      string    `json:"supervisorRootId"`
	SupervisorExecutionID string    `json:"supervisorExecutionId"`
	RuntimeInstanceID     string    `json:"runtimeInstanceId"`
	State                 string    `json:"state"`
	RetryPolicy           string    `json:"retryPolicy"`
	AdmittedAt            time.Time `json:"admittedAt"`
}

// GetOfflineDeleteDispatchReservation returns the exact immutable reservation
// bytes after revalidating the paired event and referenced commitment.
func (s *V3OperationStore) GetOfflineDeleteDispatchReservation(ctx context.Context, planID string) ([]byte, error) {
	if s == nil || uuid.Validate(planID) != nil {
		return nil, ErrOfflineAttemptCorrupt
	}
	rows, extra, err := s.readOfflineDeleteLedgerWithExtra(ctx, planID,
		s.offlineDeleteDispatchReservationKey(planID), s.offlineDeleteDispatchReservationEventKey(planID))
	if err != nil {
		return nil, err
	}
	commitment, exactCommitment, err := validateOfflineDeleteCommitment(planID, rows)
	if err != nil {
		return nil, err
	}
	if len(extra) != 2 {
		return nil, ErrOfflineAttemptCorrupt
	}
	head, event := extra[0], extra[1]
	if len(head) == 0 && len(event) == 0 {
		return nil, ErrNotFound
	}
	if len(head) == 0 || len(event) == 0 || !bytes.Equal(head, event) {
		return nil, ErrOfflineAttemptCorrupt
	}
	var dispatch offlineDeleteDispatch
	decoder := json.NewDecoder(bytes.NewReader(head))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&dispatch) != nil || decoder.Decode(&struct{}{}) != io.EOF || dispatch.SchemaVersion != offlineDeleteDispatchSchema ||
		dispatch.PlanID != commitment.PlanID || dispatch.AttemptID != commitment.AttemptID || dispatch.GrantID != commitment.GrantID ||
		dispatch.CommitmentSHA256 != offline.DeleteCommitmentSHA256(exactCommitment) || dispatch.TargetID != commitment.TargetID ||
		dispatch.DropletID != commitment.DropletID || dispatch.FenceGeneration != commitment.FenceGeneration ||
		dispatch.AuthorityEpoch != commitment.AuthorityEpoch || dispatch.SupervisorRootID != commitment.SupervisorRootID ||
		dispatch.SupervisorExecutionID != commitment.SupervisorExecutionID || dispatch.RuntimeInstanceID != commitment.RuntimeInstanceID ||
		dispatch.State != "admitted" || dispatch.RetryPolicy != offline.DeleteRetryPolicy || dispatch.AdmittedAt.IsZero() {
		return nil, ErrOfflineAttemptCorrupt
	}
	canonical, err := json.Marshal(dispatch)
	if err != nil || !bytes.Equal(canonical, head) {
		return nil, ErrOfflineAttemptCorrupt
	}
	return append([]byte(nil), head...), nil
}

// offlineDeleteSupervisorVerifier is supplied only by the local supervised
// execution path. It must prove that all three identifiers still name the
// running supervisor root, execution journal and runtime instance. The store
// deliberately knows nothing about supervisor implementation details.
type offlineDeleteSupervisorVerifier func(context.Context, string, string, string) error

type OfflineDeleteRuntimeIdentity struct {
	SupervisorRootID, SupervisorExecutionID, RuntimeInstanceID string
}

// CommitOfflineDelete persists one exact pre-dispatch commitment. Runtime
// identity is supplied by server-owned supervisor resolution, never HTTP.
func (s *V3OperationStore) CommitOfflineDelete(ctx context.Context, planID, attemptID, grantID string, identity OfflineDeleteRuntimeIdentity) (offline.DeleteCommitment, []byte, error) {
	return s.commitOfflineDelete(ctx, planID, attemptID, grantID, identity.SupervisorRootID, identity.SupervisorExecutionID, identity.RuntimeInstanceID)
}

func (s *V3OperationStore) offlineDeleteCommitmentKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-delete-commitments/" + planID
}

func (s *V3OperationStore) offlineDeleteCommitmentEventKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-delete-commitment-events/" + planID + "/00000000000000000001"
}

func (s *V3OperationStore) offlineDeleteDispatchReservationKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-delete-dispatch-reservations/" + planID
}

func (s *V3OperationStore) offlineDeleteDispatchReservationEventKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-delete-dispatch-reservation-events/" + planID + "/00000000000000000001"
}

func (s *V3OperationStore) noOfflineDeleteDispatchCompare(planID string) clientv3.Cmp {
	return clientv3.Compare(clientv3.CreateRevision(s.offlineDeleteDispatchReservationKey(planID)), "=", 0)
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
	rows, _, err := s.readOfflineDeleteLedgerWithExtra(ctx, planID)
	return rows, err
}

func (s *V3OperationStore) readOfflineDeleteLedgerWithExtra(ctx context.Context, planID string, extraKeys ...string) (offlineDeleteLedgerRows, [][]byte, error) {
	var out offlineDeleteLedgerRows
	keys := []string{s.offlineDeleteCommitmentKey(planID), s.offlineDeleteCommitmentEventKey(planID),
		s.offlineDispatchKey(planID), s.offlineAttemptKey(planID), s.offlineEffectGrantKey(planID),
		s.offlineEffectGrantEventKey(planID, 1), s.offlineEffectGrantEventKey(planID, 2),
		s.offlineCheckpointKey(planID), s.offlineCheckpointEventKey(planID, 1)}
	keys = append(keys, extraKeys...)
	ops := make([]clientv3.Op, 0, len(keys))
	for _, key := range keys {
		ops = append(ops, clientv3.OpGet(key))
	}
	response, err := s.kv.Txn(ctx).Then(ops...).Commit()
	if err != nil {
		return out, nil, err
	}
	if len(response.Responses) != len(keys) {
		return out, nil, ErrOfflineAttemptCorrupt
	}
	allRaw := make([][]byte, len(keys))
	out.raw = allRaw[:9]
	out.revisions = make([]int64, 9)
	for i, item := range response.Responses {
		rows := item.GetResponseRange().Kvs
		if len(rows) > 1 {
			return out, nil, ErrOfflineAttemptCorrupt
		}
		if len(rows) == 1 {
			allRaw[i] = rows[0].Value
			if i < len(out.revisions) {
				out.revisions[i] = rows[0].ModRevision
			}
		}
	}
	if (len(out.raw[0]) == 0) != (len(out.raw[1]) == 0) {
		return out, nil, ErrOfflineAttemptCorrupt
	}
	if out.binding, err = offline.DecodeBinding(out.raw[2]); err != nil || out.binding.SchemaVersion != offline.BindingSchemaV2 || out.binding.PlanID != planID {
		return out, nil, ErrOfflineAttemptCorrupt
	}
	if out.attempt, err = decodeOfflineAttempt(out.raw[3], planID); err != nil {
		return out, nil, ErrOfflineAttemptCorrupt
	}
	if out.grant, err = decodeOfflineEffectGrantPair(out.raw[4], out.raw[5], out.raw[6], planID); err != nil || out.grant.Phase != "consumed" {
		return out, nil, ErrOfflineAttemptCorrupt
	}
	if out.checkpoint, err = decodeOfflineCheckpointPair(out.raw[7], out.raw[8], planID); err != nil {
		return out, nil, ErrOfflineAttemptCorrupt
	}
	if out.attempt.ID != out.grant.AttemptID || out.checkpoint.AttemptID != out.attempt.ID ||
		out.attempt.BindingSHA256 != offline.DeleteCommitmentSHA256(out.raw[2]) ||
		out.grant.BindingSHA256 != out.attempt.BindingSHA256 ||
		out.checkpoint.EvidenceSHA256 != out.grant.PrechangeSHA256 ||
		out.checkpoint.FenceGeneration != out.grant.FenceGeneration || out.checkpoint.AuthorityEpoch != out.grant.AuthorityEpoch {
		return out, nil, ErrOfflineAttemptCorrupt
	}
	return out, allRaw[9:], nil
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
	return validateOfflineDeleteCommitment(planID, rows)
}

func validateOfflineDeleteCommitment(planID string, rows offlineDeleteLedgerRows) (offline.DeleteCommitment, []byte, error) {
	if len(rows.raw) != 9 || len(rows.raw[0]) == 0 {
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

// commitOfflineDelete is deliberately internal. The private transport resolves
// the server-owned runtime identity before calling it. The caller must
// supervise the identified runtime before using its bytes. The
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

// admitOfflineDelete is the final local authority check for the exact
// commitment bytes a supervised runtime is about to use. It has no HTTP route,
// provider client or credential side effect. The caller must treat a returned
// error or an ambiguous result as a permanent refusal to dispatch DELETE.
func (s *V3OperationStore) admitOfflineDelete(ctx context.Context, exactCommitment []byte, verify offlineDeleteSupervisorVerifier) error {
	return s.admitOfflineDeleteWithClock(ctx, exactCommitment, verify, time.Now)
}

func (s *V3OperationStore) admitOfflineDeleteWithClock(ctx context.Context, exactCommitment []byte, verify offlineDeleteSupervisorVerifier, clock func() time.Time) error {
	if s == nil || verify == nil || clock == nil {
		return offline.ErrDeleteCommitment
	}
	commitment, err := offline.DecodeDeleteCommitment(exactCommitment)
	if err != nil {
		return err
	}
	// Supervisor proof happens before the authority snapshot so a slow local
	// observation cannot consume the grant's remaining lifetime unnoticed.
	if err := verify(ctx, commitment.SupervisorRootID, commitment.SupervisorExecutionID, commitment.RuntimeInstanceID); err != nil {
		return err
	}

	// The binding probe selects the owner-consumption key only. The following
	// transaction re-reads that binding together with every admission row at
	// one current etcd revision and binds it back to the exact commitment.
	probe, err := s.kv.Get(ctx, s.offlineDispatchKey(commitment.PlanID))
	if err != nil {
		return err
	}
	if len(probe.Kvs) != 1 {
		return ErrOfflineDeleteCommitmentConflict
	}
	candidate, err := offline.DecodeBinding(probe.Kvs[0].Value)
	if err != nil || candidate.PlanID != commitment.PlanID || candidate.OwnerAuthorizationID == "" {
		return ErrOfflineDeleteCommitmentConflict
	}

	const (
		commitmentHead = iota
		commitmentEvent
		bindingKey
		attemptKey
		grantHead
		grantIssued
		grantConsumed
		checkpointHead
		checkpointEvent
		ownerKey
		planKey
		fenceKey
		epochKey
		abandonedKey
		terminalHead
		terminalEvent
		githubPreparation
		runnerDispatch
		runnerAttempts
		executorAttempts
		dispatchHead
		dispatchEvent
	)
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineDeleteCommitmentKey(commitment.PlanID)),
		clientv3.OpGet(s.offlineDeleteCommitmentEventKey(commitment.PlanID)),
		clientv3.OpGet(s.offlineDispatchKey(commitment.PlanID)),
		clientv3.OpGet(s.offlineAttemptKey(commitment.PlanID)),
		clientv3.OpGet(s.offlineEffectGrantKey(commitment.PlanID)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(commitment.PlanID, 1)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(commitment.PlanID, 2)),
		clientv3.OpGet(s.offlineCheckpointKey(commitment.PlanID)),
		clientv3.OpGet(s.offlineCheckpointEventKey(commitment.PlanID, 1)),
		clientv3.OpGet(s.offlineOwnerConsumptionKey(candidate.OwnerAuthorizationID)),
		clientv3.OpGet(s.opKey(commitment.PlanID)),
		clientv3.OpGet(s.fleetTargetFenceKey(commitment.TargetID)),
		clientv3.OpGet(s.fleetAuthorityEpochKey()),
		clientv3.OpGet(s.fleetTargetAbandonedKey(commitment.PlanID)),
		clientv3.OpGet(s.offlineTerminalKey(commitment.PlanID)),
		clientv3.OpGet(s.offlineTerminalEventKey(commitment.PlanID)),
		clientv3.OpGet(s.fleetGitHubDispatchPreparationKey(commitment.PlanID)),
		clientv3.OpGet(s.fleetRunnerDispatchKey(commitment.PlanID)),
		clientv3.OpGet(s.fleetRunnerAttemptPrefix(commitment.PlanID), clientv3.WithPrefix()),
		clientv3.OpGet(s.fleetExecutorMetaKey(commitment.PlanID, ""), clientv3.WithPrefix()),
		clientv3.OpGet(s.offlineDeleteDispatchReservationKey(commitment.PlanID)),
		clientv3.OpGet(s.offlineDeleteDispatchReservationEventKey(commitment.PlanID)),
	).Commit()
	if err != nil {
		return err
	}
	if len(response.Responses) != 22 {
		return ErrOfflineDeleteCommitmentConflict
	}
	rows := func(index int) [][]byte {
		rangeResult := response.Responses[index].GetResponseRange()
		if rangeResult == nil {
			return nil
		}
		out := make([][]byte, 0, len(rangeResult.Kvs))
		for _, kv := range rangeResult.Kvs {
			out = append(out, kv.Value)
		}
		return out
	}
	// A lost admission response may recover only the byte-identical reservation
	// for this commitment and supervised runtime. It does not create a second
	// dispatch or refresh any authority lifetime.
	if len(rows(dispatchHead)) != 0 || len(rows(dispatchEvent)) != 0 {
		if len(rows(dispatchHead)) != 1 || len(rows(dispatchEvent)) != 1 || !bytes.Equal(rows(dispatchHead)[0], rows(dispatchEvent)[0]) {
			return ErrOfflineDeleteCommitmentConflict
		}
		var prior offlineDeleteDispatch
		if json.Unmarshal(rows(dispatchHead)[0], &prior) != nil || prior.SchemaVersion != offlineDeleteDispatchSchema ||
			prior.PlanID != commitment.PlanID || prior.AttemptID != commitment.AttemptID || prior.GrantID != commitment.GrantID ||
			prior.CommitmentSHA256 != offline.DeleteCommitmentSHA256(exactCommitment) || prior.TargetID != commitment.TargetID ||
			prior.DropletID != commitment.DropletID || prior.FenceGeneration != commitment.FenceGeneration ||
			prior.AuthorityEpoch != commitment.AuthorityEpoch || prior.SupervisorRootID != commitment.SupervisorRootID ||
			prior.SupervisorExecutionID != commitment.SupervisorExecutionID || prior.RuntimeInstanceID != commitment.RuntimeInstanceID ||
			prior.State != "admitted" || prior.RetryPolicy != offline.DeleteRetryPolicy || prior.AdmittedAt.IsZero() {
			return ErrOfflineDeleteCommitmentConflict
		}
		return nil
	}
	required := []int{commitmentHead, commitmentEvent, bindingKey, attemptKey, grantHead, grantIssued,
		grantConsumed, checkpointHead, checkpointEvent, ownerKey, planKey, fenceKey, epochKey}
	for _, index := range required {
		if len(rows(index)) != 1 {
			return ErrOfflineDeleteCommitmentConflict
		}
	}
	for _, index := range []int{abandonedKey, terminalHead, terminalEvent, githubPreparation, runnerDispatch, runnerAttempts, executorAttempts} {
		if len(rows(index)) != 0 {
			return ErrOfflineDeleteCommitmentConflict
		}
	}
	if !bytes.Equal(rows(commitmentHead)[0], exactCommitment) || !bytes.Equal(rows(commitmentEvent)[0], exactCommitment) {
		return ErrOfflineDeleteCommitmentConflict
	}

	binding, err := offline.DecodeBinding(rows(bindingKey)[0])
	if err != nil || binding.SchemaVersion != offline.BindingSchemaV2 || binding.PlanID != commitment.PlanID ||
		binding.TargetID != commitment.TargetID || binding.OwnerAuthorizationID != candidate.OwnerAuthorizationID ||
		offline.DeleteCommitmentSHA256(rows(bindingKey)[0]) != commitment.BindingSHA256 {
		return ErrOfflineDeleteCommitmentConflict
	}
	attempt, err := decodeOfflineAttempt(rows(attemptKey)[0], commitment.PlanID)
	if err != nil || attempt.ID != commitment.AttemptID || offline.DeleteCommitmentSHA256(rows(attemptKey)[0]) != commitment.AttemptSHA256 {
		return ErrOfflineDeleteCommitmentConflict
	}
	grant, err := decodeOfflineEffectGrantPair(rows(grantHead)[0], rows(grantIssued)[0], rows(grantConsumed)[0], commitment.PlanID)
	if err != nil || grant.Phase != "consumed" || grant.ConsumedAt == nil || grant.ID != commitment.GrantID ||
		offline.DeleteCommitmentSHA256(rows(grantHead)[0]) != commitment.EffectGrantSHA256 {
		return ErrOfflineDeleteCommitmentConflict
	}
	checkpoint, err := decodeOfflineCheckpointPair(rows(checkpointHead)[0], rows(checkpointEvent)[0], commitment.PlanID)
	if err != nil || checkpoint.Prechange == nil || !checkpoint.Prechange.ProviderTopologyVerified ||
		checkpoint.AttemptID != attempt.ID || checkpoint.EvidenceSHA256 != grant.PrechangeSHA256 ||
		checkpoint.Prechange.BindingSHA256 != commitment.BindingSHA256 ||
		checkpoint.FenceGeneration != commitment.FenceGeneration || checkpoint.AuthorityEpoch != commitment.AuthorityEpoch {
		return ErrOfflineDeleteCommitmentConflict
	}
	if string(rows(ownerKey)[0]) != commitment.PlanID+":"+binding.OwnerEnvelopeSHA256 {
		return ErrOfflineDeleteCommitmentConflict
	}
	var plan v3Record
	var typed fleet.CapacityPlan
	if decodeV3Record(rows(planKey)[0], &plan) != nil || !validOfflineContractionPlan(plan.Operation, commitment.PlanID, &typed) {
		return ErrOfflineDeleteCommitmentConflict
	}
	var fence v3FleetTargetFence
	if decodeV3Record(rows(fenceKey)[0], &fence) != nil || !fence.Held || fence.HolderPlanID != commitment.PlanID ||
		fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 || uint64(fence.Generation) != commitment.FenceGeneration ||
		fence.AuthorityEpoch != commitment.AuthorityEpoch {
		return ErrOfflineDeleteCommitmentConflict
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(string(rows(epochKey)[0])), 10, 64)
	if err != nil || epoch <= 0 || epoch != commitment.AuthorityEpoch {
		return ErrOfflineDeleteCommitmentConflict
	}
	_, reconstructed, err := offline.NewDeleteCommitment(binding, attempt, grant, commitment.SupervisorRootID,
		commitment.SupervisorExecutionID, commitment.RuntimeInstanceID, commitment.CommittedAt)
	if err != nil || !bytes.Equal(reconstructed, exactCommitment) {
		return ErrOfflineDeleteCommitmentConflict
	}
	now := clock().UTC()
	if now.IsZero() || now.Before(commitment.CommittedAt) || now.Before(*grant.ConsumedAt) ||
		!now.Before(grant.ExpiresAt) || !now.Before(binding.ExpiresAt) || !now.Before(checkpoint.Prechange.ExpiresAt) {
		return ErrOfflineDeleteCommitmentConflict
	}
	dispatch := offlineDeleteDispatch{
		SchemaVersion: offlineDeleteDispatchSchema, PlanID: commitment.PlanID, AttemptID: commitment.AttemptID,
		GrantID: commitment.GrantID, CommitmentSHA256: offline.DeleteCommitmentSHA256(exactCommitment),
		TargetID: commitment.TargetID, DropletID: commitment.DropletID, FenceGeneration: commitment.FenceGeneration,
		AuthorityEpoch: commitment.AuthorityEpoch, SupervisorRootID: commitment.SupervisorRootID,
		SupervisorExecutionID: commitment.SupervisorExecutionID, RuntimeInstanceID: commitment.RuntimeInstanceID,
		State: "admitted", RetryPolicy: offline.DeleteRetryPolicy, AdmittedAt: now,
	}
	dispatchBytes, err := json.Marshal(dispatch)
	if err != nil {
		return err
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.CreateRevision(s.offlineDeleteDispatchReservationKey(commitment.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineDeleteDispatchReservationEventKey(commitment.PlanID)), "=", 0),
	}
	for _, index := range required {
		kv := response.Responses[index].GetResponseRange().Kvs[0]
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(string(kv.Key)), "=", kv.ModRevision))
	}
	for _, index := range []int{abandonedKey, terminalHead, terminalEvent, githubPreparation, runnerDispatch, runnerAttempts, executorAttempts} {
		key := response.Responses[index].GetResponseRange().Kvs
		if len(key) != 0 {
			return ErrOfflineDeleteCommitmentConflict
		}
	}
	compares = append(compares,
		clientv3.Compare(clientv3.CreateRevision(s.fleetTargetAbandonedKey(commitment.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineTerminalKey(commitment.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineTerminalEventKey(commitment.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetGitHubDispatchPreparationKey(commitment.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(commitment.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptPrefix(commitment.PlanID)).WithPrefix(), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetExecutorMetaKey(commitment.PlanID, "")).WithPrefix(), "=", 0),
	)
	txn, err := s.kv.Txn(ctx).If(compares...).Then(
		clientv3.OpPut(s.offlineDeleteDispatchReservationKey(commitment.PlanID), string(dispatchBytes)),
		clientv3.OpPut(s.offlineDeleteDispatchReservationEventKey(commitment.PlanID), string(dispatchBytes)),
	).Commit()
	if err != nil {
		return err
	}
	if !txn.Succeeded {
		return ErrOfflineDeleteCommitmentConflict
	}
	return nil
}
