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
		"/api/v1/apps/{id}/exec-sessions", "/api/v1/exec-sessions/{id}/stream", "/api/v1/host/metrics",
		"/api/v1/production/readiness",
		"/api/v1/audit/mutations", "/api/v1/production/drills", "/api/v1/production/drills/{id}/complete",
		"/api/v1/validate/infraspec", "/api/v1/fleet/validate", "/api/v1/fleet/node-pools", "/api/v1/fleet/plans", "/api/v1/fleet/node-pools/{pool}/plan",
		"/api/v1/fleet/plans/{planID}/reconciliations", "/api/v1/fleet/plans/{planID}/attempts", "/api/v1/fleet/plans/{planID}/attempts/{attemptID}", "/api/v1/fleet/github",
		"/api/v1/fleet/plans/{planID}/github/pull-request", "/api/v1/fleet/plans/{planID}/github/dispatch",
		"/api/v1/fleet/plans/{planID}/github/reconcile",
	} {
		if _, ok := paths[required]; !ok {
			t.Errorf("missing path %s", required)
		}
	}
	walkRefs(t, document, document)
}

func TestIdempotentOperationAcceptancePublishesReplayExpiry(t *testing.T) {
	var document map[string]interface{}
	if err := yaml.Unmarshal(controlV1, &document); err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	errorCode, ok := resolveRef(document, "#/components/schemas/ErrorCode")
	if !ok {
		t.Fatal("missing ErrorCode schema")
	}
	enum := errorCode.(map[string]interface{})["x-extensible-enum"].([]interface{})
	foundCode := false
	for _, value := range enum {
		foundCode = foundCode || value == "idempotency_window_expired"
	}
	if !foundCode {
		t.Fatal("ErrorCode omits idempotency_window_expired")
	}

	covered := 0
	for path, rawPath := range document["paths"].(map[string]interface{}) {
		for method, rawOperation := range rawPath.(map[string]interface{}) {
			operation, ok := rawOperation.(map[string]interface{})
			if !ok || !hasRequiredIdempotencyKey(operation["parameters"]) {
				continue
			}
			covered++
			responses, ok := operation["responses"].(map[string]interface{})
			if !ok || responses["410"] == nil {
				t.Errorf("%s %s omits the 410 replay-expiry response", strings.ToUpper(method), path)
			}
		}
	}
	if covered < 15 {
		t.Fatalf("only %d idempotent acceptance operations were checked", covered)
	}
}

func hasRequiredIdempotencyKey(raw interface{}) bool {
	parameters, _ := raw.([]interface{})
	for _, rawParameter := range parameters {
		parameter, _ := rawParameter.(map[string]interface{})
		if parameter["$ref"] == "#/components/parameters/RequiredIdempotencyKey" || parameter["name"] == "Idempotency-Key" && parameter["required"] == true {
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
