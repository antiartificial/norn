package etcdstore

import (
	"encoding/json"
	"testing"

	"norn/v2/api/database"
	"norn/v2/api/nomad"
)

func TestManagedEffectTargetsMustMatchSignedAcceptance(t *testing.T) {
	jobID, err := nomad.ManagedDeploymentJobID("orders", "west", "deploy-one")
	if err != nil {
		t.Fatal(err)
	}
	target := database.TargetIdentity{ServiceID: "pg", ServiceGeneration: 1, BindingID: "orders-primary", BindingGeneration: 2,
		Engine: database.EnginePostgreSQL, Database: "orders", Role: "orders_app"}
	targetJSON, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	setJSON, err := json.Marshal(map[string]interface{}{"schema": "norn.database-targets/v1", "profileId": "fleet", "catalogRevision": 7,
		"targets": []map[string]interface{}{{"name": "primary", "target": target}}})
	if err != nil {
		t.Fatal(err)
	}
	input := deploymentEffectInput{App: "orders", JobID: jobID, DeploymentID: "deploy-one", Region: "west",
		ManagedInputs: &nomad.ManagedJobInputRequirements{JobID: jobID, VariablePath: nomad.DatabaseVariablePath(jobID), DatabaseRevision: 7,
			RuntimeDatabaseNames: []string{"primary"}}, ExpectedDatabaseTargets: map[string]string{"primary": string(targetJSON)}}
	payload := map[string]interface{}{"databaseTargets": string(setJSON)}
	if err := validateManagedEffectTargets(input, payload); err != nil {
		t.Fatal(err)
	}
	extra, err := json.Marshal(map[string]interface{}{"schema": "norn.database-targets/v1", "profileId": "fleet", "catalogRevision": 7,
		"targets": []map[string]interface{}{{"name": "primary", "target": target}, {"name": "migration-only", "target": target}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManagedEffectTargets(input, map[string]interface{}{"databaseTargets": string(extra)}); err != nil {
		t.Fatalf("additional signed non-runtime target rejected: %v", err)
	}
	input.ExpectedDatabaseTargets["primary"] = `{"serviceId":"other"}`
	if err := validateManagedEffectTargets(input, payload); err == nil {
		t.Fatal("different target accepted")
	}
	input.ExpectedDatabaseTargets["primary"] = string(targetJSON)
	input.ManagedInputs.DatabaseRevision = 8
	if err := validateManagedEffectTargets(input, payload); err == nil {
		t.Fatal("different catalog revision accepted")
	}
	input.ManagedInputs.DatabaseRevision = 7
	if err := validateManagedEffectTargets(input, nil); err == nil {
		t.Fatal("missing signed targets accepted")
	}
}
