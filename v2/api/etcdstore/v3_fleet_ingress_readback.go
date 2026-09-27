package etcdstore

import (
	"context"
	"fmt"
	"slices"

	"norn/v2/api/ingress"
)

// FleetIngressRouteObservation is a control-side observation, not a terminal
// traffic proof. Endpoint and public-path probes remain separate.
type FleetIngressRouteObservation struct {
	Inventory   FleetIngressInventoryEvidence
	RouteSHA256 string
	Generation  uint64
	Nodes       []ingress.NodeObservation
}

func (s *V3OperationStore) ObserveCurrentFleetIngressRoute(ctx context.Context, cluster, environment string, port int, caPEM, certPEM, keyPEM []byte, desired ingress.RenderedRoute, generation uint64) (*FleetIngressRouteObservation, error) {
	load := func(ctx context.Context) (*FleetIngressInventoryEvidence, error) {
		return s.CurrentActiveFleetIngressInventory(ctx, cluster, environment, port)
	}
	observe := func(ctx context.Context, nodes []ingress.IngressNode) ([]ingress.NodeObservation, error) {
		return ingress.ObservePublishedRenderedRouteWithTLS(ctx, caPEM, certPEM, keyPEM, nodes, desired, generation)
	}
	return observeFleetIngressRouteWithInventory(ctx, load, observe, desired.SHA256, generation)
}

func observeFleetIngressRouteWithInventory(ctx context.Context, load func(context.Context) (*FleetIngressInventoryEvidence, error), observe func(context.Context, []ingress.IngressNode) ([]ingress.NodeObservation, error), desiredSHA string, generation uint64) (*FleetIngressRouteObservation, error) {
	if load == nil || observe == nil || desiredSHA == "" || generation == 0 {
		return nil, fmt.Errorf("Fleet ingress route observation is incomplete")
	}
	before, err := load(ctx)
	if err != nil {
		return nil, err
	}
	if before == nil || before.PlanStateModRevision <= 0 || before.CheckpointModRevision <= 0 || before.ActivePointerRevision <= 0 || before.ActiveClusterEpochRevision <= 0 || len(before.Nodes) < 2 {
		return nil, fmt.Errorf("Fleet ingress inventory is unavailable for route observation")
	}
	observed, err := observe(ctx, before.Nodes)
	if err != nil {
		return nil, err
	}
	if len(observed) != len(before.Nodes) {
		return nil, fmt.Errorf("Fleet ingress route readback omitted a node")
	}
	for i, node := range before.Nodes {
		if observed[i].NodeID != node.ID || observed[i].MatchedDesiredRouteSHA256 != desiredSHA || observed[i].PublishedGeneration != generation {
			return nil, fmt.Errorf("Fleet ingress route readback differs from inventory or desired revision")
		}
	}
	after, err := load(ctx)
	if err != nil || !sameFleetIngressInventory(before, after) {
		return nil, fmt.Errorf("Fleet ingress inventory changed during route readback")
	}
	return &FleetIngressRouteObservation{Inventory: *before, RouteSHA256: desiredSHA, Generation: generation, Nodes: observed}, nil
}

func sameFleetIngressInventory(before, after *FleetIngressInventoryEvidence) bool {
	return before != nil && after != nil && after.PlanID == before.PlanID && after.AttemptID == before.AttemptID && after.AttemptRevision == before.AttemptRevision && after.PlanStateModRevision == before.PlanStateModRevision && after.CheckpointID == before.CheckpointID && after.CheckpointModRevision == before.CheckpointModRevision && after.StateSerial == before.StateSerial && after.Digest == before.Digest && after.ActivePointerRevision == before.ActivePointerRevision && after.ActiveClusterEpochRevision == before.ActiveClusterEpochRevision && slices.Equal(after.Nodes, before.Nodes)
}
