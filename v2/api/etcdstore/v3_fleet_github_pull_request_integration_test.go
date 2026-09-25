package etcdstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestV3FleetGitHubPullRequestReservationAcceptsAndReplaysEtcd(t *testing.T) {
	adapter, _, _ := fleetRunnerEtcdStore(t)
	plan := fleetGitHubPullRequestPlan(t, adapter)
	request, reservation := fleetGitHubPullRequestAcceptance(t, adapter, plan.ID, "operator-a", "pr-a")
	first, err := adapter.AcceptFleetGitHubPullRequest(context.Background(), request, reservation)
	if err != nil {
		t.Fatal(err)
	}
	if first.Operation.Status != model.OperationQueued || first.Operation.ID == "" {
		t.Fatalf("accepted operation=%+v", first.Operation)
	}
	stored, err := adapter.GetFleetGitHubPullRequestReservation(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OperationID != first.Operation.ID || stored.PlanDigest != reservation.PlanDigest || stored.Proposed.Desired != 3 {
		t.Fatalf("stored reservation=%+v", stored)
	}
	replay, err := adapter.AcceptFleetGitHubPullRequest(context.Background(), request, reservation)
	if err != nil || !replay.Replayed || replay.Operation.ID != first.Operation.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changed := reservation
	changed.Proposed.Desired = 4
	if _, err := adapter.AcceptFleetGitHubPullRequest(context.Background(), request, changed); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("changed replay error=%v, want acceptance conflict", err)
	}
	other, otherReservation := fleetGitHubPullRequestAcceptance(t, adapter, plan.ID, "operator-b", "pr-b")
	if _, err := adapter.AcceptFleetGitHubPullRequest(context.Background(), other, otherReservation); err == nil || !strings.Contains(err.Error(), "reservation already exists") {
		t.Fatalf("second plan reservation error=%v", err)
	}
}

func TestV3FleetGitHubPullRequestReservationRejectsPlanAndPayloadMismatchesEtcd(t *testing.T) {
	adapter, _, _ := fleetRunnerEtcdStore(t)
	plan := fleetGitHubPullRequestPlan(t, adapter)
	request, reservation := fleetGitHubPullRequestAcceptance(t, adapter, plan.ID, "operator-a", "plan-mismatch")
	mismatchedPlan := reservation
	mismatchedPlan.SourceDigest = "sha256:" + strings.Repeat("c", 64)
	mismatchedRequest := fleetGitHubPullRequestRequest(t, adapter, mismatchedPlan, "operator-a", "plan-mismatch")
	if _, err := adapter.AcceptFleetGitHubPullRequest(context.Background(), mismatchedRequest, mismatchedPlan); err == nil || !strings.Contains(err.Error(), "reservation does not match") {
		t.Fatalf("plan mismatch error=%v", err)
	}
	request.Operation.Payload["pool"] = "control"
	var err error
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.AcceptFleetGitHubPullRequest(context.Background(), request, reservation); err == nil || !strings.Contains(err.Error(), "payload does not match") {
		t.Fatalf("payload mismatch error=%v", err)
	}
}

func fleetGitHubPullRequestAcceptance(t *testing.T, adapter *etcdstore.V3OperationStore, planID, subject, key string) (store.OperationAcceptance, etcdstore.FleetGitHubPullRequestReservation) {
	t.Helper()
	authority, err := adapter.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservation := etcdstore.FleetGitHubPullRequestReservation{PlanID: planID, PlanDigest: "sha256:" + strings.Repeat("a", 64), SourceDigest: "sha256:" + strings.Repeat("b", 64), Pool: "app", Action: "scale", Proposed: fleet.NodePool{Desired: 3, Labels: map[string]string{"role": "app"}}}
	return fleetGitHubPullRequestRequestWithAuthority(t, authority, reservation, subject, key), reservation
}

func fleetGitHubPullRequestRequest(t *testing.T, adapter *etcdstore.V3OperationStore, reservation etcdstore.FleetGitHubPullRequestReservation, subject, key string) store.OperationAcceptance {
	t.Helper()
	authority, err := adapter.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return fleetGitHubPullRequestRequestWithAuthority(t, authority, reservation, subject, key)
}

func fleetGitHubPullRequestRequestWithAuthority(t *testing.T, authority string, reservation etcdstore.FleetGitHubPullRequestReservation, subject, key string) store.OperationAcceptance {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: subject}, Kind: "fleet.github.pull-request", Resource: reservation.PlanID, Key: key}, Operation: model.Operation{ID: uuid.NewString(), Kind: "fleet.github.pull-request", Ref: reservation.PlanID, Status: model.OperationQueued, Source: "test", Risk: "GitHub PR reservation", StartedAt: now, MaxAttempts: 1, Payload: map[string]interface{}{"planId": reservation.PlanID, "planDigest": reservation.PlanDigest, "sourceDigest": reservation.SourceDigest, "pool": reservation.Pool, "action": reservation.Action, "proposed": reservation.Proposed}, Metadata: map[string]interface{}{}}, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"planId": reservation.PlanID, "planDigest": reservation.PlanDigest, "sourceDigest": reservation.SourceDigest, "pool": reservation.Pool, "action": reservation.Action, "proposed": reservation.Proposed}}
	var err error
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func fleetGitHubPullRequestPlan(t *testing.T, adapter *etcdstore.V3OperationStore) model.Operation {
	t.Helper()
	authority, err := adapter.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	finished := now
	capacity := fleet.CapacityPlan{SchemaVersion: "norn.fleet-capacity-plan/v1", ID: uuid.NewString(), Cluster: "staging-nyc3", Pool: "app", Current: fleet.NodePool{Desired: 2}, Proposed: fleet.NodePool{Desired: 3, Labels: map[string]string{"role": "app"}}, Action: "scale", Strategy: "blueGreen", SourceDigest: "sha256:" + strings.Repeat("b", 64), Digest: "sha256:" + strings.Repeat("a", 64)}
	payloadBytes, err := json.Marshal(capacity)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]interface{}{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatal(err)
	}
	plan := model.Operation{ID: capacity.ID, Kind: "fleet.capacity-plan", Ref: capacity.Pool, Status: model.OperationSucceeded, Source: "test", Risk: "plan", StartedAt: now, FinishedAt: &finished, MaxAttempts: 1, Payload: payload, Metadata: map[string]interface{}{}}
	request := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: plan.Kind, Resource: plan.Ref, Key: "plan-" + plan.ID}, Operation: plan, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.capacity-plan"}}
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	return plan
}
