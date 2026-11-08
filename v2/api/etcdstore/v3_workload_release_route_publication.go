package etcdstore

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log"

	"norn/v2/api/effect"
	"norn/v2/api/ingress"
	"norn/v2/api/store"
)

type WorkloadReleaseRoutePublication struct {
	Intent   *WorkloadReleaseRouteIntent
	Receipts []ingress.NodePublicationReceipt
	Receipt  *store.WorkloadReleaseReceiptRecord
}

type workloadRouteNodePublisher func(context.Context, []ingress.IngressNode, string, uint64, string) ([]ingress.NodePublicationReceipt, error)

// PublishProveCommitClaimedWorkloadReleaseRouteStep runs one admitted route
// transition under the current operation claim. The ingress authority listener
// for the same claim must remain active while node fanout is in progress.
func (s *V3OperationStore) PublishProveCommitClaimedWorkloadReleaseRouteStep(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock,
	step, observerPort, publisherPort, endpointPort int,
	publicationCAPEM, publicationCertPEM, publicationKeyPEM []byte,
	observerCAPEM, observerCertPEM, observerKeyPEM []byte, publicRoots *x509.CertPool) (*WorkloadReleaseRoutePublication, error) {
	loadInventory := func(ctx context.Context, cluster, environment string, port int) (*FleetIngressInventoryEvidence, error) {
		return s.CurrentActiveFleetIngressInventory(ctx, cluster, environment, port)
	}
	observePredecessor := func(ctx context.Context, cluster, environment string, port int, ca, cert, key []byte, desired ingress.RenderedRoute, generation uint64) (*FleetIngressRouteObservation, error) {
		return s.ObserveCurrentFleetIngressRoute(ctx, cluster, environment, port, ca, cert, key, desired, generation)
	}
	publish := func(ctx context.Context, nodes []ingress.IngressNode, intentID string, generation uint64, routeSHA string) ([]ingress.NodePublicationReceipt, error) {
		return ingress.PublishRouteIntentToNodesWithTLS(ctx, publicationCAPEM, publicationCertPEM, publicationKeyPEM, nodes, publisherPort, intentID, generation, routeSHA)
	}
	return s.publishProveCommitClaimedWorkloadReleaseRouteStep(ctx, claim, lock, step, observerPort, endpointPort,
		publisherPort, observerCAPEM, observerCertPEM, observerKeyPEM, loadInventory, observePredecessor, publish, func(ctx context.Context, route WorkloadReleaseRouteIntent) (*FleetIngressTrafficObservation, error) {
			return s.ObserveCurrentFleetIngressTraffic(ctx, route.FleetTarget.Cluster, route.FleetTarget.FleetEnvironment, observerPort, endpointPort,
				observerCAPEM, observerCertPEM, observerKeyPEM, route.RenderedRoute, route.Generation,
				route.TrafficProbePath, route.TrafficProbeBodySHA256, publicRoots)
		})
}

func (s *V3OperationStore) publishProveCommitClaimedWorkloadReleaseRouteStep(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock,
	step, observerPort, endpointPort, publisherPort int, observerCAPEM, observerCertPEM, observerKeyPEM []byte,
	loadInventory workloadRouteInventoryLoader, observePredecessor workloadRoutePredecessorObserver, publish workloadRouteNodePublisher,
	observe func(context.Context, WorkloadReleaseRouteIntent) (*FleetIngressTrafficObservation, error)) (result *WorkloadReleaseRoutePublication, resultErr error) {
	stage := "route-intent"
	defer func() {
		if resultErr == nil {
			return
		}
		class := "verification-or-store"
		if effect.IsDeferred(resultErr) {
			class = "pending-effect"
		} else if errors.Is(resultErr, store.ErrOperationOwnershipLost) {
			class = "ownership-lost"
		}
		log.Printf("workload route publication rejected operation=%s generation=%d stage=%s class=%s", claim.OperationID(), claim.Generation(), stage, class)
	}()
	if s == nil || lock == nil || loadInventory == nil || observePredecessor == nil || publish == nil || observe == nil || publisherPort < 1024 || publisherPort > 65535 || observerPort < 1024 || observerPort > 65535 || endpointPort < 1 || endpointPort > 65535 {
		return nil, fmt.Errorf("claimed workload route publication is unavailable")
	}
	intent, err := s.intendClaimedWorkloadReleaseRouteStep(ctx, claim, lock, step, observerPort, observerCAPEM, observerCertPEM, observerKeyPEM, loadInventory, observePredecessor)
	if err != nil {
		return nil, err
	}
	workloadIntent, _, err := s.VerifyClaimedWorkloadRelease(ctx, claim, lock)
	if err != nil {
		return nil, err
	}
	result = &WorkloadReleaseRoutePublication{Intent: intent}
	pending := func(reason string, cause error) error {
		return &effect.PendingError{Resource: "app/" + workloadIntent.App + "/route/" + workloadIntent.Region, Reason: reason, Cause: cause}
	}
	stage = "node-publication"
	receipts, err := publish(ctx, intent.Inventory.Nodes, intent.ID, intent.Generation, intent.RenderedRoute.SHA256)
	result.Receipts = receipts
	if err != nil || len(receipts) != len(intent.Inventory.Nodes) {
		return result, pending("workload ingress node publication requires same-intent reconciliation", err)
	}
	for i, node := range intent.Inventory.Nodes {
		if receipts[i].NodeID != node.ID || receipts[i].Generation != intent.Generation || receipts[i].RouteSHA256 != intent.RenderedRoute.SHA256 {
			return result, pending("workload ingress publication receipt differs from the reserved route", nil)
		}
	}
	stage = "route-and-traffic-proof"
	committed, err := s.commitClaimedWorkloadReleaseRouteStep(ctx, claim, lock, intent.ID, observerPort, observe)
	if err != nil {
		return result, pending("workload route requires fresh ingress and public traffic proof", err)
	}
	result.Receipt = committed
	return result, nil
}
