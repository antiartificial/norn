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
	"norn/v2/api/nomad"
)

func TestInvokeFunctionRejectsServiceProcessBeforeSubmission(t *testing.T) {
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "demo")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte("name: demo\ndeploy: true\nprocesses:\n  web:\n    port: 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{AppsDir: appsDir}, nomad: &nomad.Client{}}
	router := chi.NewRouter()
	router.Post("/apps/{id}/invoke", h.InvokeFunction)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/apps/demo/invoke", strings.NewReader(`{"process":"web"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not a function") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
