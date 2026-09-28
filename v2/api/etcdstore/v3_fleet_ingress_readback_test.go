package etcdstore

import (
	"context"
	"testing"

	"norn/v2/api/ingress"
)

func TestObserveFleetIngressRouteRejectsInventoryReplacement(t *testing.T) {
	before := &FleetIngressInventoryEvidence{
		PlanID: "plan", AttemptID: "attempt", AttemptRevision: 7, PlanStateModRevision: 41,
		CheckpointID: "checkpoint", CheckpointModRevision: 42, StateSerial: 8, Digest: "sha256:inventory",
		ActivePointerRevision: 43, ActiveClusterEpochRevision: 44,
		Nodes: []ingress.IngressNode{{ID: "ingress-01", APIURL: "https://10.43.0.21:18082"}, {ID: "ingress-02", APIURL: "https://10.43.0.22:18082"}},
	}
	loadCount := 0
	load := func(context.Context) (*FleetIngressInventoryEvidence, error) {
		loadCount++
		copy := *before
		return &copy, nil
	}
	observe := func(_ context.Context, nodes []ingress.IngressNode) ([]ingress.NodeObservation, error) {
		if len(nodes) != 2 || nodes[1].ID != "ingress-02" {
			t.Fatal("observer did not receive complete Fleet node set")
		}
		return []ingress.NodeObservation{{NodeID: "ingress-01", MatchedDesiredRouteSHA256: "route", PublishedGeneration: 7}, {NodeID: "ingress-02", MatchedDesiredRouteSHA256: "route", PublishedGeneration: 7}}, nil
	}
	result, err := observeFleetIngressRouteWithInventory(context.Background(), load, observe, "route", 7)
	if err != nil || result == nil || loadCount != 2 || len(result.Nodes) != 2 {
		t.Fatalf("route observation = %+v, loads=%d, %v", result, loadCount, err)
	}
	loadCount = 0
	changed := func(ctx context.Context) (*FleetIngressInventoryEvidence, error) {
		value, err := load(ctx)
		if loadCount == 2 {
			value.PlanStateModRevision++
		}
		return value, err
	}
	if _, err := observeFleetIngressRouteWithInventory(context.Background(), changed, observe, "route", 7); err == nil {
		t.Fatal("concurrent Fleet replacement was accepted")
	}
	loadCount = 0
	changedPointer := func(ctx context.Context) (*FleetIngressInventoryEvidence, error) {
		value, err := load(ctx)
		if loadCount == 2 {
			value.ActivePointerRevision++
		}
		return value, err
	}
	if _, err := observeFleetIngressRouteWithInventory(context.Background(), changedPointer, observe, "route", 7); err == nil {
		t.Fatal("changed active Fleet pointer was accepted")
	}
	short := func(context.Context, []ingress.IngressNode) ([]ingress.NodeObservation, error) {
		return []ingress.NodeObservation{{NodeID: "ingress-01", MatchedDesiredRouteSHA256: "route", PublishedGeneration: 7}}, nil
	}
	if _, err := observeFleetIngressRouteWithInventory(context.Background(), load, short, "route", 7); err == nil {
		t.Fatal("partial ingress observation was accepted")
	}
}

func TestSameFleetIngressInventoryBindsClusterAndEnvironment(t *testing.T) {
	before := &FleetIngressInventoryEvidence{PlanID: "plan", Cluster: "fleet-a", Environment: "staging"}
	after := *before
	if !sameFleetIngressInventory(before, &after) {
		t.Fatal("identical inventory differs")
	}
	after.Cluster = "fleet-b"
	if sameFleetIngressInventory(before, &after) {
		t.Fatal("different Fleet cluster accepted as the same inventory")
	}
	after = *before
	after.Environment = "production"
	if sameFleetIngressInventory(before, &after) {
		t.Fatal("different Fleet environment accepted as the same inventory")
	}
}
