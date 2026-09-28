package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

type claimedInputStagerFake struct {
	calls  int
	tamper bool
}

func (f *claimedInputStagerFake) PrepareManagedDeploymentJob(_ context.Context, deployment *model.Deployment, spec *model.InfraSpec,
	region model.ResolvedRegion, revision int64, targets map[string]database.TargetIdentity, _ map[string]string,
	_ nomad.ManagedJobSecretSource) (*nomadapi.Job, nomad.DeploymentJobEffectInput, error) {
	f.calls++
	input, err := nomad.BuildManagedDeploymentJobEffectInput(deployment, spec, region, revision, targets)
	if err != nil {
		return nil, input, err
	}
	job, err := nomad.TranslateManagedDeploymentForRegionAt(spec, deployment.ImageTag, nil, region, deployment.ID, revision)
	if err != nil {
		return nil, input, err
	}
	if f.tamper {
		if job.Meta == nil {
			job.Meta = map[string]string{}
		}
		job.Meta["unexpected"] = "different revision"
	}
	return job, input, nil
}

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
    trafficProbe:
      path: /ready
      bodySHA256: dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
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
	accepted := store.AcceptedOperation{Operation: claimed, Deployment: &model.Deployment{ID: "deploy-one", App: "pilot", Environment: "staging", SpecDigest: digest, ImageTag: "registry.example.test/pilot@sha256:" + strings.Repeat("a", 64)},
		Regions: spec.ResolvedRegions(), Intent: store.SignedAcceptanceIntent{ID: "accept-one", OperationID: claimed.ID, DeploymentID: "deploy-one", CanonicalDigest: "signed-digest"}}
	accepted.FleetAppTarget = &store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: "pilot", ControlEnvironment: "staging", Cluster: "norn-staging", FleetEnvironment: "staging/west", Region: "west", NomadRegion: accepted.Regions[0].NomadRegion, Datacenters: accepted.Regions[0].Datacenters, Generation: 1}
	load := func() (ClaimedFleetDeploymentSource, error) {
		return LoadClaimedFleetDeploymentSource(t.Context(), claimedDeploymentVerifierFake{accepted}, claimed, root)
	}
	bound, err := load()
	if err != nil || bound.Route.DeploymentID != "deploy-one" || bound.Route.Endpoint != "https://pilot.example.test" || bound.Managed.Accepted.Deployment.SpecDigest != digest {
		t.Fatalf("bound source=%+v err=%v", bound, err)
	}
	claim, err := store.NewOperationClaim(claimed.ID, "worker-one", 1)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildClaimedFleetDeploymentJobPlan(bound, claim, "test-authority")
	if err != nil || plan.Job == nil || plan.Input.DeploymentID != "deploy-one" || plan.Job.Meta[nomad.DeploymentJobDigestMeta] != plan.Input.JobDigest ||
		plan.Job.Meta[nomad.DeploymentExecutionIDMeta] != plan.Reservation.SupervisorExecutionID || plan.Reservation.OperationClaim.Generation != claim.Generation() {
		t.Fatalf("claimed job plan=%+v err=%v", plan, err)
	}
	effects := &deploymentStepEffects{created: true}
	remote := &deploymentStepRemote{state: nomad.DeploymentJobFound}
	if decision, err := EnsureDeploymentJobEffect(t.Context(), effects, remote, plan.Reservation, plan.Job); err != nil || decision.State != DeploymentJobEffectObserved || remote.submits != 1 {
		t.Fatalf("planned job was refused by effect boundary: decision=%+v err=%v submits=%d", decision, err, remote.submits)
	}
	stager := &claimedInputStagerFake{}
	staged, err := PrepareClaimedFleetDeploymentJob(t.Context(), bound, claim, "test-authority", stager, nil, nil)
	if err != nil || stager.calls != 1 || staged.Input.JobDigest != plan.Input.JobDigest || staged.Reservation.InputDigest != plan.Reservation.InputDigest {
		t.Fatalf("staged job differs from signed plan: calls=%d err=%v jobDigest=%q want=%q inputDigest=%q want=%q", stager.calls, err, staged.Input.JobDigest, plan.Input.JobDigest, staged.Reservation.InputDigest, plan.Reservation.InputDigest)
	}
	stager.tamper = true
	if _, err := PrepareClaimedFleetDeploymentJob(t.Context(), bound, claim, "test-authority", stager, nil, nil); err == nil {
		t.Fatal("staged Nomad revision differed from the signed plan")
	}
	badRoute := bound
	badRoute.Route.Region = "other"
	if _, err := BuildClaimedFleetDeploymentJobPlan(badRoute, claim, "test-authority"); err == nil {
		t.Fatal("job plan accepted a different route region")
	}
	badSpec := bound
	copySpec := *bound.Spec
	copySpec.Endpoints = append([]model.Endpoint(nil), bound.Spec.Endpoints...)
	copySpec.Endpoints[0].URL = "https://other.example.test"
	badSpec.Spec = &copySpec
	if _, err := BuildClaimedFleetDeploymentJobPlan(badSpec, claim, "test-authority"); err == nil {
		t.Fatal("job plan accepted a changed source endpoint")
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
