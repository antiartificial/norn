package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/config"
)

const (
	crossClientValidBearer      = "norn-cross-client-valid-bearer"
	crossClientWrongBearer      = "norn-cross-client-wrong-bearer"
	crossClientCoordinationFile = "/tmp/norn-cross-client-capabilities.json"
)

// TestCrossClientCapabilitiesHarness serves the actual capability handler over
// a loopback-only ephemeral port. Set NORN_CROSS_CLIENT_SWIFT_TEST_COMMAND to
// run the opt-in NornUI test while this server is alive; its process receives
// the URL and both test-only bearer values through the environment.
func TestCrossClientCapabilitiesHarness(t *testing.T) {
	cfg := &config.Config{
		Environment:                      "staging",
		Profile:                          "staging",
		ReleaseAdmissionMode:             "attested",
		ReleaseAttestationTrustMode:      "norn-signed-private",
		QualificationSigningKey:          "test-signing-key",
		GitHubActionsOIDCAudience:        "norn-staging",
		GitHubActionsReleaseBindings:     []string{"demo=acme/demo@101@202"},
		GitHubActionsAllowedWorkflowRefs: []string{"acme/demo/.github/workflows/release.yml@sha"},
		GitHubActionsAllowedRefs:         []string{"refs/heads/main"},
		GitHubActionsAllowedEvents:       []string{"push"},
		GitHubActionsAllowedEnvironments: []string{"staging"},
		GitHubActionsDefaultBranch:       "main",
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	server := &http.Server{Handler: bearerAuth(crossClientValidBearer, nil, true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/capabilities" {
			http.NotFound(w, r)
			return
		}
		writeControlCapabilitiesForConfig(cfg, w, r)
	}))}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	baseURL := "http://" + listener.Addr().String()
	writeCrossClientCoordinationFile(t, baseURL)
	capabilities := fetchCrossClientCapabilities(t, baseURL, crossClientValidBearer)
	if capabilities.Auth.Principal == nil || !capabilities.Auth.Principal.Authenticated || capabilities.Auth.Principal.Subject != "control-plane" || !containsCapability(capabilities.Auth.Principal.Scopes, "admin") {
		t.Fatalf("valid bearer principal = %#v, want authenticated control-plane admin", capabilities.Auth.Principal)
	}
	for _, feature := range []string{"norn-signed-private-v1", "release-promotions-v1", "event-cursor-replay", "fleet-v1", "fleet-inventory", "durable-fleet-capacity-plans"} {
		if !containsCapability(capabilities.Features, feature) {
			t.Fatalf("configured capability %q missing from %v", feature, capabilities.Features)
		}
	}
	for _, endpoint := range []string{"releasePromotions", "events", "fleetNodePools", "fleetPlans"} {
		if capabilities.Endpoints[endpoint] == "" {
			t.Fatalf("configured endpoint %q missing from %v", endpoint, capabilities.Endpoints)
		}
	}

	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+crossClientWrongBearer)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("wrong bearer status = %d, want 401; body=%s", response.StatusCode, body)
	}

	command := strings.TrimSpace(os.Getenv("NORN_CROSS_CLIENT_SWIFT_TEST_COMMAND"))
	if command == "" {
		t.Log("NORN_CROSS_CLIENT_SWIFT_TEST_COMMAND is unset; Go loopback contract verified without launching the opt-in Swift client")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(),
		"NORN_CROSS_CLIENT_BASE_URL="+baseURL,
		"NORN_CROSS_CLIENT_VALID_BEARER="+crossClientValidBearer,
		"NORN_CROSS_CLIENT_WRONG_BEARER="+crossClientWrongBearer,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("NornUI cross-client test failed: %v\n%s", err, output)
	}
}

func writeCrossClientCoordinationFile(t *testing.T, baseURL string) {
	t.Helper()
	file, err := os.OpenFile(crossClientCoordinationFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create exclusive cross-client coordination file %s: %v", crossClientCoordinationFile, err)
	}
	t.Cleanup(func() { _ = os.Remove(crossClientCoordinationFile) })
	if filepath.Dir(crossClientCoordinationFile) != "/tmp" {
		t.Fatalf("cross-client coordination file escaped tmp: %s", crossClientCoordinationFile)
	}
	if err := json.NewEncoder(file).Encode(struct {
		BaseURL     string `json:"baseURL"`
		ValidBearer string `json:"validBearer"`
		WrongBearer string `json:"wrongBearer"`
	}{baseURL, crossClientValidBearer, crossClientWrongBearer}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

type crossClientCapabilities struct {
	Features  []string          `json:"features"`
	Endpoints map[string]string `json:"endpoints"`
	Auth      struct {
		Principal *struct {
			Authenticated bool     `json:"authenticated"`
			Subject       string   `json:"subject"`
			Scopes        []string `json:"scopes"`
		} `json:"principal"`
	} `json:"auth"`
}

func fetchCrossClientCapabilities(t *testing.T, baseURL, bearer string) crossClientCapabilities {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("capabilities status = %d, want 200; body=%s", response.StatusCode, body)
	}
	var capabilities crossClientCapabilities
	if err := json.NewDecoder(response.Body).Decode(&capabilities); err != nil {
		t.Fatal(err)
	}
	return capabilities
}
