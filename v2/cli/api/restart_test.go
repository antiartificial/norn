package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRestartSendsIdempotencyKeyAndDecodesOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/apps/demo/restart" || r.Header.Get("Idempotency-Key") != "restart-retry-1" {
			t.Fatalf("path=%s key=%q", r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"operation-1","kind":"app.restart","status":"queued"}`))
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client()}
	operation, err := client.Restart("demo", "restart-retry-1")
	if err != nil || operation.ID != "operation-1" || operation.Kind != "app.restart" {
		t.Fatalf("operation=%+v err=%v", operation, err)
	}
}
