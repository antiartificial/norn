package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestLoadClaimedFleetDeploymentSourcePinsDeployableSpec(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "pilot")
	if err := os.Mkdir(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(appDir, "infraspec.yaml")
	source := `name: pilot
deploy: true
regions:
  west:
    nomadRegion: global
    datacenters: [dc1, dc2]
processes:
  web:
    port: 8080
endpoints:
  - url: https://pilot.example.test
    region: west
    process: web
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	spec, err := model.LoadInfraSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	claimed := model.Operation{ID: "operation-one", Kind: "app.deploy", App: "pilot", Payload: map[string]interface{}{"deploymentId": "deploy-one"}}
	accepted := store.AcceptedOperation{Operation: claimed, Deployment: &model.Deployment{ID: "deploy-one", App: "pilot", Environment: "staging", SpecDigest: digest},
		Regions: spec.ResolvedRegions(), Intent: store.SignedAcceptanceIntent{ID: "accept-one", OperationID: claimed.ID, DeploymentID: "deploy-one", CanonicalDigest: "signed-digest"}}
	accepted.FleetAppTarget = &store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: "pilot", ControlEnvironment: "staging", Cluster: "norn-staging", FleetEnvironment: "staging/west", Region: "west", NomadRegion: accepted.Regions[0].NomadRegion, Datacenters: accepted.Regions[0].Datacenters, Generation: 1}
	load := func() (ClaimedFleetDeploymentSource, error) {
		return LoadClaimedFleetDeploymentSource(t.Context(), claimedDeploymentVerifierFake{accepted}, claimed, root)
	}
	bound, err := load()
	if err != nil || bound.Route.DeploymentID != "deploy-one" || bound.Route.Endpoint != "https://pilot.example.test" || bound.Managed.Accepted.Deployment.SpecDigest != digest {
		t.Fatalf("bound source=%+v err=%v", bound, err)
	}
	if err := os.WriteFile(path, []byte(source+"# changed source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(); err != nil {
		// Comments are not part of the canonical InfraSpec digest.
		t.Fatalf("comment-only source change altered signed spec: %v", err)
	}
	changed := []byte(source + "  - url: https://other.example.test\n    region: west\n    process: web\n")
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(); err == nil {
		t.Fatal("changed deployable endpoint accepted against signed digest")
	}
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	duplicate := filepath.Join(root, "pilot-copy")
	if err := os.Mkdir(duplicate, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(duplicate, "infraspec.yaml"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(); err == nil {
		t.Fatal("duplicate deployable app source was selected")
	}
	if err := os.Remove(filepath.Join(duplicate, "infraspec.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(source, "deploy: true", "deploy: false", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(); err == nil {
		t.Fatal("disabled app source was selected for deployment")
	}
}
