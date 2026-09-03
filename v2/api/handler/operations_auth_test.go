package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"norn/v2/api/model"
)

func operationReadPrincipal(app, environment, scope string) AccessPrincipal {
	principal := privateRoutePrincipal(app)
	principal.Environment = environment
	principal.Scopes = []string{scope}
	principal.CI.Environment = environment
	principal.TokenID = "operation-token"
	return principal
}

func operationForPrincipal(kind, app, environment string, principal AccessPrincipal) *model.Operation {
	return &model.Operation{ID: "operation-id", Kind: kind, App: app, Metadata: map[string]interface{}{
		"principal": principal.Subject, "principalTokenId": principal.TokenID, "environment": environment, "requestCI": principal.CI,
	}}
}

func operationReadAuthorized(principal AccessPrincipal, operation *model.Operation) (*httptest.ResponseRecorder, bool) {
	request := WithAccessPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/operations/operation-id", nil), &principal)
	recorder := httptest.NewRecorder()
	return recorder, authorizeOperationRead(recorder, request, operation)
}

func TestReleaseOperationReadAuthorizationIsBoundToAppLaneAndOperation(t *testing.T) {
	for _, test := range []struct {
		name, kind, environment, scope string
		releaseRollback                bool
	}{
		{name: "staging preflight", kind: "app.preflight", environment: "staging", scope: ScopeReleaseStage},
		{name: "staging deployment", kind: "app.deploy", environment: "staging", scope: ScopeReleaseStage},
		{name: "production promotion", kind: "app.deploy", environment: "production", scope: ScopeReleasePromote},
		{name: "production rollback", kind: "app.rollback", environment: "production", scope: ScopeReleaseRollback, releaseRollback: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			principal := operationReadPrincipal("private-route", test.environment, test.scope)
			operation := operationForPrincipal(test.kind, "private-route", test.environment, principal)
			if test.releaseRollback {
				operation.Metadata["releaseRollback"] = true
			}
			if recorder, ok := operationReadAuthorized(principal, operation); !ok {
				t.Fatalf("own operation denied: status=%d body=%s", recorder.Code, recorder.Body.String())
			}

			crossApp := principal
			crossApp.App = "another-app"
			if recorder, ok := operationReadAuthorized(crossApp, operation); ok || recorder.Code != http.StatusForbidden {
				t.Fatalf("cross-app operation authorized: ok=%v status=%d", ok, recorder.Code)
			}
			crossEnvironment := principal
			crossEnvironment.Environment = "other"
			crossEnvironment.CI = &CIIdentity{}
			*crossEnvironment.CI = *principal.CI
			crossEnvironment.CI.Environment = "other"
			if recorder, ok := operationReadAuthorized(crossEnvironment, operation); ok || recorder.Code != http.StatusForbidden {
				t.Fatalf("cross-environment operation authorized: ok=%v status=%d", ok, recorder.Code)
			}
			refreshedToken := principal
			refreshedToken.TokenID = "refreshed-token"
			if recorder, ok := operationReadAuthorized(refreshedToken, operation); !ok {
				t.Fatalf("same-CI refreshed token denied: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			changedCI := principal
			changedCI.CI = &CIIdentity{}
			*changedCI.CI = *principal.CI
			changedCI.CI.RunID = "999"
			if recorder, ok := operationReadAuthorized(changedCI, operation); ok || recorder.Code != http.StatusForbidden {
				t.Fatalf("changed CI identity authorized: ok=%v status=%d", ok, recorder.Code)
			}
		})
	}
}

func TestFleetOperationReadRequiresExactFleetPrincipal(t *testing.T) {
	principal := operationReadPrincipal("", "production", ScopeFleetOperate)
	operation := operationForPrincipal("fleet.github.apply-dispatch", "", "production", principal)
	if recorder, ok := operationReadAuthorized(principal, operation); !ok {
		t.Fatalf("own Fleet operation denied: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	changedCI := principal
	changedCI.CI = &CIIdentity{}
	*changedCI.CI = *principal.CI
	changedCI.CI.RunID = "999"
	if recorder, ok := operationReadAuthorized(changedCI, operation); ok || recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-CI Fleet operation authorized: ok=%v status=%d", ok, recorder.Code)
	}
}

func TestOrdinaryOperationReadScopesRetainExistingBehavior(t *testing.T) {
	operation := &model.Operation{ID: "operation-id", Kind: "app.deploy", App: "private-route"}
	if recorder, ok := operationReadAuthorized(AccessPrincipal{Scopes: []string{ScopeAPIRead}}, operation); !ok {
		t.Fatalf("api:read denied: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder, ok := operationReadAuthorized(AccessPrincipal{Scopes: []string{ScopeAPIWrite}}, operation); ok || recorder.Code != http.StatusForbidden {
		t.Fatalf("api:write received read access: ok=%v status=%d", ok, recorder.Code)
	}
}

func TestLocalCompatibilityOperationReadWithoutPrincipal(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/operations/operation-id", nil)
	if !authorizeOperationRead(recorder, request, &model.Operation{ID: "operation-id"}) {
		t.Fatalf("middleware-admitted local compatibility read denied: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWorkflowOperationPollViewOmitsReleaseEvidence(t *testing.T) {
	operation := &model.Operation{ID: "operation-id", Kind: "app.deploy", App: "private-route", Status: model.OperationRunning, Message: "deploying", LastError: "", Payload: map[string]interface{}{"candidate": map[string]interface{}{"bundle": "private-spdx-evidence"}}, Metadata: map[string]interface{}{"promotionQualification": "private-spdx-evidence", "requestCI": "private-ci-evidence"}}
	encoded, err := json.Marshal(operationPollView(operation))
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || !strings.Contains(string(encoded), `"status":"running"`) {
		t.Fatalf("poll view omitted status: %s", encoded)
	}
	for _, forbidden := range []string{"private-spdx-evidence", "private-ci-evidence", "candidate", "promotionQualification", "metadata", "payload"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("poll view leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestOperationListSummaryOmitsEmbeddedEvidence(t *testing.T) {
	operation := model.Operation{ID: "operation-id", Kind: "app.deploy", App: "private-route", Status: model.OperationSucceeded, Message: "deployed", Payload: map[string]interface{}{"candidate": "private-spdx-evidence"}, Metadata: map[string]interface{}{"promotionQualification": "private-spdx-evidence"}}
	summary := operationSummary(operation)
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != model.OperationSucceeded || summary.Receipt == nil {
		t.Fatalf("operation summary lost status/receipt: %+v", summary)
	}
	for _, forbidden := range []string{"private-spdx-evidence", "candidate", "promotionQualification", "metadata", "payload"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("operation list summary leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestOperationListResponsesDisableCaching(t *testing.T) {
	// The header is set before storage access, so this also covers failure paths
	// without requiring a database in the unit suite.
	h := &Handler{}
	for name, invoke := range map[string]func(http.ResponseWriter, *http.Request){"list": h.ListOperations, "active": h.ActiveOperations} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			invoke(recorder, httptest.NewRequest(http.MethodGet, "/api/operations", nil))
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("operation list cache policy=%q", recorder.Header().Get("Cache-Control"))
			}
		})
	}
}
