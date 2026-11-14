package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet/fleetcreate"
	"norn/v2/api/model"
)

// RecordFleetCreateRecoveryReadback records the verifier's exact observation
// for the current attempt only after its one-use grant was consumed. The record
// is immutable and authorizes no effect by itself.
func (s *V3OperationStore) RecordFleetCreateRecoveryReadback(ctx context.Context, readback fleetcreate.RecoveryReadback, principal string) error {
	if s == nil || principal != fleetCreateVerifierSubject || readback.Validate() != nil {
		return ErrFleetCreateIntentRefused
	}
	fenceKey := s.fleetCreateFenceKey(readback.Cluster)
	attemptKey := s.fleetCreateAttemptKey(readback.IntentID, readback.PriorAttemptID)
	grantKey := s.fleetCreateGrantKey(readback.IntentID, readback.PriorAttemptID)
	recoveryKey := s.fleetCreateRecoveryKey(readback.IntentID, readback.PriorAttemptID)
	dispatchKey := s.fleetCreateDispatchKey(readback.IntentID)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(attemptKey), clientv3.OpGet(grantKey),
		clientv3.OpGet(s.fleetCreateGrantEventKey(readback.IntentID, readback.PriorAttemptID, 1)),
		clientv3.OpGet(s.fleetCreateGrantEventKey(readback.IntentID, readback.PriorAttemptID, 2)),
		clientv3.OpGet(recoveryKey), clientv3.OpGet(dispatchKey)).Commit()
	if err != nil {
		return err
	}
	if len(response.Responses) != 7 {
		return ErrFleetCreateIntentRefused
	}
	rows := make([][]*mvccpb.KeyValue, 7)
	for i := range rows {
		rows[i] = response.Responses[i].GetResponseRange().Kvs
	}
	if len(rows[0]) != 1 || len(rows[1]) != 1 || len(rows[2]) != 1 || len(rows[3]) != 1 || len(rows[4]) != 1 || len(rows[6]) != 1 {
		return ErrFleetCreateIntentRefused
	}
	var fence fleetCreateFenceRecord
	var attempt fleetCreateAttemptRecord
	var grant, issued, consumed fleetCreateGrantRecord
	var dispatch fleetCreateDispatchRecord
	if decodeV3Record(rows[0][0].Value, &fence) != nil || decodeV3Record(rows[1][0].Value, &attempt) != nil ||
		decodeV3Record(rows[2][0].Value, &grant) != nil || decodeV3Record(rows[3][0].Value, &issued) != nil ||
		decodeV3Record(rows[4][0].Value, &consumed) != nil || decodeV3Record(rows[6][0].Value, &dispatch) != nil {
		return ErrFleetCreateIntentRefused
	}
	digest, err := fence.Intent.Digest()
	if err != nil || fence.IntentSHA256 != digest || !readback.MatchesIntent(fence.Intent) ||
		fence.AttemptGeneration < attempt.Attempt.Generation ||
		attempt.Attempt.IntentID != readback.IntentID || !attempt.Attempt.MatchesStart(attempt.Request) ||
		grant.Grant.IntentID != readback.IntentID || grant.Grant.AttemptID != readback.PriorAttemptID ||
		!grant.Grant.MatchesRequest(fleetCreateRequestFromGrant(grant.Grant)) || !grant.Consumed || grant.ConsumedAt == nil ||
		!bytes.Equal(rows[3][0].Value, func() []byte {
			raw, _ := json.Marshal(fleetCreateGrantRecord{Grant: issued.Grant, CreatedAt: issued.CreatedAt})
			return raw
		}()) ||
		!consumed.Consumed || consumed.ConsumedAt == nil || consumed.Grant != grant.Grant || !bytes.Equal(rows[2][0].Value, rows[4][0].Value) ||
		dispatch.BindingSHA256 != dispatch.Binding.Digest() || !dispatch.Binding.MatchesIntent(fence.Intent) || !dispatch.Binding.MatchesStart(attempt.Request) ||
		readback.ExpectedNodeCount != 9 || readback.PriorGrantSHA256 != grant.Grant.Digest() {
		return ErrFleetCreateIntentRefused
	}
	// A recovery readback is valid only while its prior attempt still owns the
	// fence. Check authority before treating an existing identical record as an
	// idempotent replay; a successor attempt makes the old observation stale.
	if fence.CurrentAttemptID != readback.PriorAttemptID || fence.AttemptGeneration != attempt.Attempt.Generation {
		return ErrFleetCreateIntentRefused
	}
	if len(rows[5]) == 1 {
		var existing fleetCreateRecoveryRecord
		if decodeV3Record(rows[5][0].Value, &existing) == nil && existing.Readback == readback && existing.Digest == readback.Digest() {
			return nil
		}
		return ErrFleetCreateIntentRefused
	}
	if len(rows[5]) != 0 {
		return ErrFleetCreateIntentRefused
	}
	record := fleetCreateRecoveryRecord{Readback: readback, Digest: readback.Digest(), CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	committed, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(fenceKey), "=", rows[0][0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(attemptKey), "=", rows[1][0].ModRevision), clientv3.Compare(clientv3.ModRevision(grantKey), "=", rows[2][0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(dispatchKey), "=", rows[6][0].ModRevision), clientv3.Compare(clientv3.CreateRevision(recoveryKey), "=", 0)).
		Then(clientv3.OpPut(recoveryKey, string(raw))).Commit()
	if err != nil {
		if s.fleetCreateRecoveryReadbackIsCurrent(ctx, fenceKey, attemptKey, recoveryKey, readback) {
			return nil
		}
		return err
	}
	if !committed.Succeeded {
		return ErrFleetCreateIntentRefused
	}
	return nil
}

// fleetCreateRecoveryReadbackIsCurrent resolves a lost response only when an
// exact durable readback still belongs to the attempt that owns the fence. The
// fenced authority and recovery record are read in one etcd transaction so a
// later successor cannot make a stale record look current during reconciliation.
func (s *V3OperationStore) fleetCreateRecoveryReadbackIsCurrent(ctx context.Context, fenceKey, attemptKey, recoveryKey string, readback fleetcreate.RecoveryReadback) bool {
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(attemptKey), clientv3.OpGet(recoveryKey)).Commit()
	if err != nil || response == nil || len(response.Responses) != 3 {
		return false
	}
	rows := make([][]*mvccpb.KeyValue, 3)
	for i := range rows {
		rows[i] = response.Responses[i].GetResponseRange().Kvs
		if len(rows[i]) != 1 {
			return false
		}
	}
	var fence fleetCreateFenceRecord
	var attempt fleetCreateAttemptRecord
	var recovery fleetCreateRecoveryRecord
	if decodeV3Record(rows[0][0].Value, &fence) != nil || decodeV3Record(rows[1][0].Value, &attempt) != nil ||
		decodeV3Record(rows[2][0].Value, &recovery) != nil {
		return false
	}
	digest, err := fence.Intent.Digest()
	return err == nil && fence.IntentSHA256 == digest && readback.MatchesIntent(fence.Intent) &&
		fence.CurrentAttemptID == readback.PriorAttemptID && fence.AttemptGeneration == attempt.Attempt.Generation &&
		attempt.Attempt.IntentID == readback.IntentID && attempt.Attempt.AttemptID == readback.PriorAttemptID &&
		attempt.Attempt.MatchesStart(attempt.Request) && recovery.Readback == readback &&
		recovery.Digest == readback.Digest()
}

// StartFleetCreateRecoveryAttempt advances the same intent only after exact
// verifier recovery readback and durable consumption of the previous grant.
func (s *V3OperationStore) StartFleetCreateRecoveryAttempt(ctx context.Context, request fleetcreate.AttemptStartRequest, priorAttemptID, principal string) (fleetcreate.Attempt, error) {
	var zero fleetcreate.Attempt
	if s == nil || request.Validate() != nil || principal != fleetCreateAttemptSubject || uuid.Validate(priorAttemptID) != nil || priorAttemptID == request.AttemptID {
		return zero, ErrFleetCreateIntentRefused
	}
	fenceKey := s.fleetCreateFenceKey(request.Cluster)
	attemptKey := s.fleetCreateAttemptKey(request.IntentID, request.AttemptID)
	priorAttemptKey := s.fleetCreateAttemptKey(request.IntentID, priorAttemptID)
	grantKey := s.fleetCreateGrantKey(request.IntentID, priorAttemptID)
	recoveryKey := s.fleetCreateRecoveryKey(request.IntentID, priorAttemptID)
	dispatchKey := s.fleetCreateDispatchKey(request.IntentID)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(priorAttemptKey), clientv3.OpGet(grantKey),
		clientv3.OpGet(s.fleetCreateGrantEventKey(request.IntentID, priorAttemptID, 1)),
		clientv3.OpGet(s.fleetCreateGrantEventKey(request.IntentID, priorAttemptID, 2)), clientv3.OpGet(recoveryKey), clientv3.OpGet(dispatchKey)).Commit()
	if err != nil {
		return zero, err
	}
	if len(response.Responses) != 7 {
		return zero, ErrFleetCreateIntentRefused
	}
	rows := make([][]*mvccpb.KeyValue, 7)
	for i := range rows {
		rows[i] = response.Responses[i].GetResponseRange().Kvs
	}
	if len(rows[0]) != 1 || len(rows[1]) != 1 || len(rows[2]) != 1 || len(rows[3]) != 1 || len(rows[4]) != 1 || len(rows[5]) != 1 || len(rows[6]) != 1 {
		return zero, ErrFleetCreateIntentRefused
	}
	var fence fleetCreateFenceRecord
	var prior fleetCreateAttemptRecord
	var grant, issued, consumed fleetCreateGrantRecord
	var recovery fleetCreateRecoveryRecord
	var dispatch fleetCreateDispatchRecord
	if decodeV3Record(rows[0][0].Value, &fence) != nil || decodeV3Record(rows[1][0].Value, &prior) != nil ||
		decodeV3Record(rows[2][0].Value, &grant) != nil || decodeV3Record(rows[3][0].Value, &issued) != nil ||
		decodeV3Record(rows[4][0].Value, &consumed) != nil || decodeV3Record(rows[5][0].Value, &recovery) != nil ||
		decodeV3Record(rows[6][0].Value, &dispatch) != nil {
		return zero, ErrFleetCreateIntentRefused
	}
	intentDigest, err := fence.Intent.Digest()
	if err != nil || fence.IntentSHA256 != intentDigest || fence.Intent.Cluster != request.Cluster || fence.Intent.IntentID != request.IntentID ||
		(fence.CurrentAttemptID != priorAttemptID && fence.CurrentAttemptID != request.AttemptID) ||
		fence.AttemptGeneration < prior.Attempt.Generation || fence.AttemptGeneration > prior.Attempt.Generation+1 ||
		prior.Attempt.AttemptID != priorAttemptID || !prior.Attempt.MatchesStart(prior.Request) ||
		!grant.Consumed || grant.ConsumedAt == nil || grant.Grant.AttemptID != priorAttemptID ||
		!bytes.Equal(rows[3][0].Value, func() []byte {
			raw, _ := json.Marshal(fleetCreateGrantRecord{Grant: issued.Grant, CreatedAt: issued.CreatedAt})
			return raw
		}()) ||
		!consumed.Consumed || consumed.Grant != grant.Grant || !bytes.Equal(rows[2][0].Value, rows[4][0].Value) || recovery.Readback.Digest() != recovery.Digest ||
		recovery.Readback.PriorAttemptID != priorAttemptID || !recovery.Readback.MatchesIntent(fence.Intent) ||
		recovery.Readback.PriorGrantSHA256 != grant.Grant.Digest() || !dispatch.Binding.MatchesStart(request) ||
		request.IntentSHA256 != intentDigest || request.PlanID != fence.Intent.PlanID {
		return zero, ErrFleetCreateIntentRefused
	}
	plan, planRevision, err := s.load(ctx, request.PlanID)
	if err != nil {
		return zero, err
	}
	converge, planDigest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.ID != request.PlanID || plan.Operation.Kind != "fleet.capacity-plan" || plan.Operation.Status != model.OperationSucceeded ||
		converge == nil || cluster != request.Cluster || converge.CommitSHA != fence.Intent.CommitSHA || planDigest != "sha256:"+fence.Intent.PlanSHA256 {
		return zero, ErrFleetCreateIntentRefused
	}
	if fence.CurrentAttemptID == request.AttemptID && fence.AttemptGeneration == prior.Attempt.Generation+1 {
		stored, readErr := s.kv.Get(ctx, attemptKey)
		if readErr != nil {
			return zero, readErr
		}
		if len(stored.Kvs) != 1 {
			return zero, ErrFleetCreateIntentRefused
		}
		var record fleetCreateAttemptRecord
		if decodeV3Record(stored.Kvs[0].Value, &record) != nil || record.Request != request ||
			record.Attempt != fleetcreate.AttemptForGeneration(request, fence.AttemptGeneration) || !record.Attempt.MatchesStart(request) {
			return zero, ErrFleetCreateIntentRefused
		}
		return record.Attempt, nil
	}
	if fence.CurrentAttemptID != priorAttemptID || fence.AttemptGeneration != prior.Attempt.Generation {
		return zero, ErrFleetCreateIntentRefused
	}
	if fence.AttemptGeneration >= 9 {
		return zero, ErrFleetCreateIntentRefused
	}
	if existing, getErr := s.kv.Get(ctx, attemptKey); getErr != nil {
		return zero, getErr
	} else if len(existing.Kvs) != 0 {
		return zero, ErrFleetCreateIntentRefused
	}
	priorFence := rows[0][0]
	fence.CurrentAttemptID, fence.AttemptGeneration = request.AttemptID, fence.AttemptGeneration+1
	fenceRaw, err := json.Marshal(fence)
	if err != nil {
		return zero, err
	}
	attempt := fleetcreate.AttemptForGeneration(request, fence.AttemptGeneration)
	attemptRaw, err := json.Marshal(fleetCreateAttemptRecord{Attempt: attempt, Request: request, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)})
	if err != nil {
		return zero, err
	}
	committed, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(fenceKey), "=", priorFence.ModRevision),
		clientv3.Compare(clientv3.ModRevision(priorAttemptKey), "=", rows[1][0].ModRevision), clientv3.Compare(clientv3.ModRevision(grantKey), "=", rows[2][0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(recoveryKey), "=", rows[5][0].ModRevision), clientv3.Compare(clientv3.ModRevision(dispatchKey), "=", rows[6][0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(request.PlanID)), "=", planRevision), clientv3.Compare(clientv3.CreateRevision(attemptKey), "=", 0)).
		Then(clientv3.OpPut(fenceKey, string(fenceRaw)), clientv3.OpPut(attemptKey, string(attemptRaw))).Commit()
	if err != nil {
		fenceReadback, fenceErr := s.kv.Get(ctx, fenceKey)
		attemptReadback, attemptErr := s.kv.Get(ctx, attemptKey)
		if fenceErr == nil && attemptErr == nil && len(fenceReadback.Kvs) == 1 && len(attemptReadback.Kvs) == 1 {
			var storedFence fleetCreateFenceRecord
			var storedAttempt fleetCreateAttemptRecord
			if decodeV3Record(fenceReadback.Kvs[0].Value, &storedFence) == nil && decodeV3Record(attemptReadback.Kvs[0].Value, &storedAttempt) == nil &&
				storedFence.CurrentAttemptID == request.AttemptID && storedFence.AttemptGeneration == prior.Attempt.Generation+1 &&
				storedAttempt.Request == request && storedAttempt.Attempt == attempt {
				return attempt, nil
			}
		}
		return zero, err
	}
	if !committed.Succeeded {
		return zero, ErrFleetCreateIntentRefused
	}
	return attempt, nil
}
