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

func TestInvokeFunctionRejectsRequestMetadataWhenVariableFilesWithholdRuntimeEnv(t *testing.T) {
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "function-app")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(`
name: function-app
deploy: true
processes:
  handler:
    function: {}
    nomadVariables:
      uid: 65532
      gid: 65532
      files:
        - key: DATABASE_URL
          destination: database-url
`), 0o600); err != nil {
		t.Fatal(err)
	}

	h := &Handler{cfg: &config.Config{AppsDir: appsDir}, nomad: &nomad.Client{}}
	router := chi.NewRouter()
	router.Post("/api/v1/apps/{id}/functions", h.InvokeFunction)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/apps/function-app/functions", strings.NewReader(`{"body":"caller-data"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "function request metadata is unsupported") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
