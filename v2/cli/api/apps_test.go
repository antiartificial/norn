package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListAppsUsesVersionedRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/apps" {
			t.Errorf("request = %s %s, want GET /api/v1/apps", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"nomadStatus":"running","healthy":true}]`))
	}))
	defer server.Close()

	apps, err := New(server.URL).ListApps()
	if err != nil {
		t.Fatalf("ListApps(): %v", err)
	}
	if len(apps) != 1 || apps[0].NomadStatus != "running" || !apps[0].Healthy {
		t.Fatalf("ListApps() = %+v, want one healthy running app", apps)
	}
}
