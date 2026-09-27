package etcdstore

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"norn/v2/api/ingress"
)

func TestObserveFleetIngressTrafficJoinsEveryNodeAndPublicPath(t *testing.T) {
	bodySHA := strings.Repeat("a", 64)
	inventory := FleetIngressInventoryEvidence{PlanID: "plan", AttemptID: "attempt", AttemptRevision: 7, PlanStateModRevision: 41,
		CheckpointID: "checkpoint", CheckpointModRevision: 42, StateSerial: 8, Digest: "sha256:inventory",
		Nodes: []ingress.IngressNode{{ID: "ingress-01", APIURL: "https://10.43.0.21:18082"}, {ID: "ingress-02", APIURL: "https://10.43.0.22:18082"}}}
	loads, publicCalls := 0, 0
	load := func(context.Context) (*FleetIngressInventoryEvidence, error) {
		loads++
		copy := inventory
		return &copy, nil
	}
	readback := func(context.Context) (*FleetIngressRouteObservation, error) {
		return &FleetIngressRouteObservation{Inventory: inventory, RouteSHA256: "route", Generation: 7}, nil
	}
	probes := func(_ context.Context, nodes []ingress.RouteProbeNode) ([]ingress.NodeEndpointProbe, error) {
		if len(nodes) != 2 || nodes[0].DialAddress != "10.43.0.21:443" || nodes[1].DialAddress != "10.43.0.22:443" {
			t.Fatalf("probe targets = %+v", nodes)
		}
		return []ingress.NodeEndpointProbe{{NodeID: "ingress-01", BodySHA256: bodySHA}, {NodeID: "ingress-02", BodySHA256: bodySHA}}, nil
	}
	public := func(context.Context) error { publicCalls++; return nil }
	result, err := observeFleetIngressTraffic(context.Background(), load, readback, probes, public, 443, "/readyz", bodySHA)
	if err != nil || result == nil || !result.PublicMatched || loads != 1 || publicCalls != 1 {
		t.Fatalf("traffic observation = %+v, loads=%d public=%d err=%v", result, loads, publicCalls, err)
	}
	if _, err := observeFleetIngressTraffic(context.Background(), load, readback, func(context.Context, []ingress.RouteProbeNode) ([]ingress.NodeEndpointProbe, error) {
		return []ingress.NodeEndpointProbe{{NodeID: "ingress-01", BodySHA256: bodySHA}}, nil
	}, public, 443, "/readyz", bodySHA); err == nil {
		t.Fatal("partial node probes accepted")
	}
	if _, err := observeFleetIngressTraffic(context.Background(), load, readback, probes, func(context.Context) error { return fmt.Errorf("wrong public response") }, 443, "/readyz", bodySHA); err == nil {
		t.Fatal("failed public probe accepted")
	}
	changed := func(context.Context) (*FleetIngressInventoryEvidence, error) {
		copy := inventory
		copy.PlanStateModRevision++
		return &copy, nil
	}
	if _, err := observeFleetIngressTraffic(context.Background(), changed, readback, probes, public, 443, "/readyz", bodySHA); err == nil {
		t.Fatal("changed Fleet inventory accepted")
	}
}
