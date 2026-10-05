package etcdstore

// TestFleetFenceTwoAdapterAcquireRaceEtcd is WP8b's two-adapter acquire race
// (plan.md §3 WP8b, fixing plan-review.md m10): two independent
// V3OperationStore adapters, standing in for two separate API processes,
// race to acquire the same registered target's fence for two different
// plans through two different cluster aliases of that one target. Exactly
// one must win; the loser must observe lifecycle.CodeFleetTargetExecutionOccupied
// (the m10 race-loser re-read), and the fence's final state must match
// whichever plan actually committed, never a mix of both.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/internal/integrationtest"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// newFleetFenceRaceAdapter builds an independent V3OperationStore bound to
// its own etcd client connection, but the same prefix and authority as
// every other adapter the caller builds this way, so they contend over the
// same durable fence state the way two API processes would.
func newFleetFenceRaceAdapter(t *testing.T, prefix, authority string, signer store.AcceptanceSigner) *V3OperationStore {
	t.Helper()
	client := integrationtest.EtcdClient(t)
	adapter, err := NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

// fleetFenceRaceSeedPlan writes a minimal succeeded fleet.capacity-plan
// operation directly (the same shortcut fleet_target_conformance_test.go's
// SeedInFlightDispatch takes for its preparation fixture), so each racer's
// AcceptFleetGitHubDispatch has a plan to load and bind against without
// going through the full signed capacity-plan flow, which is irrelevant to
// this race. It still carries a real digest/sourceDigest so
// validateFleetGitHubDispatchPlanBinding's envelope match succeeds.
func fleetFenceRaceSeedPlan(t *testing.T, adapter *V3OperationStore, cluster string) (planID, digest, sourceDigest string) {
	t.Helper()
	ctx := context.Background()
	planID = uuid.NewString()
	digest, sourceDigest = "sha256:"+strings.Repeat("2", 64), "sha256:"+strings.Repeat("1", 64)
	now := time.Now().UTC().Truncate(time.Microsecond)
	capacityPlan := fleet.CapacityPlan{
		SchemaVersion: "norn.fleet-capacity-plan/v1", ID: planID, Cluster: cluster, Pool: "workers",
		Current: fleet.NodePool{Desired: 3}, Proposed: fleet.NodePool{Desired: 3}, Action: "scale",
		SourceDigest: sourceDigest, Digest: digest,
	}
	encoded, err := json.Marshal(capacityPlan)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	plan := model.Operation{
		ID: planID, Kind: "fleet.capacity-plan", Ref: "workers", Status: model.OperationSucceeded,
		Source: "test", Risk: "plan", StartedAt: now, UpdatedAt: now, FinishedAt: &now, MaxAttempts: 1,
		Payload: payload, Metadata: map[string]interface{}{},
	}
	record, err := json.Marshal(v3Record{Operation: plan})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.kv.Put(ctx, adapter.opKey(planID), string(record)); err != nil {
		t.Fatal(err)
	}
	return planID, digest, sourceDigest
}

func fleetFenceRaceDispatchAcceptance(authority, planID, planDigest, sourceDigest string) store.OperationAcceptance {
	payload := map[string]interface{}{
		"planId": planID, "planDigest": planDigest, "sourceDigest": sourceDigest, "planRunId": int64(1),
		"planSha256": strings.Repeat("b", 64), "approvedHeadSha": strings.Repeat("c", 40),
		"fleetEnvironment": "staging/nyc3", "allowDestructive": false,
	}
	now := time.Now().UTC()
	op := model.Operation{
		ID: uuid.NewString(), Kind: "fleet.github.apply-dispatch", Ref: planID, Status: model.OperationQueued,
		Source: "test", Risk: "test", Payload: map[string]interface{}{"fleetGitHub": payload}, Metadata: map[string]interface{}{},
		StartedAt: now, MaxAttempts: 1,
	}
	acceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/fleet-github", Subject: planID}, Kind: op.Kind, Resource: planID, Key: "protected-plan-receipt/v1"},
		Operation: op, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"fleetGitHub": payload},
	}
	return acceptance
}

func TestFleetFenceTwoAdapterAcquireRaceEtcd(t *testing.T) {
	client, prefix := integrationtest.Etcd(t)
	authority := uuid.NewString()
	signer, err := store.NewHMACAcceptanceSigner("norn-fleet-fence-race-signing-key")
	if err != nil {
		t.Fatal(err)
	}

	// One registrar adapter sets up the shared fixture: one target aliased
	// by two distinct clusters.
	registrar, err := NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "fence-race", StateBackend: "s3://fence-race/state"}
	target, err := registrar.RegisterFleetTarget(context.Background(), identity, []string{"cluster:fence-race-a", "cluster:fence-race-b"}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}

	adapterA := newFleetFenceRaceAdapter(t, prefix, authority, signer)
	adapterB := newFleetFenceRaceAdapter(t, prefix, authority, signer)
	planA, digestA, sourceDigestA := fleetFenceRaceSeedPlan(t, adapterA, "fence-race-a")
	planB, digestB, sourceDigestB := fleetFenceRaceSeedPlan(t, adapterB, "fence-race-b")

	acceptanceA := fleetFenceRaceDispatchAcceptance(authority, planA, digestA, sourceDigestA)
	acceptanceA.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptanceA)
	if err != nil {
		t.Fatal(err)
	}
	acceptanceB := fleetFenceRaceDispatchAcceptance(authority, planB, digestB, sourceDigestB)
	acceptanceB.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptanceB)
	if err != nil {
		t.Fatal(err)
	}
	preparedA := FleetGitHubDispatchPreparation{PlanID: planA, PlanRunID: 1, PlanSHA256: strings.Repeat("b", 64), ApprovedHeadSHA: strings.Repeat("c", 40), FleetEnvironment: "staging/nyc3"}
	preparedB := FleetGitHubDispatchPreparation{PlanID: planB, PlanRunID: 1, PlanSHA256: strings.Repeat("b", 64), ApprovedHeadSHA: strings.Repeat("c", 40), FleetEnvironment: "staging/nyc3"}

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, errA = adapterA.AcceptFleetGitHubDispatch(context.Background(), acceptanceA, preparedA)
	}()
	go func() {
		defer wg.Done()
		_, _, errB = adapterB.AcceptFleetGitHubDispatch(context.Background(), acceptanceB, preparedB)
	}()
	wg.Wait()

	winnerPlan, loserErr := "", error(nil)
	switch {
	case errA == nil && errB != nil:
		winnerPlan, loserErr = planA, errB
	case errB == nil && errA != nil:
		winnerPlan, loserErr = planB, errA
	case errA == nil && errB == nil:
		t.Fatalf("both racers acquired the fence: errA=%v errB=%v", errA, errB)
	default:
		t.Fatalf("both racers were refused: errA=%v errB=%v", errA, errB)
	}

	var fenceErr *lifecycle.FenceError
	if !errors.As(loserErr, &fenceErr) || fenceErr.Code != lifecycle.CodeFleetTargetExecutionOccupied {
		t.Fatalf("loser must observe %s (m10), got: %v", lifecycle.CodeFleetTargetExecutionOccupied, loserErr)
	}

	facts, _, err := registrar.GetFleetTargetFence(context.Background(), target.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Held || facts.HolderPlanID != winnerPlan {
		t.Fatalf("fence must end up held by the winner %s only: %+v", winnerPlan, facts)
	}
	if facts.Generation != 1 {
		t.Fatalf("exactly one acquire must have committed, generation = %d, want 1", facts.Generation)
	}

	// The loser's plan must never have bound a dispatch (its Accept never
	// committed), and the winner's must still be the only runner-attempt
	// dispatch path usable for this target.
	loserPlan := planA
	if winnerPlan == planA {
		loserPlan = planB
	}
	if _, err := registrar.GetFleetGitHubDispatchPreparation(context.Background(), loserPlan); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the race loser's plan must have no preparation committed, got err=%v", err)
	}
	if _, err := registrar.GetFleetGitHubDispatchPreparation(context.Background(), winnerPlan); err != nil {
		t.Fatalf("the race winner's plan must have a committed preparation: %v", err)
	}
}
