package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// VerifyClaimedWorkloadRelease reloads the signed acceptance and the
// control-owned Fleet target for one claimed app.workload-release operation.
// The caller must continue holding the claim and app lock before mutation.
func (s *V3OperationStore) VerifyClaimedWorkloadRelease(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock) (pipeline.WorkloadReleaseIntent, store.AcceptedOperation, error) {
	invalid := func(err error) (pipeline.WorkloadReleaseIntent, store.AcceptedOperation, error) {
		return pipeline.WorkloadReleaseIntent{}, store.AcceptedOperation{}, err
	}
	if s == nil || s.kv == nil || s.signer == nil || claim.OperationID() == "" || claim.OwnerID() == "" || claim.Generation() <= 0 || lock == nil || lock.Fence() == "" || lock.Context().Err() != nil {
		return invalid(fmt.Errorf("workload release signed acceptance is unavailable"))
	}
	currentOperation, _, err := s.load(ctx, claim.OperationID())
	if err != nil {
		return invalid(err)
	}
	if effectPayloadString(currentOperation.Operation.Payload, "workloadReleasePlan") != "" {
		intent, _, accepted, err := s.ResolveClaimedWorkloadReleasePlanFromBinding(ctx, claim, lock)
		if err != nil {
			return invalid(err)
		}
		return intent, accepted, nil
	}
	current, operationRevision, err := s.load(ctx, claim.OperationID())
	if err != nil || current.Operation.Kind != "app.workload-release" || current.Operation.Status != model.OperationRunning ||
		current.Operation.LockedBy != claim.OwnerID() || current.Generation != claim.Generation() || current.Operation.App == "" {
		return invalid(store.ErrOperationOwnershipLost)
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
		return invalid(store.ErrOperationOwnershipLost)
	}
	appLock, err := s.kv.Get(ctx, s.appLockKey(current.Operation.App))
	if err != nil || len(appLock.Kvs) != 1 || string(appLock.Kvs[0].Value) != lock.Fence() {
		return invalid(store.ErrOperationOwnershipLost)
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(claim.OperationID()))
	if err != nil || len(index.Kvs) != 1 {
		return invalid(fmt.Errorf("workload release acceptance index is unavailable"))
	}
	key := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, key)
	if err != nil || key != s.acceptanceKey(loaded.record.Identity) || loaded.record.Identity.Kind != current.Operation.Kind ||
		loaded.record.Identity.Resource != "app/"+current.Operation.App || loaded.record.Accepted.Operation.ID != claim.OperationID() ||
		loaded.record.Accepted.Intent.OperationID != claim.OperationID() {
		return invalid(fmt.Errorf("workload release acceptance index differs from claimed operation"))
	}
	accepted, err := s.replay(ctx, key, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil || accepted.Operation.ID != current.Operation.ID || accepted.Operation.Kind != current.Operation.Kind || accepted.Operation.App != current.Operation.App {
		return invalid(fmt.Errorf("workload release signed acceptance failed verification: %v", err))
	}
	want, err := json.Marshal(accepted.Operation.Payload)
	if err != nil {
		return invalid(err)
	}
	got, err := json.Marshal(current.Operation.Payload)
	if err != nil || !bytes.Equal(want, got) {
		return invalid(fmt.Errorf("claimed workload release payload differs from signed acceptance"))
	}
	intentRaw := effectPayloadString(current.Operation.Payload, "workloadReleaseIntent")
	var intent pipeline.WorkloadReleaseIntent
	if intentRaw == "" || json.Unmarshal([]byte(intentRaw), &intent) != nil || intent.Validate() != nil || intent.App != current.Operation.App {
		return invalid(fmt.Errorf("accepted workload release intent is invalid"))
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, intent.App, intent.ControlEnvironment)
	if err != nil || target.App != intent.App || target.ControlEnvironment != intent.ControlEnvironment || target.Cluster != intent.FleetCluster ||
		target.FleetEnvironment != intent.FleetEnvironment || target.Region != intent.Region || target.NomadRegion != intent.NomadRegion ||
		target.NomadNamespace != intent.Namespace || target.Generation != intent.FleetTargetGeneration || !slices.Equal(target.Datacenters, intent.FleetDatacenters) {
		return invalid(fmt.Errorf("current Fleet target differs from signed workload release intent"))
	}
	if lock.Context().Err() != nil {
		return invalid(store.ErrOperationOwnershipLost)
	}
	stillOwned, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(s.appLockKey(current.Operation.App)), "=", lock.Fence()),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(intent.App, intent.ControlEnvironment)), "=", targetRevision),
	).Then(clientv3.OpGet(s.ownerKey(claim.OperationID()))).Commit()
	if err != nil || !stillOwned.Succeeded || lock.Context().Err() != nil {
		return invalid(store.ErrOperationOwnershipLost)
	}
	accepted.FleetAppTarget = &target
	return intent, accepted, nil
}
