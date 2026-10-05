package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet/executor"
	"norn/v2/api/model"
)

func attemptCredentialPrincipal(attemptID string) *AccessPrincipal {
	return &AccessPrincipal{Subject: fleetAttemptSubjectPrefix + attemptID, Source: AccessPrincipalSourceManagedToken, TokenID: "jti-cred",
		Scopes: []string{ScopeFleetAttempt}, ExpiresAt: time.Now().Add(time.Hour)}
}

func executorLaneAttempt(status model.FleetRunnerAttemptStatus) *model.FleetRunnerAttempt {
	planID := uuid.NewString()
	droplet, invocation := "123456789", strings.Repeat("a", 32)
	return &model.FleetRunnerAttempt{
		ID: uuid.NewString(), PlanID: planID, RunnerAttemptID: executor.RunnerAttemptID(droplet, invocation, planID), WorkflowURL: executor.WorkflowURL(droplet, invocation),
		PrincipalSubject: executor.PrincipalSubject("device-1"), Status: status,
		Metadata: map[string]interface{}{"lane": executor.LaneMetadataValue, "dropletId": droplet, "invocationId": invocation},
	}
}

// TestAttemptCredentialOwnsOnlyItsLiveExecutorAttempt pins the third lane's
// ownership: the credential's own attempt, executor lane, and (for writes)
// live; reads may outlive the attempt.
func TestAttemptCredentialOwnsOnlyItsLiveExecutorAttempt(t *testing.T) {
	attempt := executorLaneAttempt(model.FleetRunnerAttemptRunning)
	credential := attemptCredentialPrincipal(attempt.ID)
	if !fleetRunnerPrincipalOwnsAttempt(*credential, attempt) || !fleetRunnerCanReadAttempt(*credential, attempt) {
		t.Fatal("the credential does not own its own live executor attempt")
	}
	if fleetRunnerPrincipalOwnsAttempt(*attemptCredentialPrincipal(uuid.NewString()), attempt) {
		t.Fatal("a credential bound to another attempt owns this one")
	}
	finished := executorLaneAttempt(model.FleetRunnerAttemptSucceeded)
	finishedCredential := attemptCredentialPrincipal(finished.ID)
	if fleetRunnerPrincipalOwnsAttempt(*finishedCredential, finished) {
		t.Fatal("the credential of a finished attempt may still write")
	}
	if !fleetRunnerCanReadAttempt(*finishedCredential, finished) || !fleetRunnerCanReadAttemptEvidence(*finishedCredential, finished) {
		t.Fatal("the credential of a finished attempt may not read its evidence")
	}
	// A GitHub-lane attempt is never reachable.
	github := executorLaneAttempt(model.FleetRunnerAttemptRunning)
	github.SourceDispatchRunID = 7
	if fleetRunnerPrincipalOwnsAttempt(*attemptCredentialPrincipal(github.ID), github) {
		t.Fatal("the credential owns a GitHub-lane attempt")
	}
	broken := executorLaneAttempt(model.FleetRunnerAttemptRunning)
	broken.Metadata = nil
	if fleetRunnerPrincipalOwnsAttempt(*attemptCredentialPrincipal(broken.ID), broken) {
		t.Fatal("the credential owns an attempt with no executor metadata")
	}
	// A device claim makes it no credential at all.
	withDevice := attemptCredentialPrincipal(attempt.ID)
	withDevice.DeviceID = "device-1"
	if fleetRunnerPrincipalOwnsAttempt(*withDevice, attempt) || isFleetAttemptCredential(*withDevice) {
		t.Fatal("a fleet:attempt principal with a device claim is the credential")
	}
}

// TestAttemptCredentialCannotStartOrCancel: start and cancel stay on the
// executor principal and the GitHub lane; the handlers refuse the credential
// even if a router matcher admitted it.
func TestAttemptCredentialCannotStartOrCancel(t *testing.T) {
	attempt := executorLaneAttempt(model.FleetRunnerAttemptRunning)
	for name, call := range map[string]func(*Handler, http.ResponseWriter, *http.Request){
		"start":  (*Handler).StartFleetRunnerAttempt,
		"cancel": (*Handler).CancelFleetRunnerAttempt,
	} {
		req := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+attempt.PlanID+"/attempts", strings.NewReader("{}")), attemptCredentialPrincipal(attempt.ID))
		rec := httptest.NewRecorder()
		call(&Handler{}, rec, req)
		if rec.Code != http.StatusForbidden || decodeProblemCode(rec) != "insufficient_scope" {
			t.Errorf("%s: %d %s, want 403 insufficient_scope", name, rec.Code, rec.Body.String())
		}
	}
}
