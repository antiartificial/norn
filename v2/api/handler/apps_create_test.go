package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/config"
	"norn/v2/api/model"
)

func TestCreateAppWritesDisabledDraftAndDeploymentGate(t *testing.T) {
	appsDir := t.TempDir()
	h := &Handler{cfg: &config.Config{AppsDir: appsDir}}
	router := chi.NewRouter()
	router.Post("/api/v1/apps", h.CreateApp)
	router.With(ValidateAppID).Put("/api/v1/apps/{id}/deployment", h.UpdateAppDeployment)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(`{"name":"orders-api","kind":"endpoint","port":9090}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	spec, err := model.LoadInfraSpec(filepath.Join(appsDir, "orders-api", "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Deploy {
		t.Fatal("new app must default deploy false")
	}
	if spec.Processes["web"].Port != 9090 {
		t.Fatalf("port=%d", spec.Processes["web"].Port)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/v1/apps/orders-api/deployment", strings.NewReader(`{"enabled":true}`))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	spec, err = model.LoadInfraSpec(filepath.Join(appsDir, "orders-api", "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !spec.Deploy {
		t.Fatal("deployment gate was not enabled")
	}
	data, _ := os.ReadFile(filepath.Join(appsDir, "orders-api", "infraspec.yaml"))
	if !strings.Contains(string(data), "deploy: true") {
		t.Fatalf("expected explicit deploy key: %s", data)
	}
}

func TestAppDeploymentUpdateRejectsSymlinkedAppDirectory(t *testing.T) {
	appsDir := t.TempDir()
	outside := t.TempDir()
	original := []byte("app: orders-api\ndeploy: false\nprocesses: {}\n")
	if err := os.WriteFile(filepath.Join(outside, "infraspec.yaml"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(appsDir, "orders-api")); err != nil {
		t.Fatal(err)
	}

	h := &Handler{cfg: &config.Config{AppsDir: appsDir}}
	router := chi.NewRouter()
	router.With(ValidateAppID).Put("/api/v1/apps/{id}/deployment", h.UpdateAppDeployment)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/apps/orders-api/deployment", strings.NewReader(`{"enabled":true}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(outside, "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("symlink target was modified: %s", got)
	}
}

func TestAppCatalogReadOnlyRejectsCreateAndDeploymentWithoutChangingFiles(t *testing.T) {
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "orders-api")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("name: orders-api\ndeploy: false\nprocesses: {}\n")
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), original, 0o640); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{AppsDir: appsDir, AppCatalogReadOnly: true}}
	router := chi.NewRouter()
	router.Post("/api/v1/apps", h.CreateApp)
	router.With(ValidateAppID).Put("/api/v1/apps/{id}/deployment", h.UpdateAppDeployment)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(`{"name":"new-app"}`)),
		httptest.NewRequest(http.MethodPut, "/api/v1/apps/orders-api/deployment", strings.NewReader(`{"enabled":true}`)),
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, request)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "app_catalog_read_only") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(appsDir, "new-app")); !os.IsNotExist(err) {
		t.Fatalf("catalog create changed filesystem: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(appDir, "infraspec.yaml"))
	if err != nil || string(got) != string(original) {
		t.Fatalf("catalog deployment mutation changed file: %q err=%v", got, err)
	}
}
