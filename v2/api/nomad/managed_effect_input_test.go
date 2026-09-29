package nomad

import (
	"testing"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

func TestBuildManagedDeploymentJobEffectInputBindsSpecAndTarget(t *testing.T) {
	spec := &model.InfraSpec{App: "orders", Processes: map[string]model.Process{"web": {Port: 8080}},
		Databases: []model.DatabaseRequirement{{Name: "primary", Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL"}}}}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	deployment := &model.Deployment{ID: "deployment-one", App: spec.App, ImageTag: "registry.example/orders@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpecDigest: digest}
	region := spec.ResolvedRegions()[0]
	target := database.TargetIdentity{ServiceID: "pg", ServiceGeneration: 1, BindingID: "orders-primary", BindingGeneration: 2,
		Engine: database.EnginePostgreSQL, Database: "orders", Role: "orders_app"}
	if _, err := BuildManagedDeploymentJobEffectInput(deployment, spec, region, 7, nil); err == nil {
		t.Fatal("missing target accepted")
	}
	input, err := BuildManagedDeploymentJobEffectInput(deployment, spec, region, 7, map[string]database.TargetIdentity{"primary": target})
	if err != nil {
		t.Fatal(err)
	}
	if input.JobID == "" || input.ManagedInputs == nil || input.ManagedInputs.DatabaseRevision != 7 ||
		input.ExpectedDatabaseTargets["primary"] == "" || input.JobDigest != "" {
		t.Fatalf("incorrect managed effect source: %+v", input)
	}
	job, err := TranslateManagedDeploymentForRegionAt(spec, deployment.ImageTag, nil, region, deployment.ID, 7)
	if err != nil || ValidateManagedJobInputPlan(job, *input.ManagedInputs) != nil {
		t.Fatalf("derived input differs from rendered job: %v", err)
	}
	deployment.SpecDigest = "sha256:changed"
	if _, err := BuildManagedDeploymentJobEffectInput(deployment, spec, region, 7, map[string]database.TargetIdentity{"primary": target}); err == nil {
		t.Fatal("changed source spec accepted")
	}
}
