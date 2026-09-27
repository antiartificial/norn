package etcdstore

import (
	"context"
	"fmt"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// ObserveClaimedInitialFleetRoute derives the expected route and exact Fleet
// members from the worker's current signed intent. This is file/Traefik
// readback only; it does not prove app endpoint or public traffic.
func (s *V3OperationStore) ObserveClaimedInitialFleetRoute(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, spec *model.InfraSpec, healthEffect effect.Token, observerPort int, caPEM, certPEM, keyPEM []byte) (*FleetIngressRouteObservation, error) {
	return s.observeClaimedInitialFleetRoute(ctx, claim, lock, spec, healthEffect, observerPort, func(ctx context.Context, intent *InitialFleetRouteIntent) (*FleetIngressRouteObservation, error) {
		return s.ObserveCurrentFleetIngressRoute(ctx, intent.FleetTarget.Cluster, intent.FleetTarget.FleetEnvironment, observerPort, caPEM, certPEM, keyPEM, intent.RenderedRoute, intent.Generation)
	})
}

func (s *V3OperationStore) observeClaimedInitialFleetRoute(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, spec *model.InfraSpec, healthEffect effect.Token, observerPort int, observe func(context.Context, *InitialFleetRouteIntent) (*FleetIngressRouteObservation, error)) (*FleetIngressRouteObservation, error) {
	if observe == nil {
		return nil, fmt.Errorf("claimed route observation is unavailable")
	}
	intent, err := s.CurrentInitialFleetRouteIntent(ctx, claim, lock, spec, observerPort)
	if err != nil {
		return nil, err
	}
	if err := s.requireHealthyDeploymentEffect(ctx, claim, intent, healthEffect); err != nil {
		return nil, err
	}
	observed, err := observe(ctx, intent)
	if err != nil || observed == nil || observed.RouteSHA256 != intent.RenderedRoute.SHA256 || observed.Generation != intent.Generation || !sameFleetIngressInventory(&intent.Inventory, &observed.Inventory) {
		return nil, fmt.Errorf("claimed route observation differs from reserved intent")
	}
	if len(observed.Nodes) != len(intent.Inventory.Nodes) {
		return nil, fmt.Errorf("claimed route observation omitted an ingress node")
	}
	for i, node := range intent.Inventory.Nodes {
		if observed.Nodes[i].NodeID != node.ID || observed.Nodes[i].PublishedGeneration != intent.Generation || observed.Nodes[i].MatchedDesiredRouteSHA256 != intent.RenderedRoute.SHA256 {
			return nil, fmt.Errorf("claimed route observation differs from an ingress node")
		}
	}
	current, err := s.CurrentInitialFleetRouteIntent(ctx, claim, lock, spec, observerPort)
	if err != nil || current == nil || current.ID != intent.ID || current.RenderedRoute.SHA256 != intent.RenderedRoute.SHA256 || !sameFleetIngressInventory(&intent.Inventory, &current.Inventory) {
		return nil, fmt.Errorf("claimed route changed during readback")
	}
	return observed, nil
}
