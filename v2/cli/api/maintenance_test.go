package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueueMaintenanceSendsCallerIdempotencyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/platform/upgrades" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "retry-key-123" {
			t.Fatalf("Idempotency-Key=%q", got)
		}
		var request map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["ref"] != "abc123" || request["mode"] != "restart" || request["drainMode"] != "wait" {
			t.Fatalf("request=%v", request)
		}
		_ = json.NewEncoder(w).Encode(Operation{ID: "operation-1", Kind: "platform.upgrade"})
	}))
	defer server.Close()

	client := New(server.URL)
	op, err := client.QueuePlatformUpgrade("abc123", "restart", "wait", "retry-key-123")
	if err != nil {
		t.Fatal(err)
	}
	if op.ID != "operation-1" {
		t.Fatalf("operation=%+v", op)
	}
}
