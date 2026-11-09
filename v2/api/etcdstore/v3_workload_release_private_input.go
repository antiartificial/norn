package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
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
func (s *V3OperationStore) AcceptPrivateWorkloadRelease(ctx context.Context, input store.OperationAcceptance, qualification model.ReleaseQualification, trustedQualificationKeys []string, material []byte, keys *store.WorkloadReleaseInputKeyRing, lineageObserver nomad.ActiveDeploymentLineageObserver) (store.AcceptedOperation, error) {
	if s == nil || s.kv == nil || s.signer == nil || keys == nil || input.Identity.Kind != "app.workload-release" ||
		input.Operation.Kind != "app.workload-release" || input.Operation.ID == "" || input.Operation.App == "" ||
		input.Operation.Status != model.OperationQueued || input.Identity.Resource != "app/"+input.Operation.App ||
		!input.Admission.OneActiveMutablePerApp || input.Deployment != nil || len(input.Regions) != 0 ||
		workloadReleaseRequestDigest(input.Operation) == "" {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "private workload release acceptance is incomplete"}
	}
	// The server-computed request digest covers the caller's stable selection
	// (qualification ID and requested replica count), not mutable source files,
	// current scheduler observations, or the SOPS snapshot captured for the
	// first acceptance. Resolve it before validating or opening any fresh facts.
	if prior, err := s.ResolveIdentity(ctx, input.Identity); err == nil {
		if prior.Operation.Kind != "app.workload-release" || prior.Operation.App != input.Operation.App ||
			workloadReleaseRequestDigest(input.Operation) == "" ||
			workloadReleaseRequestDigest(input.Operation) != workloadReleaseRequestDigest(prior.Operation) {
			return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
		}
		return prior, nil
	} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
		return store.AcceptedOperation{}, err
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
	// Exact retries match the qualification in the signed acceptance even if
	// its trust, validity window, or source deployment can no longer be verified.
	payload["workloadReleaseQualification"] = qualification
	input.Operation.Payload = payload
	if err := plan.ValidateWorkloadReleaseQualification(qualification, trustedQualificationKeys, time.Now().UTC()); err != nil {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "workload release qualification does not authorize the plan"}
	}
	if err := s.verifyWorkloadReleaseQualificationSource(ctx, plan, qualification); err != nil {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "workload release qualification has no matching successful staging deployment"}
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
	activePredecessor, predecessorCompares, err := s.workloadReleasePredecessorCompares(ctx, acceptedPlan)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if err := verifyWorkloadReleaseServingLineage(ctx, acceptedPlan, activePredecessor, lineageObserver); err != nil {
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
	if err := verifyWorkloadReleaseServingLineage(ctx, acceptedPlan, activePredecessor, lineageObserver); err != nil {
		return store.AcceptedOperation{}, err
	}
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
	return resolvePrivateWorkloadReleaseReplay(input, prior)
}

// verifyWorkloadReleaseQualificationSource binds the signed qualification to
// a completed V3 staging deployment. Signature validity alone does not prove
// that this control plane actually accepted and completed the cited release.
func (s *V3OperationStore) verifyWorkloadReleaseQualificationSource(ctx context.Context, plan pipeline.WorkloadReleasePlan, qualification model.ReleaseQualification) error {
	index, err := s.kv.Get(ctx, s.deploymentAcceptanceIndexKey(qualification.DeploymentID))
	if err != nil {
		return fmt.Errorf("load qualified deployment acceptance: %w", err)
	}
	if len(index.Kvs) != 1 {
		return fmt.Errorf("qualified deployment acceptance is missing")
	}
	key := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, key)
	if err != nil {
		return err
	}
	if loaded.record.Accepted.Intent.DeploymentID != qualification.DeploymentID || s.acceptanceKey(loaded.record.Identity) != key {
		return fmt.Errorf("qualified deployment index does not match its signed acceptance")
	}
	accepted, err := s.replay(ctx, key, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil {
		return err
	}
	if accepted.Operation.Kind != "app.deploy" || accepted.Operation.Status != model.OperationSucceeded || accepted.Operation.App != plan.Intent.App || accepted.Operation.Source != "release-control-api" {
		return fmt.Errorf("qualified deployment operation is not a successful release for the planned app")
	}
	deployment, err := s.GetDeployment(ctx, qualification.DeploymentID)
	if err != nil {
		return fmt.Errorf("load qualified deployment projection: %w", err)
	}
	if deployment == nil || deployment.ID != qualification.DeploymentID || deployment.App != qualification.App || deployment.Environment != "staging" || deployment.SourceKind != "release" {
		return fmt.Errorf("qualified deployment identity or environment differs from the signed qualification")
	}
	if deployment.CommitSHA != plan.Intent.SourceRevision || qualification.SourceSHA != plan.Intent.SourceRevision {
		return fmt.Errorf("qualified deployment source differs from the signed workload release")
	}
	if deployment.ImageTag != plan.Intent.TargetImage || qualification.Artifact != plan.Intent.TargetImage {
		return fmt.Errorf("qualified deployment image differs from the signed workload release")
	}
	if deployment.SpecDigest != plan.Intent.TargetSpecDigest || accepted.Operation.Payload["specDigest"] != plan.Intent.TargetSpecDigest {
		return fmt.Errorf("qualified deployment spec differs from the signed workload release")
	}
	if accepted.Operation.Payload["sourceSha"] != plan.Intent.SourceRevision || accepted.Operation.Payload["artifact"] != plan.Intent.TargetImage {
		return fmt.Errorf("qualified deployment operation provenance differs from the signed workload release")
	}
	if len(deployment.Regions) == 0 {
		return fmt.Errorf("qualified staging deployment has no completed region results")
	}
	for _, region := range deployment.Regions {
		if region.Status != model.StatusDeployed || region.ActiveWeight != region.DesiredWeight {
			return fmt.Errorf("qualified staging deployment has an incomplete region")
		}
	}
	return nil
}

// workloadReleasePredecessorCompares makes accepted plans name the currently
// control-owned serving deployment and its immediate successor route
// generation. Both revisions are checked by the acceptance transaction, so a
// route handoff racing admission cannot leave a stale release queued.
func (s *V3OperationStore) workloadReleasePredecessorCompares(ctx context.Context, plan pipeline.WorkloadReleasePlan) (activeFleetRoute, []clientv3.Cmp, error) {
	intent := plan.Intent
	activeKey := s.fleetActiveRouteKey(intent.App, intent.ControlEnvironment, intent.Region)
	activeResponse, err := s.kv.Get(ctx, activeKey)
	if err != nil || len(activeResponse.Kvs) != 1 {
		return activeFleetRoute{}, nil, fmt.Errorf("workload release requires a current active route predecessor")
	}
	var active activeFleetRoute
	if decodeV3Record(activeResponse.Kvs[0].Value, &active) != nil || active.IntentID == "" || active.DeploymentID == "" ||
		active.Generation == 0 || active.Generation == ^uint64(0) || active.RouteSHA256 == "" || active.JobDigest == "" ||
		active.DeploymentID != intent.OldDeploymentID || active.JobDigest != intent.OldJobDigest || intent.FirstRouteGeneration != active.Generation+1 {
		return activeFleetRoute{}, nil, fmt.Errorf("workload release plan differs from the current active route predecessor")
	}
	reservationKey := s.fleetRouteReservationKey(intent.App, intent.ControlEnvironment, intent.Region)
	reservationResponse, err := s.kv.Get(ctx, reservationKey)
	if err != nil || len(reservationResponse.Kvs) != 1 {
		return activeFleetRoute{}, nil, fmt.Errorf("workload release active route reservation is unavailable")
	}
	var reservation initialFleetRouteReservation
	if decodeV3Record(reservationResponse.Kvs[0].Value, &reservation) != nil || reservation.IntentID != active.IntentID || reservation.Generation != active.Generation {
		return activeFleetRoute{}, nil, fmt.Errorf("workload release active route differs from its current reservation")
	}
	return active, []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(activeKey), "=", activeResponse.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(reservationKey), "=", reservationResponse.Kvs[0].ModRevision),
	}, nil
}

func verifyWorkloadReleaseServingLineage(ctx context.Context, plan pipeline.WorkloadReleasePlan, active activeFleetRoute,
	observer nomad.ActiveDeploymentLineageObserver) error {
	intent := plan.Intent
	if observer == nil || active.OperationID == "" || active.Process != intent.Process || active.SpecDigest != intent.OldSpecDigest ||
		active.ImageTag == "" || !model.IsContentAddressedImage(active.ImageTag) ||
		active.JobDigest != intent.OldJobDigest || active.DeploymentID != intent.OldDeploymentID {
		return fmt.Errorf("workload release requires a complete trusted serving-lineage observer")
	}
	separator := strings.LastIndex(active.ImageTag, "@sha256:")
	if separator <= 0 || active.ImageTag[separator+1:] != intent.OldImageDigest {
		return fmt.Errorf("workload release old image digest differs from active route lineage")
	}
	lineage, err := observer.ObserveActiveDeploymentLineage(ctx, intent.App, intent.Region, intent.NomadRegion, intent.Namespace,
		active.DeploymentID, active.OperationID, active.SpecDigest, strings.TrimPrefix(active.JobDigest, "sha256:"), active.ImageTag, intent.Process)
	if err != nil {
		return fmt.Errorf("observe current workload serving lineage: %w", err)
	}
	wantAllocations := append([]string(nil), intent.OldAllocationIDs...)
	gotAllocations := append([]string(nil), lineage.AllocationIDs...)
	sort.Strings(wantAllocations)
	sort.Strings(gotAllocations)
	if lineage.App != intent.App || lineage.DeploymentID != active.DeploymentID || lineage.OperationID != active.OperationID ||
		lineage.Region != intent.Region || lineage.NomadRegion != intent.NomadRegion || lineage.Namespace != intent.Namespace ||
		lineage.SpecDigest != active.SpecDigest || lineage.ImageTag != active.ImageTag || lineage.JobDigest != strings.TrimPrefix(active.JobDigest, "sha256:") ||
		lineage.JobID != intent.OldJobID || lineage.Process != intent.Process || lineage.JobVersion != intent.OldJobVersion ||
		lineage.ModifyIndex != intent.OldJobModifyIndex || lineage.Replicas != len(intent.OldAllocationIDs) || !slices.Equal(gotAllocations, wantAllocations) {
		return fmt.Errorf("workload release plan differs from freshly observed serving lineage")
	}
	return nil
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

func resolvePrivateWorkloadReleaseReplay(input store.OperationAcceptance, prior store.AcceptedOperation) (store.AcceptedOperation, error) {
	if prior.Operation.Kind != "app.workload-release" || prior.Operation.App != input.Operation.App ||
		workloadReleaseRequestDigest(input.Operation) == "" ||
		workloadReleaseRequestDigest(input.Operation) != workloadReleaseRequestDigest(prior.Operation) {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
	}
	return prior, nil
}

func workloadReleaseRequestDigest(operation model.Operation) string {
	if operation.Metadata == nil {
		return ""
	}
	digest, _ := operation.Metadata["workloadReleaseRequestDigest"].(string)
	if len(digest) != len("sha256:")+64 || !strings.HasPrefix(digest, "sha256:") {
		return ""
	}
	for _, character := range digest[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return ""
		}
	}
	return digest
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
