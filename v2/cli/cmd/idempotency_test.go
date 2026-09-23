package cmd

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"norn/v2/cli/api"
)

func TestDeployCommandPrintsGeneratedKeyBeforeAmbiguousTransportFailure(t *testing.T) {
	originalClient, originalKey := client, deployIdempotencyKey
	t.Cleanup(func() {
		client = originalClient
		deployIdempotencyKey = originalKey
	})

	var seenKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenKey = r.Header.Get("Idempotency-Key")
		http.Error(w, "acceptance outcome unknown", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client = api.New(server.URL)
	deployIdempotencyKey = ""
	var stderr bytes.Buffer
	deployCmd.SetErr(&stderr)

	if err := deployCmd.RunE(deployCmd, []string{"app-one", "main"}); err == nil {
		t.Fatal("expected transport response failure")
	}
	if seenKey == "" || !strings.HasPrefix(seenKey, "norn-deploy-") {
		t.Fatalf("generated request key=%q", seenKey)
	}
	if got := stderr.String(); !strings.Contains(got, "idempotency key: "+seenKey) {
		t.Fatalf("stderr=%q does not expose sent key %q", got, seenKey)
	}
}

func TestDeployCommandTerminalSameKeyReplayReturnsImmediately(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		wantErr bool
	}{
		{name: "succeeded", status: "succeeded"},
		{name: "failed", status: "failed", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalClient, originalKey := client, deployIdempotencyKey
			t.Cleanup(func() {
				client = originalClient
				deployIdempotencyKey = originalKey
			})

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if got := r.Header.Get("Idempotency-Key"); got != "same-retry-key" {
					t.Fatalf("Idempotency-Key=%q", got)
				}
				_, _ = fmt.Fprintf(w, `{"sagaId":"saga-one","operationId":"operation-one","status":%q,"replayed":true}`, tt.status)
			}))
			defer server.Close()
			client = api.New(server.URL)
			deployIdempotencyKey = "same-retry-key"
			deployCmd.SetErr(&bytes.Buffer{})

			for range 2 {
				err := deployCmd.RunE(deployCmd, []string{"app-one", "main"})
				if (err != nil) != tt.wantErr {
					t.Fatalf("RunE error=%v, wantErr=%v", err, tt.wantErr)
				}
			}
			if requests != 2 {
				t.Fatalf("requests=%d; terminal replay should not poll or stream", requests)
			}
		})
	}
}

func TestDeployGroupPartialFailurePrintsEveryMemberAndFails(t *testing.T) {
	result := &api.DeployGroupResult{Deploys: []api.GroupDeploy{
		{App: "app-one", SagaID: "saga-one", OperationID: "operation-one", Replayed: true},
		{App: "app-two", Error: "app app-two not found"},
	}}
	var output bytes.Buffer
	err := writeDeployGroupResult(&output, result)
	if err == nil || !strings.Contains(err.Error(), "same idempotency key") {
		t.Fatalf("error=%v", err)
	}
	for _, expected := range []string{"app-one", "replayed", "app-two", "not found"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output=%q missing %q", output.String(), expected)
		}
	}
}

func TestDurablePollingObservesTerminalOperationWithoutSagaTerminalEvent(t *testing.T) {
	tests := []struct {
		status  string
		wantErr bool
	}{
		{status: "succeeded"},
		{status: "failed", wantErr: true},
		{status: "canceled", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			originalClient := client
			t.Cleanup(func() { client = originalClient })
			operationReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/operations/operation-one":
					operationReads++
					status := "running"
					if operationReads > 1 {
						status = tt.status
					}
					_, _ = fmt.Fprintf(w, `{"id":"operation-one","status":%q,"message":"terminal result"}`, status)
				case "/api/saga/saga-one":
					_, _ = fmt.Fprint(w, `[]`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client = api.New(server.URL)

			err := streamViaDurablePolling("operation-one", "saga-one", 0)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v, wantErr=%v", err, tt.wantErr)
			}
			if operationReads != 2 {
				t.Fatalf("operation reads=%d, want 2", operationReads)
			}
		})
	}
}

func TestDeployCommandReusesExplicitKeyAcrossAmbiguousRetries(t *testing.T) {
	originalClient, originalKey := client, deployIdempotencyKey
	t.Cleanup(func() {
		client = originalClient
		deployIdempotencyKey = originalKey
	})

	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Idempotency-Key"))
		http.Error(w, "acceptance outcome unknown", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client = api.New(server.URL)
	deployIdempotencyKey = "operator-retry-key"
	var stderr bytes.Buffer
	deployCmd.SetErr(&stderr)

	for range 2 {
		if err := deployCmd.RunE(deployCmd, []string{"app-one", "main"}); err == nil {
			t.Fatal("expected transport response failure")
		}
	}
	if len(seen) != 2 || seen[0] != "operator-retry-key" || seen[1] != seen[0] {
		t.Fatalf("sent keys=%v", seen)
	}
	if strings.Count(stderr.String(), "idempotency key: operator-retry-key") != 2 {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestFleetPlanCommandPrintsAndSendsGeneratedKey(t *testing.T) {
	originalClient, originalKey := client, fleetIdempotencyKey
	t.Cleanup(func() {
		client = originalClient
		fleetIdempotencyKey = originalKey
	})
	var seenKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenKey = r.Header.Get("Idempotency-Key")
		http.Error(w, "acceptance outcome unknown", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client = api.New(server.URL)
	fleetIdempotencyKey = ""
	var stderr bytes.Buffer
	fleetPlanCmd.SetErr(&stderr)

	if err := fleetPlanCmd.RunE(fleetPlanCmd, []string{"app"}); err == nil {
		t.Fatal("expected transport response failure")
	}
	if seenKey == "" || !strings.HasPrefix(seenKey, "norn-fleet-plan-") {
		t.Fatalf("generated fleet plan key=%q", seenKey)
	}
	if !strings.Contains(stderr.String(), "idempotency key: "+seenKey) {
		t.Fatalf("stderr=%q does not expose sent key %q", stderr.String(), seenKey)
	}
}
