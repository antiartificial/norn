package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"norn/v2/api/database"
)

func TestVerifySignedDeployedMySQLSourceBinding(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	acceptedStore, db := stores[0], dbs[0]
	ctx := context.Background()
	specDigest := "sha256:" + strings.Repeat("a", 64)
	source := database.TargetIdentity{ServiceID: "mysql-primary", ServiceGeneration: 3, BindingID: "wordpress-primary", BindingGeneration: 5, Engine: database.EngineMySQL, Database: "wordpress", Role: "wordpress_runtime"}
	targetSet := mysqlDeployedTargetSet{Schema: mysqlDeployedDatabaseTargetsSchema, ProfileID: "mini", CatalogRevision: 29,
		Targets: []mysqlDeployedNamedTarget{{Name: "primary", Target: source}}}
	encoded, err := json.Marshal(targetSet)
	if err != nil {
		t.Fatal(err)
	}
	input := newAcceptance(t, acceptedStore, "mysql-deployed-source", "operator", "wordpress", true)
	input.Deployment.SpecDigest = specDigest
	input.Operation.Payload["specDigest"] = specDigest
	input.Operation.Payload["databaseTargets"] = string(encoded)
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := acceptedStore.Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='succeeded',finished_at=clock_timestamp() WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE deployments SET status='deployed',spec_digest=$2,finished_at=clock_timestamp() WHERE id=$1`, accepted.Deployment.ID, specDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE deployment_regions SET status='deployed',active_weight=desired_weight WHERE deployment_id=$1 AND region='west' AND nomad_region='global'`, accepted.Deployment.ID); err != nil {
		t.Fatal(err)
	}
	request := MySQLDeployedSourceBindingRequest{DeploymentID: accepted.Deployment.ID, App: "wordpress", SpecDigest: specDigest,
		Region: "west", NomadRegion: "global", ProfileID: "mini", LogicalID: "primary", CatalogRevision: 29, Source: source}
	binding, err := db.VerifySignedDeployedMySQLSourceBinding(ctx, acceptedStore, request)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256([]byte(mysqlDeployedDatabaseTargetsSchema + "\x00" + string(encoded)))
	if binding.OperationID != accepted.Operation.ID || binding.AcceptanceIntentID != accepted.AcceptanceIntentID || binding.DeploymentID != accepted.Deployment.ID ||
		binding.DatabaseBindingSchema != mysqlDeployedDatabaseTargetsSchema || binding.DatabaseBindingSHA256 != hex.EncodeToString(wantDigest[:]) || binding.Source != source {
		t.Fatalf("binding=%+v", binding)
	}

	tests := []struct {
		name   string
		change func(*MySQLDeployedSourceBindingRequest)
	}{
		{name: "wrong app", change: func(r *MySQLDeployedSourceBindingRequest) { r.App = "another-app" }},
		{name: "wrong logical", change: func(r *MySQLDeployedSourceBindingRequest) { r.LogicalID = "analytics" }},
		{name: "wrong source", change: func(r *MySQLDeployedSourceBindingRequest) { r.Source.Role = "another_role" }},
		{name: "wrong catalog", change: func(r *MySQLDeployedSourceBindingRequest) { r.CatalogRevision++ }},
		{name: "wrong deployment", change: func(r *MySQLDeployedSourceBindingRequest) { r.DeploymentID = uuid.NewString() }},
		{name: "wrong spec", change: func(r *MySQLDeployedSourceBindingRequest) { r.SpecDigest = "sha256:" + strings.Repeat("b", 64) }},
		{name: "wrong region", change: func(r *MySQLDeployedSourceBindingRequest) { r.Region = "east" }},
		{name: "wrong Nomad region", change: func(r *MySQLDeployedSourceBindingRequest) { r.NomadRegion = "other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.change(&changed)
			if _, err := db.VerifySignedDeployedMySQLSourceBinding(ctx, acceptedStore, changed); !errors.Is(err, ErrMySQLDeployedSourceBinding) {
				t.Fatalf("unsafe binding err=%v", err)
			}
		})
	}
}

func TestVerifySignedDeployedMySQLSourceBindingRequiresSuccessfulDeploymentAndRegion(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	acceptedStore, db := stores[0], dbs[0]
	ctx := context.Background()
	specDigest := "sha256:" + strings.Repeat("c", 64)
	source := database.TargetIdentity{ServiceID: "mysql", ServiceGeneration: 1, BindingID: "app", BindingGeneration: 1, Engine: database.EngineMySQL, Database: "app", Role: "app"}
	encoded, _ := json.Marshal(mysqlDeployedTargetSet{Schema: mysqlDeployedDatabaseTargetsSchema, ProfileID: "mini", CatalogRevision: 4, Targets: []mysqlDeployedNamedTarget{{Name: "primary", Target: source}}})
	input := newAcceptance(t, acceptedStore, "mysql-deployed-not-terminal", "operator", "app", true)
	input.Deployment.SpecDigest = specDigest
	input.Operation.Payload["specDigest"], input.Operation.Payload["databaseTargets"] = specDigest, string(encoded)
	input.Fingerprint, _ = CanonicalOperationRequestFingerprint(input)
	accepted, err := acceptedStore.Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	request := MySQLDeployedSourceBindingRequest{DeploymentID: accepted.Deployment.ID, App: "app", SpecDigest: specDigest, Region: "west", NomadRegion: "global", ProfileID: "mini", LogicalID: "primary", CatalogRevision: 4, Source: source}
	if _, err := db.VerifySignedDeployedMySQLSourceBinding(ctx, acceptedStore, request); !errors.Is(err, ErrMySQLDeployedSourceBinding) {
		t.Fatalf("queued deployment accepted: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='succeeded' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE deployments SET status='deployed' WHERE id=$1`, accepted.Deployment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.VerifySignedDeployedMySQLSourceBinding(ctx, acceptedStore, request); !errors.Is(err, ErrMySQLDeployedSourceBinding) {
		t.Fatalf("undeployed region accepted: %v", err)
	}
}
