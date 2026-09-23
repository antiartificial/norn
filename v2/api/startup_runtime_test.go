package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"norn/v2/api/startup"
	"norn/v2/api/store"
)

func TestValidatePassiveBind(t *testing.T) {
	for _, bind := range []string{"127.0.0.1", "::1", "localhost"} {
		if err := validatePassiveBind(bind); err != nil {
			t.Fatalf("loopback %q rejected: %v", bind, err)
		}
	}
	for _, bind := range []string{"", "0.0.0.0", "::", "192.0.2.10", "control.example.test"} {
		if err := validatePassiveBind(bind); err == nil {
			t.Fatalf("non-loopback %q accepted", bind)
		}
	}
}

func TestPassiveHandlerExposesOnlyStatusRoutes(t *testing.T) {
	cfg := startup.Config{SchemaMode: startup.SchemaModeCheck, StartupMode: startup.ModePassive, SchemaTimeout: time.Minute}
	h := newPassiveHandler("v-test", "database-test", store.SchemaStatus{
		CurrentMigrationVersion: 3,
		MinimumReaderVersion:    1,
		MinimumWriterVersion:    2,
	}, cfg)

	for _, path := range []string{"/api/health", "/api/version", "/api/schema"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d: %s", path, rec.Code, rec.Body.String())
		}
	}

	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/api/apps"},
		{http.MethodGet, "/api/v1/capabilities"},
		{http.MethodPost, "/api/apps/demo/deploy"},
		{http.MethodPost, "/api/webhooks/github"},
		{http.MethodPost, "/api/health"},
	} {
		req := httptest.NewRequest(request.method, request.path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d, want rejected", request.method, request.path, rec.Code)
		}
	}
}

func TestDatabaseIdentityRequiresSigningKeyAndBindsExactConfiguration(t *testing.T) {
	key := strings.Repeat("k", 32)
	a := databaseIdentity("postgresql://control-a", key)
	b := databaseIdentity("postgresql://control-b", key)
	if a == "" || a == b || databaseIdentity("postgresql://control-a", "short") != "" {
		t.Fatalf("database identities a=%q b=%q", a, b)
	}
}
