package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
)

func TestIngressQueueReceiptDoesNotClaimCompletion(t *testing.T) {
	before := ingressWait
	ingressWait = false
	t.Cleanup(func() { ingressWait = before })
	command := &cobra.Command{}
	var output bytes.Buffer
	command.SetOut(&output)
	if err := handleIngressOperation(command, &api.Operation{ID: "op-1", Kind: "app.cloudflared-mutate", Status: "queued"}, "cloudflared forge"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "queued as operation op-1") || strings.Contains(output.String(), "completed") {
		t.Fatalf("output=%q", output.String())
	}
}

func TestIngressTerminalFailureAndNoOpAreReportedExactly(t *testing.T) {
	command := &cobra.Command{}
	var output bytes.Buffer
	command.SetOut(&output)
	if err := handleIngressOperation(command, &api.Operation{ID: "op-1", Status: "failed", Message: "restart failed"}, "cloudflared forge"); err == nil || !strings.Contains(err.Error(), "restart failed") {
		t.Fatalf("error=%v", err)
	}
	if err := handleIngressOperation(command, &api.Operation{Status: "unchanged"}, "cloudflared teardown"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "unchanged") {
		t.Fatalf("output=%q", output.String())
	}
}

func TestForgeCommandPrintsRetryKeyAndQueuedOperation(t *testing.T) {
	previousClient, previousKey, previousWait := client, ingressIdempotencyKey, ingressWait
	t.Cleanup(func() {
		client, ingressIdempotencyKey, ingressWait = previousClient, previousKey, previousWait
		forgeCmd.SetOut(os.Stdout)
		forgeCmd.SetErr(os.Stderr)
	})
	var sentKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sentKey = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"op-1","kind":"app.cloudflared-mutate","status":"queued"}`))
	}))
	defer server.Close()
	client = api.New(server.URL)
	ingressIdempotencyKey = ""
	ingressWait = false
	var output, stderr bytes.Buffer
	forgeCmd.SetOut(&output)
	forgeCmd.SetErr(&stderr)
	if err := forgeCmd.RunE(forgeCmd, []string{"demo"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sentKey, "norn-forge-") || !strings.Contains(stderr.String(), "idempotency key: "+sentKey) || !strings.Contains(output.String(), "queued as operation op-1") || strings.Contains(output.String(), "configured") {
		t.Fatalf("key=%q stderr=%q output=%q", sentKey, stderr.String(), output.String())
	}
}
