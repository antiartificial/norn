package etcdstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

func TestV3ScaleTargetRequiresCompletedActiveManagedRoute(t *testing.T) {
	operations, client, _ := deploymentEtcdStore(t)
	ctx := context.Background()
	if _, err := operations.ActiveFleetScaleTarget(ctx, "demo", "staging", "west"); err == nil {
		t.Fatal("accepted scale without an active Fleet route")
	}
	request := zeroTrafficDeploymentAdmissionRequest(t, operations.authority)
	request.Deployment.SpecDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var err error
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := operations.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := operations.ClaimNextOperation(ctx, "deploy-worker", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, "demo")
	if err != nil || !acquired {
		t.Fatalf("lock=%t err=%v", acquired, err)
	}
	result := *accepted.Deployment
	result.Status = model.StatusDeployed
	regions := []model.DeploymentRegion{{DeploymentID: result.ID, Region: "west", NomadRegion: "global", Status: model.StatusDeployed, DesiredWeight: 0, ActiveWeight: 0, UpdatedAt: time.Now().UTC()}}
	if err := operations.finishClaimedDeployment(ctx, claim, lock, result, regions, model.OperationSucceeded, "deployed", nil); err != nil {
		lock.Release()
		t.Fatal(err)
	}
	lock.Release()

	target, _, err := operations.loadFleetAppTarget(ctx, "demo", "staging")
	if err != nil {
		t.Fatal(err)
	}
	intentID := uuid.NewString()
	intent := InitialFleetRouteIntent{SchemaVersion: initialFleetRouteIntentSchema, ID: intentID, App: "demo", ControlEnvironment: "staging", OperationID: accepted.Operation.ID,
		DeploymentID: accepted.Deployment.ID, AcceptanceID: accepted.Intent.ID, AcceptanceDigest: accepted.Intent.CanonicalDigest, SpecDigest: accepted.Deployment.SpecDigest,
		Region: "west", NomadRegion: "global", FleetTarget: target, Generation: 1, RenderedRoute: ingress.RenderedRoute{SHA256: "sha256:route"}}
	intentKey := operations.initialFleetRouteKey(intent.App, intent.ControlEnvironment, intent.Region, intent.DeploymentID)
	put := func(key string, value interface{}) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Put(ctx, key, string(encoded)); err != nil {
			t.Fatal(err)
		}
	}
	put(intentKey, intent)
	if _, err := client.Put(ctx, operations.initialFleetRouteIDKey(intentID), intentKey); err != nil {
		t.Fatal(err)
	}
	activeKey := operations.fleetActiveRouteKey("demo", "staging", "west")
	put(activeKey, activeFleetRoute{IntentID: intentID, DeploymentID: accepted.Deployment.ID, Generation: 1, RouteSHA256: "sha256:route"})
	reservationKey := operations.fleetRouteReservationKey("demo", "staging", "west")
	put(reservationKey, initialFleetRouteReservation{IntentID: intentID, Generation: 1})
	want, err := nomad.ManagedDeploymentJobID("demo", "west", accepted.Deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := operations.ActiveFleetScaleTarget(ctx, "demo", "staging", "west")
	if err != nil || got.JobID != want || got.DeploymentID != accepted.Deployment.ID || got.SpecDigest != accepted.Deployment.SpecDigest {
		t.Fatalf("active scale target=%+v err=%v", got, err)
	}
	put(reservationKey, initialFleetRouteReservation{IntentID: uuid.NewString(), Generation: 1})
	if _, err := operations.ActiveFleetScaleTarget(ctx, "demo", "staging", "west"); err == nil {
		t.Fatal("accepted a scale target while another route intent held the reservation")
	}
	put(reservationKey, initialFleetRouteReservation{IntentID: intentID, Generation: 1})
	put(activeKey, activeFleetRoute{IntentID: intentID, DeploymentID: uuid.NewString(), Generation: 1, RouteSHA256: "sha256:route"})
	if _, err := operations.ActiveFleetScaleTarget(ctx, "demo", "staging", "west"); err == nil {
		t.Fatal("accepted a route marker pointed at another deployment")
	}
}

// The work-staging pilot was submitted directly to Nomad. Its external
// admission receipt does not create a native deployment route, and the
// controller must never infer ownership from a matching app name alone.
func TestDirectPilotIsNotANativeScaleTarget(t *testing.T) {
	operations, _, _ := deploymentEtcdStore(t)
	if target, err := operations.ActiveFleetScaleTarget(context.Background(), "hello-norn-mysql", "staging", "nyc3"); err == nil {
		t.Fatalf("direct Nomad pilot was accepted as Norn-owned: %+v", target)
	}
	managedID, err := nomad.ManagedDeploymentJobID("hello-norn-mysql", "nyc3", "replacement-deployment")
	if err != nil || managedID == "hello-norn-mysql" {
		t.Fatalf("native replacement did not receive an independent job identity: %q, %v", managedID, err)
	}
}

func TestV3ScaleTargetResolvesCompletedWorkloadRoute(t *testing.T) {
	adapter, client, _ := functionInvocationEffectAttemptStore(t)
	ctx := context.Background()
	const app = "widgets"
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: app, ControlEnvironment: "staging", Cluster: "norn-staging", FleetEnvironment: "staging/nyc3", Region: "nyc3", NomadRegion: "global", NomadNamespace: "default", Datacenters: []string{"nyc3"}, Generation: 1}
	if _, err := adapter.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	intent := pipeline.WorkloadReleaseIntent{
		ID: "release-scale-target", App: app, Region: target.Region, NomadRegion: target.NomadRegion, Namespace: target.NomadNamespace,
		ControlEnvironment: target.ControlEnvironment, FleetCluster: target.Cluster, FleetEnvironment: target.FleetEnvironment, FleetDatacenters: append([]string(nil), target.Datacenters...), FleetTargetGeneration: target.Generation,
		Process: "web", Endpoint: "https://widgets.example.test", PublicPaths: []string{"/proof"}, RetryGETPaths: []string{"/proof"}, PrivateReadinessPath: "/ready", TrafficProbePath: "/proof", TrafficProbeBodySHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SpecDigest: functionInvocationEffectAttemptDigest, SourceRevision: "source-1", OldDeploymentID: "old-deploy", OldImageDigest: functionInvocationEffectAttemptDigest,
		OldSpecDigest: functionInvocationEffectAttemptDigest, OldJobID: "widgets-web-old", OldJobDigest: functionInvocationEffectAttemptDigest, OldJobVersion: 1, OldJobModifyIndex: 2,
		TargetDeploymentID: "target-deploy", TargetImage: "registry.example.test/widgets@" + functionInvocationEffectAttemptDigest, TargetImageDigest: functionInvocationEffectAttemptDigest,
		TargetJobID: "widgets-web-target", TargetJobDigest: functionInvocationEffectAttemptDigest, TargetSpecDigest: functionInvocationEffectAttemptDigest,
		TargetOperationID: "target-operation", TargetExecutionID: "target-execution", OldAllocationIDs: []string{"old-allocation"}, TargetJobVersion: 1, TargetJobModifyIndex: 3,
		TargetReplicas: 1, SurgeLimit: 1, PolicyApprovalID: workloadReleaseTestPolicyApprovalID,
		PolicyDigest: workloadReleaseTestPolicyDigest(app, target), FirstRouteGeneration: 4,
		RouteSteps: []pipeline.WorkloadReleaseRouteStep{{OldWeight: 100, TargetWeight: 0}, {OldWeight: 90, TargetWeight: 10}, {OldWeight: 0, TargetWeight: 100}},
	}
	intentBytes, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: app, Status: model.OperationQueued, MaxAttempts: 3, Source: "test", Payload: map[string]interface{}{"workloadReleaseIntent": string(intentBytes)}}
	acceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "workload-release-test", Subject: "operator"}, Kind: operation.Kind, Resource: "app/" + app, Key: uuid.NewString()},
		Operation: operation, Audit: store.AcceptanceAuditContext{Source: "workload-release-test"}, Semantics: map[string]interface{}{"intentId": intent.ID},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := adapter.ClaimNextOperation(ctx, "workload-release-worker", time.Minute, []string{operation.Kind})
	if err != nil || claimed == nil {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("lock=%t err=%v", acquired, err)
	}
	defer lock.Release()
	rendered, err := intent.RenderRouteStep(2)
	if err != nil {
		t.Fatal(err)
	}
	receipt := pipeline.WorkloadReleaseReceipt{
		IntentID: intent.ID, Phase: pipeline.WorkloadReleaseComplete, RouteStep: 2, RouteOldWeight: 0, RouteTargetWeight: 100,
		RouteGeneration: 6, RouteSHA256: "sha256:" + rendered.SHA256, OldJobStoppedVersion: 2, OldJobStoppedModifyIndex: 3,
		DrainedOldIDs: []string{"old-allocation"}, TargetAllocations: []pipeline.WorkloadReleaseAllocationObservation{{
			AllocationID: "target-allocation", NodeID: "app-01", JobID: intent.TargetJobID, Namespace: intent.Namespace,
			TaskGroup: intent.Process, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
			ImageDigest: intent.TargetImageDigest, Healthy: true,
		}},
	}
	if err := receipt.ValidateAgainst(intent); err != nil {
		t.Fatalf("complete receipt invalid: %v", err)
	}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, intent.ID, "", receiptBytes); err != nil {
		t.Fatal(err)
	}
	if err := adapter.FinishClaimedOperationWithAppLock(ctx, claim, lock, model.OperationSucceeded, "workload release complete", nil); err != nil {
		t.Fatal(err)
	}
	put := func(key string, value interface{}) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Put(ctx, key, string(encoded)); err != nil {
			t.Fatal(err)
		}
	}
	put(adapter.fleetRouteReservationKey(app, target.ControlEnvironment, target.Region), initialFleetRouteReservation{IntentID: intent.ID, Generation: receipt.RouteGeneration})
	put(adapter.fleetActiveRouteKey(app, target.ControlEnvironment, target.Region), activeFleetRoute{IntentID: intent.ID, DeploymentID: intent.TargetDeploymentID, Generation: receipt.RouteGeneration, RouteSHA256: rendered.SHA256})

	got, err := adapter.ActiveFleetScaleTarget(ctx, app, target.ControlEnvironment, target.Region)
	if err != nil || got.JobID != intent.TargetJobID || got.DeploymentID != intent.TargetDeploymentID || got.SpecDigest != intent.TargetSpecDigest || got.NomadNamespace != intent.Namespace {
		t.Fatalf("workload active scale target=%+v err=%v", got, err)
	}
	put(adapter.fleetActiveRouteKey(app, target.ControlEnvironment, target.Region), activeFleetRoute{IntentID: intent.ID, DeploymentID: intent.TargetDeploymentID, Generation: receipt.RouteGeneration + 1, RouteSHA256: receipt.RouteSHA256})
	if _, err := adapter.ActiveFleetScaleTarget(ctx, app, target.ControlEnvironment, target.Region); err == nil {
		t.Fatal("accepted a workload route generation that differs from its complete receipt")
	}
	put(adapter.fleetActiveRouteKey(app, target.ControlEnvironment, target.Region), activeFleetRoute{IntentID: intent.ID, DeploymentID: intent.TargetDeploymentID, Generation: receipt.RouteGeneration, RouteSHA256: rendered.SHA256})
	changedTarget := target
	changedTarget.Generation++
	put(adapter.fleetAppTargetKey(app, target.ControlEnvironment), changedTarget)
	if _, err := adapter.ActiveFleetScaleTarget(ctx, app, target.ControlEnvironment, target.Region); err == nil {
		t.Fatal("accepted a workload route after Fleet target placement changed")
	}
}
