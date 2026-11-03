package etcdstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/ingress"
	"norn/v2/api/model"
)

func TestFleetTrafficObservationWaitsForReadbackPropagation(t *testing.T) {
	sha := strings.Repeat("a", 64)
	body := strings.Repeat("b", 64)
	inventory := FleetIngressInventoryEvidence{Nodes: []ingress.IngressNode{{ID: "ingress-01"}, {ID: "ingress-02"}}}
	intent := &InitialFleetRouteIntent{Generation: 2, RenderedRoute: ingress.RenderedRoute{SHA256: sha}, Inventory: inventory, Endpoint: model.FleetRouteEndpoint{ProbePath: "/route-proof", ProbeBodySHA256: body}}
	ready := &FleetIngressTrafficObservation{Route: FleetIngressRouteObservation{Inventory: inventory, RouteSHA256: sha, Generation: 2, Nodes: []ingress.NodeObservation{{NodeID: "ingress-01", MatchedDesiredRouteSHA256: sha, PublishedGeneration: 2}, {NodeID: "ingress-02", MatchedDesiredRouteSHA256: sha, PublishedGeneration: 2}}}, ProbePath: "/route-proof", EndpointBodySHA256: body, NodeEndpoints: []ingress.NodeEndpointProbe{{NodeID: "ingress-01", BodySHA256: body}, {NodeID: "ingress-02", BodySHA256: body}}, PublicMatched: true}
	attempts := 0
	observed, err := observeFleetTrafficUntilReady(context.Background(), intent, func(context.Context, *InitialFleetRouteIntent) (*FleetIngressTrafficObservation, error) {
		attempts++
		if attempts == 1 {
			return nil, ingress.ErrRoutePropagationPending
		}
		return ready, nil
	}, time.Millisecond)
	if err != nil || observed != ready || attempts != 2 {
		t.Fatalf("propagation observation=%+v attempts=%d err=%v", observed, attempts, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := observeFleetTrafficUntilReady(ctx, intent, func(context.Context, *InitialFleetRouteIntent) (*FleetIngressTrafficObservation, error) {
		return nil, ingress.ErrRoutePropagationPending
	}, time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded observation error=%v", err)
	}
	attempts = 0
	if _, err := observeFleetTrafficUntilReady(context.Background(), intent, func(context.Context, *InitialFleetRouteIntent) (*FleetIngressTrafficObservation, error) {
		attempts++
		return nil, errors.New("TLS verification failed")
	}, time.Millisecond); err == nil || attempts != 1 {
		t.Fatalf("non-propagation error retried: attempts=%d err=%v", attempts, err)
	}
	wrong := *ready
	wrong.EndpointBodySHA256 = strings.Repeat("c", 64)
	attempts = 0
	if _, err := observeFleetTrafficUntilReady(context.Background(), intent, func(context.Context, *InitialFleetRouteIntent) (*FleetIngressTrafficObservation, error) {
		attempts++
		return &wrong, nil
	}, time.Millisecond); err == nil || attempts != 1 {
		t.Fatalf("invalid successful observation retried: attempts=%d err=%v", attempts, err)
	}
}
