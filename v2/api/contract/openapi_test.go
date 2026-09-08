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
		"/api/v1/fleet/plans/{planID}/attempts/{attemptID}/cancel",
		"/api/v1/deployments", "/api/v1/deployments/{id}", "/api/v1/deployments/{id}/steps", "/api/v1/services/manifest",
		"/api/v1/apps/{id}/private-attestations",
		"/api/v1/apps/{id}/external-deployments/begin", "/api/v1/apps/{id}/external-deployments/resume",
		"/api/v1/apps/{id}/external-deployments/admit", "/api/v1/apps/{id}/external-deployments/cleanup",
		"/api/v1/apps/{id}/external-deployments/reconcile",
		"/api/v1/apps/{id}/external-deployments/context/{admissionId}",
		"/api/v1/fleet/plans/{planID}/github/pull-request", "/api/v1/fleet/plans/{planID}/github/dispatch",
	} {
		if _, ok := paths[required]; !ok {
			t.Errorf("missing path %s", required)
		}
	}
	components := document["components"].(map[string]interface{})
	schemas := components["schemas"].(map[string]interface{})
	appStatus := schemas["AppStatus"].(map[string]interface{})["properties"].(map[string]interface{})
	infraSpec := appStatus["spec"].(map[string]interface{})["properties"].(map[string]interface{})
	placement := infraSpec["placement"].(map[string]interface{})["properties"].(map[string]interface{})
	distinctHosts := placement["distinctHosts"].(map[string]interface{})["description"].(string)
	if !strings.Contains(distinctHosts, "two replicas plus one live canary requires three eligible clients") {
		t.Error("InfraSpec distinctHosts must document rollout headroom for two replicas and one canary")
	}
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
	if _, legacy := paths["/api/v1/apps/{id}/external-deployments"]; legacy {
		t.Error("legacy external-deployments endpoint must not be advertised beside v4 admission")
	}
	beginRequest := schemas["ExternalFleetAdmissionBeginRequest"].(map[string]interface{})
	if !containsRequiredField(beginRequest["required"].([]interface{}), "logicalIdentity") {
		t.Error("ExternalFleetAdmissionBeginRequest must require logicalIdentity")
	}
	logicalIdentity := schemas["ExternalFleetLogicalIdentity"].(map[string]interface{})
	if logicalIdentity["properties"].(map[string]interface{})["schemaVersion"].(map[string]interface{})["const"] != "norn.external-fleet-logical-identity/v4" {
		t.Error("ExternalFleetLogicalIdentity must use the v4 logical identity schema")
	}
	logicalFleet := schemas["ExternalFleetLogicalExecutionIdentity"].(map[string]interface{})
	for _, field := range []string{"namespace", "planId", "planSha256", "rootAttemptId", "fleetCommit"} {
		if !containsRequiredField(logicalFleet["required"].([]interface{}), field) {
			t.Errorf("ExternalFleetLogicalExecutionIdentity must require stable %s", field)
		}
	}
	nonceResponse := schemas["ExternalFleetAdmissionNonceResponse"].(map[string]interface{})
	for _, field := range []string{"admissionId", "state", "nonce", "expiresAt", "nonceGeneration"} {
		if !containsRequiredField(nonceResponse["required"].([]interface{}), field) {
			t.Errorf("ExternalFleetAdmissionNonceResponse must require %s", field)
		}
	}
	if nonceResponse["properties"].(map[string]interface{})["nonce"].(map[string]interface{})["x-sensitive"] != true {
		t.Error("ExternalFleetAdmissionNonceResponse.nonce must be explicitly sensitive")
	}
	replayResponse := schemas["ExternalFleetAdmissionReplayResponse"].(map[string]interface{})
	for _, field := range []string{"admissionId", "state", "nonceGeneration", "operationId"} {
		if !containsRequiredField(replayResponse["required"].([]interface{}), field) {
			t.Errorf("ExternalFleetAdmissionReplayResponse must require %s", field)
		}
	}
	resumeRequest := schemas["ExternalFleetAdmissionResumeRequest"].(map[string]interface{})
	for _, field := range []string{"admissionId", "logicalIdentity"} {
		if !containsRequiredField(resumeRequest["required"].([]interface{}), field) {
			t.Errorf("ExternalFleetAdmissionResumeRequest must require %s", field)
		}
	}
	cleanupRequest := schemas["ExternalFleetAdmissionCleanupRequest"].(map[string]interface{})
	for _, field := range []string{"admissionId", "operationId", "receiptDigest", "cleanupIntentDigest", "absenceProofDigest"} {
		if !containsRequiredField(cleanupRequest["required"].([]interface{}), field) {
			t.Errorf("ExternalFleetAdmissionCleanupRequest must require immutable %s", field)
		}
	}
	v4Receipt := schemas["ExternalFleetDeploymentReceiptV4"].(map[string]interface{})
	if v4Receipt["properties"].(map[string]interface{})["schemaVersion"].(map[string]interface{})["const"] != "norn.external-fleet-deployment-receipt/v4" {
		t.Error("ExternalFleetDeploymentReceiptV4 must use the v4 receipt contract")
	}
	for _, field := range []string{"admissionId", "nonce", "sourceSha", "artifact", "candidate", "fleet"} {
		if !containsRequiredField(v4Receipt["required"].([]interface{}), field) {
			t.Errorf("ExternalFleetDeploymentReceiptV4 must require %s", field)
		}
	}
	v4Proof := schemas["ExternalFleetExecutionProofV4"].(map[string]interface{})
	for _, field := range []string{"migration", "runtime", "planId", "rootAttemptId", "fleetCommit"} {
		if !containsRequiredField(v4Proof["required"].([]interface{}), field) {
			t.Errorf("ExternalFleetExecutionProofV4 must require %s", field)
		}
	}
	v4NomadProof := schemas["ExternalFleetNomadJobProofV4"].(map[string]interface{})
	for _, field := range []string{"evalCreateIndex", "evalJobModifyIndex", "jobCreateIndex", "jobModifyIndex", "jobVersion", "currentSpec", "currentSpecSha256", "submission", "submissionSha256", "evaluationChainIds", "checkpointId"} {
		if !containsRequiredField(v4NomadProof["required"].([]interface{}), field) {
			t.Errorf("ExternalFleetNomadJobProofV4 must require %s", field)
		}
	}
	v4Context := schemas["ExternalFleetAdmissionContext"].(map[string]interface{})
	for _, field := range []string{"schemaVersion", "admissionId", "state", "logicalIdentity", "logicalDigest", "nonceGeneration", "cleanupState", "retryLineage", "checkpoints"} {
		if !containsRequiredField(v4Context["required"].([]interface{}), field) {
			t.Errorf("ExternalFleetAdmissionContext must require %s", field)
		}
	}
	assertV4ExternalAdmissionPaths(t, paths)
	runnerAttempt := schemas["FleetRunnerAttempt"].(map[string]interface{})
	if !containsRequiredField(runnerAttempt["required"].([]interface{}), "rootAttemptId") {
		t.Error("FleetRunnerAttempt must require server-owned rootAttemptId")
	}
	if runnerAttempt["properties"].(map[string]interface{})["rootAttemptId"].(map[string]interface{})["readOnly"] != true {
		t.Error("FleetRunnerAttempt.rootAttemptId must be server-derived/readOnly")
	}
	for _, field := range []string{"sourceDispatchRunId", "pilotRunId", "recovery"} {
		if runnerAttempt["properties"].(map[string]interface{})[field].(map[string]interface{})["readOnly"] != true {
			t.Errorf("FleetRunnerAttempt.%s must be server-derived/readOnly", field)
		}
	}
	if runnerAttempt["properties"].(map[string]interface{})["phaseStartedAt"].(map[string]interface{})["readOnly"] != true {
		t.Error("FleetRunnerAttempt.phaseStartedAt must be server-owned/readOnly")
	}
	if _, ok := runnerAttempt["properties"].(map[string]interface{})["timing"]; !ok {
		t.Error("FleetRunnerAttempt must expose additive advisory timing")
	}
	runnerStart := schemas["FleetRunnerAttemptStartRequest"].(map[string]interface{})
	for _, field := range []string{"dispatchNonce", "sourceDispatchRunId", "pilotRunId"} {
		if !containsRequiredField(runnerStart["required"].([]interface{}), field) {
			t.Errorf("FleetRunnerAttemptStartRequest must require %s", field)
		}
	}
	if runnerStart["properties"].(map[string]interface{})["dispatchNonce"].(map[string]interface{})["writeOnly"] != true {
		t.Error("FleetRunnerAttemptStartRequest.dispatchNonce must be write-only")
	}
	for _, field := range []string{"operationClass", "createdNodeCount"} {
		if _, ok := runnerStart["properties"].(map[string]interface{})[field]; !ok {
			t.Errorf("FleetRunnerAttemptStartRequest must expose optional %s", field)
		}
	}
	operationClass := runnerStart["properties"].(map[string]interface{})["operationClass"].(map[string]interface{})
	if !containsRequiredField(operationClass["enum"].([]interface{}), "cold_start") || !containsRequiredField(operationClass["enum"].([]interface{}), "unknown") {
		t.Error("FleetRunnerAttemptStartRequest.operationClass must permit cold_start and conservative unknown")
	}
	timing := schemas["FleetRunnerTiming"].(map[string]interface{})
	if !containsRequiredField(timing["required"].([]interface{}), "provenance") {
		t.Error("FleetRunnerTiming must require provenance")
	}
	reconciliation := schemas["FleetReconciliationRequest"].(map[string]interface{})
	if !containsRequiredField(reconciliation["required"].([]interface{}), "attemptId") {
		t.Error("FleetReconciliationRequest must require an attemptId in the protected runner rollout")
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

func assertV4ExternalAdmissionPaths(t *testing.T, paths map[string]interface{}) {
	t.Helper()
	for _, path := range []string{
		"/api/v1/apps/{id}/external-deployments/begin",
		"/api/v1/apps/{id}/external-deployments/resume",
		"/api/v1/apps/{id}/external-deployments/admit",
		"/api/v1/apps/{id}/external-deployments/cleanup",
	} {
		operation := paths[path].(map[string]interface{})["post"].(map[string]interface{})
		parameters := operation["parameters"].([]interface{})
		if len(parameters) != 1 || parameters[0].(map[string]interface{})["$ref"] != "#/components/parameters/RequiredIdempotencyKey" {
			t.Errorf("%s must require the stable Idempotency-Key", path)
		}
		responses := operation["responses"].(map[string]interface{})
		if _, ok := responses["200"]; !ok {
			t.Errorf("%s must document idempotent/recovery success", path)
		}
	}

	admit := paths["/api/v1/apps/{id}/external-deployments/admit"].(map[string]interface{})["post"].(map[string]interface{})
	admitSchema := admit["requestBody"].(map[string]interface{})["content"].(map[string]interface{})["application/json"].(map[string]interface{})["schema"].(map[string]interface{})
	if admitSchema["$ref"] != "#/components/schemas/ExternalFleetDeploymentAdmissionRequest" {
		t.Error("v4 admit must expose only its v4 envelope")
	}
	if _, legacy := paths["/api/v1/apps/{id}/external-deployments"]; legacy {
		t.Error("legacy action/receipt endpoint must not be a public contract path")
	}
	admitResponses := admit["responses"].(map[string]interface{})
	created := admitResponses["201"].(map[string]interface{})
	if _, ok := created["headers"].(map[string]interface{})["Location"]; !ok {
		t.Error("v4 admission creation must document its operation Location header")
	}
	contextResponses := paths["/api/v1/apps/{id}/external-deployments/context/{admissionId}"].(map[string]interface{})["get"].(map[string]interface{})["responses"].(map[string]interface{})
	if contextResponses["400"] == nil || contextResponses["409"] == nil || contextResponses["404"] != nil {
		t.Error("external admission context must document runtime 400/409 responses, not 404")
	}
	for _, path := range []string{
		"/api/v1/apps/{id}/external-deployments/begin",
		"/api/v1/apps/{id}/external-deployments/resume",
		"/api/v1/apps/{id}/external-deployments/admit",
		"/api/v1/apps/{id}/external-deployments/cleanup",
		"/api/v1/apps/{id}/external-deployments/context/{admissionId}",
	} {
		method := "post"
		if strings.Contains(path, "/context/") {
			method = "get"
		}
		responses := paths[path].(map[string]interface{})[method].(map[string]interface{})["responses"].(map[string]interface{})
		for status, raw := range responses {
			if status == "400" || status == "403" || status == "404" || status == "409" || status == "503" {
				continue
			}
			response := raw.(map[string]interface{})
			headers, ok := response["headers"].(map[string]interface{})
			if !ok || headers["Cache-Control"] == nil || headers["Pragma"] == nil {
				t.Errorf("%s %s must document no-store response headers", method, path)
			}
		}
	}
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
