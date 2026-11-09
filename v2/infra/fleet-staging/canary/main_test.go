package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCanaryReportsPlacementAndStableProbe(t *testing.T) {
	id := identity{Version: "source-sha", Node: "app-03", Allocation: "alloc-123"}
	handler := routes(id)
	probe := httptest.NewRecorder()
	handler.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/route-proof", nil))
	if probe.Code != http.StatusOK || probe.Body.String() != "norn-fleet-canary\n" {
		t.Fatalf("route proof: status=%d body=%q", probe.Code, probe.Body.String())
	}
	if probe.Header().Get("X-Norn-Node") != "app-03" || probe.Header().Get("X-Norn-Allocation") != "alloc-123" {
		t.Fatalf("route proof lost placement identity: %v", probe.Header())
	}
	version := httptest.NewRecorder()
	handler.ServeHTTP(version, httptest.NewRequest(http.MethodGet, "/version", nil))
	var got identity
	if err := json.Unmarshal(version.Body.Bytes(), &got); err != nil || got != id {
		t.Fatalf("version identity: %+v, %v", got, err)
	}
	if version.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("version response may be cached: %v", version.Header())
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d", path, response.Code)
		}
	}
}
