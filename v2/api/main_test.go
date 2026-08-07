package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/handler"
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

func TestBearerAuthProtectsWebSocketsAndEnforcesScopes(t *testing.T) {
	if got := controlScopeForRequest(httptest.NewRequest(http.MethodGet, "/ws", nil)); got != handler.ScopeEventsRead {
		t.Fatalf("/ws scope = %q", got)
	}
	if got := controlScopeForRequest(httptest.NewRequest(http.MethodGet, "/api/apps/demo/exec", nil)); got != handler.ScopeAppsExec {
		t.Fatalf("exec scope = %q", got)
	}
	if publicControlPath("/ws") {
		t.Fatal("/ws must not be public when API token auth is enabled")
	}
	if publicControlPath("/api/apps/demo/exec") {
		t.Fatal("exec must not be public when API token auth is enabled")
	}
}

func TestBearerAuthEnforcesIssuedTokenScopes(t *testing.T) {
	cfg := &config.Config{APIToken: "control-plane-token"}
	h := handler.New(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil)
	issue := httptest.NewRequest(http.MethodPost, "/api/access/tokens", bytes.NewBufferString(`{
		"ttl":"1h","note":"read client","scopes":["api:read","events:read"]
	}`))
	issued := httptest.NewRecorder()
	h.CreateAccessToken(issued, issue)
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue status = %d: %s", issued.Code, issued.Body.String())
	}
	var token struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	authenticated := bearerAuth(cfg.APIToken, h)(next)

	for _, tt := range []struct {
		path string
		want int
	}{
		{path: "/api/apps", want: http.StatusNoContent},
		{path: "/ws", want: http.StatusNoContent},
		{path: "/api/apps/demo/exec", want: http.StatusForbidden},
		{path: "/api/v1/platform/upgrades", want: http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		req.RemoteAddr = "100.64.0.2:1234"
		req.Header.Set("Authorization", "Bearer "+token.Token)
		rec := httptest.NewRecorder()
		authenticated.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("%s status = %d, want %d; body=%s", tt.path, rec.Code, tt.want, rec.Body.String())
		}
	}
}
