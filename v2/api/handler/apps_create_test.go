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
