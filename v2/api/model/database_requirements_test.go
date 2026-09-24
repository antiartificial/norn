package model

import (
	"os"
	"strings"
	"testing"
)

// The example app is the counterpart of
// database/testdata/catalog-example.json (logical name "primary").
func TestAppV2ExampleDecodesStrictlyAndValidates(t *testing.T) {
	data, err := os.ReadFile("testdata/app-v2-example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ParseInfraSpecDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	result := ValidateSpec(spec)
	for _, finding := range result.Findings {
		if finding.Severity == "error" {
			t.Fatalf("example is invalid: %+v", finding)
		}
	}
	if !spec.NamedDatabases() || spec.EffectiveMigrationDatabase() != "primary" {
		t.Fatalf("example databases = %+v", spec.Databases)
	}
	if names := spec.DatabaseEnvNames(); names["DATABASE_URL"] != "primary" || names["DATABASE_URL_FILE"] != "primary" {
		t.Fatalf("owned variables = %v", names)
	}
	// Discovery decodes the same document identically.
	discovered, err := ParseInfraSpec(data)
	if err != nil || len(discovered.Databases) != 1 {
		t.Fatalf("discovery = %+v, %v", discovered, err)
	}
}

func TestAppV2UnknownFieldsFailEvenInLenientDiscovery(t *testing.T) {
	document := "schemaVersion: norn.app/v2\nname: shop\nprocesses:\n  web:\n    command: x\ndatabases:\n  - name: primary\n    purpose: application\n    capabilities: [runtime]\n    runtime:\n      evn: DATABASE_URL\n"
	if _, err := ParseInfraSpec([]byte(document)); err == nil {
		t.Fatal("misspelled v2 database field was ignored")
	}
	// v1 discovery keeps its lenient compatibility behaviour.
	if _, err := ParseInfraSpec([]byte("name: legacy\nprocesses:\n  web:\n    command: x\nunknownLegacyField: 1\n")); err != nil {
		t.Fatalf("v1 lenient discovery changed: %v", err)
	}
}

func TestDatabaseDeclarationRejections(t *testing.T) {
	base := func() *InfraSpec {
		return &InfraSpec{SchemaVersion: AppSchemaV2, App: "shop", Processes: map[string]Process{"web": {Command: "x"}, "nightly": {Command: "y", Schedule: "@daily"}},
			Databases:  []DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"runtime", "migration", "snapshot", "restore"}, Runtime: &DatabaseRuntime{Env: "DATABASE_URL", FileEnv: "DATABASE_URL_FILE"}}},
			Migrations: "npm run migrate"}
	}
	if findings := base().DatabaseDeclarationFindings(); len(findings) != 0 {
		t.Fatalf("base spec findings = %+v", findings)
	}
	cases := map[string]struct {
		mutate func(*InfraSpec)
		field  string
	}{
		"unknown schema":         {func(s *InfraSpec) { s.SchemaVersion = "norn.app/v3" }, "schemaVersion"},
		"databases without v2":   {func(s *InfraSpec) { s.SchemaVersion = "" }, "databases"},
		"migrationDb without v2": {func(s *InfraSpec) { s.SchemaVersion = ""; s.Databases = nil; s.MigrationDatabase = "primary" }, "migrationDatabase"},
		"mixed named and legacy": {func(s *InfraSpec) { s.Infrastructure = &Infrastructure{Postgres: &PostgresInfra{Database: "shop"}} }, "infrastructure.postgres"},
		"v2 without databases":   {func(s *InfraSpec) { s.Databases = nil; s.Migrations = "" }, "databases"},
		"bad name":               {func(s *InfraSpec) { s.Databases[0].Name = "Primary" }, "databases[0].name"},
		"duplicate name": {func(s *InfraSpec) {
			s.Databases = append(s.Databases, DatabaseRequirement{Name: "primary", Purpose: "application", Capabilities: []string{"snapshot"}})
		}, "databases[1].name"},
		"control purpose":       {func(s *InfraSpec) { s.Databases[0].Purpose = "control" }, "databases[0].purpose"},
		"unknown capability":    {func(s *InfraSpec) { s.Databases[0].Capabilities = append(s.Databases[0].Capabilities, "replicate") }, "databases[0].capabilities"},
		"restore w/o snapshot":  {func(s *InfraSpec) { s.Databases[0].Capabilities = []string{"runtime", "migration", "restore"} }, "databases[0].capabilities"},
		"runtime w/o block":     {func(s *InfraSpec) { s.Databases[0].Runtime = nil }, "databases[0].runtime"},
		"block w/o runtime cap": {func(s *InfraSpec) { s.Databases[0].Capabilities = []string{"migration", "snapshot"} }, "databases[0].runtime"},
		"empty runtime":         {func(s *InfraSpec) { s.Databases[0].Runtime = &DatabaseRuntime{} }, "databases[0].runtime"},
		"incomplete components": {func(s *InfraSpec) {
			s.Databases[0].Runtime = &DatabaseRuntime{Components: &DatabaseRuntimeComponents{Host: "WORDPRESS_DB_HOST"}}
		}, "databases[0].runtime.components"},
		"component collision": {func(s *InfraSpec) {
			s.Databases[0].Runtime = &DatabaseRuntime{Components: &DatabaseRuntimeComponents{Host: "WORDPRESS_DB_HOST", User: "WORDPRESS_DB_USER", Password: "WORDPRESS_DB_HOST", Name: "WORDPRESS_DB_NAME"}}
		}, "databases[0].runtime.components."},
		"same var for value and path": {func(s *InfraSpec) {
			s.Databases[0].Runtime.FileEnv = "DATABASE_URL"
		}, "databases[0].runtime."},
		"reserved prefix":  {func(s *InfraSpec) { s.Databases[0].Runtime.Env = "NORN_DATABASE_URL" }, "databases[0].runtime.env"},
		"app env conflict": {func(s *InfraSpec) { s.Env = map[string]string{"DATABASE_URL": "postgres://elsewhere"} }, "env.DATABASE_URL"},
		"secret conflict":  {func(s *InfraSpec) { s.Secrets = []string{"database_url"} }, "secrets"},
		"worker env": {func(s *InfraSpec) {
			s.Processes["web"] = Process{Command: "x", Env: map[string]string{"DATABASE_URL_FILE": "/tmp/x"}}
		}, "processes.web.env.DATABASE_URL_FILE"},
		"cron env": {func(s *InfraSpec) {
			s.Processes["nightly"] = Process{Command: "y", Schedule: "@daily", Env: map[string]string{"DATABASE_URL": "x"}}
		}, "processes.nightly.env.DATABASE_URL"},
		"undeclared target": {func(s *InfraSpec) { s.MigrationDatabase = "analytics" }, "migrationDatabase"},
		"ambiguous migration": {func(s *InfraSpec) {
			s.Databases = append(s.Databases, DatabaseRequirement{Name: "analytics", Purpose: "application", Capabilities: []string{"migration"}})
		}, "migrationDatabase"},
		"migration capability": {func(s *InfraSpec) { s.Databases[0].Capabilities = []string{"runtime", "snapshot"} }, "migrationDatabase"},
	}
	for name, test := range cases {
		spec := base()
		test.mutate(spec)
		found := false
		for _, finding := range spec.DatabaseDeclarationFindings() {
			if finding.Severity == "error" && strings.HasPrefix(finding.Field, test.field) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no error on %s in %+v", name, test.field, spec.DatabaseDeclarationFindings())
		}
		if ValidateSpec(spec).Valid {
			t.Errorf("%s: ValidateSpec accepted the spec", name)
		}
	}
}

func TestDatabaseEnvConflictsCoverRuntimeSources(t *testing.T) {
	spec := &InfraSpec{SchemaVersion: AppSchemaV2, Databases: []DatabaseRequirement{{Name: "primary", Runtime: &DatabaseRuntime{Env: "DATABASE_URL", FileEnv: "DATABASE_URL_FILE"}}}}
	conflicts := spec.DatabaseEnvConflicts(map[string]string{"DATABASE_URL": "x", "OTHER": "y"}, map[string]string{"DATABASE_URL_FILE": "z"})
	if strings.Join(conflicts, ",") != "DATABASE_URL,DATABASE_URL_FILE" {
		t.Fatalf("conflicts = %v", conflicts)
	}
	if conflicts := (&InfraSpec{}).DatabaseEnvConflicts(map[string]string{"DATABASE_URL": "x"}); len(conflicts) != 0 {
		t.Fatalf("v1 spec conflicts = %v", conflicts)
	}
}

func TestDatabaseEnvConflictsCoverWordPressComponents(t *testing.T) {
	spec := &InfraSpec{SchemaVersion: AppSchemaV2, Databases: []DatabaseRequirement{{Name: "wordpress", Runtime: &DatabaseRuntime{Components: &DatabaseRuntimeComponents{
		Host: "WORDPRESS_DB_HOST", User: "WORDPRESS_DB_USER", Password: "WORDPRESS_DB_PASSWORD", Name: "WORDPRESS_DB_NAME",
	}}}}}
	conflicts := spec.DatabaseEnvConflicts(map[string]string{"WORDPRESS_DB_PASSWORD": "shadow"}, map[string]string{"WORDPRESS_DB_HOST": "other"})
	if strings.Join(conflicts, ",") != "WORDPRESS_DB_HOST,WORDPRESS_DB_PASSWORD" {
		t.Fatalf("WordPress database variable conflicts = %v", conflicts)
	}
}
