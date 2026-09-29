package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/model"
)

// A legacy migration (no database profile) sees only process basics and
// PGDATABASE naming its declared database; nothing of the API process's
// control-plane environment.
func TestLegacyMigrationDoesNotInheritControlCredentials(t *testing.T) {
	canaries := map[string]string{
		"NORN_DATABASE_URL":     "postgres://norn:CONTROL_DSN_CANARY@control/norn",
		"NORN_API_TOKEN":        "API_TOKEN_CANARY",
		"NOMAD_TOKEN":           "NOMAD_TOKEN_CANARY",
		"CONSUL_HTTP_TOKEN":     "CONSUL_TOKEN_CANARY",
		"SOPS_AGE_KEY_FILE":     "/secret/SOPS_KEY_CANARY",
		"AWS_SECRET_ACCESS_KEY": "AWS_KEY_CANARY",
		"GITHUB_TOKEN":          "GITHUB_TOKEN_CANARY",
		"PGHOST":                "CONTROL_HOST_CANARY",
		"PGPASSWORD":            "CONTROL_PASSWORD_CANARY",
		"PGSERVICE":             "CONTROL_SERVICE_CANARY",
		"PGDATABASE":            "CONTROL_DB_CANARY",
		"DATABASE_URL":          "postgres://CONTROL_URL_CANARY/norn",
	}
	for name, value := range canaries {
		t.Setenv(name, value)
	}
	out := filepath.Join(t.TempDir(), "env.txt")
	spec := &model.InfraSpec{App: "legacy", Migrations: "env > " + out,
		Infrastructure: &model.Infrastructure{Postgres: &model.PostgresInfra{Database: "legacy_db"}}}
	p := &Pipeline{}
	if err := p.migrate(context.Background(), &state{spec: spec, databaseOpened: true}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	environment := string(data)
	for name, value := range canaries {
		if strings.Contains(environment, value) || (name != "PGDATABASE" && strings.Contains(environment, name+"=")) {
			t.Fatalf("legacy migration inherited %s", name)
		}
	}
	for _, kept := range []string{"PGDATABASE=legacy_db", "PATH="} {
		if !strings.Contains(environment, kept) {
			t.Fatalf("legacy migration lost %s", kept)
		}
	}
}

// Nomad job IDs are private variable namespaces only if no two apps share
// one: app "a-b" collides with process "b" of app "a".
func TestDeliveryRefusesCollidingJobIDs(t *testing.T) {
	appsDir := t.TempDir()
	write := func(app, spec string) {
		if err := os.MkdirAll(filepath.Join(appsDir, app), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(appsDir, app, "infraspec.yaml"), []byte(spec), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("shop", "name: shop\nprocesses:\n  web:\n    command: x\n  nightly:\n    command: y\n    schedule: \"@daily\"\n")
	write("shop-nightly", "name: shop-nightly\nprocesses:\n  web:\n    command: x\n")
	write("other", "name: other\nprocesses:\n  web:\n    command: x\n")
	p := &Pipeline{AppsDir: appsDir}
	shop, _ := p.findSpec("shop")
	colliding, _ := p.findSpec("shop-nightly")
	other, _ := p.findSpec("other")
	for _, spec := range []*model.InfraSpec{shop, colliding} {
		err := p.requireDistinctJobIDs(spec)
		if _, ok := err.(*DatabaseTargetError); !ok || !strings.Contains(err.Error(), "collides") {
			t.Fatalf("%s collision = %v", spec.App, err)
		}
	}
	if err := p.requireDistinctJobIDs(other); err != nil {
		t.Fatalf("distinct app = %v", err)
	}
}
