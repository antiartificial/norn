package etcdstore

import (
	"context"
	"crypto/x509"
	"fmt"

	"norn/v2/api/effect"
	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type InitialFleetRoutePublication struct {
	Intent   *InitialFleetRouteIntent
	Receipts []ingress.NodePublicationReceipt
	Proof    *InitialFleetTrafficProof
}

type initialFleetNodePublisher func(context.Context, []ingress.IngressNode, string, uint64, string) ([]ingress.NodePublicationReceipt, error)
type initialFleetTrafficObserver func(context.Context, *InitialFleetRouteIntent) (*FleetIngressTrafficObservation, error)

// PublishProveCompleteClaimedInitialFleetRoute requires the caller to keep
// ServeClaimedInitialFleetRouteAuthority active for this claim during fanout.
// Node publishers receive only the reserved intent ID; actual file, Traefik,
// endpoint and public readback is independent of their immediate receipts.
func (s *V3OperationStore) PublishProveCompleteClaimedInitialFleetRoute(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock,
	spec *model.InfraSpec, healthEffect effect.Token, observerPort, publisherPort, endpointPort int,
	publisherCAPEM, publisherCertPEM, publisherKeyPEM, observerCAPEM, observerCertPEM, observerKeyPEM []byte,
	publicRoots *x509.CertPool) (*InitialFleetRoutePublication, error) {
	publish := func(ctx context.Context, nodes []ingress.IngressNode, intentID string, generation uint64, routeSHA string) ([]ingress.NodePublicationReceipt, error) {
		return ingress.PublishRouteIntentToNodesWithTLS(ctx, publisherCAPEM, publisherCertPEM, publisherKeyPEM, nodes, publisherPort, intentID, generation, routeSHA)
	}
	observe := func(ctx context.Context, intent *InitialFleetRouteIntent) (*FleetIngressTrafficObservation, error) {
		return s.ObserveCurrentFleetIngressTraffic(ctx, intent.FleetTarget.Cluster, intent.FleetTarget.FleetEnvironment, observerPort, endpointPort,
			observerCAPEM, observerCertPEM, observerKeyPEM, intent.RenderedRoute, intent.Generation,
			intent.Endpoint.ProbePath, intent.Endpoint.ProbeBodySHA256, publicRoots)
	}
	return s.publishProveCompleteClaimedInitialFleetRoute(ctx, claim, lock, spec, healthEffect, observerPort, publish, observe)
}

func (s *V3OperationStore) publishProveCompleteClaimedInitialFleetRoute(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock,
	spec *model.InfraSpec, healthEffect effect.Token, observerPort int, publish initialFleetNodePublisher,
	observe initialFleetTrafficObserver) (*InitialFleetRoutePublication, error) {
	if s == nil || publish == nil || observe == nil {
		return nil, fmt.Errorf("claimed Fleet route publication is unavailable")
	}
	intent, err := s.IntendInitialFleetRoute(ctx, claim, lock, spec, observerPort)
	if err != nil {
		return nil, err
	}
	if err := s.requireHealthyDeploymentEffect(ctx, claim, intent, healthEffect); err != nil {
		return nil, err
	}
	if _, err := s.CurrentInitialFleetRouteIntent(ctx, claim, lock, spec, observerPort); err != nil {
		return nil, err
	}
	resource := "app/" + intent.App + "/route/" + intent.Region
	pending := func(reason string, cause error) error {
		return &effect.PendingError{EffectID: healthEffect.EffectID, Resource: resource, Reason: reason, Cause: cause}
	}
	receipts, err := publish(ctx, intent.Inventory.Nodes, intent.ID, intent.Generation, intent.RenderedRoute.SHA256)
	if err != nil || len(receipts) != len(intent.Inventory.Nodes) {
		return &InitialFleetRoutePublication{Intent: intent, Receipts: receipts}, pending("ingress node publication requires reconciliation", err)
	}
	for i, node := range intent.Inventory.Nodes {
		if receipts[i].NodeID != node.ID || receipts[i].Generation != intent.Generation || receipts[i].RouteSHA256 != intent.RenderedRoute.SHA256 {
			return &InitialFleetRoutePublication{Intent: intent, Receipts: receipts}, pending("ingress node publication returned a different revision", nil)
		}
	}
	proof, err := s.recordClaimedInitialFleetTrafficProof(ctx, claim, lock, spec, healthEffect, observerPort, observe)
	if err != nil {
		return &InitialFleetRoutePublication{Intent: intent, Receipts: receipts}, pending("ingress traffic proof requires reconciliation", err)
	}
	result := &InitialFleetRoutePublication{Intent: intent, Receipts: receipts, Proof: proof}
	if err := s.CompleteClaimedInitialFleetDeployment(ctx, claim, lock, spec, observerPort); err != nil {
		return result, pending("proved route could not complete deployment", err)
	}
	return result, nil
}
