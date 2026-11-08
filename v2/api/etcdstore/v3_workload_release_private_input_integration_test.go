package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"norn/v2/api/model"
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
	intent.TargetJobVersion, intent.TargetJobModifyIndex = 0, 0
	planBytes, err := json.Marshal(pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: app, Status: model.OperationQueued,
		MaxAttempts: 3, Source: "operator-api", Payload: map[string]interface{}{"workloadReleasePlan": string(planBytes)}}
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
	accept := func(request store.OperationAcceptance) error {
		t.Helper()
		_, err := adapter.AcceptPrivateWorkloadRelease(ctx, request, material, keys)
		return err
	}
	if err := accept(input); err == nil {
		t.Fatal("accepted a workload release without a control-owned active route predecessor")
	}
	active := activeFleetRoute{IntentID: "current-serving-route", DeploymentID: intent.OldDeploymentID, Generation: intent.FirstRouteGeneration - 1, RouteSHA256: "sha256:current-route"}
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
	accepted, err := adapter.AcceptPrivateWorkloadRelease(ctx, input, material, keys)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Operation.ID != operation.ID || accepted.Operation.Payload["workloadReleasePrivateRecordId"] != operation.ID || accepted.FleetAppTarget == nil || !sameFleetAppTarget(*accepted.FleetAppTarget, target) {
		t.Fatalf("accepted private workload release lost signed plan or target binding: %+v", accepted)
	}
	competing := input
	competing.Operation = operation
	competing.Operation.ID = uuid.NewString()
	competing.Identity.Key = uuid.NewString()
	if _, err := adapter.AcceptPrivateWorkloadRelease(ctx, competing, material, keys); err == nil {
		t.Fatal("second mutable workload release was accepted while the first app operation remained active")
	}
	opened, err := adapter.OpenPrivateWorkloadRelease(ctx, accepted.Operation, keys)
	if err != nil || !bytes.Equal(opened, material) {
		t.Fatalf("opened private SOPS snapshot differs: equal=%v err=%v", bytes.Equal(opened, material), err)
	}
	replayed, err := adapter.AcceptPrivateWorkloadRelease(ctx, input, material, keys)
	if err != nil || replayed.Operation.ID != accepted.Operation.ID || replayed.Intent.CanonicalDigest != accepted.Intent.CanonicalDigest {
		t.Fatalf("same private workload request did not return original acceptance: replay=%+v err=%v", replayed, err)
	}
	if _, err := adapter.AcceptPrivateWorkloadRelease(ctx, input, []byte("different-sops-snapshot"), keys); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("same idempotency key accepted different private inputs: %v", err)
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
