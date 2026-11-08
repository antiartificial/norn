package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
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

func (s *V3OperationStore) workloadReleaseStageTargetEffect(ctx context.Context, operationID, effectID string) (effect.Record, string, int64, error) {
	index, err := s.kv.Get(ctx, s.prefix+"/v3/effects/tokens/"+effectID)
	if err != nil || len(index.Kvs) != 1 {
		return effect.Record{}, "", 0, fmt.Errorf("workload release stage effect is unavailable")
	}
	key := string(index.Kvs[0].Value)
	if !strings.HasPrefix(key, s.prefix+"/v3/effects/workload-release/") {
		return effect.Record{}, "", 0, fmt.Errorf("workload release stage effect belongs to another namespace")
	}
	response, err := s.kv.Get(ctx, key)
	var record effect.Record
	if err != nil || len(response.Kvs) != 1 || json.Unmarshal(response.Kvs[0].Value, &record) != nil ||
		record.Token.EffectID != effectID || record.Reservation.OperationClaim.OperationID != operationID ||
		record.Reservation.Stage != workloadReleaseStageTargetEffectStage || key != (&V3CanaryEffectReservations{operations: s, namespace: "workload-release"}).effectKey(record.Reservation) {
		return effect.Record{}, "", 0, fmt.Errorf("workload release stage effect identity is invalid")
	}
	return record, key, response.Kvs[0].ModRevision, nil
}

// MarkWorkloadReleaseStageTargetAttempt durably grants at most one Nomad
// create attempt for the reserved stage effect under the live operation claim.
func (s *V3OperationStore) MarkWorkloadReleaseStageTargetAttempt(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, effectID, runtimeInstanceID string) (bool, error) {
	verified, err := s.verifyClaimedWorkloadReleasePlan(ctx, claim, lock)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(runtimeInstanceID) == "" {
		return false, fmt.Errorf("workload release stage attempt requires a controller instance identity")
	}
	record, recordKey, recordRevision, err := s.workloadReleaseStageTargetEffect(ctx, claim.OperationID(), effectID)
	if err != nil {
		return false, err
	}
	digest, err := verified.Plan.Digest()
	if err != nil || record.Reservation.OperationClaim.Generation != claim.Generation() || record.Reservation.SupervisorExecutionID != workloadReleaseStageTargetExecutionID(claim.OperationID(), digest) {
		return false, fmt.Errorf("workload release stage effect differs from current claim or signed plan")
	}
	attemptKey := workloadReleaseStageTargetAttemptKey(s.prefix, record.Token)
	prior, err := s.kv.Get(ctx, attemptKey)
	if err != nil {
		return false, err
	}
	if len(prior.Kvs) == 1 {
		var attempt workloadReleaseStageTargetAttempt
		if json.Unmarshal(prior.Kvs[0].Value, &attempt) != nil || attempt.Token != record.Token || attempt.InputDigest != record.Reservation.InputDigest {
			return false, fmt.Errorf("workload release stage attempt marker is invalid")
		}
		return false, nil
	}
	if record.Lifecycle != effect.LifecycleReserved {
		return false, effect.ErrStaleToken
	}
	ownerKey := s.ownerKey(claim.OperationID())
	owner, err := s.kv.Get(ctx, ownerKey)
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
		return false, store.ErrOperationOwnershipLost
	}
	gateKey := (&V3CanaryEffectReservations{operations: s, namespace: "workload-release"}).gateKey(verified.App)
	marker := workloadReleaseStageTargetAttempt{Token: record.Token, InputDigest: record.Reservation.InputDigest, RuntimeInstanceID: runtimeInstanceID}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return false, err
	}
	txn, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", verified.OperationRevision),
		clientv3.Compare(clientv3.ModRevision(ownerKey), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(ownerKey), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.ModRevision(recordKey), "=", recordRevision),
		clientv3.Compare(clientv3.Value(gateKey), "=", recordKey),
		clientv3.Compare(clientv3.CreateRevision(attemptKey), "=", 0),
	).Then(clientv3.OpPut(attemptKey, string(encoded))).Commit()
	if err != nil {
		return false, err
	}
	if !txn.Succeeded || lock.Context().Err() != nil {
		return false, store.ErrOperationOwnershipLost
	}
	return true, nil
}

func (s *V3OperationStore) verifyWorkloadReleaseStageTargetAttempt(ctx context.Context, claim store.OperationClaim, effectID string, plan pipeline.WorkloadReleasePlan) (workloadReleaseStageTargetAttempt, error) {
	record, _, _, err := s.workloadReleaseStageTargetEffect(ctx, claim.OperationID(), effectID)
	if err != nil {
		return workloadReleaseStageTargetAttempt{}, err
	}
	digest, err := plan.Digest()
	if err != nil || record.Reservation.SupervisorExecutionID != workloadReleaseStageTargetExecutionID(claim.OperationID(), digest) ||
		record.Reservation.OperationClaim.Generation > claim.Generation() || record.Lifecycle != effect.LifecycleReserved && record.Lifecycle != effect.LifecycleLaunched && record.Lifecycle != effect.LifecycleCompleted {
		return workloadReleaseStageTargetAttempt{}, fmt.Errorf("workload release stage effect does not match the accepted target")
	}
	attemptKey := workloadReleaseStageTargetAttemptKey(s.prefix, record.Token)
	response, err := s.kv.Get(ctx, attemptKey)
	var attempt workloadReleaseStageTargetAttempt
	if err != nil || len(response.Kvs) != 1 || json.Unmarshal(response.Kvs[0].Value, &attempt) != nil || attempt.Token != record.Token || attempt.InputDigest != record.Reservation.InputDigest {
		return workloadReleaseStageTargetAttempt{}, fmt.Errorf("workload release stage target has no valid one-use attempt marker")
	}
	if strings.TrimSpace(attempt.RuntimeInstanceID) == "" {
		return workloadReleaseStageTargetAttempt{}, fmt.Errorf("workload release stage attempt has no controller instance identity")
	}
	return attempt, nil
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

// BindClaimedWorkloadReleaseTarget derives an exact create-only Nomad lookup
// from the signed plan, observes it, then persists the resulting binding once
// while the operation claim, app lock, and Fleet target generation are current.
// A matching retry is idempotent; a conflicting observation cannot overwrite
// the first target.
func (s *V3OperationStore) BindClaimedWorkloadReleaseTarget(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, effectID string,
	observer interface {
		LookupDeploymentJobRevision(context.Context, nomad.CASDeploymentJobRequest) (nomad.DeploymentJobObservation, error)
	}) (pipeline.WorkloadReleaseStagedTarget, error) {
	verified, err := s.verifyClaimedWorkloadReleasePlan(ctx, claim, lock)
	if err != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, err
	}
	if observer == nil || strings.TrimSpace(effectID) == "" {
		return pipeline.WorkloadReleaseStagedTarget{}, fmt.Errorf("workload release target observer and stage effect are required")
	}
	attempt, err := s.verifyWorkloadReleaseStageTargetAttempt(ctx, claim, effectID, verified.Plan)
	if err != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, err
	}
	request := nomad.CASDeploymentJobRequest{
		App: verified.Plan.Intent.TargetJobID, JobID: verified.Plan.Intent.TargetJobID,
		Region: verified.Plan.Intent.NomadRegion, Namespace: verified.Plan.Intent.Namespace,
		DeploymentID: verified.Plan.Intent.TargetDeploymentID, SpecDigest: verified.Plan.Intent.TargetSpecDigest,
		OperationID: verified.Plan.Intent.TargetOperationID, ExecutionID: verified.Plan.Intent.TargetExecutionID,
		JobDigest: strings.TrimPrefix(verified.Plan.Intent.TargetJobDigest, "sha256:"), ImageTag: verified.Plan.Intent.TargetImage,
	}
	observation, err := observer.LookupDeploymentJobRevision(ctx, request)
	if err != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, fmt.Errorf("workload release exact Nomad target readback is unavailable: %w", err)
	}
	if observation.State != nomad.DeploymentJobFound {
		return pipeline.WorkloadReleaseStagedTarget{}, fmt.Errorf("workload release exact Nomad target readback is not found")
	}
	binding, err := verified.Plan.BindObservedTarget(effectID, claim.Generation(), request, observation)
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
	stageEffect, stageEffectKey, stageEffectRevision, err := s.workloadReleaseStageTargetEffect(ctx, claim.OperationID(), effectID)
	if err != nil || stageEffect.Lifecycle != effect.LifecycleReserved && stageEffect.Lifecycle != effect.LifecycleLaunched {
		return pipeline.WorkloadReleaseStagedTarget{}, fmt.Errorf("workload release stage effect is not open for target binding")
	}
	attemptKey := workloadReleaseStageTargetAttemptKey(s.prefix, stageEffect.Token)
	attemptRecord, err := s.kv.Get(ctx, attemptKey)
	var storedAttempt workloadReleaseStageTargetAttempt
	if err != nil || len(attemptRecord.Kvs) != 1 || json.Unmarshal(attemptRecord.Kvs[0].Value, &storedAttempt) != nil || storedAttempt != attempt {
		return pipeline.WorkloadReleaseStagedTarget{}, fmt.Errorf("workload release stage attempt changed before target binding")
	}
	stageEffect.Lifecycle = effect.LifecycleCompleted
	stageEffect.Execution = effect.ExecutionIdentity{Supervisor: stageEffect.Reservation.Supervisor,
		SupervisorExecutionID: stageEffect.Reservation.SupervisorExecutionID, RuntimeInstanceID: attempt.RuntimeInstanceID}
	stageEffect.Completion = &effect.Completion{Outcome: effect.OutcomeSucceeded, Verification: effect.Verification{
		Decision: effect.VerificationSucceeded, InputDigest: stageEffect.Reservation.InputDigest, ResultDigest: binding.ObservationDigest,
		ResultReference: key, SupervisorExecutionID: stageEffect.Reservation.SupervisorExecutionID, RuntimeInstanceID: attempt.RuntimeInstanceID,
		EvidenceSource: "nomad-exact-job-readback", EvidenceReference: fmt.Sprintf("%s/%s/%d/%d", request.Region, request.EffectiveJobID(), observation.Version, observation.JobModifyIndex),
		ObservedAt: time.Now().UTC().Truncate(time.Microsecond),
	}}
	stageEffectBytes, err := json.Marshal(stageEffect)
	if err != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, err
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
		clientv3.Compare(clientv3.ModRevision(stageEffectKey), "=", stageEffectRevision),
		clientv3.Compare(clientv3.Value((&V3CanaryEffectReservations{operations: s, namespace: "workload-release"}).gateKey(verified.App)), "=", stageEffectKey),
		clientv3.Compare(clientv3.ModRevision(attemptKey), "=", attemptRecord.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
	).Then(clientv3.OpPut(key, string(encoded)), clientv3.OpPut(stageEffectKey, string(stageEffectBytes)),
		clientv3.OpDelete((&V3CanaryEffectReservations{operations: s, namespace: "workload-release"}).gateKey(verified.App))).Commit()
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
