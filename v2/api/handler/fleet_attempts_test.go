package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"norn/v2/api/fleet"
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

func TestCanonicalRunnerAttemptIDMatchesFleetCallerContract(t *testing.T) {
	ci := &CIIdentity{Repository: "acme/norn-fleet", RunID: "123456789", RunAttempt: "3"}
	if got, want := canonicalRunnerAttemptID(ci), "github-actions:acme/norn-fleet:123456789:3"; got != want {
		t.Fatalf("canonical runner attempt ID = %q, want %q", got, want)
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
