package etcdstore

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

func TestV3ScaleTargetRequiresCompletedActiveManagedRoute(t *testing.T) {
	ctx, client, operations, _, _, _ := newScaleFoundationFixture(t)
	if _, _, err := operations.ActiveFleetScaleTarget(ctx, "widgets", "staging", "us-central"); err == nil {
		t.Fatal("accepted scale without an active Fleet route")
	}
	deploymentID := uuid.NewString()
	intentID := uuid.NewString()
	opID := uuid.NewString()
	target := store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: "widgets", ControlEnvironment: "staging", Cluster: "fleet", FleetEnvironment: "staging/us-central", Region: "us-central", NomadRegion: "global", Datacenters: []string{"dc1"}, Generation: 1}
	intent := InitialFleetRouteIntent{SchemaVersion: initialFleetRouteIntentSchema, ID: intentID, App: "widgets", ControlEnvironment: "staging", OperationID: opID, DeploymentID: deploymentID, Region: "us-central", NomadRegion: "global", FleetTarget: target, Generation: 1, RenderedRoute: ingress.RenderedRoute{SHA256: "sha256:route"}}
	intentKey := operations.initialFleetRouteKey(intent.App, intent.ControlEnvironment, intent.Region, deploymentID)
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
	put(operations.fleetAppTargetKey("widgets", "staging"), target)
	if _, err := client.Put(ctx, operations.initialFleetRouteIDKey(intentID), intentKey); err != nil {
		t.Fatal(err)
	}
	activeKey := operations.fleetActiveRouteKey("widgets", "staging", "us-central")
	put(activeKey, activeFleetRoute{IntentID: intentID, DeploymentID: deploymentID, Generation: 1, RouteSHA256: "sha256:route"})
	put(operations.opKey(opID), v3Record{Operation: model.Operation{ID: opID, Kind: "app.deploy", App: "widgets", Status: model.OperationSucceeded, Payload: map[string]interface{}{"deploymentId": deploymentID}}, Generation: 1})
	want, err := nomad.ManagedDeploymentJobID("widgets", "us-central", deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, gotDeployment, err := operations.ActiveFleetScaleTarget(ctx, "widgets", "staging", "us-central")
	if err != nil || jobID != want || gotDeployment != deploymentID {
		t.Fatalf("active scale target=%q %q err=%v", jobID, gotDeployment, err)
	}
	put(activeKey, activeFleetRoute{IntentID: intentID, DeploymentID: uuid.NewString(), Generation: 1, RouteSHA256: "sha256:route"})
	if _, _, err := operations.ActiveFleetScaleTarget(ctx, "widgets", "staging", "us-central"); err == nil {
		t.Fatal("accepted a route marker pointed at another deployment")
	}
}
