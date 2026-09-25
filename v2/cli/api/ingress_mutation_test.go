package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIngressMutationsSendRetryKeyAndDecodeQueueReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		invoke     func(*Client) (*Operation, error)
	}{
		{"forge", "/api/apps/demo/forge", func(c *Client) (*Operation, error) { return c.Forge("demo", "retry-key") }},
		{"teardown", "/api/apps/demo/teardown", func(c *Client) (*Operation, error) { return c.Teardown("demo", "retry-key") }},
		{"toggle", "/api/apps/demo/endpoints/toggle", func(c *Client) (*Operation, error) {
			return c.ToggleEndpoint("demo", "demo.example.com", true, "retry-key")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != tc.path || r.Header.Get("Idempotency-Key") != "retry-key" {
					t.Fatalf("method=%s path=%s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"id":"operation-1","kind":"app.cloudflared-mutate","status":"queued"}`))
			}))
			defer server.Close()
			op, err := tc.invoke(New(server.URL))
			if err != nil || op == nil || op.ID != "operation-1" || op.Status != "queued" {
				t.Fatalf("op=%+v err=%v", op, err)
			}
		})
	}
}

func TestIngressMutationRejectsMissingRetryKey(t *testing.T) {
	c := New("http://unused")
	if _, err := c.Forge("demo", ""); err == nil {
		t.Fatal("forge accepted missing key")
	}
	if _, err := c.Teardown("demo", ""); err == nil {
		t.Fatal("teardown accepted missing key")
	}
	if _, err := c.ToggleEndpoint("demo", "demo.example.com", true, ""); err == nil {
		t.Fatal("toggle accepted missing key")
	}
}
