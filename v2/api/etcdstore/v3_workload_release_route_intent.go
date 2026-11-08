package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/ingress"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

const workloadReleaseRouteIntentSchema = "norn.workload-release-route-intent/v1"

// WorkloadReleaseRouteIntent is an immutable per-step node-publication
// authorization. The parent workload intent remains the active-route lineage;
// this opaque ID is used only by ingress nodes for one route generation.
type WorkloadReleaseRouteIntent struct {
	SchemaVersion          string                        `json:"schemaVersion"`
	ID                     string                        `json:"id"`
	IntentID               string                        `json:"intentId"`
	OperationID            string                        `json:"operationId"`
	AcceptanceDigest       string                        `json:"acceptanceDigest"`
	ReceiptDigest          string                        `json:"receiptDigest"`
	ClaimGeneration        int64                         `json:"claimGeneration"`
	Step                   int                           `json:"step"`
	Generation             uint64                        `json:"generation"`
	RenderedRoute          ingress.RenderedRoute         `json:"renderedRoute"`
	TrafficProbePath       string                        `json:"trafficProbePath"`
	TrafficProbeBodySHA256 string                        `json:"trafficProbeBodySha256"`
	FleetTarget            FleetAppTarget                `json:"fleetTarget"`
	Inventory              FleetIngressInventoryEvidence `json:"inventory"`
	PriorActive            activeFleetRoute              `json:"priorActive"`
	PriorActiveRevision    int64                         `json:"priorActiveRevision"`
	CreatedAt              time.Time                     `json:"createdAt"`
}

func workloadReleaseRouteIntentID(intentID string, step int) string {
	sum := sha256.Sum256([]byte(intentID + "\x00" + strconv.Itoa(step)))
	return "wlr-" + hex.EncodeToString(sum[:])
}

func (s *V3OperationStore) workloadReleaseRouteIntentKey(id string) string {
	return s.prefix + "/v3/workload-release-route-intents/" + id
}

type workloadRouteInventoryLoader func(context.Context, string, string, int) (*FleetIngressInventoryEvidence, error)
type workloadRoutePredecessorObserver func(context.Context, string, string, int, []byte, []byte, []byte, ingress.RenderedRoute, uint64) (*FleetIngressRouteObservation, error)

// IntendClaimedWorkloadReleaseRouteStep persists one forward route step and
// moves the shared reservation before publication. The active pointer remains
// unchanged until the later proof-and-commit method succeeds, causing scale
// target readers to fail closed during publication and recovery.
func (s *V3OperationStore) IntendClaimedWorkloadReleaseRouteStep(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, step, observerPort int, caPEM, certPEM, keyPEM []byte) (*WorkloadReleaseRouteIntent, error) {
	if observerPort < 1024 || observerPort > 65535 || len(caPEM) == 0 || len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, fmt.Errorf("workload route intent requires a pinned ingress observer")
	}
	return s.intendClaimedWorkloadReleaseRouteStep(ctx, claim, lock, step, observerPort, caPEM, certPEM, keyPEM,
		func(ctx context.Context, cluster, environment string, port int) (*FleetIngressInventoryEvidence, error) {
			return s.CurrentActiveFleetIngressInventory(ctx, cluster, environment, port)
		},
		func(ctx context.Context, cluster, environment string, port int, caPEM, certPEM, keyPEM []byte, desired ingress.RenderedRoute, generation uint64) (*FleetIngressRouteObservation, error) {
			return s.ObserveCurrentFleetIngressRoute(ctx, cluster, environment, port, caPEM, certPEM, keyPEM, desired, generation)
		})
}

func (s *V3OperationStore) intendClaimedWorkloadReleaseRouteStep(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, step, observerPort int,
	caPEM, certPEM, keyPEM []byte, loadInventory workloadRouteInventoryLoader, observeRoute workloadRoutePredecessorObserver) (*WorkloadReleaseRouteIntent, error) {
	if s == nil || loadInventory == nil || observeRoute == nil || observerPort < 1024 || observerPort > 65535 || lock == nil || lock.Fence() == "" || lock.Context().Err() != nil {
		return nil, fmt.Errorf("workload route intent requires an active claim, lock, and ingress observers")
	}
	workloadIntent, accepted, err := s.VerifyClaimedWorkloadRelease(ctx, claim, lock)
	if err != nil {
		return nil, err
	}
	if step < 0 || step >= len(workloadIntent.RouteSteps) {
		return nil, fmt.Errorf("workload route step is outside the signed intent")
	}
	if uint64(step) > ^uint64(0)-workloadIntent.FirstRouteGeneration {
		return nil, fmt.Errorf("workload route generation overflows")
	}
	generation := workloadIntent.FirstRouteGeneration + uint64(step)
	receiptRecord, err := s.LoadWorkloadReleaseReceipt(ctx, workloadIntent.ID)
	if err != nil || receiptRecord == nil || receiptRecord.OperationID != claim.OperationID() {
		return nil, fmt.Errorf("workload route receipt is unavailable: %v", err)
	}
	storedReceipt, err := s.kv.Get(ctx, s.workloadReleaseReceiptKey(workloadIntent.ID))
	var receiptEnvelope v3WorkloadReleaseReceipt
	if err != nil || len(storedReceipt.Kvs) != 1 || decodeV3Record(storedReceipt.Kvs[0].Value, &receiptEnvelope) != nil ||
		receiptEnvelope.OperationID != claim.OperationID() || receiptEnvelope.ReceiptDigest != receiptRecord.ReceiptDigest || string(receiptEnvelope.Receipt) != string(receiptRecord.Receipt) {
		return nil, fmt.Errorf("workload route receipt changed during verification")
	}
	var receipt pipeline.WorkloadReleaseReceipt
	if json.Unmarshal(receiptRecord.Receipt, &receipt) != nil || receipt.ValidateAgainst(workloadIntent) != nil {
		return nil, fmt.Errorf("workload route receipt is invalid")
	}
	rendered, err := workloadIntent.RenderRouteStep(step)
	if err != nil {
		return nil, err
	}
	activeKey := s.fleetActiveRouteKey(workloadIntent.App, workloadIntent.ControlEnvironment, workloadIntent.Region)
	activeResponse, err := s.kv.Get(ctx, activeKey)
	if err != nil || len(activeResponse.Kvs) != 1 {
		return nil, fmt.Errorf("workload route requires an active predecessor: %v", err)
	}
	var prior activeFleetRoute
	if decodeV3Record(activeResponse.Kvs[0].Value, &prior) != nil {
		return nil, fmt.Errorf("active workload route is invalid")
	}
	inventory, err := loadInventory(ctx, workloadIntent.FleetCluster, workloadIntent.FleetEnvironment, observerPort)
	if err != nil {
		return nil, err
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, workloadIntent.App, workloadIntent.ControlEnvironment)
	if err != nil || accepted.FleetAppTarget == nil || !sameFleetAppTarget(target, *accepted.FleetAppTarget) {
		return nil, fmt.Errorf("workload Fleet target changed before route reservation")
	}
	id := workloadReleaseRouteIntentID(workloadIntent.ID, step)
	key := s.workloadReleaseRouteIntentKey(id)
	if existing, err := s.kv.Get(ctx, key); err != nil {
		return nil, err
	} else if len(existing.Kvs) == 1 {
		var record WorkloadReleaseRouteIntent
		if decodeV3Record(existing.Kvs[0].Value, &record) != nil || record.SchemaVersion != workloadReleaseRouteIntentSchema || record.ID != id ||
			record.IntentID != workloadIntent.ID || record.OperationID != claim.OperationID() || record.Step != step || record.Generation != generation ||
			record.AcceptanceDigest != accepted.Intent.CanonicalDigest ||
			record.TrafficProbePath != workloadIntent.TrafficProbePath || record.TrafficProbeBodySHA256 != workloadIntent.TrafficProbeBodySHA256 ||
			!reflect.DeepEqual(record.RenderedRoute, rendered) || !sameFleetIngressInventory(&record.Inventory, inventory) || !sameFleetAppTarget(record.FleetTarget, target) ||
			record.ClaimGeneration <= 0 || record.CreatedAt.IsZero() {
			return nil, fmt.Errorf("existing workload route intent differs from same-intent recovery")
		}
		alreadyCommitted := receipt.RouteStep == step && receipt.RouteGeneration == generation && receipt.RouteSHA256 == "sha256:"+rendered.SHA256 &&
			receipt.RouteOldWeight == workloadIntent.RouteSteps[step].OldWeight && receipt.RouteTargetWeight == workloadIntent.RouteSteps[step].TargetWeight &&
			prior == (activeFleetRoute{IntentID: workloadIntent.ID, DeploymentID: workloadIntent.TargetDeploymentID, Generation: generation, RouteSHA256: rendered.SHA256})
		if !alreadyCommitted && (record.ReceiptDigest != receiptRecord.ReceiptDigest || record.PriorActiveRevision != activeResponse.Kvs[0].ModRevision || record.PriorActive != prior ||
			!workloadRouteStepAllowed(workloadIntent, receipt, step) || !workloadRoutePredecessorMatches(workloadIntent, receipt, step, rendered, prior)) {
			return nil, fmt.Errorf("existing workload route intent differs from same-intent recovery")
		}
		reservationKey := s.fleetRouteReservationKey(workloadIntent.App, workloadIntent.ControlEnvironment, workloadIntent.Region)
		reservation, err := s.kv.Get(ctx, reservationKey)
		if err != nil || len(reservation.Kvs) != 1 {
			return nil, fmt.Errorf("same-intent workload route reservation is unavailable")
		}
		var reserved initialFleetRouteReservation
		if decodeV3Record(reservation.Kvs[0].Value, &reserved) != nil || reserved.IntentID != workloadIntent.ID || reserved.Generation != record.Generation {
			return nil, fmt.Errorf("same-intent workload route reservation changed")
		}
		return &record, nil
	} else if len(existing.Kvs) != 0 {
		return nil, fmt.Errorf("workload route intent index is ambiguous")
	}
	if !workloadRouteStepAllowed(workloadIntent, receipt, step) || !workloadRoutePredecessorMatches(workloadIntent, receipt, step, rendered, prior) {
		return nil, fmt.Errorf("active route differs from the workload release predecessor")
	}
	priorRouteStep := step
	if step > 0 {
		priorRouteStep = step - 1
	}
	priorRendered, err := workloadIntent.RenderRouteStep(priorRouteStep)
	if err != nil {
		return nil, err
	}
	observed, err := observeRoute(ctx, workloadIntent.FleetCluster, workloadIntent.FleetEnvironment, observerPort,
		caPEM, certPEM, keyPEM, priorRendered, prior.Generation)
	if err != nil || observed == nil {
		return nil, fmt.Errorf("workload route predecessor readback failed: %v", err)
	}
	if !sameFleetIngressInventory(&observed.Inventory, inventory) {
		return nil, fmt.Errorf("workload ingress inventory changed during predecessor readback")
	}
	reservationKey := s.fleetRouteReservationKey(workloadIntent.App, workloadIntent.ControlEnvironment, workloadIntent.Region)
	reservation, err := s.kv.Get(ctx, reservationKey)
	if err != nil || len(reservation.Kvs) != 1 {
		return nil, fmt.Errorf("workload route reservation predecessor is unavailable: %v", err)
	}
	var reserved initialFleetRouteReservation
	if decodeV3Record(reservation.Kvs[0].Value, &reserved) != nil || reserved.IntentID != prior.IntentID || reserved.Generation != prior.Generation {
		return nil, fmt.Errorf("workload route reservation differs from the active predecessor")
	}
	operation, operationRevision, err := s.load(ctx, claim.OperationID())
	if err != nil || operation.Operation.Status != "running" || operation.Generation != claim.Generation() || operation.Operation.LockedBy != claim.OwnerID() {
		return nil, store.ErrOperationOwnershipLost
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
		return nil, store.ErrOperationOwnershipLost
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(claim.OperationID()))
	if err != nil || len(index.Kvs) != 1 {
		return nil, fmt.Errorf("workload acceptance index changed before route reservation")
	}
	acceptanceKey := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, acceptanceKey)
	if err != nil || loaded.record.Accepted.Intent.CanonicalDigest != accepted.Intent.CanonicalDigest {
		return nil, fmt.Errorf("workload signed acceptance changed before route reservation")
	}
	inventoryCompares, err := s.fleetIngressInventoryCompares(*inventory)
	if err != nil {
		return nil, err
	}
	newReservation, err := json.Marshal(initialFleetRouteReservation{IntentID: workloadIntent.ID, Generation: generation})
	if err != nil {
		return nil, err
	}
	record := WorkloadReleaseRouteIntent{SchemaVersion: workloadReleaseRouteIntentSchema, ID: id, IntentID: workloadIntent.ID,
		OperationID: claim.OperationID(), AcceptanceDigest: accepted.Intent.CanonicalDigest, ReceiptDigest: receiptRecord.ReceiptDigest,
		ClaimGeneration: claim.Generation(), Step: step, Generation: generation, RenderedRoute: rendered, FleetTarget: target,
		TrafficProbePath: workloadIntent.TrafficProbePath, TrafficProbeBodySHA256: workloadIntent.TrafficProbeBodySHA256,
		Inventory: *inventory, PriorActive: prior, PriorActiveRevision: activeResponse.Kvs[0].ModRevision, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	encodedRecord, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if lock.Context().Err() != nil {
		return nil, store.ErrOperationOwnershipLost
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.Value(s.appLockKey(workloadIntent.App)), "=", lock.Fence()),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(workloadIntent.App, workloadIntent.ControlEnvironment)), "=", targetRevision),
		clientv3.Compare(clientv3.ModRevision(s.operationAcceptanceIndexKey(claim.OperationID())), "=", index.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(acceptanceKey), "=", loaded.revision),
		clientv3.Compare(clientv3.ModRevision(s.workloadReleaseReceiptKey(workloadIntent.ID)), "=", storedReceipt.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(activeKey), "=", activeResponse.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(reservationKey), "=", reservation.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
	}
	compares = append(compares, inventoryCompares...)
	result, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(key, string(encodedRecord)), clientv3.OpPut(reservationKey, string(newReservation))).Commit()
	if err != nil {
		return nil, err
	}
	if !result.Succeeded {
		return nil, fmt.Errorf("workload route reservation changed before commit")
	}
	return &record, nil
}

// AuthorizeClaimedWorkloadReleaseRouteForNode returns content only from the
// durable per-step record and only while the parent claim, lock, reservation,
// Fleet target, receipt predecessor, and ingress inventory still match.
func (s *V3OperationStore) AuthorizeClaimedWorkloadReleaseRouteForNode(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, stepIntentID, nodeID string, observerPort int) (*ingress.AuthorizedRoutePublication, error) {
	if stepIntentID == "" || nodeID == "" || observerPort < 1024 || observerPort > 65535 {
		return nil, fmt.Errorf("workload route publication identity is incomplete")
	}
	workloadIntent, accepted, err := s.VerifyClaimedWorkloadRelease(ctx, claim, lock)
	if err != nil {
		return nil, err
	}
	response, err := s.kv.Get(ctx, s.workloadReleaseRouteIntentKey(stepIntentID))
	if err != nil || len(response.Kvs) != 1 {
		return nil, fmt.Errorf("durable workload route intent is unavailable: %v", err)
	}
	var record WorkloadReleaseRouteIntent
	if decodeV3Record(response.Kvs[0].Value, &record) != nil || record.SchemaVersion != workloadReleaseRouteIntentSchema || record.ID != stepIntentID ||
		record.IntentID != workloadIntent.ID || record.OperationID != claim.OperationID() || record.AcceptanceDigest != accepted.Intent.CanonicalDigest ||
		record.Step < 0 || record.Step >= len(workloadIntent.RouteSteps) || record.ID != workloadReleaseRouteIntentID(workloadIntent.ID, record.Step) ||
		uint64(record.Step) > ^uint64(0)-workloadIntent.FirstRouteGeneration || record.Generation != workloadIntent.FirstRouteGeneration+uint64(record.Step) {
		return nil, fmt.Errorf("durable workload route intent differs from signed acceptance")
	}
	if record.TrafficProbePath != workloadIntent.TrafficProbePath || record.TrafficProbeBodySHA256 != workloadIntent.TrafficProbeBodySHA256 {
		return nil, fmt.Errorf("durable workload traffic probe differs from signed acceptance")
	}
	rendered, err := workloadIntent.RenderRouteStep(record.Step)
	if err != nil || !reflect.DeepEqual(record.RenderedRoute, rendered) {
		return nil, fmt.Errorf("durable workload route bytes differ from signed intent")
	}
	receiptRecord, err := s.LoadWorkloadReleaseReceipt(ctx, workloadIntent.ID)
	if err != nil || receiptRecord == nil {
		return nil, fmt.Errorf("workload route receipt predecessor changed")
	}
	storedReceipt, err := s.kv.Get(ctx, s.workloadReleaseReceiptKey(workloadIntent.ID))
	var receiptEnvelope v3WorkloadReleaseReceipt
	if err != nil || len(storedReceipt.Kvs) != 1 || decodeV3Record(storedReceipt.Kvs[0].Value, &receiptEnvelope) != nil ||
		receiptEnvelope.OperationID != claim.OperationID() || receiptEnvelope.ReceiptDigest != receiptRecord.ReceiptDigest || string(receiptEnvelope.Receipt) != string(receiptRecord.Receipt) {
		return nil, fmt.Errorf("workload route receipt changed during authorization")
	}
	var receipt pipeline.WorkloadReleaseReceipt
	if json.Unmarshal(receiptRecord.Receipt, &receipt) != nil || receipt.ValidateAgainst(workloadIntent) != nil {
		return nil, fmt.Errorf("workload route step is no longer authorized by receipt progress")
	}
	alreadyCommitted := receiptRecord.ReceiptDigest != record.ReceiptDigest
	if alreadyCommitted {
		if receipt.RouteStep != record.Step || receipt.RouteGeneration != record.Generation || receipt.RouteSHA256 != "sha256:"+record.RenderedRoute.SHA256 ||
			receipt.RouteOldWeight != workloadIntent.RouteSteps[record.Step].OldWeight || receipt.RouteTargetWeight != workloadIntent.RouteSteps[record.Step].TargetWeight {
			return nil, fmt.Errorf("workload route receipt advanced beyond this exact same-generation retry")
		}
	} else if !workloadRouteStepAllowed(workloadIntent, receipt, record.Step) {
		return nil, fmt.Errorf("workload route step is no longer authorized by receipt progress")
	}
	reservationKey := s.fleetRouteReservationKey(workloadIntent.App, workloadIntent.ControlEnvironment, workloadIntent.Region)
	reservation, err := s.kv.Get(ctx, reservationKey)
	if err != nil || len(reservation.Kvs) != 1 {
		return nil, fmt.Errorf("workload route reservation is unavailable: %v", err)
	}
	var reserved initialFleetRouteReservation
	if decodeV3Record(reservation.Kvs[0].Value, &reserved) != nil || reserved.IntentID != workloadIntent.ID || reserved.Generation != record.Generation {
		return nil, fmt.Errorf("workload route reservation differs from durable intent")
	}
	activeKey := s.fleetActiveRouteKey(workloadIntent.App, workloadIntent.ControlEnvironment, workloadIntent.Region)
	active, err := s.kv.Get(ctx, activeKey)
	if err != nil || len(active.Kvs) != 1 || (!alreadyCommitted && active.Kvs[0].ModRevision != record.PriorActiveRevision) {
		return nil, fmt.Errorf("workload route active predecessor changed")
	}
	var prior activeFleetRoute
	if decodeV3Record(active.Kvs[0].Value, &prior) != nil || (!alreadyCommitted && prior != record.PriorActive) ||
		(alreadyCommitted && prior != (activeFleetRoute{IntentID: workloadIntent.ID, DeploymentID: workloadIntent.TargetDeploymentID, Generation: record.Generation, RouteSHA256: record.RenderedRoute.SHA256})) {
		return nil, fmt.Errorf("workload route active predecessor differs from intent")
	}
	inventory, err := s.CurrentActiveFleetIngressInventory(ctx, workloadIntent.FleetCluster, workloadIntent.FleetEnvironment, observerPort)
	if err != nil || !sameFleetIngressInventory(&record.Inventory, inventory) {
		return nil, fmt.Errorf("workload route ingress inventory changed")
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, workloadIntent.App, workloadIntent.ControlEnvironment)
	if err != nil || accepted.FleetAppTarget == nil || !sameFleetAppTarget(target, *accepted.FleetAppTarget) || !sameFleetAppTarget(target, record.FleetTarget) {
		return nil, fmt.Errorf("workload route Fleet target changed")
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 {
		return nil, store.ErrOperationOwnershipLost
	}
	operation, operationRevision, err := s.load(ctx, claim.OperationID())
	if err != nil || operation.Operation.Status != "running" || operation.Generation != claim.Generation() || operation.Operation.LockedBy != claim.OwnerID() {
		return nil, store.ErrOperationOwnershipLost
	}
	appLock, err := s.kv.Get(ctx, s.appLockKey(workloadIntent.App))
	if err != nil || len(appLock.Kvs) != 1 || string(appLock.Kvs[0].Value) != lock.Fence() || lock.Context().Err() != nil {
		return nil, store.ErrOperationOwnershipLost
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(claim.OperationID()))
	if err != nil || len(index.Kvs) != 1 {
		return nil, fmt.Errorf("workload acceptance index changed during route authorization")
	}
	acceptanceKey := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, acceptanceKey)
	if err != nil || loaded.record.Accepted.Intent.CanonicalDigest != record.AcceptanceDigest {
		return nil, fmt.Errorf("workload acceptance changed during route authorization")
	}
	inventoryCompares, err := s.fleetIngressInventoryCompares(record.Inventory)
	if err != nil {
		return nil, err
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.Value(s.appLockKey(workloadIntent.App)), "=", lock.Fence()),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(workloadIntent.App, workloadIntent.ControlEnvironment)), "=", targetRevision),
		clientv3.Compare(clientv3.ModRevision(s.operationAcceptanceIndexKey(claim.OperationID())), "=", index.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(acceptanceKey), "=", loaded.revision),
		clientv3.Compare(clientv3.ModRevision(s.workloadReleaseReceiptKey(workloadIntent.ID)), "=", storedReceipt.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.workloadReleaseRouteIntentKey(stepIntentID)), "=", response.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(reservationKey), "=", reservation.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(activeKey), "=", active.Kvs[0].ModRevision),
	}
	compares = append(compares, inventoryCompares...)
	current, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpGet(s.ownerKey(claim.OperationID()))).Commit()
	if err != nil || !current.Succeeded || lock.Context().Err() != nil {
		return nil, store.ErrOperationOwnershipLost
	}
	for _, node := range record.Inventory.Nodes {
		if node.ID == nodeID {
			expected := ingress.PublishedRouteRevision{Generation: record.PriorActive.Generation, RouteSHA256: record.PriorActive.RouteSHA256, Present: true}
			return &ingress.AuthorizedRoutePublication{IntentID: record.ID, NodeID: nodeID, Route: record.RenderedRoute, Generation: record.Generation, Expected: expected}, nil
		}
	}
	return nil, fmt.Errorf("ingress node is absent from the workload route inventory")
}

func workloadRouteStepAllowed(intent pipeline.WorkloadReleaseIntent, receipt pipeline.WorkloadReleaseReceipt, step int) bool {
	if step < 0 || step >= len(intent.RouteSteps) {
		return false
	}
	healthy := 0
	for _, allocation := range receipt.TargetAllocations {
		if allocation.Healthy {
			healthy++
		}
	}
	if healthy != intent.TargetReplicas {
		return false
	}
	if receipt.RouteGeneration == 0 {
		return step == 0 && receipt.Phase == pipeline.WorkloadReleaseWarming
	}
	if step != receipt.RouteStep+1 || receipt.RouteGeneration != intent.FirstRouteGeneration+uint64(receipt.RouteStep) {
		return false
	}
	if receipt.Phase == pipeline.WorkloadReleaseRouting {
		return step < len(intent.RouteSteps)
	}
	return false
}

func workloadRoutePredecessorMatches(intent pipeline.WorkloadReleaseIntent, receipt pipeline.WorkloadReleaseReceipt, step int, rendered ingress.RenderedRoute, prior activeFleetRoute) bool {
	if step == 0 {
		return prior.IntentID != "" && prior.DeploymentID == intent.OldDeploymentID && prior.Generation != ^uint64(0) && prior.Generation+1 == intent.FirstRouteGeneration && prior.RouteSHA256 == rendered.SHA256
	}
	previous, err := intent.RenderRouteStep(step - 1)
	return err == nil && prior.IntentID == intent.ID && prior.DeploymentID == intent.TargetDeploymentID &&
		prior.Generation == receipt.RouteGeneration && prior.RouteSHA256 == strings.TrimPrefix(receipt.RouteSHA256, "sha256:") &&
		prior.RouteSHA256 == previous.SHA256
}
