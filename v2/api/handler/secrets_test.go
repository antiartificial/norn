package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/config"
)

func TestSecretsMigrationPlanReportsPlainEnvKeysWithoutValues(t *testing.T) {
	root := t.TempDir()
	appsDir := filepath.Join(root, "apps")
	appDir := filepath.Join(appsDir, "secret-app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := []byte(`
name: secret-app
deploy: true
secrets:
  - API_KEY
env:
  API_KEY: super-secret-value
  PUBLIC_URL: https://example.test
processes:
  web:
    port: 8080
    env:
      DATABASE_URL: postgres://user:pass@localhost:5432/db
`)
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), spec, 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{AppsDir: appsDir}}
	req := httptest.NewRequest(http.MethodGet, "/api/secrets/migration-plan?app=secret-app", nil)
	rec := httptest.NewRecorder()

	h.SecretsMigrationPlan(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var plan SecretMigrationPlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Count != 2 {
		t.Fatalf("count = %d, want 2: %+v", plan.Count, plan.Items)
	}
	body := rec.Body.String()
	if containsAny(body, "super-secret-value", "user:pass") {
		t.Fatalf("migration plan leaked a secret value: %s", body)
	}
	if plan.Items[0].App != "secret-app" || plan.Items[0].Key == "" || plan.Items[0].Action == "" {
		t.Fatalf("unexpected migration item: %+v", plan.Items[0])
	}
}

func TestSecretsMigrationPlanMissingAppReturns404(t *testing.T) {
	appsDir := t.TempDir()
	h := &Handler{cfg: &config.Config{AppsDir: appsDir}}
	req := httptest.NewRequest(http.MethodGet, "/api/secrets/migration-plan?app=missing", nil)
	rec := httptest.NewRecorder()

	h.SecretsMigrationPlan(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
