package etcdstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

func TestV3PrivateWorkloadReleaseAcceptanceIsAtomicReplaySafeAndKeyRecoverable(t *testing.T) {
	adapter, client, _ := functionInvocationEffectAttemptStore(t)
	ctx := context.Background()
	const app = "widgets"
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: app, ControlEnvironment: "staging", Cluster: "norn-staging",
		FleetEnvironment: "staging/nyc3", Region: "nyc3", NomadRegion: "global", NomadNamespace: "default",
		Datacenters: []string{"nyc3"}, Generation: 1}
	if _, err := adapter.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	intent := workloadReleaseEtcdIntentFixture(app, target)
	intent.SourceRevision = strings.Repeat("a", 40)
	intent.TargetJobVersion, intent.TargetJobModifyIndex = 0, 0
	planBytes, err := json.Marshal(pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: app, Status: model.OperationQueued,
		MaxAttempts: 3, Source: "operator-api", Payload: map[string]interface{}{"workloadReleasePlan": string(planBytes)},
		Metadata: map[string]interface{}{}}
	input := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "https://operator.example.test", Subject: "operator-1"},
			Kind: operation.Kind, Resource: "app/" + app, Key: uuid.NewString()},
		Operation: operation, Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
		Audit: store.AcceptanceAuditContext{Source: "workload-release-test"}, Semantics: map[string]interface{}{"planDigest": "bound-by-plan"},
	}
	keys, err := store.NewWorkloadReleaseInputKeyRing("release-input-v1", map[string][]byte{"release-input-v1": bytes.Repeat([]byte{0x5a}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	material := []byte("exact-encrypted-sops-snapshot-v1")
	qualification, trustedQualificationKey := privateInputQualification(t, intent, "missing-qualified-deployment")
	requestDigest, err := pipeline.WorkloadReleaseRequestDigest(qualification.ID, intent.TargetReplicas)
	if err != nil {
		t.Fatal(err)
	}
	operation.Metadata["workloadReleaseRequestDigest"] = requestDigest
	input.Operation = operation
	write := func(key string, value interface{}) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Put(ctx, key, string(encoded)); err != nil {
			t.Fatal(err)
		}
	}
	lineageObserver := newWorkloadReleaseLineageObserverFixture(intent, "old-operation")
	accept := func(request store.OperationAcceptance) error {
		t.Helper()
		_, err := adapter.AcceptPrivateWorkloadRelease(ctx, request, qualification, []string{trustedQualificationKey}, material, keys, lineageObserver)
		return err
	}
	if err := accept(input); err == nil {
		t.Fatal("accepted signed qualification without a control-owned successful staging deployment")
	}
	wrongDigest := input
	wrongDigest.Operation = operation
	wrongDigest.Operation.ID = uuid.NewString()
	wrongDigest.Identity.Key = uuid.NewString()
	wrongDigest.Operation.Metadata = map[string]interface{}{"workloadReleaseRequestDigest": "sha256:" + strings.Repeat("1", 64)}
	if err := accept(wrongDigest); !errors.Is(err, store.ErrAcceptanceInvalid) {
		t.Fatalf("accepted a request digest that does not bind its qualification and replica selection: %v", err)
	}
	qualification.DeploymentID = acceptPrivateWorkloadReleaseSource(t, adapter, intent, target)
	resignPrivateInputQualification(t, &qualification)
	plan, err := acceptedWorkloadReleasePlan(input.Operation.Payload, app)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.verifyWorkloadReleaseQualificationSource(ctx, plan, qualification); err != nil {
		t.Fatalf("completed source deployment did not satisfy qualification: %v", err)
	}
	if err := accept(input); err == nil {
		t.Fatal("accepted a workload release without a control-owned active route predecessor")
	}
	active := activeFleetRoute{IntentID: "current-serving-route", DeploymentID: intent.OldDeploymentID, Generation: intent.FirstRouteGeneration - 1,
		RouteSHA256: "sha256:current-route", JobDigest: intent.OldJobDigest, OperationID: "old-operation",
		SpecDigest: intent.OldSpecDigest, ImageTag: "registry.example.test/widgets@" + intent.OldImageDigest, Process: intent.Process}
	write(adapter.fleetActiveRouteKey(app, target.ControlEnvironment, target.Region), active)
	write(adapter.fleetRouteReservationKey(app, target.ControlEnvironment, target.Region), initialFleetRouteReservation{IntentID: active.IntentID, Generation: active.Generation})
	stale := input
	stale.Operation = operation
	stale.Operation.ID = uuid.NewString()
	stale.Identity.Key = uuid.NewString()
	staleIntent := intent
	staleIntent.OldDeploymentID = "superseded-deployment"
	stalePlan, err := json.Marshal(pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: staleIntent})
	if err != nil {
		t.Fatal(err)
	}
	stale.Operation.Payload = map[string]interface{}{"workloadReleasePlan": string(stalePlan)}
	if err := accept(stale); err == nil {
		t.Fatal("accepted a workload release whose predecessor differs from the active route")
	}
	staleDigest := input
	staleDigest.Operation = operation
	staleDigest.Operation.ID = uuid.NewString()
	staleDigest.Identity.Key = uuid.NewString()
	wrongDigestIntent := intent
	wrongDigestIntent.OldJobDigest = "sha256:" + strings.Repeat("9", 64)
	wrongDigestPlan, err := json.Marshal(pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: wrongDigestIntent})
	if err != nil {
		t.Fatal(err)
	}
	staleDigest.Operation.Payload = map[string]interface{}{"workloadReleasePlan": string(wrongDigestPlan)}
	if err := accept(staleDigest); err == nil {
		t.Fatal("accepted a predecessor digest that differs from the controller-owned active route")
	}
	staleGeneration := input
	staleGeneration.Operation = operation
	staleGeneration.Operation.ID = uuid.NewString()
	staleGeneration.Identity.Key = uuid.NewString()
	wrongGenerationIntent := intent
	wrongGenerationIntent.FirstRouteGeneration++
	wrongGenerationPlan, err := json.Marshal(pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: wrongGenerationIntent})
	if err != nil {
		t.Fatal(err)
	}
	staleGeneration.Operation.Payload = map[string]interface{}{"workloadReleasePlan": string(wrongGenerationPlan)}
	if err := accept(staleGeneration); err == nil {
		t.Fatal("accepted a workload release with a non-successor route generation")
	}
	write(adapter.fleetRouteReservationKey(app, target.ControlEnvironment, target.Region), initialFleetRouteReservation{IntentID: "different-route", Generation: active.Generation})
	staleReservation := input
	staleReservation.Operation = operation
	staleReservation.Operation.ID = uuid.NewString()
	staleReservation.Identity.Key = uuid.NewString()
	if err := accept(staleReservation); err == nil {
		t.Fatal("accepted a workload release while the active route reservation disagreed")
	}
	write(adapter.fleetRouteReservationKey(app, target.ControlEnvironment, target.Region), initialFleetRouteReservation{IntentID: active.IntentID, Generation: active.Generation})
	missingObserver := input
	missingObserver.Operation = operation
	missingObserver.Operation.ID = uuid.NewString()
	missingObserver.Identity.Key = uuid.NewString()
	if _, err := adapter.AcceptPrivateWorkloadRelease(ctx, missingObserver, qualification, []string{trustedQualificationKey}, material, keys, nil); err == nil {
		t.Fatal("accepted a release without a trusted serving-lineage observer")
	}
	wrongObservedLineage := *lineageObserver
	wrongObservedLineage.lineage.ModifyIndex++
	wrongObservedRequest := input
	wrongObservedRequest.Operation = operation
	wrongObservedRequest.Operation.ID = uuid.NewString()
	wrongObservedRequest.Identity.Key = uuid.NewString()
	if _, err := adapter.AcceptPrivateWorkloadRelease(ctx, wrongObservedRequest, qualification, []string{trustedQualificationKey}, material, keys, &wrongObservedLineage); err == nil {
		t.Fatal("accepted a predecessor whose live Nomad modify index changed")
	}
	if _, err := adapter.AcceptPrivateWorkloadRelease(ctx, input, qualification, nil, material, keys, lineageObserver); !errors.Is(err, store.ErrAcceptanceInvalid) {
		t.Fatalf("fresh admission must require a trusted qualification: %v", err)
	}
	accepted, err := adapter.AcceptPrivateWorkloadRelease(ctx, input, qualification, []string{trustedQualificationKey}, material, keys, lineageObserver)
	if err != nil {
		t.Fatal(err)
	}
	if lineageObserver.calls != 2 {
		t.Fatalf("fresh serving lineage was not rechecked immediately before acceptance: calls=%d", lineageObserver.calls)
	}
	if accepted.Operation.ID != operation.ID || accepted.Operation.Payload["workloadReleasePrivateRecordId"] != operation.ID || accepted.FleetAppTarget == nil || !sameFleetAppTarget(*accepted.FleetAppTarget, target) {
		t.Fatalf("accepted private workload release lost signed plan or target binding: %+v", accepted)
	}
	competing := input
	competing.Operation = operation
	competing.Operation.ID = uuid.NewString()
	competing.Identity.Key = uuid.NewString()
	if _, err := adapter.AcceptPrivateWorkloadRelease(ctx, competing, qualification, []string{trustedQualificationKey}, material, keys, lineageObserver); err == nil {
		t.Fatal("second mutable workload release was accepted while the first app operation remained active")
	}
	opened, err := adapter.OpenPrivateWorkloadRelease(ctx, accepted.Operation, keys)
	if err != nil || !bytes.Equal(opened, material) {
		t.Fatalf("opened private SOPS snapshot differs: equal=%v err=%v", bytes.Equal(opened, material), err)
	}
	priorLineageReads := lineageObserver.calls
	if _, err := adapter.kv.Delete(ctx, adapter.deploymentKey(qualification.DeploymentID)); err != nil {
		t.Fatalf("remove mutable source projection before exact replay: %v", err)
	}
	replayInput := input
	replayInput.Operation.ID = uuid.NewString()
	replayInput.Operation.Payload = map[string]interface{}{"malformedMutablePlan": true}
	replayed, err := adapter.AcceptPrivateWorkloadRelease(ctx, replayInput, model.ReleaseQualification{}, nil, material, keys, lineageObserver)
	if err != nil || !replayed.Replayed || replayed.Operation.ID != accepted.Operation.ID || replayed.Intent.CanonicalDigest != accepted.Intent.CanonicalDigest {
		t.Fatalf("same private workload request did not return original acceptance without current qualification trust: replay=%+v err=%v", replayed, err)
	}
	if lineageObserver.calls != priorLineageReads {
		t.Fatalf("idempotent replay performed new mutable lineage reads: before=%d after=%d", priorLineageReads, lineageObserver.calls)
	}
	replayedAfterSecretRotation, err := adapter.AcceptPrivateWorkloadRelease(ctx, replayInput, model.ReleaseQualification{}, nil, []byte("different-sops-snapshot"), keys, lineageObserver)
	if err != nil || !replayedAfterSecretRotation.Replayed || replayedAfterSecretRotation.Operation.ID != accepted.Operation.ID {
		t.Fatalf("same request did not replay original acceptance after current SOPS input changed: replay=%+v err=%v", replayedAfterSecretRotation, err)
	}
	differentQualification := qualification
	differentQualification.ID += "-different"
	resignPrivateInputQualification(t, &differentQualification)
	changedRequest := input
	changedQualificationDigest, err := pipeline.WorkloadReleaseRequestDigest(differentQualification.ID, intent.TargetReplicas)
	if err != nil {
		t.Fatal(err)
	}
	changedRequest.Operation.Metadata = map[string]interface{}{"workloadReleaseRequestDigest": changedQualificationDigest}
	if _, err := adapter.AcceptPrivateWorkloadRelease(ctx, changedRequest, differentQualification, []string{trustedQualificationKey}, material, keys, lineageObserver); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("same idempotency key accepted different qualification evidence: %v", err)
	}
	changedReplicaRequest := input
	changedIntent := intent
	changedIntent.TargetReplicas++
	changedPlan, err := json.Marshal(pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: changedIntent})
	if err != nil {
		t.Fatal(err)
	}
	changedReplicaRequest.Operation.Payload = map[string]interface{}{"workloadReleasePlan": string(changedPlan)}
	changedReplicaDigest, err := pipeline.WorkloadReleaseRequestDigest(qualification.ID, changedIntent.TargetReplicas)
	if err != nil {
		t.Fatal(err)
	}
	changedReplicaRequest.Operation.Metadata = map[string]interface{}{"workloadReleaseRequestDigest": changedReplicaDigest}
	if _, err := adapter.AcceptPrivateWorkloadRelease(ctx, changedReplicaRequest, qualification, []string{trustedQualificationKey}, material, keys, lineageObserver); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("same idempotency key accepted a different target size: %v", err)
	}
	if accepted.Operation.Payload["workloadReleaseQualification"] == nil {
		t.Fatal("signed operation omitted its release qualification evidence")
	}
	ids, err := adapter.RequiredWorkloadReleaseInputKeys(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "release-input-v1" {
		t.Fatalf("required private input keys=%v err=%v", ids, err)
	}
	withoutRetainedKey, err := store.NewWorkloadReleaseInputKeyRing("other-v1", map[string][]byte{"other-v1": bytes.Repeat([]byte{0x6b}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.OpenPrivateWorkloadRelease(ctx, accepted.Operation, withoutRetainedKey); err == nil {
		t.Fatal("private workload input opened without its retained envelope key")
	}
	tampered := accepted.Operation
	tampered.Payload = make(map[string]interface{}, len(accepted.Operation.Payload))
	for key, value := range accepted.Operation.Payload {
		tampered.Payload[key] = value
	}
	tampered.Payload["workloadReleasePrivateInputDigest"] = "sha256:substituted"
	if _, err := adapter.OpenPrivateWorkloadRelease(ctx, tampered, keys); err == nil {
		t.Fatal("private workload input opened after signed digest substitution")
	}
}

func privateInputQualification(t *testing.T, intent pipeline.WorkloadReleaseIntent, deploymentID string) (model.ReleaseQualification, string) {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x71}, ed25519.SeedSize))
	now := time.Now().UTC().Truncate(time.Microsecond)
	qualification := model.ReleaseQualification{
		SchemaVersion: model.ReleaseQualificationSchema,
		ID:            "qualification-" + intent.App,
		App:           intent.App,
		Environment:   "staging",
		DeploymentID:  deploymentID,
		SourceSHA:     intent.SourceRevision,
		Artifact:      intent.TargetImage,
		IssuedAt:      now.Add(-time.Minute),
		ExpiresAt:     now.Add(time.Hour),
		Candidate: model.ReleaseCandidate{Attestation: model.ReleaseAttestationIdentity{
			MaterialSHA: intent.SourceRevision, SubjectDigest: intent.TargetImageDigest,
		}},
	}
	resignPrivateInputQualification(t, &qualification)
	trusted := base64.RawStdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey))
	return qualification, trusted
}

func acceptPrivateWorkloadReleaseSource(t *testing.T, adapter *V3OperationStore, intent pipeline.WorkloadReleaseIntent, target FleetAppTarget) string {
	t.Helper()
	ctx := context.Background()
	deploymentID, sagaID := uuid.NewString(), uuid.NewString()
	request := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: adapter.authority,
			Actor: store.OperationActor{Issuer: "https://operator.example.test", Subject: "release-operator"},
			Kind:  "app.deploy", Resource: "app/" + intent.App, Key: "qualified-source-" + deploymentID},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: intent.App, SagaID: sagaID,
			Status: model.OperationQueued, Source: "release-control-api", Payload: map[string]interface{}{
				"deploymentId": deploymentID, "sourceSha": intent.SourceRevision, "artifact": intent.TargetImage,
				"specDigest": intent.TargetSpecDigest,
			}},
		Deployment: &model.Deployment{ID: deploymentID, App: intent.App, Environment: "staging", SagaID: sagaID,
			Status: model.StatusQueued, CommitSHA: intent.SourceRevision, ImageTag: intent.TargetImage,
			SpecDigest: intent.TargetSpecDigest, SourceKind: "release", SourceRef: intent.SourceRevision, StartedAt: time.Now().UTC()},
		Regions: []model.ResolvedRegion{{Name: target.Region, NomadRegion: target.NomadRegion,
			Datacenters: append([]string(nil), target.Datacenters...), TrafficWeight: 0}},
		Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
		Audit:     store.AcceptanceAuditContext{Source: "release-qualification-test"},
		Semantics: map[string]interface{}{"fleetAppTarget": target},
	}
	var err error
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_, claim, err := adapter.ClaimNextOperation(ctx, "qualification-source-worker", time.Minute, []string{"app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, intent.App)
	if err != nil || !acquired {
		t.Fatalf("source deployment app lock acquired=%v err=%v", acquired, err)
	}
	defer lock.Release()
	deployment := *accepted.Deployment
	deployment.Status = model.StatusDeployed
	regions := []model.DeploymentRegion{{DeploymentID: deploymentID, Region: target.Region, NomadRegion: target.NomadRegion,
		Status: model.StatusDeployed, DesiredWeight: 0, ActiveWeight: 0, EvalID: "qualified-source-eval"}}
	if err := adapter.finishClaimedDeployment(ctx, claim, lock, deployment, regions, model.OperationSucceeded, "qualified source deployed", nil); err != nil {
		t.Fatal(err)
	}
	return deploymentID
}

func resignPrivateInputQualification(t *testing.T, qualification *model.ReleaseQualification) {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x71}, ed25519.SeedSize))
	payload := []byte(model.CanonicalReleaseQualificationPayload(*qualification))
	keyID := model.ReleaseQualificationKeyID(privateKey.Public().(ed25519.PublicKey))
	signature := base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, model.DSSEPAE(model.ReleaseQualificationPayloadType, payload)))
	qualification.KeyID, qualification.Signature = keyID, signature
	qualification.DSSE = model.DSSEEnvelope{
		PayloadType: model.ReleaseQualificationPayloadType,
		Payload:     base64.RawStdEncoding.EncodeToString(payload),
		Signatures:  []model.DSSESignature{{KeyID: keyID, Sig: signature}},
	}
}

type workloadReleaseLineageObserverFixture struct {
	lineage nomad.ActiveDeploymentLineage
	calls   int
}

func newWorkloadReleaseLineageObserverFixture(intent pipeline.WorkloadReleaseIntent, operationID string) *workloadReleaseLineageObserverFixture {
	return &workloadReleaseLineageObserverFixture{lineage: nomad.ActiveDeploymentLineage{
		App: intent.App, DeploymentID: intent.OldDeploymentID, OperationID: operationID,
		Region: intent.Region, NomadRegion: intent.NomadRegion, Namespace: intent.Namespace,
		SpecDigest: intent.OldSpecDigest, ImageTag: "registry.example.test/widgets@" + intent.OldImageDigest,
		JobID: intent.OldJobID, JobDigest: strings.TrimPrefix(intent.OldJobDigest, "sha256:"),
		JobVersion: intent.OldJobVersion, ModifyIndex: intent.OldJobModifyIndex,
		Process: intent.Process, Replicas: len(intent.OldAllocationIDs), AllocationIDs: append([]string(nil), intent.OldAllocationIDs...),
	}}
}

func (f *workloadReleaseLineageObserverFixture) ObserveActiveDeploymentLineage(_ context.Context, app, region, nomadRegion, namespace,
	deploymentID, operationID, specDigest, expectedJobDigest, imageTag, process string) (nomad.ActiveDeploymentLineage, error) {
	f.calls++
	lineage := f.lineage
	if app != lineage.App || region != lineage.Region || nomadRegion != lineage.NomadRegion || namespace != lineage.Namespace ||
		deploymentID != lineage.DeploymentID || operationID != lineage.OperationID || specDigest != lineage.SpecDigest ||
		expectedJobDigest != lineage.JobDigest || imageTag != lineage.ImageTag || process != lineage.Process {
		return nomad.ActiveDeploymentLineage{}, errors.New("lineage request differs from active route fixture")
	}
	return lineage, nil
}
