package model

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInfraSpec(t *testing.T) {
	dir := t.TempDir()
	specFile := filepath.Join(dir, "infraspec.yaml")

	yaml := `app: test-app
role: webserver
port: 3000
healthcheck: /health
hosts:
  external: test.example.com
  internal: test-app-service
build:
  dockerfile: Dockerfile
  test: npm test
services:
  postgres:
    database: test_db
  kv:
    namespace: test-app
  events:
    topics: [user.created, user.updated]
secrets:
  - DATABASE_URL
  - API_KEY
migrations:
  command: npm run db:migrate
  database: test_db
artifacts:
  retain: 3
`
	if err := os.WriteFile(specFile, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	spec, err := LoadInfraSpec(specFile)
	if err != nil {
		t.Fatalf("LoadInfraSpec: %v", err)
	}

	if spec.App != "test-app" {
		t.Errorf("App = %q, want %q", spec.App, "test-app")
	}
	if spec.Role != "webserver" {
		t.Errorf("Role = %q, want %q", spec.Role, "webserver")
	}
	if spec.Port != 3000 {
		t.Errorf("Port = %d, want %d", spec.Port, 3000)
	}
	if spec.Healthcheck != "/health" {
		t.Errorf("Healthcheck = %q, want %q", spec.Healthcheck, "/health")
	}

	// Hosts
	if spec.Hosts == nil {
		t.Fatal("Hosts is nil")
	}
	if spec.Hosts.External != "test.example.com" {
		t.Errorf("Hosts.External = %q", spec.Hosts.External)
	}
	if spec.Hosts.Internal != "test-app-service" {
		t.Errorf("Hosts.Internal = %q", spec.Hosts.Internal)
	}

	// Build
	if spec.Build == nil {
		t.Fatal("Build is nil")
	}
	if spec.Build.Dockerfile != "Dockerfile" {
		t.Errorf("Build.Dockerfile = %q", spec.Build.Dockerfile)
	}
	if spec.Build.Test != "npm test" {
		t.Errorf("Build.Test = %q", spec.Build.Test)
	}

	// Services
	if spec.Services == nil {
		t.Fatal("Services is nil")
	}
	if spec.Services.Postgres == nil || spec.Services.Postgres.Database != "test_db" {
		t.Errorf("Services.Postgres = %v", spec.Services.Postgres)
	}
	if spec.Services.KV == nil || spec.Services.KV.Namespace != "test-app" {
		t.Errorf("Services.KV = %v", spec.Services.KV)
	}
	if spec.Services.Events == nil || len(spec.Services.Events.Topics) != 2 {
		t.Errorf("Services.Events = %v", spec.Services.Events)
	}

	// Secrets
	if len(spec.Secrets) != 2 {
		t.Errorf("Secrets = %v, want 2 items", spec.Secrets)
	}

	// Migrations
	if spec.Migrations == nil || spec.Migrations.Command != "npm run db:migrate" {
		t.Errorf("Migrations = %v", spec.Migrations)
	}

	// Artifacts
	if spec.Artifacts == nil || spec.Artifacts.Retain != 3 {
		t.Errorf("Artifacts.Retain = %v, want 3", spec.Artifacts)
	}
}

func TestLoadInfraSpec_DefaultArtifacts(t *testing.T) {
	dir := t.TempDir()
	specFile := filepath.Join(dir, "infraspec.yaml")

	yaml := `app: minimal
role: worker
`
	if err := os.WriteFile(specFile, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	spec, err := LoadInfraSpec(specFile)
	if err != nil {
		t.Fatalf("LoadInfraSpec: %v", err)
	}

	if spec.App != "minimal" {
		t.Errorf("App = %q", spec.App)
	}
	if spec.Artifacts == nil {
		t.Fatal("Artifacts should default to non-nil")
	}
	if spec.Artifacts.Retain != 5 {
		t.Errorf("Artifacts.Retain = %d, want default 5", spec.Artifacts.Retain)
	}
}

func TestLoadInfraSpec_FileNotFound(t *testing.T) {
	_, err := LoadInfraSpec("/nonexistent/infraspec.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadInfraSpec_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	specFile := filepath.Join(dir, "infraspec.yaml")

	if err := os.WriteFile(specFile, []byte("not: [valid: yaml: {broken"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadInfraSpec(specFile)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}
