package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

type v3DeploymentRegionResult struct {
	Result model.DeploymentRegion `json:"result"`
}

func (s *V3OperationStore) deploymentRegionResultKey(id, name string) string {
	return s.prefix + "/v3/deployment-region-results/" + deploymentKeyPart(id) + "/" + deploymentKeyPart(name)
}

// finishClaimedDeployment is private until the etcd deploy worker verifies
// every external effect and can supply a complete result. It terminalizes the
// operation, deployment, region observations, and app admission in one claim-
// and app-lock-fenced transaction.
func (s *V3OperationStore) finishClaimedDeployment(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, result model.Deployment, regions []model.DeploymentRegion, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	if lock == nil || lock.Fence() == "" || lock.Context().Err() != nil || (status != model.OperationSucceeded && status != model.OperationFailed) {
		return store.ErrOperationOwnershipLost
	}
	record, operationRevision, err := s.load(ctx, claim.OperationID())
	if err != nil || record.Operation.Status != model.OperationRunning || record.Operation.LockedBy != claim.OwnerID() || record.Generation != claim.Generation() || record.Operation.App == "" {
		return store.ErrOperationOwnershipLost
	}
	op := record.Operation
	if value, _ := op.Payload["deploymentId"].(string); value == "" || value != result.ID || result.App != op.App || result.SagaID != op.SagaID {
		return fmt.Errorf("deployment result does not match claimed operation")
	}
	if (status == model.OperationSucceeded && result.Status != model.StatusDeployed) || (status == model.OperationFailed && result.Status != model.StatusFailed) {
		return fmt.Errorf("deployment and operation terminal statuses differ")
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
	if err != nil {
		return err
	}
	if len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
		return store.ErrOperationOwnershipLost
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(claim.OperationID()))
	if err != nil {
		return err
	}
	if len(index.Kvs) != 1 {
		return fmt.Errorf("deployment acceptance index is missing")
	}
	acceptanceKey := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, acceptanceKey)
	if err != nil {
		return fmt.Errorf("load deployment acceptance: %w", err)
	}
	if loaded.record.Accepted.Operation.ID != claim.OperationID() || loaded.record.Accepted.Intent.DeploymentID != result.ID {
		return fmt.Errorf("deployment acceptance link is invalid")
	}
	accepted, err := s.replay(ctx, acceptanceKey, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil {
		return err
	}
	if len(regions) != len(accepted.Regions) {
		return fmt.Errorf("deployment region result count differs from accepted regions")
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if op.Metadata == nil {
		op.Metadata = map[string]interface{}{}
	}
	for key, value := range metadata {
		op.Metadata[key] = value
	}
	op.Status, op.Message, op.LockedBy, op.LockedUntil, op.FinishedAt = status, message, "", nil, &now
	result.StartedAt = accepted.Deployment.StartedAt
	result.FinishedAt = &now
	result.Regions = nil
	if err := store.VerifyAcceptanceEvidence(store.AcceptanceEvidence{Identity: loaded.record.Identity, IdentityFingerprint: accepted.Intent.Fingerprint, IdentityOperationID: op.ID, Intent: accepted.Intent, Operation: op, Deployment: &result, Regions: accepted.Regions}); err != nil {
		return &store.AcceptanceSignatureError{Err: err}
	}
	encodedOperation, err := json.Marshal(v3Record{Operation: op, Generation: record.Generation})
	if err != nil {
		return err
	}
	encodedDeployment, err := json.Marshal(v3DeploymentRecord{Deployment: result})
	if err != nil {
		return err
	}
	deployment, err := s.kv.Get(ctx, s.deploymentKey(result.ID))
	if err != nil {
		return err
	}
	if len(deployment.Kvs) != 1 {
		return fmt.Errorf("accepted deployment record is unavailable")
	}
	comparisons := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.opKey(op.ID)), "=", operationRevision),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(op.ID)), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(s.ownerKey(op.ID)), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.Value(s.appLockKey(op.App)), "=", lock.Fence()),
		clientv3.Compare(clientv3.ModRevision(s.deploymentKey(result.ID)), "=", deployment.Kvs[0].ModRevision),
	}
	ops := []clientv3.Op{
		clientv3.OpPut(s.opKey(op.ID), string(encodedOperation)),
		clientv3.OpPut(s.deploymentKey(result.ID), string(encodedDeployment)),
		clientv3.OpDelete(s.ownerKey(op.ID)),
		clientv3.OpDelete(s.runningKey(op.ID)),
	}
	byName := make(map[string]model.DeploymentRegion, len(regions))
	for _, region := range regions {
		if region.Region == "" || byName[region.Region].Region != "" || region.DeploymentID != result.ID {
			return fmt.Errorf("deployment region result identity is invalid")
		}
		byName[region.Region] = region
	}
	for _, expected := range accepted.Regions {
		region, found := byName[expected.Name]
		if !found || region.NomadRegion != expected.NomadRegion || region.DesiredWeight != expected.TrafficWeight || region.ActiveWeight < 0 || region.ActiveWeight > expected.TrafficWeight {
			return fmt.Errorf("deployment region result differs from accepted placement")
		}
		switch region.Status {
		case model.StatusQueued, model.StatusBuilding, model.StatusTesting, model.StatusMigrating, model.StatusSubmitting, model.StatusHealthy, model.StatusDeployed, model.StatusFailed:
		default:
			return fmt.Errorf("deployment region result status is invalid")
		}
		if status == model.OperationSucceeded && (region.Status != model.StatusDeployed || region.ActiveWeight != expected.TrafficWeight) {
			return fmt.Errorf("successful deployment has incomplete region result")
		}
		if status == model.OperationFailed && region.Status != model.StatusFailed && op.Metadata["externalEffectRecoveryPending"] != true && op.Metadata["manualRecoveryRequired"] != true {
			return fmt.Errorf("failed deployment has unresolved region without recovery hold")
		}
		region.UpdatedAt = now
		encoded, err := json.Marshal(v3DeploymentRegionResult{Result: region})
		if err != nil {
			return err
		}
		key := s.deploymentRegionResultKey(result.ID, region.Region)
		comparisons = append(comparisons, clientv3.Compare(clientv3.CreateRevision(key), "=", 0))
		ops = append(ops, clientv3.OpPut(key, string(encoded)))
	}
	admissionCompares, admissionOps, err := s.releaseAppAdmission(ctx, op)
	if err != nil {
		return err
	}
	if op.Metadata["manualRecoveryRequired"] != true && op.Metadata["externalEffectRecoveryPending"] != true && len(admissionOps) == 0 {
		return fmt.Errorf("deployment app admission index is missing")
	}
	comparisons = append(comparisons, admissionCompares...)
	ops = append(ops, admissionOps...)
	txn, err := s.kv.Txn(ctx).If(comparisons...).Then(ops...).Commit()
	if err != nil {
		return err
	}
	if !txn.Succeeded {
		return store.ErrOperationOwnershipLost
	}
	return nil
}
