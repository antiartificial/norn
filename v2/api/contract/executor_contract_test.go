package contract

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"norn/v2/api/fleet/controller"
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
		"FleetProviderEvidenceRequest":        reflect.TypeOf(executor.ProviderEvidenceRequest{}),
		"FleetExecutorStopAttestationRequest": reflect.TypeOf(executor.StopAttestationRequest{}),
		"FleetExecutorTofuLock":               reflect.TypeOf(executor.TofuLock{}),
		"FleetExecutorAttemptStartRequest":    reflect.TypeOf(executor.AttemptStartRequest{}),
		"FleetExecutorAttemptMetadata":        reflect.TypeOf(executor.AttemptMetadata{}),
		"FleetConvergeView":                   reflect.TypeOf(executor.ConvergeView{}),
		"FleetConvergeRefusalRequest":         reflect.TypeOf(executor.ConvergeRefusalRequest{}),
		"FleetConvergeRefusalTofuLock":        reflect.TypeOf(executor.RefusalTofuLock{}),
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
		"/api/v1/fleet/plans/{planID}/converge-refusals": "createFleetConvergeRefusal",
		"/api/v1/fleet/plans/{planID}/provider-evidence": "recordFleetProviderEvidence",
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

// TestExecutorWorkViewAndEnrollmentArePinned pins what the P1 loop reads: the
// executor route list, the converge work view on the desired revision, the
// derived resumeApproved, and the enrollment exchange's pending answer.
func TestExecutorWorkViewAndEnrollmentArePinned(t *testing.T) {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(controlV1, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]interface{})
	seen := map[string]bool{}
	for _, route := range executor.ExecutorRoutes {
		method, path, _ := strings.Cut(route, " ")
		if seen[route] {
			t.Errorf("duplicate executor route %s", route)
		}
		seen[route] = true
		item, _ := paths[path].(map[string]interface{})
		if _, ok := item[strings.ToLower(method)].(map[string]interface{}); !ok {
			t.Errorf("executor route %s is not in the OpenAPI document", route)
		}
	}
	if !seen["GET /api/v1/fleet/resources/{name}"] || seen["GET /api/v1/fleet/resources"] {
		t.Error("the executor reads exactly the single-resource view, never the list")
	}
	// Provider evidence is admin only: never an executor route.
	if seen["POST /api/v1/fleet/plans/{planID}/provider-evidence"] {
		t.Error("provider evidence is an admin route, not an executor route")
	}
	// The attempt-bound credential reaches a subset of the executor routes,
	// never start, cancel, grants, attestations or rotation.
	for _, route := range executor.AttemptCredentialRoutes {
		if !seen[route] {
			t.Errorf("attempt credential route %s is not an executor route", route)
		}
		if strings.HasSuffix(route, "/cancel") || strings.HasSuffix(route, "/attempts") || strings.Contains(route, "credential-grants") ||
			strings.Contains(route, "stop-attestations") || strings.Contains(route, "/auth/") || strings.Contains(route, "/resources/") {
			t.Errorf("attempt credential must not reach %s", route)
		}
	}
	scopes, _ := resolveRef(doc, "#/components/securitySchemes/bearerAuth")
	if desc, _ := scopes.(map[string]interface{})["description"].(string); !strings.Contains(desc, executor.AttemptScope) {
		t.Errorf("bearerAuth must document the internal-only scope %s", executor.AttemptScope)
	}

	desired, _ := executorSchemaProps(t, doc, "FleetResourceDesired")
	if ref, _ := desired["converge"].(map[string]interface{})["$ref"].(string); ref != "#/components/schemas/FleetConvergeView" {
		t.Error("FleetResourceDesired.converge must reference FleetConvergeView")
	}
	_, desiredRequired := executorSchemaProps(t, doc, "FleetResourceDesired")
	if containsRequiredField(desiredRequired, "converge") {
		t.Error("FleetResourceDesired.converge must stay optional")
	}
	status, _ := executorSchemaProps(t, doc, "FleetResourceStatus")
	if !enumSet(status["nextAction"].(map[string]interface{})["enum"])[executor.NextActionApproveConverge] {
		t.Errorf("status.nextAction enum omits %s", executor.NextActionApproveConverge)
	}
	blocker, _ := executorSchemaProps(t, doc, "FleetResourceBlocker")
	reasonDoc, _ := blocker["reason"].(map[string]interface{})["description"].(string)
	for _, reason := range executor.BlockerReasons {
		if !strings.Contains(reasonDoc, reason) {
			t.Errorf("blocker.reason description omits %s", reason)
		}
	}
	for _, code := range []string{executor.CodeFleetExecutionModeUnavailable, executor.CodeFleetConvergeFactsFailed} {
		if executor.CodeStatus[code] != 503 {
			t.Errorf("%s must be a 503", code)
		}
	}
	active := status["active"].(map[string]interface{})
	resume, _ := active["properties"].(map[string]interface{})["resumeApproved"].(map[string]interface{})
	if resume["type"] != "boolean" {
		t.Error("status.active.resumeApproved must be a boolean")
	}
	activeRequired, _ := active["required"].([]interface{})
	for _, field := range []string{executor.ActiveResumeNeededAttemptIDField, executor.ActiveCommitSHAField} {
		prop, _ := active["properties"].(map[string]interface{})[field].(map[string]interface{})
		if prop["type"] != "string" || containsRequiredField(activeRequired, field) {
			t.Errorf("status.active.%s must be an optional string", field)
		}
	}
	if containsRequiredField(activeRequired, "resumeApproved") {
		t.Error("status.active.resumeApproved must stay optional")
	}

	exchange := paths["/api/v1/enrollments/{id}/exchange"].(map[string]interface{})["post"].(map[string]interface{})
	if _, ok := exchange["responses"].(map[string]interface{})[strconv.Itoa(executor.EnrollmentPendingStatus)]; !ok {
		t.Errorf("exchange must document the %d pending answer", executor.EnrollmentPendingStatus)
	}
	if desc, _ := exchange["description"].(string); !strings.Contains(desc, executor.EnrollmentPendingCode) {
		t.Errorf("exchange description must name %s", executor.EnrollmentPendingCode)
	}
	errorCode, _ := resolveRef(doc, "#/components/schemas/ErrorCode")
	found := false
	for _, v := range errorCode.(map[string]interface{})["x-extensible-enum"].([]interface{}) {
		found = found || v == executor.EnrollmentPendingCode
	}
	if !found {
		t.Errorf("ErrorCode omits %s", executor.EnrollmentPendingCode)
	}
}

// TestRescanResponseIsTheWatchDecision pins that the rescan route documents
// the watcher's WatchDecision (not the resource view) field by field.
func TestRescanResponseIsTheWatchDecision(t *testing.T) {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(controlV1, &doc); err != nil {
		t.Fatal(err)
	}
	post := doc["paths"].(map[string]interface{})["/api/v1/fleet/resources/{name}/converge"].(map[string]interface{})["post"].(map[string]interface{})
	ok := post["responses"].(map[string]interface{})["200"].(map[string]interface{})
	schema := ok["content"].(map[string]interface{})["application/json"].(map[string]interface{})["schema"].(map[string]interface{})
	if schema["$ref"] != "#/components/schemas/FleetWatchDecision" {
		t.Fatalf("rescan 200 schema = %v, want FleetWatchDecision", schema["$ref"])
	}
	props, required := executorSchemaProps(t, doc, "FleetWatchDecision")
	all, wantRequired := jsonFields(reflect.TypeOf(controller.WatchDecision{}))
	var got, gotRequired []string
	for name := range props {
		got = append(got, name)
	}
	for _, r := range required {
		gotRequired = append(gotRequired, r.(string))
	}
	for _, list := range [][]string{all, got, wantRequired, gotRequired} {
		sort.Strings(list)
	}
	if !reflect.DeepEqual(all, got) || !reflect.DeepEqual(wantRequired, gotRequired) {
		t.Errorf("FleetWatchDecision properties %v required %v, want %v required %v", got, gotRequired, all, wantRequired)
	}
	actions := enumSet(props["action"].(map[string]interface{})["enum"])
	for _, a := range []controller.WatchAction{controller.WatchRecorded, controller.WatchExists, controller.WatchNone, controller.WatchPending,
		controller.WatchUnverified, controller.WatchHistoryRewritten, controller.WatchError} {
		if !actions[string(a)] {
			t.Errorf("FleetWatchDecision.action enum omits %s", a)
		}
	}
	if len(actions) != 7 {
		t.Errorf("FleetWatchDecision.action enum has %d values, want 7", len(actions))
	}
}
