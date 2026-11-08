package etcdstore

import (
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// CommitClaimedWorkloadReleaseRouteStep independently verifies ingress route
// readback and public traffic, then atomically advances the active route and
// the workload receipt. The caller may safely retry after a lost response.
func (s *V3OperationStore) CommitClaimedWorkloadReleaseRouteStep(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock,
	stepIntentID string, observerPort, endpointPort int, caPEM, certPEM, keyPEM []byte, publicRoots *x509.CertPool) (*store.WorkloadReleaseReceiptRecord, error) {
	return s.commitClaimedWorkloadReleaseRouteStep(ctx, claim, lock, stepIntentID, observerPort, func(ctx context.Context, intent WorkloadReleaseRouteIntent) (*FleetIngressTrafficObservation, error) {
		return s.ObserveCurrentFleetIngressTraffic(ctx, intent.FleetTarget.Cluster, intent.FleetTarget.FleetEnvironment,
			observerPort, endpointPort, caPEM, certPEM, keyPEM, intent.RenderedRoute, intent.Generation,
			intent.TrafficProbePath, intent.TrafficProbeBodySHA256, publicRoots)
	})
}

// The injected observer keeps the same production commit fences available to
// deterministic tests without making network calls.
func (s *V3OperationStore) commitClaimedWorkloadReleaseRouteStep(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock,
	stepIntentID string, observerPort int, observe func(context.Context, WorkloadReleaseRouteIntent) (*FleetIngressTrafficObservation, error)) (*store.WorkloadReleaseReceiptRecord, error) {
	if s == nil || s.kv == nil || lock == nil || observe == nil || stepIntentID == "" || observerPort < 1024 || observerPort > 65535 {
		return nil, fmt.Errorf("workload route commit inputs are incomplete")
	}
	intent, accepted, err := s.VerifyClaimedWorkloadRelease(ctx, claim, lock)
	if err != nil {
		return nil, err
	}
	routeResponse, err := s.kv.Get(ctx, s.workloadReleaseRouteIntentKey(stepIntentID))
	if err != nil || len(routeResponse.Kvs) != 1 {
		return nil, fmt.Errorf("durable workload route intent is unavailable: %v", err)
	}
	var route WorkloadReleaseRouteIntent
	if decodeV3Record(routeResponse.Kvs[0].Value, &route) != nil || route.SchemaVersion != workloadReleaseRouteIntentSchema || route.ID != stepIntentID ||
		route.IntentID != intent.ID || route.OperationID != claim.OperationID() || route.AcceptanceDigest != accepted.Intent.CanonicalDigest || route.Step < 0 || route.Step >= len(intent.RouteSteps) ||
		route.Generation != intent.FirstRouteGeneration+uint64(route.Step) || route.ID != workloadReleaseRouteIntentID(intent.ID, route.Step) {
		return nil, fmt.Errorf("durable workload route intent differs from signed acceptance")
	}
	rendered, err := intent.RenderRouteStep(route.Step)
	if err != nil || !reflect.DeepEqual(rendered, route.RenderedRoute) {
		return nil, fmt.Errorf("workload route bytes differ from signed intent")
	}
	receiptRecord, err := s.LoadWorkloadReleaseReceipt(ctx, intent.ID)
	if err != nil || receiptRecord == nil || receiptRecord.OperationID != claim.OperationID() {
		return nil, fmt.Errorf("workload route receipt predecessor changed")
	}
	var receipt pipeline.WorkloadReleaseReceipt
	if json.Unmarshal(receiptRecord.Receipt, &receipt) != nil || receipt.ValidateAgainst(intent) != nil {
		return nil, fmt.Errorf("workload route receipt is invalid")
	}
	if receiptRecord.ReceiptDigest != route.ReceiptDigest {
		// A previous attempt may have committed both records and lost its
		// response. Accept only the exact successor after fresh route proof.
		activeKey := s.fleetActiveRouteKey(intent.App, intent.ControlEnvironment, intent.Region)
		active, activeErr := s.kv.Get(ctx, activeKey)
		wantRoute := activeFleetRoute{IntentID: intent.ID, DeploymentID: intent.TargetDeploymentID, Generation: route.Generation, RouteSHA256: route.RenderedRoute.SHA256}
		var actualRoute activeFleetRoute
		observation, observeErr := observe(ctx, route)
		inventory, inventoryErr := s.CurrentActiveFleetIngressInventory(ctx, intent.FleetCluster, intent.FleetEnvironment, observerPort)
		if activeErr == nil && len(active.Kvs) == 1 && decodeV3Record(active.Kvs[0].Value, &actualRoute) == nil && actualRoute == wantRoute &&
			receipt.RouteStep == route.Step && receipt.RouteGeneration == route.Generation && receipt.RouteSHA256 == "sha256:"+route.RenderedRoute.SHA256 &&
			receipt.RouteOldWeight == intent.RouteSteps[route.Step].OldWeight && receipt.RouteTargetWeight == intent.RouteSteps[route.Step].TargetWeight &&
			observeErr == nil && workloadTrafficObservationMatches(intent, route, observation) && inventoryErr == nil && sameFleetIngressInventory(&route.Inventory, inventory) {
			return receiptRecord, nil
		}
		return nil, fmt.Errorf("workload route commit retry is not the exact verified successor")
	}
	if !workloadRouteStepAllowed(intent, receipt, route.Step) {
		return nil, fmt.Errorf("workload route step is no longer authorized by receipt progress")
	}
	if receipt.RouteGeneration > 0 && (receipt.RouteGeneration+1 != route.Generation || receipt.RouteStep+1 != route.Step) {
		return nil, fmt.Errorf("workload route is not the next receipt generation")
	}
	if receipt.RouteGeneration == 0 && (route.Step != 0 || route.Generation != intent.FirstRouteGeneration) {
		return nil, fmt.Errorf("initial workload route generation differs from intent")
	}
	reservationKey := s.fleetRouteReservationKey(intent.App, intent.ControlEnvironment, intent.Region)
	reservation, err := s.kv.Get(ctx, reservationKey)
	if err != nil || len(reservation.Kvs) != 1 {
		return nil, fmt.Errorf("workload route reservation is unavailable")
	}
	var reserved initialFleetRouteReservation
	if decodeV3Record(reservation.Kvs[0].Value, &reserved) != nil || reserved.IntentID != intent.ID || reserved.Generation != route.Generation {
		return nil, fmt.Errorf("workload route reservation differs from durable intent")
	}
	activeKey := s.fleetActiveRouteKey(intent.App, intent.ControlEnvironment, intent.Region)
	active, err := s.kv.Get(ctx, activeKey)
	if err != nil || len(active.Kvs) != 1 || active.Kvs[0].ModRevision != route.PriorActiveRevision {
		return nil, fmt.Errorf("workload route active predecessor changed")
	}
	var prior activeFleetRoute
	if decodeV3Record(active.Kvs[0].Value, &prior) != nil || prior != route.PriorActive {
		return nil, fmt.Errorf("workload route active predecessor differs from durable intent")
	}
	observation, err := observe(ctx, route)
	if err != nil || !workloadTrafficObservationMatches(intent, route, observation) {
		return nil, fmt.Errorf("workload route or traffic proof does not match the admitted step: %v", err)
	}
	currentInventory, err := s.CurrentActiveFleetIngressInventory(ctx, intent.FleetCluster, intent.FleetEnvironment, observerPort)
	if err != nil || !sameFleetIngressInventory(&route.Inventory, currentInventory) || !sameFleetIngressInventory(&route.Inventory, &observation.Route.Inventory) {
		return nil, fmt.Errorf("workload ingress inventory changed before route commit")
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, intent.App, intent.ControlEnvironment)
	if err != nil || accepted.FleetAppTarget == nil || !sameFleetAppTarget(target, *accepted.FleetAppTarget) || !sameFleetAppTarget(target, route.FleetTarget) {
		return nil, fmt.Errorf("workload Fleet target changed before route commit")
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(claim.OperationID()))
	if err != nil || len(index.Kvs) != 1 {
		return nil, fmt.Errorf("workload acceptance index changed before route commit")
	}
	acceptanceKey := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, acceptanceKey)
	if err != nil || loaded.record.Accepted.Intent.CanonicalDigest != route.AcceptanceDigest {
		return nil, fmt.Errorf("workload signed acceptance changed before route commit")
	}
	storedReceipt, err := s.kv.Get(ctx, s.workloadReleaseReceiptKey(intent.ID))
	var envelope v3WorkloadReleaseReceipt
	if err != nil || len(storedReceipt.Kvs) != 1 || decodeV3Record(storedReceipt.Kvs[0].Value, &envelope) != nil ||
		envelope.IntentID != intent.ID || envelope.OperationID != claim.OperationID() || envelope.ReceiptDigest != receiptRecord.ReceiptDigest || string(envelope.Receipt) != string(receiptRecord.Receipt) {
		return nil, fmt.Errorf("workload receipt envelope changed before route commit")
	}
	step := intent.RouteSteps[route.Step]
	next := receipt
	next.Phase = pipeline.WorkloadReleaseRouting
	if route.Step >= 1 {
		next.Phase = pipeline.WorkloadReleaseDraining
	}
	next.RouteStep = route.Step
	next.RouteGeneration = route.Generation
	next.RouteOldWeight = step.OldWeight
	next.RouteTargetWeight = step.TargetWeight
	next.RouteSHA256 = "sha256:" + route.RenderedRoute.SHA256
	if err := next.ValidateSuccessor(intent, receipt); err != nil {
		return nil, fmt.Errorf("verified route cannot advance workload receipt: %w", err)
	}
	rawReceipt, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	activeBytes, err := json.Marshal(activeFleetRoute{IntentID: intent.ID, DeploymentID: intent.TargetDeploymentID, Generation: route.Generation, RouteSHA256: route.RenderedRoute.SHA256})
	if err != nil {
		return nil, err
	}
	nextEnvelope := v3WorkloadReleaseReceipt{IntentID: intent.ID, OperationID: claim.OperationID(), Receipt: rawReceipt,
		ReceiptDigest: checkpointDigest(rawReceipt), ClaimGeneration: claim.Generation(), Revision: envelope.Revision + 1, UpdatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	envelopeBytes, err := json.Marshal(nextEnvelope)
	if err != nil {
		return nil, err
	}
	operation, operationRevision, err := s.load(ctx, claim.OperationID())
	if err != nil || operation.Operation.Status != "running" || operation.Operation.LockedBy != claim.OwnerID() || operation.Generation != claim.Generation() {
		return nil, store.ErrOperationOwnershipLost
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
		return nil, store.ErrOperationOwnershipLost
	}
	appLock, err := s.kv.Get(ctx, s.appLockKey(intent.App))
	if err != nil || len(appLock.Kvs) != 1 || string(appLock.Kvs[0].Value) != lock.Fence() || lock.Context().Err() != nil {
		return nil, store.ErrOperationOwnershipLost
	}
	inventoryCompares, err := s.fleetIngressInventoryCompares(route.Inventory)
	if err != nil {
		return nil, err
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.Value(s.appLockKey(intent.App)), "=", lock.Fence()),
		clientv3.Compare(clientv3.ModRevision(s.operationAcceptanceIndexKey(claim.OperationID())), "=", index.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(acceptanceKey), "=", loaded.revision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(intent.App, intent.ControlEnvironment)), "=", targetRevision),
		clientv3.Compare(clientv3.ModRevision(s.workloadReleaseRouteIntentKey(stepIntentID)), "=", routeResponse.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.workloadReleaseReceiptKey(intent.ID)), "=", storedReceipt.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(reservationKey), "=", reservation.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(activeKey), "=", active.Kvs[0].ModRevision),
	}
	compares = append(compares, inventoryCompares...)
	if lock.Context().Err() != nil {
		return nil, store.ErrOperationOwnershipLost
	}
	txn, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(activeKey, string(activeBytes)), clientv3.OpPut(s.workloadReleaseReceiptKey(intent.ID), string(envelopeBytes))).Commit()
	if err != nil {
		return nil, err
	}
	if !txn.Succeeded {
		return nil, fmt.Errorf("workload route or receipt changed before atomic promotion")
	}
	return &store.WorkloadReleaseReceiptRecord{IntentID: intent.ID, OperationID: claim.OperationID(), Receipt: rawReceipt,
		ReceiptDigest: nextEnvelope.ReceiptDigest, ClaimGeneration: claim.Generation(), Revision: nextEnvelope.Revision, UpdatedAt: nextEnvelope.UpdatedAt}, nil
}

func workloadTrafficObservationMatches(intent pipeline.WorkloadReleaseIntent, route WorkloadReleaseRouteIntent, observed *FleetIngressTrafficObservation) bool {
	if observed == nil || !observed.PublicMatched || observed.ProbePath != intent.TrafficProbePath || observed.EndpointBodySHA256 != intent.TrafficProbeBodySHA256 ||
		observed.Route.RouteSHA256 != route.RenderedRoute.SHA256 || observed.Route.Generation != route.Generation ||
		!sameFleetIngressInventory(&route.Inventory, &observed.Route.Inventory) || len(observed.Route.Nodes) != len(route.Inventory.Nodes) || len(observed.NodeEndpoints) != len(route.Inventory.Nodes) {
		return false
	}
	if decoded, err := hex.DecodeString(observed.EndpointBodySHA256); err != nil || hex.EncodeToString(decoded) != observed.EndpointBodySHA256 {
		return false
	}
	for i, node := range route.Inventory.Nodes {
		if observed.Route.Nodes[i].NodeID != node.ID || observed.Route.Nodes[i].MatchedDesiredRouteSHA256 != route.RenderedRoute.SHA256 || observed.Route.Nodes[i].PublishedGeneration != route.Generation ||
			observed.NodeEndpoints[i].NodeID != node.ID || observed.NodeEndpoints[i].BodySHA256 != intent.TrafficProbeBodySHA256 {
			return false
		}
	}
	return true
}
