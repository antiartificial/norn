package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

func TestRunnerAttemptRequestValidationAndPhaseOrdering(t *testing.T) {
	valid := fleet.RunnerAttemptCreateRequest{SchemaVersion: fleet.RunnerAttemptSchemaVersion, RunnerAttemptID: "github-run-7", CommitSHA: strings.Repeat("a", 40), PlanSHA256: strings.Repeat("b", 64), DispatchNonce: strings.Repeat("c", 64), SourceDispatchRunID: "7", WorkflowURL: "https://github.com/acme/norn-fleet/actions/runs/7", HeartbeatTimeoutSeconds: 120}
	if err := validateRunnerAttemptCreate(valid); err != nil {
		t.Fatal(err)
	}
	valid.CommitSHA = "not-a-sha"
	if err := validateRunnerAttemptCreate(valid); err == nil {
		t.Fatal("invalid immutable binding accepted")
	}
	if got := nextFleetReconciliationPhase("infrastructure_applied"); got != "inventory_generated" {
		t.Fatalf("next phase=%q", got)
	}
	if got := nextFleetReconciliationPhase("complete"); got != "" {
		t.Fatalf("complete advanced to %q", got)
	}
}

func TestDestructiveRecoveryAlwaysRestartsAtPrechangeProof(t *testing.T) {
	destructive := &model.Operation{Payload: map[string]interface{}{"action": "replace"}}
	previous := fleet.RunnerAttempt{CurrentPhase: "provider_applying"}
	if got := fleetRunnerAttemptResumePhase(destructive, previous); got != "prechange_verified" {
		t.Fatalf("destructive recovery phase=%q, want prechange_verified", got)
	}
	nondestructive := &model.Operation{Payload: map[string]interface{}{"action": "scale", "current": map[string]interface{}{"desired": 2}, "proposed": map[string]interface{}{"desired": 3}}}
	if got := fleetRunnerAttemptResumePhase(nondestructive, previous); got != "provider_applying" {
		t.Fatalf("non-destructive recovery phase=%q, want inherited provider_applying", got)
	}
}

func TestFleetReadScopeAcceptsOnlyReadOrOperate(t *testing.T) {
	if !(AccessPrincipal{Scopes: []string{ScopeFleetOperate}}).Allows(ScopeFleetOperate) {
		t.Fatal("fleet scope not recognized")
	}
	if (AccessPrincipal{Scopes: []string{ScopeReleaseStage}}).Allows(ScopeFleetOperate) {
		t.Fatal("release token can operate fleet")
	}
}

func TestFleetRunnerLifecycleRejectsNonOIDCFleetToken(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/x/attempts", nil), &AccessPrincipal{Scopes: []string{ScopeFleetOperate}})
	if _, ok := requireFleetRunnerPrincipal(recorder, request); ok || recorder.Code != http.StatusForbidden {
		t.Fatalf("non-CI fleet token was accepted: status=%d", recorder.Code)
	}
}
