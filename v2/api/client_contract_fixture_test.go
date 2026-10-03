package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"norn/v2/api/config"
	"norn/v2/api/handler"
)

func TestSignedStagingClientCapabilityFixtureMatchesHandler(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	request = handler.WithAccessPrincipal(request, &handler.AccessPrincipal{
		Subject:  "norn-client-fixture-operator",
		DeviceID: "client-fixture-device",
		Scopes:   []string{handler.ScopeAPIRead, handler.ScopeAPIWrite, handler.ScopeEventsRead},
	})
	recorder := httptest.NewRecorder()
	writeControlCapabilitiesForConfig(&config.Config{
		Profile:                          "norn-client-fixture",
		Environment:                      "staging",
		ReleaseAdmissionMode:             "attested",
		ReleaseAttestationTrustMode:      "norn-signed-private",
		QualificationSigningKey:          "fixture-signing-key",
		GitHubActionsOIDCAudience:        "norn-staging",
		GitHubActionsReleaseBindings:     []string{"demo=acme/demo@101@202"},
		GitHubActionsAllowedWorkflowRefs: []string{"acme/demo/.github/workflows/release.yml@fixture-sha"},
		GitHubActionsAllowedRefs:         []string{"refs/heads/main"},
		GitHubActionsAllowedEvents:       []string{"workflow_dispatch"},
		GitHubActionsAllowedEnvironments: []string{"staging"},
		GitHubActionsDefaultBranch:       "main",
	}, recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "private, no-store")
	}

	actual := canonicalClientCapabilityJSON(t, recorder.Body.Bytes(), true)
	fixturePath := filepath.Join("testdata", "client-contract", "v1", "signed-staging-capabilities.json")
	wantJSON, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", fixturePath, err)
	}
	want := canonicalClientCapabilityJSON(t, wantJSON, false)
	if !bytes.Equal(actual, want) {
		t.Fatalf("%s drifted from writeControlCapabilitiesForConfig output\nactual:\n%s", fixturePath, actual)
	}
}

func canonicalClientCapabilityJSON(t *testing.T, document []byte, normalizeServerVersion bool) []byte {
	t.Helper()
	var capability map[string]interface{}
	if err := json.Unmarshal(document, &capability); err != nil {
		t.Fatalf("decode capability document: %v", err)
	}
	if normalizeServerVersion {
		capability["serverVersion"] = "client-contract-fixture"
	}
	canonical, err := json.MarshalIndent(capability, "", "  ")
	if err != nil {
		t.Fatalf("encode canonical capability document: %v", err)
	}
	return append(canonical, '\n')
}
