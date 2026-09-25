package etcdstore_test

import (
	"context"
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
	plan := fleetRunnerPlan(t, adapter, "scale")
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

func fleetGitHubPullRequestAcceptance(t *testing.T, adapter *etcdstore.V3OperationStore, planID, subject, key string) (store.OperationAcceptance, etcdstore.FleetGitHubPullRequestReservation) {
	t.Helper()
	authority, err := adapter.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	reservation := etcdstore.FleetGitHubPullRequestReservation{PlanID: planID, PlanDigest: "sha256:" + strings.Repeat("a", 64), SourceDigest: "sha256:" + strings.Repeat("b", 64), Pool: "app", Action: "scale", Proposed: fleet.NodePool{Desired: 3, Labels: map[string]string{"role": "app"}}}
	request := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: subject}, Kind: "fleet.github.pull-request", Resource: planID, Key: key}, Operation: model.Operation{ID: uuid.NewString(), Kind: "fleet.github.pull-request", Ref: planID, Status: model.OperationQueued, Source: "test", Risk: "GitHub PR reservation", StartedAt: now, MaxAttempts: 1, Payload: map[string]interface{}{"planId": planID, "planDigest": reservation.PlanDigest, "sourceDigest": reservation.SourceDigest, "pool": reservation.Pool, "action": reservation.Action}, Metadata: map[string]interface{}{}}, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"planId": planID, "planDigest": reservation.PlanDigest, "sourceDigest": reservation.SourceDigest, "pool": reservation.Pool, "action": reservation.Action, "proposed": reservation.Proposed}}
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	return request, reservation
}
