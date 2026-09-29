package nomad

import (
	"encoding/json"
	"testing"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

func TestPrepareManagedDeploymentJobPreflightsBeforeDelivery(t *testing.T) {
	client, fake := newFakeVariableClient(t)
	spec := &model.InfraSpec{App: "orders", Processes: map[string]model.Process{"web": {Port: 8080,
		NomadVariables: &model.NomadVariableFiles{UID: 65532, GID: 65532, Files: []model.NomadVariableFile{{Key: "API_TOKEN", Destination: "token"}}}}},
		Databases: []model.DatabaseRequirement{{Name: "primary", Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL"}}}}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	deployment := &model.Deployment{ID: "deploy-one", App: spec.App, ImageTag: "registry.example/orders@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpecDigest: digest}
	region := spec.ResolvedRegions()[0]
	target := database.TargetIdentity{ServiceID: "pg", ServiceGeneration: 1, BindingID: "orders-primary", BindingGeneration: 2,
		Engine: database.EnginePostgreSQL, Database: "orders", Role: "orders_app"}
	encoded, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]string{DatabaseItemKey("primary"): "postgres://private", DatabaseTargetItemKey("primary"): string(encoded)}
	targets := map[string]database.TargetIdentity{"primary": target}
	secret := &managedSecretSource{values: map[string]string{}}
	prepare := func(material map[string]string) error {
		_, _, err := client.PrepareManagedDeploymentJob(t.Context(), deployment, spec, region, 7, targets, material, secret)
		return err
	}
	if err := prepare(items); err == nil || fake.writes != 0 {
		t.Fatalf("missing secret wrote Variable: writes=%d err=%v", fake.writes, err)
	}
	secret.values["API_TOKEN"] = "private-token"
	wrong := map[string]string{DatabaseItemKey("primary"): items[DatabaseItemKey("primary")], DatabaseTargetItemKey("primary"): "different"}
	if err := prepare(wrong); err == nil || fake.writes != 0 {
		t.Fatalf("wrong target wrote Variable: writes=%d err=%v", fake.writes, err)
	}
	job, input, err := client.PrepareManagedDeploymentJob(t.Context(), deployment, spec, region, 7, targets, items, secret)
	if err != nil || job == nil || input.ManagedInputs == nil || *job.ID != input.JobID || fake.writes != 2 {
		t.Fatalf("managed preparation job=%v input=%+v writes=%d err=%v", job != nil, input, fake.writes, err)
	}
	variable := fake.variables[input.ManagedInputs.VariablePath]
	if variable.Items["API_TOKEN"] != "private-token" || variable.Items[DatabaseRevisionItemKey("primary", 7)] != items[DatabaseItemKey("primary")] {
		t.Fatal("prepared job variable lacks exact private material")
	}
}
