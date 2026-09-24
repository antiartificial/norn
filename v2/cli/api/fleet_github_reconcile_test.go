package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReconcileFleetGitHubUsesOperatorEndpointAndDecodesQueuedOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/fleet/plans/plan-1/github/reconcile" {
			t.Fatalf("request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["kind"] != "pull-request" {
			t.Fatalf("body=%v err=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"outcome":"ambiguous","operation":{"id":"op-1","status":"queued"}}`))
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client()}
	result, err := client.ReconcileFleetGitHub("plan-1", "pull-request")
	if err != nil || result.Outcome != "ambiguous" || result.Operation.ID != "op-1" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
