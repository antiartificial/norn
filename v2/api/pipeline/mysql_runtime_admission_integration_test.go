package pipeline

import (
	"context"
	"testing"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func qualifiedWordPressRuntimeSpec() *model.InfraSpec {
	return &model.InfraSpec{
		SchemaVersion: model.AppSchemaV2, App: "wordpress", StartupAdapter: model.StartupAdapterWordPressVerifiedTLS,
		Build:     &model.BuildSpec{Image: model.QualifiedWordPressVerifiedTLSImage},
		Processes: map[string]model.Process{"web": {Port: 80}},
		Volumes:   []model.VolumeSpec{{Name: "wordpress-content", Mount: "/var/www/html/wp-content"}},
		Databases: []model.DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"},
			Runtime: &model.DatabaseRuntime{Components: &model.DatabaseRuntimeComponents{
				Host: "WORDPRESS_DB_HOST", User: "WORDPRESS_DB_USER", Password: "WORDPRESS_DB_PASSWORD", Name: "WORDPRESS_DB_NAME",
			}, TLS: &model.DatabaseRuntimeTLS{CAFileEnv: "MYSQL_SSL_CA"}}}},
	}
}

func qualifiedMySQLRuntimeCatalog() database.Catalog {
	return database.Catalog{APIVersion: database.APIVersion,
		Services: []database.DatabaseService{{APIVersion: database.APIVersion, ID: "wp-mysql", Generation: 1,
			Purpose: database.PurposeApplication, Engine: database.EngineMySQL, EngineVersion: "8.4", ProviderRef: "local:test-mysql",
			Endpoint: database.DatabaseEndpoint{Host: "mysql.test.internal", Port: 3306},
			Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSVerifyFull},
			Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilityRuntime}}}},
		Bindings: []database.DatabaseBinding{{APIVersion: database.APIVersion, ID: "wp-primary", ServiceID: "wp-mysql", Database: "wordpress", Role: "wordpress", Generation: 1,
			CredentialRef: "secret:wp/password", TLS: database.DatabaseTLS{Mode: database.TLSVerifyFull, ServerName: "mysql.test.internal", CARef: "secret:wp/ca"}}},
		Profiles: []database.DeploymentProfile{{APIVersion: database.APIVersion, ID: "mini", Topology: database.DeploymentTopologyLocal,
			AvailabilityClass: database.AvailabilitySingleHost, DatabaseBindings: map[string]string{"primary": "wp-primary"}}}}
}

func TestVerifiedMySQLRuntimeAdmissionBindsOnlyQualifiedWordPress(t *testing.T) {
	p, _, _ := acceptancePipelineFixture(t)
	ctx := context.Background()
	catalog := qualifiedMySQLRuntimeCatalog()
	p.DatabaseTargets = &DatabaseTargets{ProfileID: "mini", Catalog: func(context.Context) (store.DatabaseCatalogRevision, error) {
		return store.DatabaseCatalogRevision{Revision: 1, Catalog: catalog}, nil
	}}
	spec := qualifiedWordPressRuntimeSpec()
	if result := model.ValidateSpec(spec); !result.Valid {
		t.Fatalf("qualified WordPress spec invalid: %+v", result.Findings)
	}
	// The baseline kind uses the same named-target binder while permitting an
	// empty disposable control store without a connected Nomad inventory.
	op := model.Operation{Kind: DatabaseBaselineKind, App: spec.App}
	bound, err := p.bindNamedDatabaseTargets(ctx, spec, op)
	if err != nil || bound.Payload[databaseTargetsPayloadKey] == nil {
		t.Fatalf("qualified runtime acceptance=%+v err=%v", bound.Payload, err)
	}
	resolver, err := database.NewResolver(catalog)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "mini", Purpose: database.PurposeApplication,
		LogicalResourceID: "primary", RequiredCapabilities: []database.Capability{database.CapabilityRuntime}})
	if err != nil {
		t.Fatal(err)
	}
	recorded := &recordedTargetSet{Schema: recordedTargetSetSchema, ProfileID: "mini", CatalogRevision: 1,
		Targets: []recordedNamedTarget{{Name: "primary", Target: resolved.Target}}}
	for name, mutate := range map[string]func(*model.InfraSpec){
		"generic consumer": func(s *model.InfraSpec) { s.StartupAdapter = "" },
		"mutable image":    func(s *model.InfraSpec) { s.Build.Image = "wordpress:6.8.2-php8.3-apache" },
		"missing content":  func(s *model.InfraSpec) { s.Volumes = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := qualifiedWordPressRuntimeSpec()
			mutate(changed)
			if _, err := p.bindNamedDatabaseTargets(ctx, changed, op); err == nil {
				t.Fatal("unqualified MySQL runtime was accepted")
			}
			if _, err := p.openNamedTargets(ctx, recorded, changed); err == nil {
				t.Fatal("unqualified MySQL runtime was opened after acceptance")
			}
		})
	}
}
