package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// The worker supplies only the pinned InfraSpec and observer port. All route,
// proof, effect and Fleet authority is reloaded from control state.
type initialFleetCompletionSource struct {
	Spec         *model.InfraSpec
	ObserverPort int
}

type activeFleetRoute struct {
	IntentID     string `json:"intentId"`
	DeploymentID string `json:"deploymentId"`
	Generation   uint64 `json:"generation"`
	RouteSHA256  string `json:"routeSha256"`
}

func (s *V3OperationStore) initialFleetCompletionFences(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, accepted store.AcceptedOperation, source initialFleetCompletionSource) ([]clientv3.Cmp, []clientv3.Op, error) {
	if source.Spec == nil || accepted.Deployment == nil || accepted.FleetAppTarget == nil || len(accepted.Regions) != 1 || accepted.Regions[0].TrafficWeight != 100 {
		return nil, nil, fmt.Errorf("positive active traffic requires a single signed Fleet route")
	}
	intent, err := s.CurrentInitialFleetRouteIntent(ctx, claim, lock, source.Spec, source.ObserverPort)
	if err != nil {
		return nil, nil, fmt.Errorf("deployment-bound ingress intent is unavailable: %w", err)
	}
	if intent.OperationID != claim.OperationID() || intent.DeploymentID != accepted.Deployment.ID || intent.AcceptanceID != accepted.Intent.ID || intent.AcceptanceDigest != accepted.Intent.CanonicalDigest || intent.SpecDigest != accepted.Deployment.SpecDigest || intent.Generation != 1 || !sameFleetAppTarget(intent.FleetTarget, *accepted.FleetAppTarget) {
		return nil, nil, fmt.Errorf("deployment-bound ingress intent differs from acceptance")
	}
	proofKey := s.initialFleetTrafficProofKey(intent.ID)
	storedProof, err := s.kv.Get(ctx, proofKey)
	if err != nil || len(storedProof.Kvs) != 1 {
		return nil, nil, fmt.Errorf("deployment-bound ingress proof is unavailable: %v", err)
	}
	var proof InitialFleetTrafficProof
	if decodeV3Record(storedProof.Kvs[0].Value, &proof) != nil || proof.SchemaVersion != initialFleetTrafficProofSchema || proof.IntentID != intent.ID || proof.OperationID != claim.OperationID() || proof.DeploymentID != accepted.Deployment.ID || proof.AcceptanceDigest != accepted.Intent.CanonicalDigest || proof.SpecDigest != accepted.Deployment.SpecDigest || proof.ObservedAt.IsZero() || !trafficObservationMatchesIntent(intent, &proof.Observation) {
		return nil, nil, fmt.Errorf("deployment-bound ingress proof differs from signed route")
	}
	if err := s.requireHealthyDeploymentEffect(ctx, claim, intent, proof.HealthEffect); err != nil {
		return nil, nil, err
	}
	effects, err := NewV3DeploymentEffectReservations(s)
	if err != nil {
		return nil, nil, err
	}
	_, effectKey, effectRevision, err := effects.loadToken(ctx, proof.HealthEffect)
	if err != nil {
		return nil, nil, err
	}
	attemptKey := effects.submitAttemptKey(proof.HealthEffect)
	attempt, err := s.kv.Get(ctx, attemptKey)
	if err != nil || len(attempt.Kvs) != 1 {
		return nil, nil, fmt.Errorf("deployment health submit attempt is unavailable")
	}
	intentKey := s.initialFleetRouteKey(intent.App, intent.ControlEnvironment, intent.Region, intent.DeploymentID)
	storedIntent, err := s.kv.Get(ctx, intentKey)
	if err != nil || len(storedIntent.Kvs) != 1 {
		return nil, nil, fmt.Errorf("deployment-bound route intent is unavailable")
	}
	var checkedIntent InitialFleetRouteIntent
	if decodeV3Record(storedIntent.Kvs[0].Value, &checkedIntent) != nil || !reflect.DeepEqual(checkedIntent, *intent) {
		return nil, nil, fmt.Errorf("deployment-bound route intent changed")
	}
	reservationKey := s.fleetRouteReservationKey(intent.App, intent.ControlEnvironment, intent.Region)
	reservation, err := s.kv.Get(ctx, reservationKey)
	if err != nil || len(reservation.Kvs) != 1 {
		return nil, nil, fmt.Errorf("deployment-bound route reservation is unavailable")
	}
	var reserved initialFleetRouteReservation
	if decodeV3Record(reservation.Kvs[0].Value, &reserved) != nil || reserved.IntentID != intent.ID || reserved.Generation != intent.Generation {
		return nil, nil, fmt.Errorf("deployment-bound route reservation differs from intent")
	}
	indexKey := s.initialFleetRouteIDKey(intent.ID)
	index, err := s.kv.Get(ctx, indexKey)
	if err != nil || len(index.Kvs) != 1 || string(index.Kvs[0].Value) != intentKey {
		return nil, nil, fmt.Errorf("deployment-bound route index differs from intent")
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, intent.App, intent.ControlEnvironment)
	if err != nil || !sameFleetAppTarget(intent.FleetTarget, target) {
		return nil, nil, fmt.Errorf("Fleet app target changed before active route")
	}
	acceptanceIndexKey := s.operationAcceptanceIndexKey(claim.OperationID())
	acceptanceIndex, err := s.kv.Get(ctx, acceptanceIndexKey)
	if err != nil || len(acceptanceIndex.Kvs) != 1 {
		return nil, nil, fmt.Errorf("deployment acceptance index changed before active route")
	}
	acceptanceKey := string(acceptanceIndex.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, acceptanceKey)
	if err != nil || loaded.record.Accepted.Intent.CanonicalDigest != accepted.Intent.CanonicalDigest {
		return nil, nil, fmt.Errorf("deployment acceptance changed before active route")
	}
	inventoryCompares, err := s.fleetIngressInventoryCompares(intent.Inventory)
	if err != nil {
		return nil, nil, err
	}
	activeKey := s.fleetActiveRouteKey(intent.App, intent.ControlEnvironment, intent.Region)
	active, err := json.Marshal(activeFleetRoute{IntentID: intent.ID, DeploymentID: intent.DeploymentID, Generation: intent.Generation, RouteSHA256: intent.RenderedRoute.SHA256})
	if err != nil {
		return nil, nil, err
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(proofKey), "=", storedProof.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(intentKey), "=", storedIntent.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(reservationKey), "=", reservation.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(indexKey), "=", index.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(intent.App, intent.ControlEnvironment)), "=", targetRevision),
		clientv3.Compare(clientv3.ModRevision(acceptanceIndexKey), "=", acceptanceIndex.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(acceptanceKey), "=", loaded.revision),
		clientv3.Compare(clientv3.ModRevision(effectKey), "=", effectRevision),
		clientv3.Compare(clientv3.ModRevision(attemptKey), "=", attempt.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(activeKey), "=", 0),
	}
	compares = append(compares, inventoryCompares...)
	return compares, []clientv3.Op{clientv3.OpPut(activeKey, string(active))}, nil
}
