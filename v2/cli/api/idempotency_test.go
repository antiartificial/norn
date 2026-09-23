package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnqueueMethodsSendCallerIdempotencyKey(t *testing.T) {
	tests := []struct {
		name string
		path string
		call func(*Client) error
	}{
		{name: "deploy", path: "/api/apps/app-one/deploy", call: func(c *Client) error { _, err := c.Deploy("app-one", "main", "retry-key"); return err }},
		{name: "preflight", path: "/api/apps/app-one/preflight", call: func(c *Client) error { _, err := c.Preflight("app-one", "main", "retry-key"); return err }},
		{name: "rollback", path: "/api/apps/app-one/rollback", call: func(c *Client) error { _, err := c.Rollback("app-one", "retry-key"); return err }},
		{name: "deploy group", path: "/api/deploy-groups/group-one/deploy", call: func(c *Client) error { _, err := c.RunDeployGroup("group-one", "main", "retry-key"); return err }},
		{name: "webhook replay", path: "/api/webhooks/deliveries/delivery-one/replay", call: func(c *Client) error {
			_, err := c.ReplayWebhookDelivery("delivery-one", "deploy", "retry-key")
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != tt.path {
					t.Fatalf("request=%s %s, want POST %s", r.Method, r.URL.Path, tt.path)
				}
				if got := r.Header.Get("Idempotency-Key"); got != "retry-key" {
					t.Fatalf("Idempotency-Key=%q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"sagaId":"saga-one","deploys":[],"mode":"deploy","app":"app-one"}`)
			}))
			defer server.Close()

			if err := tt.call(New(server.URL)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEnqueueMethodRejectsMissingIdempotencyKeyBeforeTransport(t *testing.T) {
	transportCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		transportCalled = true
	}))
	defer server.Close()

	if _, err := New(server.URL).Deploy("app-one", "main", "  "); err == nil {
		t.Fatal("expected missing idempotency key rejection")
	}
	if transportCalled {
		t.Fatal("transport was called without an idempotency key")
	}
}
