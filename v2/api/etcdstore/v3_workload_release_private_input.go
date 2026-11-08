package etcdstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

const workloadReleasePrivateInputRecordSchema = "norn.workload-release-private-input-record/v1"

type v3WorkloadReleasePrivateInputRecord struct {
	SchemaVersion string                             `json:"schemaVersion"`
	KeyID         string                             `json:"keyId"`
	Digest        string                             `json:"digest"`
	Envelope      store.WorkloadReleaseInputEnvelope `json:"envelope"`
}

var _ store.PrivateWorkloadReleaseStore = (*V3OperationStore)(nil)

func (s *V3OperationStore) workloadReleasePrivateInputKey(operationID string) string {
	return s.prefix + "/v3/workload-release-private-inputs/" + operationID
}

func (s *V3OperationStore) workloadReleasePrivateInputPrefix() string {
	return s.prefix + "/v3/workload-release-private-inputs/"
}

// AcceptPrivateWorkloadRelease atomically stores the signed plan, active-app
// fence, control-owned Fleet target revision, and encrypted SOPS snapshot.
func (s *V3OperationStore) AcceptPrivateWorkloadRelease(ctx context.Context, input store.OperationAcceptance, qualification model.ReleaseQualification, trustedQualificationKeys []string, material []byte, keys *store.WorkloadReleaseInputKeyRing) (store.AcceptedOperation, error) {
	if s == nil || s.kv == nil || s.signer == nil || keys == nil || input.Identity.Kind != "app.workload-release" ||
		input.Operation.Kind != "app.workload-release" || input.Operation.ID == "" || input.Operation.App == "" ||
		input.Operation.Status != model.OperationQueued || input.Identity.Resource != "app/"+input.Operation.App ||
		!input.Admission.OneActiveMutablePerApp || input.Deployment != nil || len(input.Regions) != 0 {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "private workload release acceptance is incomplete"}
	}
	if input.Operation.Payload == nil {
		input.Operation.Payload = map[string]interface{}{}
	}
	payload := make(map[string]interface{}, len(input.Operation.Payload)+4)
	for key, value := range input.Operation.Payload {
		payload[key] = value
	}
	for _, field := range []string{"workloadReleasePrivateRecordId", "workloadReleasePrivateInputDigest", "workloadReleasePrivateKeyId", "workloadReleaseQualification"} {
		if _, exists := payload[field]; exists {
			return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "workload release payload contains a reserved private-input field"}
		}
	}
	plan, err := acceptedWorkloadReleasePlan(payload, input.Operation.App)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if err := plan.ValidateWorkloadReleaseQualification(qualification, trustedQualificationKeys, time.Now().UTC()); err != nil {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "workload release qualification does not authorize the plan"}
	}
	payload["workloadReleaseQualification"] = qualification
	input.Operation.Payload = payload
	if prior, err := s.ResolveIdentity(ctx, input.Identity); err == nil {
		return s.resolvePrivateWorkloadReleaseReplay(ctx, input, material, keys, prior)
	} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
		return store.AcceptedOperation{}, err
	}
	planDigest, err := plan.Digest()
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	binding := store.WorkloadReleaseInputBinding{Authority: input.Identity.Authority, Operation: input.Operation.ID, App: input.Operation.App, PlanDigest: planDigest}
	envelope, err := keys.Seal(binding, material)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	digest, err := store.WorkloadReleaseInputDigest(envelope)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	payload["workloadReleasePrivateRecordId"] = input.Operation.ID
	payload["workloadReleasePrivateInputDigest"] = digest
	payload["workloadReleasePrivateKeyId"] = envelope.KeyID
	input.Operation.Payload = payload
	input.Fingerprint, err = store.CanonicalOperationRequestFingerprint(input)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	acceptance, err := s.normalize(input)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	acceptedPlan, err := acceptedWorkloadReleasePlan(acceptance.Operation.Payload, acceptance.Operation.App)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, acceptedPlan.Intent.App, acceptedPlan.Intent.ControlEnvironment)
	if err != nil {
		return store.AcceptedOperation{}, fmt.Errorf("load current workload release Fleet target: %w", err)
	}
	if !workloadReleasePlanMatchesFleetTarget(acceptedPlan, target) {
		return store.AcceptedOperation{}, fmt.Errorf("workload release plan differs from current control-owned Fleet target")
	}
	predecessorCompares, err := s.workloadReleasePredecessorCompares(ctx, acceptedPlan)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	appAdmission, err := s.prepareAppAdmission(ctx, acceptance)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	key := s.acceptanceKey(acceptance.Identity)
	acceptedAt := time.Now().UTC().Truncate(time.Microsecond)
	identityID, intentID := uuid.NewString(), uuid.NewString()
	intent, err := store.SealOperationAcceptance(ctx, s.signer, acceptance, identityID, intentID, acceptedAt)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	accepted := store.AcceptedOperation{Operation: acceptance.Operation, RequestIdentityID: identityID,
		AcceptanceIntentID: intentID, Intent: intent, FleetAppTarget: &target}
	acceptanceBytes, err := json.Marshal(v3Acceptance{Identity: acceptance.Identity, Accepted: accepted})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	operationBytes, err := json.Marshal(v3Record{Operation: acceptance.Operation})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	privateBytes, err := json.Marshal(v3WorkloadReleasePrivateInputRecord{SchemaVersion: workloadReleasePrivateInputRecordSchema,
		KeyID: envelope.KeyID, Digest: digest, Envelope: envelope})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	privateKey := s.workloadReleasePrivateInputKey(acceptance.Operation.ID)
	acceptanceIndex := s.operationAcceptanceIndexKey(acceptance.Operation.ID)
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.opKey(acceptance.Operation.ID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(privateKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(acceptanceIndex), "=", 0),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(target.App, target.ControlEnvironment)), "=", targetRevision),
	}
	compares = append(compares, appAdmission.compares...)
	compares = append(compares, predecessorCompares...)
	puts := []clientv3.Op{
		clientv3.OpPut(key, string(acceptanceBytes)), clientv3.OpPut(acceptanceIndex, key),
		clientv3.OpPut(s.opKey(acceptance.Operation.ID), string(operationBytes)),
		clientv3.OpPut(s.operationKindIndexKey(acceptance.Operation.Kind, acceptedAt, acceptance.Operation.ID), acceptance.Operation.ID),
		clientv3.OpPut(privateKey, string(privateBytes)),
	}
	puts = append(puts, appAdmission.puts...)
	txn, err := s.kv.Txn(ctx).If(compares...).Then(puts...).Commit()
	if err != nil {
		return store.AcceptedOperation{}, &store.AcceptanceIndeterminateError{Err: err}
	}
	if txn.Succeeded {
		return accepted, nil
	}
	prior, resolveErr := s.ResolveIdentity(ctx, acceptance.Identity)
	if errors.Is(resolveErr, store.ErrAcceptanceNotFound) {
		if _, admissionErr := s.prepareAppAdmission(ctx, acceptance); admissionErr != nil {
			return store.AcceptedOperation{}, admissionErr
		}
	}
	if resolveErr != nil {
		return store.AcceptedOperation{}, &store.AcceptanceIndeterminateError{Err: errors.Join(errors.New("private workload release transaction did not commit"), resolveErr)}
	}
	delete(payload, "workloadReleasePrivateRecordId")
	delete(payload, "workloadReleasePrivateInputDigest")
	delete(payload, "workloadReleasePrivateKeyId")
	input.Operation.Payload = payload
	return s.resolvePrivateWorkloadReleaseReplay(ctx, input, material, keys, prior)
}

// workloadReleasePredecessorCompares makes accepted plans name the currently
// control-owned serving deployment and its immediate successor route
// generation. Both revisions are checked by the acceptance transaction, so a
// route handoff racing admission cannot leave a stale release queued.
func (s *V3OperationStore) workloadReleasePredecessorCompares(ctx context.Context, plan pipeline.WorkloadReleasePlan) ([]clientv3.Cmp, error) {
	intent := plan.Intent
	activeKey := s.fleetActiveRouteKey(intent.App, intent.ControlEnvironment, intent.Region)
	activeResponse, err := s.kv.Get(ctx, activeKey)
	if err != nil || len(activeResponse.Kvs) != 1 {
		return nil, fmt.Errorf("workload release requires a current active route predecessor")
	}
	var active activeFleetRoute
	if decodeV3Record(activeResponse.Kvs[0].Value, &active) != nil || active.IntentID == "" || active.DeploymentID == "" ||
		active.Generation == 0 || active.Generation == ^uint64(0) || active.RouteSHA256 == "" ||
		active.DeploymentID != intent.OldDeploymentID || intent.FirstRouteGeneration != active.Generation+1 {
		return nil, fmt.Errorf("workload release plan differs from the current active route predecessor")
	}
	reservationKey := s.fleetRouteReservationKey(intent.App, intent.ControlEnvironment, intent.Region)
	reservationResponse, err := s.kv.Get(ctx, reservationKey)
	if err != nil || len(reservationResponse.Kvs) != 1 {
		return nil, fmt.Errorf("workload release active route reservation is unavailable")
	}
	var reservation initialFleetRouteReservation
	if decodeV3Record(reservationResponse.Kvs[0].Value, &reservation) != nil || reservation.IntentID != active.IntentID || reservation.Generation != active.Generation {
		return nil, fmt.Errorf("workload release active route differs from its current reservation")
	}
	return []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(activeKey), "=", activeResponse.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(reservationKey), "=", reservationResponse.Kvs[0].ModRevision),
	}, nil
}

func acceptedWorkloadReleasePlan(payload map[string]interface{}, app string) (pipeline.WorkloadReleasePlan, error) {
	raw := effectPayloadString(payload, "workloadReleasePlan")
	var plan pipeline.WorkloadReleasePlan
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var trailing json.RawMessage
	if raw == "" || decoder.Decode(&plan) != nil || decoder.Decode(&trailing) != io.EOF || plan.Validate() != nil || plan.Intent.App != app {
		return pipeline.WorkloadReleasePlan{}, &store.AcceptanceValidationError{Reason: "signed workload release plan is invalid"}
	}
	canonical, err := json.Marshal(plan)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return pipeline.WorkloadReleasePlan{}, &store.AcceptanceValidationError{Reason: "workload release plan must use canonical JSON"}
	}
	return plan, nil
}

func workloadReleasePlanMatchesFleetTarget(plan pipeline.WorkloadReleasePlan, target FleetAppTarget) bool {
	intent := plan.Intent
	return intent.App == target.App && intent.ControlEnvironment == target.ControlEnvironment && intent.FleetCluster == target.Cluster &&
		intent.FleetEnvironment == target.FleetEnvironment && intent.Region == target.Region && intent.NomadRegion == target.NomadRegion &&
		intent.Namespace == target.NomadNamespace && intent.FleetTargetGeneration == target.Generation && slices.Equal(intent.FleetDatacenters, target.Datacenters)
}

func (s *V3OperationStore) resolvePrivateWorkloadReleaseReplay(ctx context.Context, input store.OperationAcceptance, material []byte, keys *store.WorkloadReleaseInputKeyRing, prior store.AcceptedOperation) (store.AcceptedOperation, error) {
	if prior.Operation.Kind != "app.workload-release" || prior.Operation.App != input.Operation.App {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
	}
	stored, err := s.OpenPrivateWorkloadRelease(ctx, prior.Operation, keys)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	defer clear(stored)
	wantHash, gotHash := sha256.Sum256(material), sha256.Sum256(stored)
	if subtle.ConstantTimeCompare(wantHash[:], gotHash[:]) != 1 {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
	}
	payload := make(map[string]interface{}, len(input.Operation.Payload)+3)
	for key, value := range input.Operation.Payload {
		payload[key] = value
	}
	for _, field := range []string{"workloadReleasePrivateRecordId", "workloadReleasePrivateInputDigest", "workloadReleasePrivateKeyId"} {
		if _, exists := payload[field]; exists {
			return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
		}
		value, exists := prior.Operation.Payload[field]
		if !exists {
			return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
		}
		payload[field] = value
	}
	input.Operation.Payload = payload
	input.Fingerprint, err = store.CanonicalOperationRequestFingerprint(input)
	if err != nil || input.Fingerprint != prior.Intent.Fingerprint {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
	}
	return prior, nil
}

func (s *V3OperationStore) OpenPrivateWorkloadRelease(ctx context.Context, operation model.Operation, keys *store.WorkloadReleaseInputKeyRing) ([]byte, error) {
	if s == nil || s.kv == nil || keys == nil || operation.Kind != "app.workload-release" || operation.ID == "" || operation.App == "" {
		return nil, errors.New("private workload release input is unavailable")
	}
	response, err := s.kv.Get(ctx, s.workloadReleasePrivateInputKey(operation.ID))
	if err != nil || len(response.Kvs) != 1 {
		return nil, errors.New("private workload release input record is unavailable")
	}
	var record v3WorkloadReleasePrivateInputRecord
	if decodeV3Record(response.Kvs[0].Value, &record) != nil || record.SchemaVersion != workloadReleasePrivateInputRecordSchema ||
		record.KeyID == "" || record.KeyID != record.Envelope.KeyID ||
		operation.Payload["workloadReleasePrivateRecordId"] != operation.ID || operation.Payload["workloadReleasePrivateInputDigest"] != record.Digest ||
		operation.Payload["workloadReleasePrivateKeyId"] != record.KeyID {
		return nil, errors.New("private workload release input record differs from signed operation")
	}
	digest, err := store.WorkloadReleaseInputDigest(record.Envelope)
	if err != nil || digest != record.Digest {
		return nil, errors.New("private workload release input envelope digest is invalid")
	}
	plan, err := acceptedWorkloadReleasePlan(operation.Payload, operation.App)
	if err != nil {
		return nil, err
	}
	planDigest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	binding := store.WorkloadReleaseInputBinding{Authority: s.authority, Operation: operation.ID, App: operation.App, PlanDigest: planDigest}
	return keys.Open(binding, record.Envelope)
}

func (s *V3OperationStore) RequiredWorkloadReleaseInputKeys(ctx context.Context) ([]string, error) {
	if s == nil || s.kv == nil {
		return nil, errors.New("private workload release input store is unavailable")
	}
	response, err := s.kv.Get(ctx, s.workloadReleasePrivateInputPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	ids := make(map[string]struct{}, len(response.Kvs))
	for _, item := range response.Kvs {
		var record v3WorkloadReleasePrivateInputRecord
		if decodeV3Record(item.Value, &record) != nil || record.SchemaVersion != workloadReleasePrivateInputRecordSchema || record.KeyID == "" || record.KeyID != record.Envelope.KeyID {
			return nil, fmt.Errorf("decode private workload release input record %q", string(item.Key))
		}
		digest, err := store.WorkloadReleaseInputDigest(record.Envelope)
		if err != nil || digest != record.Digest {
			return nil, fmt.Errorf("private workload release input record %q failed digest verification", string(item.Key))
		}
		ids[record.KeyID] = struct{}{}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}
