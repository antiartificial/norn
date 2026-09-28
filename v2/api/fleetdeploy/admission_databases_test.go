package fleetdeploy

import (
	"encoding/json"
	"strings"
	"testing"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestBindFirstFleetDeploymentDatabasesUsesCatalogIdentitiesOnly(t *testing.T) {
	catalog := database.Catalog{APIVersion: database.APIVersion,
		Services: []database.DatabaseService{{APIVersion: database.APIVersion, ID: "pg", Generation: 1, Purpose: database.PurposeApplication,
			Engine: database.EnginePostgreSQL, EngineVersion: "16", ProviderRef: "local:pg", Endpoint: database.DatabaseEndpoint{Host: "127.0.0.1", Port: 5432},
			Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled}, Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilityRuntime}}}},
		Bindings: []database.DatabaseBinding{{APIVersion: database.APIVersion, ID: "primary", ServiceID: "pg", Database: "app", Role: "app", Generation: 1,
			CredentialRef: "secret:app/db", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}}},
		Profiles: []database.DeploymentProfile{{APIVersion: database.APIVersion, ID: "local", Topology: database.DeploymentTopologyLocal,
			AvailabilityClass: database.AvailabilitySingleHost, DatabaseBindings: map[string]string{"primary": "primary"}}}}
	spec := &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: "pilot", Databases: []model.DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"}, Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL"}}}}
	active := store.DatabaseCatalogRevision{Revision: 3, Catalog: catalog}
	encoded, err := BindFirstFleetDeploymentDatabases(spec, "local", active)
	if err != nil || strings.Contains(encoded, "secret:") {
		t.Fatalf("bound target=%q err=%v", encoded, err)
	}
	var bound struct {
		Schema          string `json:"schema"`
		ProfileID       string `json:"profileId"`
		CatalogRevision int64  `json:"catalogRevision"`
		Targets         []struct {
			Name   string                  `json:"name"`
			Target database.TargetIdentity `json:"target"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(encoded), &bound); err != nil || bound.Schema != "norn.database-targets/v1" || bound.ProfileID != "local" || bound.CatalogRevision != 3 || len(bound.Targets) != 1 || bound.Targets[0].Name != "primary" || bound.Targets[0].Target.BindingID != "primary" {
		t.Fatalf("signed database set=%+v err=%v", bound, err)
	}
	if _, err := BindFirstFleetDeploymentDatabases(spec, "missing", active); err == nil {
		t.Fatal("unknown profile selected a target")
	}
	if _, err := BindFirstFleetDeploymentDatabases(spec, "local", store.DatabaseCatalogRevision{Catalog: catalog}); err == nil {
		t.Fatal("unrevisioned catalog selected a target")
	}
	migration := *spec
	migration.Databases = append([]model.DatabaseRequirement(nil), spec.Databases...)
	migration.Databases[0].Capabilities = []string{"runtime", "migration"}
	if _, err := BindFirstFleetDeploymentDatabases(&migration, "local", active); err == nil {
		t.Fatal("first-route binder admitted an unexecuted migration")
	}
	legacy := &model.InfraSpec{App: "pilot", Infrastructure: &model.Infrastructure{Postgres: &model.PostgresInfra{Database: "app"}}}
	if _, err := BindFirstFleetDeploymentDatabases(legacy, "local", active); err == nil {
		t.Fatal("first-route binder admitted ambient legacy PostgreSQL")
	}
	noDatabaseMigration := &model.InfraSpec{App: "pilot", Migrations: "./migrate"}
	if _, err := BindFirstFleetDeploymentDatabases(noDatabaseMigration, "local", active); err == nil {
		t.Fatal("first-route binder admitted a legacy migration with no named database")
	}
}
