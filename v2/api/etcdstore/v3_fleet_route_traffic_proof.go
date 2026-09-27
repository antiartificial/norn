package etcdstore

import (
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

const initialFleetTrafficProofSchema = "norn.fleet-initial-traffic-proof/v1"

// InitialFleetTrafficProof is an immutable observation receipt, not terminal
// deployment authority. Completion must reload it and compare the source
// revisions again before writing positive active weight.
type InitialFleetTrafficProof struct {
	SchemaVersion    string                         `json:"schemaVersion"`
	IntentID         string                         `json:"intentId"`
	OperationID      string                         `json:"operationId"`
	DeploymentID     string                         `json:"deploymentId"`
	AcceptanceDigest string                         `json:"acceptanceDigest"`
	SpecDigest       string                         `json:"specDigest"`
	HealthEffect     effect.Token                   `json:"healthEffect"`
	Observation      FleetIngressTrafficObservation `json:"observation"`
	ObservedAt       time.Time                      `json:"observedAt"`
}

func (s *V3OperationStore) initialFleetTrafficProofKey(intentID string) string {
	return s.prefix + "/v3/fleet-initial-traffic-proofs/" + intentID
}

// RecordClaimedInitialFleetTrafficProof probes the route selected by live
// signed control state, then stores its observation under claim and inventory
// revision fences. The probe path and expected body must be chosen by the
// worker's trusted deployment policy; they are not part of signed InfraSpec
// yet, so this receipt alone cannot authorize terminal active traffic.
func (s *V3OperationStore) RecordClaimedInitialFleetTrafficProof(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, spec *model.InfraSpec, healthEffect effect.Token, observerPort, endpointPort int, caPEM, certPEM, keyPEM []byte, probePath, expectedBodySHA256 string, publicRoots *x509.CertPool) (*InitialFleetTrafficProof, error) {
	return s.recordClaimedInitialFleetTrafficProof(ctx, claim, lock, spec, healthEffect, observerPort, func(ctx context.Context, intent *InitialFleetRouteIntent) (*FleetIngressTrafficObservation, error) {
		return s.ObserveCurrentFleetIngressTraffic(ctx, intent.FleetTarget.Cluster, intent.FleetTarget.FleetEnvironment, observerPort, endpointPort, caPEM, certPEM, keyPEM, intent.RenderedRoute, intent.Generation, probePath, expectedBodySHA256, publicRoots)
	})
}

func (s *V3OperationStore) recordClaimedInitialFleetTrafficProof(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, spec *model.InfraSpec, healthEffect effect.Token, observerPort int, observe func(context.Context, *InitialFleetRouteIntent) (*FleetIngressTrafficObservation, error)) (*InitialFleetTrafficProof, error) {
	if s == nil || observe == nil || lock == nil || spec == nil {
		return nil, fmt.Errorf("claimed traffic proof is unavailable")
	}
	intent, err := s.CurrentInitialFleetRouteIntent(ctx, claim, lock, spec, observerPort)
	if err != nil {
		return nil, err
	}
	if err := s.requireHealthyDeploymentEffect(ctx, claim, intent, healthEffect); err != nil {
		return nil, err
	}
	observation, err := observe(ctx, intent)
	if err != nil || !trafficObservationMatchesIntent(intent, observation) {
		return nil, fmt.Errorf("traffic observation differs from claimed route: %v", err)
	}
	current, err := s.CurrentInitialFleetRouteIntent(ctx, claim, lock, spec, observerPort)
	if err != nil || current == nil || current.ID != intent.ID || !sameFleetIngressInventory(&intent.Inventory, &current.Inventory) {
		return nil, fmt.Errorf("claimed route changed during traffic probes")
	}
	if err := s.requireHealthyDeploymentEffect(ctx, claim, intent, healthEffect); err != nil {
		return nil, err
	}
	proof := &InitialFleetTrafficProof{SchemaVersion: initialFleetTrafficProofSchema, IntentID: intent.ID, OperationID: intent.OperationID,
		DeploymentID: intent.DeploymentID, AcceptanceDigest: intent.AcceptanceDigest, SpecDigest: intent.SpecDigest,
		HealthEffect: healthEffect, Observation: *observation, ObservedAt: time.Now().UTC().Truncate(time.Microsecond)}
	encoded, err := json.Marshal(proof)
	if err != nil {
		return nil, err
	}
	operation, operationRevision, err := s.load(ctx, claim.OperationID())
	if err != nil || operation.Operation.Status != model.OperationRunning || operation.Operation.LockedBy != claim.OwnerID() || operation.Generation != claim.Generation() {
		return nil, store.ErrOperationOwnershipLost
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
		return nil, store.ErrOperationOwnershipLost
	}
	intentKey := s.initialFleetRouteKey(intent.App, intent.ControlEnvironment, intent.Region, intent.DeploymentID)
	storedIntent, err := s.kv.Get(ctx, intentKey)
	if err != nil || len(storedIntent.Kvs) != 1 {
		return nil, fmt.Errorf("claimed route intent is unavailable")
	}
	var decodedIntent InitialFleetRouteIntent
	if decodeV3Record(storedIntent.Kvs[0].Value, &decodedIntent) != nil || decodedIntent.ID != intent.ID || decodedIntent.RenderedRoute.SHA256 != intent.RenderedRoute.SHA256 {
		return nil, fmt.Errorf("claimed route intent changed before proof commit")
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, intent.App, intent.ControlEnvironment)
	if err != nil || !sameFleetAppTarget(intent.FleetTarget, target) {
		return nil, fmt.Errorf("Fleet app target changed before proof commit")
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(claim.OperationID()))
	if err != nil || len(index.Kvs) != 1 {
		return nil, fmt.Errorf("deployment acceptance index changed before proof commit")
	}
	acceptanceKey := string(index.Kvs[0].Value)
	accepted, err := s.loadAcceptance(ctx, acceptanceKey)
	if err != nil || accepted.record.Accepted.Intent.CanonicalDigest != intent.AcceptanceDigest {
		return nil, fmt.Errorf("deployment acceptance changed before proof commit")
	}
	reservationKey := s.fleetRouteReservationKey(intent.App, intent.ControlEnvironment, intent.Region)
	reservation, err := s.kv.Get(ctx, reservationKey)
	if err != nil || len(reservation.Kvs) != 1 {
		return nil, fmt.Errorf("route reservation changed before proof commit")
	}
	idIndex, err := s.kv.Get(ctx, s.initialFleetRouteIDKey(intent.ID))
	if err != nil || len(idIndex.Kvs) != 1 || string(idIndex.Kvs[0].Value) != intentKey {
		return nil, fmt.Errorf("route intent ID index changed before proof commit")
	}
	effects, err := NewV3DeploymentEffectReservations(s)
	if err != nil {
		return nil, err
	}
	_, effectKey, effectRevision, err := effects.loadToken(ctx, healthEffect)
	if err != nil {
		return nil, err
	}
	attempt, err := s.kv.Get(ctx, effects.submitAttemptKey(healthEffect))
	if err != nil || len(attempt.Kvs) != 1 {
		return nil, fmt.Errorf("deployment submit attempt changed before proof commit")
	}
	inventoryCompares, err := s.fleetIngressInventoryCompares(intent.Inventory)
	if err != nil {
		return nil, err
	}
	comparisons := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.Value(s.appLockKey(intent.App)), "=", lock.Fence()),
		clientv3.Compare(clientv3.ModRevision(intentKey), "=", storedIntent.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(reservationKey), "=", reservation.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.initialFleetRouteIDKey(intent.ID)), "=", idIndex.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(s.fleetActiveRouteKey(intent.App, intent.ControlEnvironment, intent.Region)), "=", 0),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(intent.App, intent.ControlEnvironment)), "=", targetRevision),
		clientv3.Compare(clientv3.ModRevision(s.operationAcceptanceIndexKey(claim.OperationID())), "=", index.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(acceptanceKey), "=", accepted.revision),
		clientv3.Compare(clientv3.ModRevision(effectKey), "=", effectRevision),
		clientv3.Compare(clientv3.ModRevision(effects.submitAttemptKey(healthEffect)), "=", attempt.Kvs[0].ModRevision),
	}
	comparisons = append(comparisons, inventoryCompares...)
	proofKey := s.initialFleetTrafficProofKey(intent.ID)
	createCompares := append(append([]clientv3.Cmp(nil), comparisons...), clientv3.Compare(clientv3.CreateRevision(proofKey), "=", 0))
	txn, err := s.kv.Txn(ctx).If(createCompares...).Then(clientv3.OpPut(proofKey, string(encoded))).Commit()
	if err != nil {
		return nil, err
	}
	if txn.Succeeded {
		return proof, nil
	}
	prior, err := s.kv.Get(ctx, proofKey)
	if err != nil || len(prior.Kvs) != 1 {
		return nil, store.ErrOperationOwnershipLost
	}
	var existing InitialFleetTrafficProof
	if decodeV3Record(prior.Kvs[0].Value, &existing) != nil || existing.SchemaVersion != initialFleetTrafficProofSchema ||
		existing.IntentID != proof.IntentID || existing.OperationID != proof.OperationID || existing.DeploymentID != proof.DeploymentID ||
		existing.AcceptanceDigest != proof.AcceptanceDigest || existing.SpecDigest != proof.SpecDigest || existing.HealthEffect != proof.HealthEffect ||
		!reflect.DeepEqual(existing.Observation, proof.Observation) || existing.ObservedAt.IsZero() {
		return nil, fmt.Errorf("existing traffic proof conflicts with current observation")
	}
	replayCompares := append(append([]clientv3.Cmp(nil), comparisons...), clientv3.Compare(clientv3.ModRevision(proofKey), "=", prior.Kvs[0].ModRevision))
	replay, err := s.kv.Txn(ctx).If(replayCompares...).Then(clientv3.OpGet(s.ownerKey(claim.OperationID()))).Commit()
	if err != nil || !replay.Succeeded || len(replay.Responses) != 1 || len(replay.Responses[0].GetResponseRange().Kvs) != 1 || replay.Responses[0].GetResponseRange().Kvs[0].Lease == 0 {
		return nil, store.ErrOperationOwnershipLost
	}
	return &existing, nil
}

func trafficObservationMatchesIntent(intent *InitialFleetRouteIntent, observed *FleetIngressTrafficObservation) bool {
	if intent == nil || observed == nil || !observed.PublicMatched || !strings.HasPrefix(observed.ProbePath, "/") || strings.HasPrefix(observed.ProbePath, "//") || strings.ContainsAny(observed.ProbePath, "?#") || len(observed.EndpointBodySHA256) != 64 ||
		observed.Route.RouteSHA256 != intent.RenderedRoute.SHA256 || observed.Route.Generation != intent.Generation ||
		!sameFleetIngressInventory(&intent.Inventory, &observed.Route.Inventory) || len(observed.Route.Nodes) != len(intent.Inventory.Nodes) || len(observed.NodeEndpoints) != len(intent.Inventory.Nodes) {
		return false
	}
	if digest, err := hex.DecodeString(observed.EndpointBodySHA256); err != nil || hex.EncodeToString(digest) != observed.EndpointBodySHA256 {
		return false
	}
	for i, node := range intent.Inventory.Nodes {
		if observed.Route.Nodes[i].NodeID != node.ID || observed.Route.Nodes[i].MatchedDesiredRouteSHA256 != intent.RenderedRoute.SHA256 || observed.Route.Nodes[i].PublishedGeneration != intent.Generation ||
			observed.NodeEndpoints[i].NodeID != node.ID || observed.NodeEndpoints[i].BodySHA256 != observed.EndpointBodySHA256 {
			return false
		}
	}
	return true
}
