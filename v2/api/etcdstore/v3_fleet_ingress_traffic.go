package etcdstore

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"norn/v2/api/ingress"
)

// FleetIngressTrafficObservation joins private route readback, exact-node
// endpoint probes and normal public DNS response. It still needs accepted
// deployment binding and durable terminal fencing before active traffic.
type FleetIngressTrafficObservation struct {
	Route              FleetIngressRouteObservation
	ProbePath          string
	EndpointBodySHA256 string
	NodeEndpoints      []ingress.NodeEndpointProbe
	PublicMatched      bool
}

func (s *V3OperationStore) ObserveCurrentFleetIngressTraffic(ctx context.Context, cluster, environment string, observerPort, endpointPort int, caPEM, certPEM, keyPEM []byte, desired ingress.RenderedRoute, generation uint64, probePath, expectedBodySHA256 string, publicRoots *x509.CertPool) (*FleetIngressTrafficObservation, error) {
	if err := ingress.RequireTLSRenderedRoute(desired); err != nil {
		return nil, err
	}
	load := func(ctx context.Context) (*FleetIngressInventoryEvidence, error) {
		return s.CurrentActiveFleetIngressInventory(ctx, cluster, environment, observerPort)
	}
	readback := func(ctx context.Context) (*FleetIngressRouteObservation, error) {
		return s.ObserveCurrentFleetIngressRoute(ctx, cluster, environment, observerPort, caPEM, certPEM, keyPEM, desired, generation)
	}
	probeNodes := func(ctx context.Context, nodes []ingress.RouteProbeNode) ([]ingress.NodeEndpointProbe, error) {
		return ingress.ProbeRenderedRouteNodes(ctx, nodes, desired, probePath, expectedBodySHA256, publicRoots)
	}
	probePublic := func(ctx context.Context) error {
		return ingress.ProbeRenderedRoutePublic(ctx, desired, probePath, expectedBodySHA256, publicRoots)
	}
	return observeFleetIngressTraffic(ctx, load, readback, probeNodes, probePublic, endpointPort, probePath, expectedBodySHA256)
}

func observeFleetIngressTraffic(ctx context.Context, load func(context.Context) (*FleetIngressInventoryEvidence, error), readback func(context.Context) (*FleetIngressRouteObservation, error), probeNodes func(context.Context, []ingress.RouteProbeNode) ([]ingress.NodeEndpointProbe, error), probePublic func(context.Context) error, endpointPort int, probePath, bodySHA string) (*FleetIngressTrafficObservation, error) {
	if load == nil || readback == nil || probeNodes == nil || probePublic == nil || endpointPort < 1 || endpointPort > 65535 || probePath == "" || len(bodySHA) != 64 {
		return nil, fmt.Errorf("Fleet ingress traffic observation is incomplete")
	}
	route, err := readback(ctx)
	if err != nil {
		return nil, err
	}
	if route == nil {
		return nil, fmt.Errorf("Fleet ingress route readback is unavailable")
	}
	if route.Inventory.ActivePointerRevision <= 0 || route.Inventory.ActiveClusterEpochRevision <= 0 {
		return nil, fmt.Errorf("Fleet ingress route is not bound to the active inventory")
	}
	probeTargets := make([]ingress.RouteProbeNode, 0, len(route.Inventory.Nodes))
	for _, node := range route.Inventory.Nodes {
		parsed, err := url.Parse(node.APIURL)
		if err != nil || parsed.Scheme != "https" || net.ParseIP(parsed.Hostname()) == nil {
			return nil, fmt.Errorf("Fleet ingress probe node is invalid")
		}
		probeTargets = append(probeTargets, ingress.RouteProbeNode{ID: node.ID, DialAddress: net.JoinHostPort(parsed.Hostname(), strconv.Itoa(endpointPort))})
	}
	endpoints, err := probeNodes(ctx, probeTargets)
	if err != nil {
		return nil, err
	}
	if len(endpoints) != len(probeTargets) {
		return nil, fmt.Errorf("Fleet ingress endpoint probe omitted a node")
	}
	for i, endpoint := range endpoints {
		if endpoint.NodeID != probeTargets[i].ID || endpoint.BodySHA256 != bodySHA {
			return nil, fmt.Errorf("Fleet ingress endpoint probe differs from inventory or expected body")
		}
	}
	if err := probePublic(ctx); err != nil {
		return nil, err
	}
	after, err := load(ctx)
	if err != nil || !sameFleetIngressInventory(&route.Inventory, after) {
		return nil, fmt.Errorf("Fleet ingress inventory changed during endpoint or public probes")
	}
	return &FleetIngressTrafficObservation{Route: *route, ProbePath: probePath, EndpointBodySHA256: bodySHA, NodeEndpoints: endpoints, PublicMatched: true}, nil
}
