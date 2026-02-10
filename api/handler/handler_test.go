package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"norn/api/config"
	"norn/api/hub"
)

// newTestHandler creates a handler with no DB and no K8s (both nil).
// Useful for testing endpoints that only need app discovery.
func newTestHandler(t *testing.T, appsDir string) *Handler {
	t.Helper()
	ws := hub.New()
	go ws.Run()
	cfg := &config.Config{
		Port:    "0",
		AppsDir: appsDir,
	}
	return &Handler{
		db:   nil,
		kube: nil,
		ws:   ws,
		cfg:  cfg,
	}
}

func writeInfraSpec(t *testing.T, dir, app, content string) {
	t.Helper()
	appDir := filepath.Join(dir, app)
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverApps(t *testing.T) {
	dir := t.TempDir()
	writeInfraSpec(t, dir, "app-a", "app: app-a\nrole: webserver\nport: 3000\n")
	writeInfraSpec(t, dir, "app-b", "app: app-b\nrole: worker\n")
	// app-c has no infraspec — should be skipped
	os.MkdirAll(filepath.Join(dir, "app-c"), 0755)

	h := newTestHandler(t, dir)
	specs, err := h.discoverApps()
	if err != nil {
		t.Fatalf("discoverApps: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("got %d apps, want 2", len(specs))
	}

	names := map[string]bool{}
	for _, s := range specs {
		names[s.App] = true
	}
	if !names["app-a"] || !names["app-b"] {
		t.Errorf("expected app-a and app-b, got %v", names)
	}
}

func TestDiscoverApps_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandler(t, dir)
	specs, err := h.discoverApps()
	if err != nil {
		t.Fatalf("discoverApps: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("got %d apps, want 0", len(specs))
	}
}

func TestLoadSpec(t *testing.T) {
	dir := t.TempDir()
	writeInfraSpec(t, dir, "my-app", "app: my-app\nrole: cron\n")
	h := newTestHandler(t, dir)

	spec, err := h.loadSpec("my-app")
	if err != nil {
		t.Fatalf("loadSpec: %v", err)
	}
	if spec.App != "my-app" {
		t.Errorf("App = %q", spec.App)
	}
	if spec.Role != "cron" {
		t.Errorf("Role = %q", spec.Role)
	}
}

func TestLoadSpec_NotFound(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandler(t, dir)

	_, err := h.loadSpec("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent app")
	}
}

func TestHealthEndpoint_NoK8s(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandler(t, dir)

	// Health endpoint needs db for postgres check — without db it will panic.
	// We're testing that the K8s check returns "unknown" when kube is nil.
	// To avoid the postgres panic, we test checkKubernetes directly.
	result := h.checkKubernetes(nil)
	if result.Status != "unknown" {
		t.Errorf("expected kubernetes status 'unknown' when kube is nil, got %q", result.Status)
	}
	if result.Details != "not configured" {
		t.Errorf("expected details 'not configured', got %q", result.Details)
	}
}

func TestK8sRequiredEndpoints_Return503(t *testing.T) {
	dir := t.TempDir()
	writeInfraSpec(t, dir, "test-app", "app: test-app\nrole: webserver\n")
	h := newTestHandler(t, dir)

	tests := []struct {
		name    string
		method  string
		path    string
		handler http.HandlerFunc
	}{
		{"StreamLogs", "GET", "/api/apps/test-app/logs", h.StreamLogs},
		{"Restart", "POST", "/api/apps/test-app/restart", h.Restart},
		{"Deploy", "POST", "/api/apps/test-app/deploy", h.Deploy},
		{"Rollback", "POST", "/api/apps/test-app/rollback", h.Rollback},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := chi.NewRouter()
			switch tt.method {
			case "GET":
				r.Get("/api/apps/{id}/logs", tt.handler)
			case "POST":
				r.Post("/api/apps/{id}/"+filepath.Base(tt.path), tt.handler)
			}

			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s: status = %d, want 503", tt.name, w.Code)
			}
		})
	}
}

func TestFormatReady(t *testing.T) {
	tests := []struct {
		ready, total int
		want         string
	}{
		{0, 0, "0/0"},
		{1, 3, "1/3"},
		{3, 3, "3/3"},
	}
	for _, tt := range tests {
		got := formatReady(tt.ready, tt.total)
		if got != tt.want {
			t.Errorf("formatReady(%d, %d) = %q, want %q", tt.ready, tt.total, got, tt.want)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, map[string]string{"hello": "world"})

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", w.Header().Get("Content-Type"))
	}

	var result map[string]string
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["hello"] != "world" {
		t.Errorf("body = %v", result)
	}
}
