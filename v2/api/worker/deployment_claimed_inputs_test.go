package worker

import (
	"context"
	"encoding/json"
	"testing"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type claimedDeploymentVerifierFake struct{ accepted store.AcceptedOperation }

func (f claimedDeploymentVerifierFake) VerifyClaimedDeployment(context.Context, model.Operation) (store.AcceptedOperation, error) {
	return f.accepted, nil
}

func TestVerifyClaimedManagedDeploymentSelectsSignedRuntimeTargets(t *testing.T) {
	spec := &model.InfraSpec{App: "orders", Databases: []model.DatabaseRequirement{
		{Name: "primary", Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL"}}, {Name: "archive"},
	}}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	target := database.TargetIdentity{ServiceID: "pg", ServiceGeneration: 1, BindingID: "orders-primary", BindingGeneration: 2,
		Engine: database.EnginePostgreSQL, Database: "orders", Role: "orders_app"}
	set, err := json.Marshal(map[string]interface{}{"schema": "norn.database-targets/v1", "profileId": "fleet", "catalogRevision": 7,
		"targets": []map[string]interface{}{{"name": "primary", "target": target}, {"name": "archive", "target": target}}})
	if err != nil {
		t.Fatal(err)
	}
	claimed := model.Operation{ID: "operation-one", Kind: "app.deploy", App: spec.App, Payload: map[string]interface{}{"deploymentId": "deploy-one", "databaseTargets": string(set)}}
	accepted := store.AcceptedOperation{Operation: claimed, Deployment: &model.Deployment{ID: "deploy-one", App: spec.App, SpecDigest: digest}}
	verified, err := VerifyClaimedManagedDeployment(t.Context(), claimedDeploymentVerifierFake{accepted}, claimed, spec)
	if err != nil || verified.CatalogRevision != 7 || len(verified.RuntimeTargets) != 1 || verified.RuntimeTargets["primary"] != target {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	accepted.Deployment.SpecDigest = "changed"
	if _, err := VerifyClaimedManagedDeployment(t.Context(), claimedDeploymentVerifierFake{accepted}, claimed, spec); err == nil {
		t.Fatal("changed spec accepted")
	}
	accepted.Deployment.SpecDigest = digest
	accepted.Operation.Payload = map[string]interface{}{"deploymentId": "deploy-one"}
	if _, err := VerifyClaimedManagedDeployment(t.Context(), claimedDeploymentVerifierFake{accepted}, claimed, spec); err == nil {
		t.Fatal("missing signed targets accepted")
	}
}
