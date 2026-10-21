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
	want, err := nomad.ManagedDeploymentJobID("demo", "west", accepted.Deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := operations.ActiveFleetScaleTarget(ctx, "demo", "staging", "west")
	if err != nil || got.JobID != want || got.DeploymentID != accepted.Deployment.ID || got.SpecDigest != accepted.Deployment.SpecDigest {
		t.Fatalf("active scale target=%+v err=%v", got, err)
	}
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
