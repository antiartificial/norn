package contract

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestControlOpenAPIParsesAndLocalRefsResolve(t *testing.T) {
	var document map[string]interface{}
	if err := yaml.Unmarshal(controlV1, &document); err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	if document["openapi"] != "3.1.0" {
		t.Fatalf("openapi version = %v", document["openapi"])
	}
	paths, ok := document["paths"].(map[string]interface{})
	if !ok || len(paths) < 15 {
		t.Fatalf("expected versioned paths, got %T (%d)", document["paths"], len(paths))
	}
	for _, required := range []string{
		"/api/v1/apps", "/api/v1/apps/{id}/deployment",
		"/api/v1/apps/{id}/snapshots", "/api/v1/apps/{id}/snapshots/retention", "/api/v1/apps/{id}/snapshots/{snapshot}/restore",
		"/api/v1/apps/{id}/migrations", "/api/v1/apps/{id}/rollbacks",
		"/api/v1/enrollments", "/api/v1/events/info", "/api/v1/operations/{id}/cancel",
		"/api/v1/apps/{id}/exec-sessions", "/api/v1/exec-sessions/{id}/stream", "/api/v1/host/metrics", "/api/v1/host/runtime",
		"/api/v1/production/readiness",
		"/api/v1/audit/mutations", "/api/v1/production/drills", "/api/v1/production/drills/{id}/complete",
		"/api/v1/validate/infraspec", "/api/v1/fleet/validate", "/api/v1/fleet/node-pools", "/api/v1/fleet/plans", "/api/v1/fleet/node-pools/{pool}/plan",
		"/api/v1/fleet/plans/{planID}/reconciliations", "/api/v1/fleet/github",
		"/api/v1/fleet/plans/{planID}/attempts", "/api/v1/fleet/plans/{planID}/attempts/{attemptID}",
		"/api/v1/fleet/plans/{planID}/attempts/{attemptID}/heartbeat", "/api/v1/fleet/plans/{planID}/attempts/{attemptID}/advance",
		"/api/v1/fleet/plans/{planID}/attempts/{attemptID}/retry", "/api/v1/fleet/plans/{planID}/attempts/{attemptID}/cancel",
		"/api/v1/deployments", "/api/v1/deployments/{id}", "/api/v1/deployments/{id}/steps", "/api/v1/services/manifest",
		"/api/v1/apps/{id}/private-attestations",
		"/api/v1/fleet/plans/{planID}/github/pull-request", "/api/v1/fleet/plans/{planID}/github/dispatch",
	} {
		if _, ok := paths[required]; !ok {
			t.Errorf("missing path %s", required)
		}
	}
	components := document["components"].(map[string]interface{})
	schemas := components["schemas"].(map[string]interface{})
	for schemaName, fields := range map[string][]string{
		"ReleaseRequest":       {"sourceSha", "artifact", "candidate"},
		"ReleaseCandidate":     {"repositoryVisibility", "signerWorkflowRef", "signerWorkflowSha", "attestation"},
		"ReleaseQualification": {"issuedAt", "expiresAt", "keyId", "signature", "candidate", "dsse"},
	} {
		schema := schemas[schemaName].(map[string]interface{})
		required, _ := schema["required"].([]interface{})
		for _, field := range fields {
			if !containsRequiredField(required, field) {
				t.Errorf("%s must require %s", schemaName, field)
			}
		}
	}
	releaseCandidate := schemas["ReleaseCandidate"].(map[string]interface{})["properties"].(map[string]interface{})
	for _, field := range []string{"repositoryVisibility", "signerWorkflowRef", "signerWorkflowSha"} {
		if releaseCandidate[field].(map[string]interface{})["readOnly"] != true {
			t.Errorf("ReleaseCandidate.%s must be server-derived/readOnly", field)
		}
	}
	attestation := schemas["ReleaseAttestationIdentity"].(map[string]interface{})["properties"].(map[string]interface{})
	for _, field := range []string{"mode", "verifier"} {
		if attestation[field].(map[string]interface{})["readOnly"] != true {
			t.Errorf("ReleaseAttestationIdentity.%s must be server-derived/readOnly", field)
		}
	}
	exchange := paths["/api/v1/auth/github-actions/exchange"].(map[string]interface{})["post"].(map[string]interface{})
	exchangeSchema := exchange["requestBody"].(map[string]interface{})["content"].(map[string]interface{})["application/json"].(map[string]interface{})["schema"].(map[string]interface{})
	if containsRequiredField(exchangeSchema["required"].([]interface{}), "app") {
		t.Error("fleet OIDC exchange must permit an omitted app")
	}
	walkRefs(t, document, document)
}

func containsRequiredField(fields []interface{}, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}

func walkRefs(t *testing.T, root map[string]interface{}, value interface{}) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			if key == "$ref" {
				ref, ok := child.(string)
				if !ok || !strings.HasPrefix(ref, "#/") {
					t.Fatalf("unsupported ref %v", child)
				}
				if _, ok := resolveRef(root, ref); !ok {
					t.Errorf("unresolved ref %s", ref)
				}
				continue
			}
			walkRefs(t, root, child)
		}
	case []interface{}:
		for _, child := range typed {
			walkRefs(t, root, child)
		}
	}
}

func resolveRef(root map[string]interface{}, ref string) (interface{}, bool) {
	var current interface{} = root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		object, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = object[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
		if !ok {
			return nil, false
		}
	}
	if current == nil {
		return nil, false
	}
	_ = fmt.Sprintf("%v", current)
	return current, true
}
