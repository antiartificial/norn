package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestFileServerServesRootAndIndexFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>Norn</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	fileServer(r, dir)

	for _, path := range []string{"/", "/nested/route"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
		}
		if rec.Body.String() != "<html>Norn</html>" {
			t.Fatalf("%s body = %q, want index", path, rec.Body.String())
		}
	}
}

func TestBearerAuthAllowsServiceManifestDiscovery(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	authenticated := bearerAuth("control-plane-token", nil)(next)

	tests := []struct {
		name       string
		path       string
		token      string
		wantStatus int
	}{
		{name: "manifest is public", path: "/api/services/manifest", wantStatus: http.StatusNoContent},
		{name: "control endpoint remains protected", path: "/api/apps", wantStatus: http.StatusUnauthorized},
		{name: "control endpoint accepts token", path: "/api/apps", token: "control-plane-token", wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.RemoteAddr = "100.64.0.2:1234"
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			rec := httptest.NewRecorder()
			authenticated.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}
