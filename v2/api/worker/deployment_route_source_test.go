package worker

import (
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestVerifyClaimedFleetRouteSourceUsesPinnedEndpointAndProcess(t *testing.T) {
	spec := &model.InfraSpec{App: "pilot", Processes: map[string]model.Process{"web": {Port: 8080}, "admin": {Port: 9090}}, Endpoints: []model.Endpoint{{URL: "https://pilot.example.test", Region: "nyc3", Process: "web"}}}
	claimed := model.Operation{ID: "operation-one", Kind: "app.deploy", App: "pilot", Payload: map[string]interface{}{"deploymentId": "deploy-one"}}
	accepted := store.AcceptedOperation{Operation: claimed, Deployment: &model.Deployment{ID: "deploy-one", App: "pilot"}, Regions: []model.ResolvedRegion{{Name: "nyc3"}}, Intent: store.SignedAcceptanceIntent{ID: "accept-one", OperationID: "operation-one", DeploymentID: "deploy-one", CanonicalDigest: "signed-digest"}}
	var err error
	accepted.Deployment.SpecDigest, err = model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	verify := func() (VerifiedFleetRouteSource, error) {
		return VerifyClaimedFleetRouteSource(t.Context(), claimedDeploymentVerifierFake{accepted}, claimed, spec, "nyc3")
	}
	got, err := verify()
	if err != nil || got.Endpoint != "https://pilot.example.test" || got.Process != "web" || got.Port != 8080 || got.SpecDigest != accepted.Deployment.SpecDigest || got.OperationID != claimed.ID || got.DeploymentID != accepted.Deployment.ID || got.AcceptanceID != accepted.Intent.ID || got.AcceptanceDigest != accepted.Intent.CanonicalDigest {
		t.Fatalf("route source=%+v err=%v", got, err)
	}
	accepted.Intent.DeploymentID = "different"
	if _, err := verify(); err == nil {
		t.Fatal("acceptance linked to a different deployment")
	}
	accepted.Intent.DeploymentID = "deploy-one"
	spec.Endpoints[0].Process = "admin"
	if _, err := verify(); err == nil {
		t.Fatal("changed endpoint process accepted against pinned digest")
	}
	spec.Endpoints[0].Process = "web"
	spec.Endpoints = append(spec.Endpoints, spec.Endpoints[0])
	accepted.Deployment.SpecDigest, _ = model.InfraSpecDigest(spec)
	if _, err := verify(); err == nil {
		t.Fatal("ambiguous route endpoint accepted")
	}
	spec.Endpoints = spec.Endpoints[:1]
	for _, invalid := range []model.Endpoint{
		{URL: "https://pilot.example.test", Region: "nyc3"},
		{URL: "http://pilot.example.test", Region: "nyc3", Process: "web"},
		{URL: "https://pilot.example.test/path", Region: "nyc3", Process: "web"},
	} {
		spec.Endpoints[0] = invalid
		accepted.Deployment.SpecDigest, _ = model.InfraSpecDigest(spec)
		if _, err := verify(); err == nil {
			t.Fatalf("invalid route source %+v accepted", invalid)
		}
	}
	accepted.Regions = nil
	if _, err := verify(); err == nil {
		t.Fatal("unsigned route region accepted")
	}
}
