package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

const fleetDeploymentRecoveryEffect = "fleetDeploymentRecoveryEffect"

// RequeueHeldFleetDeployment permits one read-only recovery of a failed Fleet
// deployment whose completed job effect and exclusive app admission remain bound.
func (s *V3OperationStore) RequeueHeldFleetDeployment(ctx context.Context, operationID string, token effect.Token) error {
	rec, rev, err := s.load(ctx, operationID)
	if err != nil {
		return err
	}
	op := rec.Operation
	if op.Kind != "app.deploy" || op.Status != model.OperationFailed || op.Metadata["manualRecoveryRequired"] != true || op.Metadata["externalEffectRecoveryPending"] != true || op.Metadata["effectId"] != token.EffectID || op.App == "" {
		return fmt.Errorf("operation is not a held Fleet deployment")
	}
	if marker, present := op.Metadata[fleetDeploymentRecoveryEffect]; present {
		encodedMarker, err := json.Marshal(marker)
		var prior effect.Token
		if err != nil || json.Unmarshal(encodedMarker, &prior) != nil || prior != token || op.Metadata["recoveredAfterLeaseExpiry"] != true {
			return fmt.Errorf("held recovery marker differs from expired claim")
		}
	}
	accepted, err := s.VerifyClaimedDeployment(ctx, op)
	if err != nil || accepted.FleetAppTarget == nil || accepted.Deployment == nil || len(accepted.Regions) != 1 || accepted.Regions[0].TrafficWeight != 100 {
		return fmt.Errorf("held deployment acceptance is invalid: %v", err)
	}
	effects, err := NewV3DeploymentEffectReservations(s)
	if err != nil {
		return err
	}
	effectRecord, effectKey, effectRev, err := effects.loadToken(ctx, token)
	if err != nil || token.Generation > rec.Generation || effectRecord.Lifecycle != effect.LifecycleCompleted || effectRecord.Completion == nil || effectRecord.Completion.Outcome != effect.OutcomeSucceeded || effectRecord.Completion.Verification.Decision != effect.VerificationSucceeded || effectRecord.Completion.Verification.EvidenceSource != "nomad-job-health" {
		return fmt.Errorf("held deployment effect is not completed and healthy")
	}
	attempted, attemptErr := effects.SubmitAttempted(ctx, token)
	if attemptErr != nil || !attempted {
		return fmt.Errorf("held deployment effect has no submission")
	}
	if _, err := effects.validateDeploymentEffectReservation(ctx, effectRecord.Reservation, op); err != nil {
		return err
	}
	var input deploymentEffectInput
	if json.Unmarshal(effectRecord.Reservation.LaunchPayload, &input) != nil || input.Region != accepted.Regions[0].Name || input.NomadRegion != accepted.Regions[0].NomadRegion || input.DeploymentID != accepted.Deployment.ID {
		return fmt.Errorf("held deployment effect differs from placement")
	}
	keys := []string{s.operationAcceptanceIndexKey(op.ID), effects.tokenKey(token.EffectID), effects.submitAttemptKey(token), s.appAdmissionActiveKey(op.App, op.ID), s.appAdmissionExclusiveKey(op.App), s.appAdmissionFenceKey(op.App), s.deploymentKey(accepted.Deployment.ID)}
	rows := make([]*clientv3.GetResponse, len(keys))
	compares := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(s.opKey(op.ID)), "=", rev), clientv3.Compare(clientv3.ModRevision(effectKey), "=", effectRev), clientv3.Compare(clientv3.CreateRevision(s.ownerKey(op.ID)), "=", 0), clientv3.Compare(clientv3.CreateRevision(s.runningKey(op.ID)), "=", 0), clientv3.Compare(clientv3.CreateRevision(effects.gateKey(op.App)), "=", 0)}
	for i, key := range keys {
		rows[i], err = s.kv.Get(ctx, key)
		if err != nil || len(rows[i].Kvs) != 1 {
			return fmt.Errorf("held deployment fence is missing: %s", key)
		}
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(key), "=", rows[i].Kvs[0].ModRevision))
	}
	if string(rows[1].Kvs[0].Value) != effectKey || string(rows[3].Kvs[0].Value) != op.ID || string(rows[4].Kvs[0].Value) != op.ID {
		return fmt.Errorf("held deployment fence differs")
	}
	regionKey := s.deploymentRegionKey(accepted.Deployment.ID, accepted.Regions[0].Name)
	regionRow, err := s.kv.Get(ctx, regionKey)
	if err != nil || len(regionRow.Kvs) != 1 {
		return fmt.Errorf("held deployment region missing")
	}
	var region v3DeploymentRegionRecord
	if decodeV3Record(regionRow.Kvs[0].Value, &region) != nil || !reflect.DeepEqual(region.Region, accepted.Regions[0]) {
		return fmt.Errorf("held deployment region changed")
	}
	compares = append(compares, clientv3.Compare(clientv3.ModRevision(regionKey), "=", regionRow.Kvs[0].ModRevision))
	acceptanceKey := string(rows[0].Kvs[0].Value)
	acceptance, err := s.loadAcceptance(ctx, acceptanceKey)
	if err != nil {
		return err
	}
	if acceptance.record.Accepted.Intent.CanonicalDigest != accepted.Intent.CanonicalDigest || acceptanceKey != s.acceptanceKey(acceptance.record.Identity) {
		return fmt.Errorf("held deployment acceptance changed")
	}
	compares = append(compares, clientv3.Compare(clientv3.ModRevision(acceptanceKey), "=", acceptance.revision))
	op.Metadata[fleetDeploymentRecoveryEffect] = token
	op.Status, op.Message, op.FinishedAt, op.LockedUntil, op.LockedBy = model.OperationQueued, "Fleet deployment recovery queued", nil, nil, ""
	op.NextAttemptAt = time.Time{}
	rec.Operation = op
	encoded, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	txn, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(s.opKey(op.ID), string(encoded))).Commit()
	if err != nil {
		return err
	}
	if !txn.Succeeded {
		return fmt.Errorf("held deployment changed during recovery admission")
	}
	return nil
}

// ClaimedFleetDeploymentRecovery returns the predecessor effect only for a
// requeued recovery claim. An absent marker selects the ordinary deployment path.
func (s *V3OperationStore) ClaimedFleetDeploymentRecovery(ctx context.Context, claim store.OperationClaim) (*effect.Record, error) {
	rec, _, err := s.load(ctx, claim.OperationID())
	if err != nil || rec.Operation.Status != model.OperationRunning || rec.Operation.LockedBy != claim.OwnerID() || rec.Generation != claim.Generation() {
		return nil, store.ErrOperationOwnershipLost
	}
	raw, exists := rec.Operation.Metadata[fleetDeploymentRecoveryEffect]
	if !exists {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var token effect.Token
	if json.Unmarshal(b, &token) != nil {
		return nil, fmt.Errorf("invalid recovery effect marker")
	}
	effects, err := NewV3DeploymentEffectReservations(s)
	if err != nil {
		return nil, err
	}
	record, _, _, err := effects.loadToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if record.Lifecycle != effect.LifecycleCompleted || record.Reservation.OperationClaim.OperationID != claim.OperationID() || token.Generation >= claim.Generation() {
		return nil, fmt.Errorf("recovery effect differs from claim")
	}
	if _, err := effects.validateDeploymentEffectReservation(ctx, record.Reservation, rec.Operation); err != nil {
		return nil, err
	}
	attempted, err := effects.SubmitAttempted(ctx, token)
	if err != nil || !attempted {
		return nil, fmt.Errorf("recovery effect has no submission")
	}
	return &record, nil
}

func (s *V3OperationStore) fleetRecoveryHealthKey(operationID string, generation int64) string {
	return fmt.Sprintf("%s/v3/fleet-deployment-recovery-health/%s/%020d", s.prefix, operationID, generation)
}

// RecordClaimedFleetRecoveryHealth commits a claim-bound observation only
// after the worker's read-only verifier returns successfully.
func (s *V3OperationStore) RecordClaimedFleetRecoveryHealth(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, remote *nomad.Client) (effect.Token, error) {
	if lock == nil || lock.Fence() == "" || lock.Context().Err() != nil || remote == nil {
		return effect.Token{}, store.ErrOperationOwnershipLost
	}
	rec, rev, err := s.load(ctx, claim.OperationID())
	if err != nil {
		return effect.Token{}, err
	}
	record, err := s.ClaimedFleetDeploymentRecovery(ctx, claim)
	if err != nil || record == nil {
		return effect.Token{}, fmt.Errorf("recovery record unavailable: %v", err)
	}
	effects, err := NewV3DeploymentEffectReservations(s)
	if err != nil {
		return effect.Token{}, err
	}
	_, key, effectRev, err := effects.loadToken(ctx, record.Token)
	if err != nil {
		return effect.Token{}, err
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
		return effect.Token{}, store.ErrOperationOwnershipLost
	}
	if err := nomad.VerifyCompletedDeploymentJobHealth(ctx, remote, *record); err != nil {
		return effect.Token{}, err
	}
	proofKey := s.fleetRecoveryHealthKey(claim.OperationID(), claim.Generation())
	encoded, err := json.Marshal(record.Token)
	if err != nil {
		return effect.Token{}, err
	}
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", rev), clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision), clientv3.Compare(clientv3.Value(s.appLockKey(rec.Operation.App)), "=", lock.Fence()), clientv3.Compare(clientv3.ModRevision(key), "=", effectRev), clientv3.Compare(clientv3.CreateRevision(proofKey), "=", 0)).Then(clientv3.OpPut(proofKey, string(encoded))).Commit()
	if err != nil {
		return effect.Token{}, err
	}
	if !txn.Succeeded {
		return effect.Token{}, store.ErrOperationOwnershipLost
	}
	return record.Token, nil
}

func (s *V3OperationStore) requireFleetRecoveryHealth(ctx context.Context, claim store.OperationClaim, token effect.Token) (clientv3.Cmp, error) {
	key := s.fleetRecoveryHealthKey(claim.OperationID(), claim.Generation())
	row, err := s.kv.Get(ctx, key)
	if err != nil || len(row.Kvs) != 1 {
		return clientv3.Cmp{}, fmt.Errorf("fresh Fleet recovery health proof is unavailable")
	}
	var got effect.Token
	if json.Unmarshal(row.Kvs[0].Value, &got) != nil || got != token {
		return clientv3.Cmp{}, fmt.Errorf("fresh Fleet recovery health proof differs")
	}
	return clientv3.Compare(clientv3.ModRevision(key), "=", row.Kvs[0].ModRevision), nil
}

// RequeueHeldFleetDeploymentByID resolves the exact retained effect from the
// held receipt; the operator cannot choose or replace effect evidence.
func (s *V3OperationStore) RequeueHeldFleetDeploymentByID(ctx context.Context, operationID, deploymentID string) error {
	op, _, err := s.load(ctx, operationID)
	if err != nil {
		return err
	}
	if deploymentID == "" || op.Operation.Payload["deploymentId"] != deploymentID {
		return fmt.Errorf("deployment identity differs from held operation")
	}
	effectID, ok := op.Operation.Metadata["effectId"].(string)
	if !ok || effectID == "" {
		return fmt.Errorf("held operation has no effect identity")
	}
	effects, err := NewV3DeploymentEffectReservations(s)
	if err != nil {
		return err
	}
	index, err := s.kv.Get(ctx, effects.tokenKey(effectID))
	if err != nil || len(index.Kvs) != 1 {
		return fmt.Errorf("held effect index unavailable")
	}
	row, err := s.kv.Get(ctx, string(index.Kvs[0].Value))
	if err != nil || len(row.Kvs) != 1 {
		return fmt.Errorf("held effect unavailable")
	}
	var record effect.Record
	if json.Unmarshal(row.Kvs[0].Value, &record) != nil || record.Token.EffectID != effectID {
		return fmt.Errorf("held effect identity differs")
	}
	return s.RequeueHeldFleetDeployment(ctx, operationID, record.Token)
}

// StopClaimedFleetDeploymentRecovery retains the original failed deployment
// projection and both holds after a failed fresh health check.
func (s *V3OperationStore) StopClaimedFleetDeploymentRecovery(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock) error {
	if lock == nil || lock.Fence() == "" || lock.Context().Err() != nil {
		return store.ErrOperationOwnershipLost
	}
	rec, rev, err := s.load(ctx, claim.OperationID())
	if err != nil || rec.Operation.Status != model.OperationRunning || rec.Operation.LockedBy != claim.OwnerID() || rec.Generation != claim.Generation() || rec.Operation.Metadata[fleetDeploymentRecoveryEffect] == nil || rec.Operation.Metadata["manualRecoveryRequired"] != true || rec.Operation.Metadata["externalEffectRecoveryPending"] != true {
		return store.ErrOperationOwnershipLost
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
		return store.ErrOperationOwnershipLost
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	rec.Operation.Status, rec.Operation.Message, rec.Operation.LockedBy, rec.Operation.LockedUntil, rec.Operation.FinishedAt = model.OperationFailed, "Fleet deployment recovery stopped: fresh Nomad health was not verified", "", nil, &now
	delete(rec.Operation.Metadata, fleetDeploymentRecoveryEffect)
	encoded, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", rev), clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision), clientv3.Compare(clientv3.Value(s.appLockKey(rec.Operation.App)), "=", lock.Fence())).Then(clientv3.OpPut(s.opKey(claim.OperationID()), string(encoded)), clientv3.OpDelete(s.ownerKey(claim.OperationID())), clientv3.OpDelete(s.runningKey(claim.OperationID()))).Commit()
	if err != nil {
		return err
	}
	if !txn.Succeeded {
		return store.ErrOperationOwnershipLost
	}
	return nil
}
