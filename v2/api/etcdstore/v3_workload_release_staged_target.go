package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

const workloadReleaseStagedTargetRecordSchema = "norn.workload-release-staged-target-record/v1"

type v3WorkloadReleaseStagedTargetRecord struct {
	SchemaVersion string                               `json:"schemaVersion"`
	OperationID   string                               `json:"operationId"`
	PlanDigest    string                               `json:"planDigest"`
	Binding       pipeline.WorkloadReleaseStagedTarget `json:"binding"`
	BindingDigest string                               `json:"bindingDigest"`
	WrittenAt     time.Time                            `json:"writtenAt"`
}

type claimedWorkloadReleasePlan struct {
	Plan              pipeline.WorkloadReleasePlan
	Accepted          store.AcceptedOperation
	OperationRevision int64
	OwnerRevision     int64
	App               string
	Target            FleetAppTarget
	TargetRevision    int64
}

func (s *V3OperationStore) workloadReleaseStagedTargetKey(operationID string) string {
	sum := sha256.Sum256([]byte(operationID))
	return s.prefix + "/v3/workload-release-staged-targets/" + hex.EncodeToString(sum[:])
}

func (s *V3OperationStore) verifyClaimedWorkloadReleasePlan(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock) (claimedWorkloadReleasePlan, error) {
	invalid := func(err error) (claimedWorkloadReleasePlan, error) { return claimedWorkloadReleasePlan{}, err }
	if s == nil || s.kv == nil || s.signer == nil || claim.OperationID() == "" || claim.OwnerID() == "" || claim.Generation() <= 0 ||
		lock == nil || lock.Fence() == "" || lock.Context().Err() != nil {
		return invalid(fmt.Errorf("workload release signed plan is unavailable"))
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
	acceptanceKey := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, acceptanceKey)
	if err != nil || acceptanceKey != s.acceptanceKey(loaded.record.Identity) || loaded.record.Identity.Kind != current.Operation.Kind ||
		loaded.record.Identity.Resource != "app/"+current.Operation.App || loaded.record.Accepted.Operation.ID != claim.OperationID() ||
		loaded.record.Accepted.Intent.OperationID != claim.OperationID() {
		return invalid(fmt.Errorf("workload release acceptance index differs from claimed operation"))
	}
	accepted, err := s.replay(ctx, acceptanceKey, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil || accepted.Operation.ID != current.Operation.ID || accepted.Operation.Kind != current.Operation.Kind || accepted.Operation.App != current.Operation.App {
		return invalid(fmt.Errorf("workload release signed acceptance failed verification: %v", err))
	}
	want, err := json.Marshal(accepted.Operation.Payload)
	if err != nil {
		return invalid(err)
	}
	got, err := json.Marshal(current.Operation.Payload)
	if err != nil || string(want) != string(got) {
		return invalid(fmt.Errorf("claimed workload release payload differs from signed acceptance"))
	}
	planRaw := effectPayloadString(current.Operation.Payload, "workloadReleasePlan")
	var plan pipeline.WorkloadReleasePlan
	if planRaw == "" || json.Unmarshal([]byte(planRaw), &plan) != nil || plan.Validate() != nil || plan.Intent.App != current.Operation.App {
		return invalid(fmt.Errorf("accepted workload release plan is invalid"))
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, plan.Intent.App, plan.Intent.ControlEnvironment)
	if err != nil || target.App != plan.Intent.App || target.ControlEnvironment != plan.Intent.ControlEnvironment || target.Cluster != plan.Intent.FleetCluster ||
		target.FleetEnvironment != plan.Intent.FleetEnvironment || target.Region != plan.Intent.Region || target.NomadRegion != plan.Intent.NomadRegion ||
		target.NomadNamespace != plan.Intent.Namespace || target.Generation != plan.Intent.FleetTargetGeneration || !slices.Equal(target.Datacenters, plan.Intent.FleetDatacenters) {
		return invalid(fmt.Errorf("current Fleet target differs from signed workload release plan"))
	}
	if lock.Context().Err() != nil {
		return invalid(store.ErrOperationOwnershipLost)
	}
	stillOwned, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.Value(s.appLockKey(current.Operation.App)), "=", lock.Fence()),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(plan.Intent.App, plan.Intent.ControlEnvironment)), "=", targetRevision),
	).Then(clientv3.OpGet(s.ownerKey(claim.OperationID()))).Commit()
	if err != nil || !stillOwned.Succeeded || lock.Context().Err() != nil {
		return invalid(store.ErrOperationOwnershipLost)
	}
	accepted.FleetAppTarget = &target
	return claimedWorkloadReleasePlan{Plan: plan, Accepted: accepted, OperationRevision: operationRevision,
		OwnerRevision: owner.Kvs[0].ModRevision, App: current.Operation.App, Target: target, TargetRevision: targetRevision}, nil
}

// BindClaimedWorkloadReleaseTarget persists a plan-bound exact scheduler
// observation once, while the signed operation, worker claim, app lock, and
// Fleet target generation are all current. A matching retry is idempotent;
// any different binding is a conflict and cannot overwrite the first target.
func (s *V3OperationStore) BindClaimedWorkloadReleaseTarget(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, binding pipeline.WorkloadReleaseStagedTarget) (pipeline.WorkloadReleaseStagedTarget, error) {
	verified, err := s.verifyClaimedWorkloadReleasePlan(ctx, claim, lock)
	if err != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, err
	}
	if _, err := verified.Plan.Resolve(binding, binding.EffectID, claim.Generation()); err != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, err
	}
	bindingBytes, err := json.Marshal(binding)
	if err != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, err
	}
	bindingDigest := checkpointDigest(bindingBytes)
	record := v3WorkloadReleaseStagedTargetRecord{SchemaVersion: workloadReleaseStagedTargetRecordSchema,
		OperationID: claim.OperationID(), PlanDigest: binding.PlanDigest, Binding: binding,
		BindingDigest: bindingDigest, WrittenAt: time.Now().UTC().Truncate(time.Microsecond)}
	encoded, err := json.Marshal(record)
	if err != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, err
	}
	key := s.workloadReleaseStagedTargetKey(claim.OperationID())
	prior, err := s.kv.Get(ctx, key)
	if err != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, err
	}
	if len(prior.Kvs) != 0 {
		stored, err := decodeWorkloadReleaseStagedTarget(prior.Kvs[0].Value, claim.OperationID(), verified.Plan)
		if err != nil {
			return pipeline.WorkloadReleaseStagedTarget{}, err
		}
		if stored.BindingDigest != bindingDigest {
			return pipeline.WorkloadReleaseStagedTarget{}, fmt.Errorf("workload release staged target binding is write-once")
		}
		if lock.Context().Err() != nil {
			return pipeline.WorkloadReleaseStagedTarget{}, store.ErrOperationOwnershipLost
		}
		return stored.Binding, nil
	}
	if binding.ClaimGeneration != claim.Generation() {
		return pipeline.WorkloadReleaseStagedTarget{}, fmt.Errorf("new staged target binding must be written by its current claim generation")
	}
	if lock.Context().Err() != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, store.ErrOperationOwnershipLost
	}
	txn, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", verified.OperationRevision),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", verified.OwnerRevision),
		clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.Value(s.appLockKey(verified.App)), "=", lock.Fence()),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(verified.Target.App, verified.Target.ControlEnvironment)), "=", verified.TargetRevision),
		clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
	).Then(clientv3.OpPut(key, string(encoded))).Commit()
	if err != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, err
	}
	if !txn.Succeeded || lock.Context().Err() != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, store.ErrOperationOwnershipLost
	}
	return binding, nil
}

// ResolveClaimedWorkloadReleasePlan reloads the signed plan and its immutable
// staged-target binding under the live claim and app lock before deriving the
// strict execution intent used by existing release controllers.
func (s *V3OperationStore) ResolveClaimedWorkloadReleasePlan(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, expectedEffectID string) (pipeline.WorkloadReleaseIntent, pipeline.WorkloadReleaseStagedTarget, store.AcceptedOperation, error) {
	verified, err := s.verifyClaimedWorkloadReleasePlan(ctx, claim, lock)
	if err != nil {
		return pipeline.WorkloadReleaseIntent{}, pipeline.WorkloadReleaseStagedTarget{}, store.AcceptedOperation{}, err
	}
	response, err := s.kv.Get(ctx, s.workloadReleaseStagedTargetKey(claim.OperationID()))
	if err != nil || len(response.Kvs) != 1 {
		return pipeline.WorkloadReleaseIntent{}, pipeline.WorkloadReleaseStagedTarget{}, store.AcceptedOperation{}, fmt.Errorf("workload release staged target binding is unavailable")
	}
	record, err := decodeWorkloadReleaseStagedTarget(response.Kvs[0].Value, claim.OperationID(), verified.Plan)
	if err != nil {
		return pipeline.WorkloadReleaseIntent{}, pipeline.WorkloadReleaseStagedTarget{}, store.AcceptedOperation{}, err
	}
	intent, err := verified.Plan.Resolve(record.Binding, expectedEffectID, claim.Generation())
	if err != nil {
		return pipeline.WorkloadReleaseIntent{}, pipeline.WorkloadReleaseStagedTarget{}, store.AcceptedOperation{}, err
	}
	if lock.Context().Err() != nil {
		return pipeline.WorkloadReleaseIntent{}, pipeline.WorkloadReleaseStagedTarget{}, store.AcceptedOperation{}, store.ErrOperationOwnershipLost
	}
	return intent, record.Binding, verified.Accepted, nil
}

func decodeWorkloadReleaseStagedTarget(raw []byte, operationID string, plan pipeline.WorkloadReleasePlan) (v3WorkloadReleaseStagedTargetRecord, error) {
	var record v3WorkloadReleaseStagedTargetRecord
	if err := decodeV3Record(raw, &record); err != nil {
		return record, fmt.Errorf("decode workload release staged target binding: %w", err)
	}
	bindingBytes, err := json.Marshal(record.Binding)
	if err != nil || record.SchemaVersion != workloadReleaseStagedTargetRecordSchema || record.OperationID != operationID ||
		record.PlanDigest == "" || record.PlanDigest != record.Binding.PlanDigest || record.BindingDigest != checkpointDigest(bindingBytes) {
		return record, fmt.Errorf("workload release staged target record failed identity or integrity verification")
	}
	if _, err := plan.Resolve(record.Binding, record.Binding.EffectID, record.Binding.ClaimGeneration); err != nil {
		return record, fmt.Errorf("workload release staged target does not resolve against signed plan: %w", err)
	}
	return record, nil
}
