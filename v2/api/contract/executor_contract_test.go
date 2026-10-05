package contract

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"norn/v2/api/fleet/executor"
)

func executorSchemaProps(t *testing.T, doc map[string]interface{}, name string) (map[string]interface{}, []interface{}) {
	t.Helper()
	raw, ok := resolveRef(doc, "#/components/schemas/"+name)
	if !ok {
		t.Fatalf("missing schema %s", name)
	}
	schema := raw.(map[string]interface{})
	required, _ := schema["required"].([]interface{})
	return schema["properties"].(map[string]interface{}), required
}

func jsonFields(typ reflect.Type) (all, required []string) {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Anonymous {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		all = append(all, name)
		if !strings.Contains(opts, "omitempty") {
			required = append(required, name)
		}
	}
	sort.Strings(all)
	sort.Strings(required)
	return
}

// TestExecutorPinnedShapesMatchOpenAPI keeps fleet/executor (ADR 0010) and the
// OpenAPI document from drifting: same wire fields, same required set.
func TestExecutorPinnedShapesMatchOpenAPI(t *testing.T) {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(controlV1, &doc); err != nil {
		t.Fatal(err)
	}
	for schema, typ := range map[string]reflect.Type{
		"FleetConvergeApproveRequest":         reflect.TypeOf(executor.ApproveRequest{}),
		"FleetExecutionModeRequest":           reflect.TypeOf(executor.ExecutionModeRequest{}),
		"FleetCredentialGrantRequest":         reflect.TypeOf(executor.CredentialGrantRequest{}),
		"FleetCredentialGrant":                reflect.TypeOf(executor.CredentialGrantResponse{}),
		"FleetProviderEvidence":               reflect.TypeOf(executor.ProviderEvidence{}),
		"FleetExecutorStopAttestationRequest": reflect.TypeOf(executor.StopAttestationRequest{}),
		"FleetExecutorTofuLock":               reflect.TypeOf(executor.TofuLock{}),
		"FleetExecutorAttemptStartRequest":    reflect.TypeOf(executor.AttemptStartRequest{}),
		"FleetExecutorAttemptMetadata":        reflect.TypeOf(executor.AttemptMetadata{}),
	} {
		props, required := executorSchemaProps(t, doc, schema)
		var got, req []string
		for k := range props {
			got = append(got, k)
		}
		for _, r := range required {
			req = append(req, r.(string))
		}
		sort.Strings(got)
		sort.Strings(req)
		wantAll, wantReq := jsonFields(typ)
		if !reflect.DeepEqual(got, wantAll) {
			t.Errorf("%s properties = %v, Go type has %v", schema, got, wantAll)
		}
		// omitempty on a Go field means optional in the schema; tofuLock and
		// resume-style fields follow the same rule.
		if !reflect.DeepEqual(req, wantReq) {
			t.Errorf("%s required = %v, Go type requires %v", schema, req, wantReq)
		}
	}

	// The grant phase and type enums are exactly the union over both
	// backend profiles of the Go admission table.
	grantProps, _ := executorSchemaProps(t, doc, "FleetCredentialGrantRequest")
	wantPhases, wantTypes := map[string]bool{}, map[string]bool{}
	for _, table := range executor.GrantTypes {
		for phase, allowed := range table {
			wantPhases[phase] = true
			for _, a := range allowed {
				wantTypes[a] = true
			}
		}
	}
	gotPhases := enumSet(grantProps["phase"].(map[string]interface{})["enum"])
	gotTypes := enumSet(grantProps["types"].(map[string]interface{})["items"].(map[string]interface{})["enum"])
	if !reflect.DeepEqual(gotPhases, wantPhases) {
		t.Errorf("grant phase enum = %v, GrantTypes phases = %v", gotPhases, wantPhases)
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Errorf("grant type enum = %v, GrantTypes types = %v", gotTypes, wantTypes)
	}
}

func enumSet(raw interface{}) map[string]bool {
	set := map[string]bool{}
	for _, v := range raw.([]interface{}) {
		set[v.(string)] = true
	}
	return set
}

func TestExecutorRoutesCodesAndReleaseEvidenceArePublished(t *testing.T) {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(controlV1, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]interface{})
	for path, op := range map[string]string{
		"/api/v1/fleet/resources/{name}/converge":        "rescanFleetResourceConverge",
		"/api/v1/fleet/resources/{name}/execution-mode":  "setFleetResourceExecutionMode",
		"/api/v1/fleet/plans/{planID}/converge/approve":  "approveFleetConverge",
		"/api/v1/fleet/plans/{planID}/credential-grants": "createFleetCredentialGrant",
		"/api/v1/fleet/executor/stop-attestations":       "createFleetExecutorStopAttestation",
	} {
		item, ok := paths[path].(map[string]interface{})
		if !ok {
			t.Errorf("missing path %s", path)
			continue
		}
		post, _ := item["post"].(map[string]interface{})
		if post["operationId"] != op {
			t.Errorf("%s operationId = %v, want %s", path, post["operationId"], op)
		}
		// Every new route may write a ledger operation, so each requires an
		// Idempotency-Key (the rescan route included).
		params, _ := post["parameters"].([]interface{})
		hasKey := false
		for _, p := range params {
			hasKey = hasKey || p.(map[string]interface{})["$ref"] == "#/components/parameters/IdempotencyKey"
		}
		if !hasKey {
			t.Errorf("%s must require an Idempotency-Key", path)
		}
	}

	// The existing start route keeps the GitHub-lane body unchanged and adds
	// the executor body as a second alternative.
	start := paths["/api/v1/fleet/plans/{planID}/attempts"].(map[string]interface{})["post"].(map[string]interface{})
	schema := start["requestBody"].(map[string]interface{})["content"].(map[string]interface{})["application/json"].(map[string]interface{})["schema"].(map[string]interface{})
	alternatives := map[string]bool{}
	for _, alt := range schema["oneOf"].([]interface{}) {
		alternatives[alt.(map[string]interface{})["$ref"].(string)] = true
	}
	if len(alternatives) != 2 || !alternatives["#/components/schemas/FleetRunnerAttemptStartRequest"] || !alternatives["#/components/schemas/FleetExecutorAttemptStartRequest"] {
		t.Errorf("start route request alternatives = %v", alternatives)
	}
	errorCode, _ := resolveRef(doc, "#/components/schemas/ErrorCode")
	enum := map[string]bool{}
	for _, v := range errorCode.(map[string]interface{})["x-extensible-enum"].([]interface{}) {
		enum[v.(string)] = true
	}
	if len(executor.CodeStatus) == 0 {
		t.Fatal("executor.CodeStatus is empty")
	}
	for code := range executor.CodeStatus {
		if !enum[code] {
			t.Errorf("ErrorCode omits %s", code)
		}
	}
	for _, schema := range []string{"FleetTargetReleaseRequest", "FleetTargetAbandonPlanRequest"} {
		props, required := executorSchemaProps(t, doc, schema)
		if ref, _ := props["providerEvidence"].(map[string]interface{})["$ref"].(string); ref != "#/components/schemas/FleetProviderEvidence" {
			t.Errorf("%s providerEvidence must reference FleetProviderEvidence", schema)
		}
		if containsRequiredField(required, "providerEvidence") {
			t.Errorf("%s must keep providerEvidence optional so existing requests are unchanged", schema)
		}
	}
}
