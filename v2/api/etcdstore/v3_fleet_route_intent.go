package etcdstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

const initialFleetRouteIntentSchema = "norn.fleet-route-intent/v1"

// InitialFleetRouteIntent is a durable, unpublished first-route reservation.
// The ingress publisher and terminal traffic proof are separate boundaries.
type InitialFleetRouteIntent struct {
	SchemaVersion      string                        `json:"schemaVersion"`
	ID                 string                        `json:"id"`
	App                string                        `json:"app"`
	ControlEnvironment string                        `json:"controlEnvironment"`
	OperationID        string                        `json:"operationId"`
	DeploymentID       string                        `json:"deploymentId"`
	AcceptanceID       string                        `json:"acceptanceId"`
	AcceptanceDigest   string                        `json:"acceptanceDigest"`
	SpecDigest         string                        `json:"specDigest"`
	Region             string                        `json:"region"`
	NomadRegion        string                        `json:"nomadRegion"`
	Endpoint           model.FleetRouteEndpoint      `json:"endpoint"`
	FleetTarget        store.FleetAppTarget          `json:"fleetTarget"`
	Inventory          FleetIngressInventoryEvidence `json:"inventory"`
	Generation         uint64                        `json:"generation"`
	RenderedRoute      ingress.RenderedRoute         `json:"renderedRoute"`
}

type initialFleetRouteReservation struct {
	IntentID   string `json:"intentId"`
	Generation uint64 `json:"generation"`
}

func (s *V3OperationStore) initialFleetRouteKey(app, environment, region, deploymentID string) string {
	sum := sha256.Sum256([]byte(app + "\x00" + environment + "\x00" + region + "\x00" + deploymentID))
	return s.prefix + "/v3/fleet-route-intents/" + hex.EncodeToString(sum[:])
}

func (s *V3OperationStore) fleetRouteReservationKey(app, environment, region string) string {
	sum := sha256.Sum256([]byte(app + "\x00" + environment + "\x00" + region))
	return s.prefix + "/v3/fleet-route-reservations/" + hex.EncodeToString(sum[:])
}

func (s *V3OperationStore) fleetActiveRouteKey(app, environment, region string) string {
	sum := sha256.Sum256([]byte(app + "\x00" + environment + "\x00" + region))
	return s.prefix + "/v3/fleet-active-routes/" + hex.EncodeToString(sum[:])
}

// IntendInitialFleetRoute reserves generation one for the first route of an
// accepted deployment. It derives the endpoint and backend from the pinned
// InfraSpec and signed deployment, and selects the current active Fleet plan
// server-side. It never publishes a route or records active traffic.
func (s *V3OperationStore) IntendInitialFleetRoute(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, spec *model.InfraSpec, observerPort int) (*InitialFleetRouteIntent, error) {
	return s.initialFleetRouteIntent(ctx, claim, lock, spec, observerPort, false)
}

// CurrentInitialFleetRouteIntent is read-only. A publisher must call it before
// each external mutation and after readback; a replaced Fleet member or
// changed control authority invalidates the previously reserved intent.
func (s *V3OperationStore) CurrentInitialFleetRouteIntent(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, spec *model.InfraSpec, observerPort int) (*InitialFleetRouteIntent, error) {
	return s.initialFleetRouteIntent(ctx, claim, lock, spec, observerPort, true)
}

func (s *V3OperationStore) initialFleetRouteIntent(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, spec *model.InfraSpec, observerPort int, requireExisting bool) (*InitialFleetRouteIntent, error) {
	if lock == nil || lock.Fence() == "" || lock.Context().Err() != nil || spec == nil || observerPort < 1024 || observerPort > 65535 {
		return nil, store.ErrOperationOwnershipLost
	}
	record, operationRevision, err := s.load(ctx, claim.OperationID())
	if err != nil || record.Operation.Kind != "app.deploy" || record.Operation.Status != model.OperationRunning || record.Operation.LockedBy != claim.OwnerID() || record.Generation != claim.Generation() || record.Operation.App != spec.App {
		return nil, store.ErrOperationOwnershipLost
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
		return nil, store.ErrOperationOwnershipLost
	}
	accepted, err := s.VerifyClaimedDeployment(ctx, record.Operation)
	if err != nil || accepted.FleetAppTarget == nil || accepted.Deployment == nil || len(accepted.Regions) != 1 {
		return nil, fmt.Errorf("claimed Fleet deployment is unqualified: %v", err)
	}
	target := *accepted.FleetAppTarget
	region := accepted.Regions[0]
	digest, err := model.InfraSpecDigest(spec)
	if err != nil || digest != accepted.Deployment.SpecDigest || target.App != spec.App || target.ControlEnvironment != accepted.Deployment.Environment || target.Region != region.Name || target.NomadRegion != region.NomadRegion || !slices.Equal(target.Datacenters, region.Datacenters) || region.TrafficWeight != 100 {
		return nil, fmt.Errorf("initial Fleet route differs from pinned source or signed placement")
	}
	var pinnedRegion *model.ResolvedRegion
	for _, candidate := range spec.ResolvedRegions() {
		if candidate.Name == region.Name {
			copy := candidate
			pinnedRegion = &copy
			break
		}
	}
	if pinnedRegion == nil || pinnedRegion.NomadRegion != region.NomadRegion || pinnedRegion.TrafficWeight != region.TrafficWeight || !slices.Equal(pinnedRegion.Datacenters, region.Datacenters) {
		return nil, fmt.Errorf("initial Fleet route placement differs from pinned InfraSpec")
	}
	endpoint, err := model.ResolveFleetRouteEndpoint(spec, region.Name)
	if err != nil {
		return nil, err
	}
	rendered, err := ingress.RenderWeightedRoute(ingress.WeightedRoute{App: spec.App, Process: endpoint.Process, Region: region.Name, Endpoint: endpoint.Origin, Backends: []ingress.WeightedBackend{{DeploymentID: accepted.Deployment.ID, Weight: 100}}})
	if err != nil || ingress.RequireTLSRenderedRoute(rendered) != nil {
		return nil, fmt.Errorf("initial Fleet route cannot be rendered securely: %v", err)
	}
	intentKey := s.initialFleetRouteKey(spec.App, target.ControlEnvironment, region.Name, accepted.Deployment.ID)
	reservationKey := s.fleetRouteReservationKey(spec.App, target.ControlEnvironment, region.Name)
	activeRouteKey := s.fleetActiveRouteKey(spec.App, target.ControlEnvironment, region.Name)
	reservation, err := s.kv.Get(ctx, reservationKey)
	if err != nil {
		return nil, err
	}
	if len(reservation.Kvs) != 0 {
		intent, intentRevision, err := s.replayInitialFleetRouteIntent(ctx, reservation.Kvs[0].Value, intentKey, accepted, endpoint, rendered)
		if err != nil || !requireExisting {
			return intent, err
		}
		currentTarget, targetRevision, err := s.loadFleetAppTarget(ctx, target.App, target.ControlEnvironment)
		if err != nil || !sameFleetAppTarget(target, currentTarget) {
			return nil, fmt.Errorf("Fleet app target changed after route intent")
		}
		currentInventory, err := s.CurrentActiveFleetIngressInventory(ctx, target.Cluster, target.FleetEnvironment, observerPort)
		if err != nil || !sameFleetIngressInventory(&intent.Inventory, currentInventory) {
			return nil, fmt.Errorf("active Fleet ingress inventory changed after route intent")
		}
		reservationAfter, err := s.kv.Get(ctx, reservationKey)
		if err != nil || len(reservationAfter.Kvs) != 1 || reservationAfter.Kvs[0].ModRevision != reservation.Kvs[0].ModRevision {
			return nil, fmt.Errorf("Fleet route reservation changed during revalidation")
		}
		intentAfter, err := s.kv.Get(ctx, intentKey)
		if err != nil || len(intentAfter.Kvs) != 1 || intentAfter.Kvs[0].ModRevision != intentRevision {
			return nil, fmt.Errorf("Fleet route intent changed during revalidation")
		}
		activeRoute, err := s.kv.Get(ctx, activeRouteKey)
		if err != nil || len(activeRoute.Kvs) != 0 {
			return nil, fmt.Errorf("initial Fleet route was superseded by an active route")
		}
		// Inventory readback can take long enough for the operation lease or
		// app lock to change. Recheck all local authority in one etcd snapshot
		// before returning an intent as currently publishable.
		stillCurrent, err := s.kv.Txn(ctx).If(
			clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
			clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
			clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
			clientv3.Compare(clientv3.Value(s.appLockKey(spec.App)), "=", lock.Fence()),
			clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(target.App, target.ControlEnvironment)), "=", targetRevision),
			clientv3.Compare(clientv3.ModRevision(reservationKey), "=", reservation.Kvs[0].ModRevision),
			clientv3.Compare(clientv3.ModRevision(intentKey), "=", intentRevision),
			clientv3.Compare(clientv3.CreateRevision(activeRouteKey), "=", 0),
		).Then(clientv3.OpGet(s.ownerKey(claim.OperationID()))).Commit()
		if err != nil || !stillCurrent.Succeeded || len(stillCurrent.Responses) != 1 || len(stillCurrent.Responses[0].GetResponseRange().Kvs) != 1 || stillCurrent.Responses[0].GetResponseRange().Kvs[0].Lease == 0 {
			return nil, store.ErrOperationOwnershipLost
		}
		return intent, nil
	}
	if requireExisting {
		return nil, fmt.Errorf("initial Fleet route intent has not been reserved")
	}
	activeRoute, err := s.kv.Get(ctx, activeRouteKey)
	if err != nil || len(activeRoute.Kvs) != 0 {
		return nil, fmt.Errorf("initial Fleet route requires no prior active route")
	}
	currentTarget, targetRevision, err := s.loadFleetAppTarget(ctx, target.App, target.ControlEnvironment)
	if err != nil || !sameFleetAppTarget(target, currentTarget) {
		return nil, fmt.Errorf("Fleet app target changed after deployment acceptance")
	}
	inventory, err := s.CurrentActiveFleetIngressInventory(ctx, target.Cluster, target.FleetEnvironment, observerPort)
	if err != nil {
		return nil, err
	}
	inventoryCompares, err := s.fleetIngressInventoryCompares(*inventory)
	if err != nil {
		return nil, err
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(claim.OperationID()))
	if err != nil || len(index.Kvs) != 1 {
		return nil, fmt.Errorf("deployment acceptance index changed")
	}
	acceptanceKey := string(index.Kvs[0].Value)
	loadedAcceptance, err := s.loadAcceptance(ctx, acceptanceKey)
	if err != nil {
		return nil, fmt.Errorf("deployment acceptance changed")
	}
	rechecked, err := s.replay(ctx, acceptanceKey, loadedAcceptance, loadedAcceptance.record.Identity, accepted.Intent.Fingerprint)
	if err != nil || rechecked.Intent.ID != accepted.Intent.ID || rechecked.Intent.CanonicalDigest != accepted.Intent.CanonicalDigest || rechecked.Deployment == nil || rechecked.Deployment.ID != accepted.Deployment.ID {
		return nil, fmt.Errorf("deployment acceptance changed before route intent")
	}
	intent := InitialFleetRouteIntent{SchemaVersion: initialFleetRouteIntentSchema, ID: uuid.NewString(), App: spec.App, ControlEnvironment: target.ControlEnvironment,
		OperationID: claim.OperationID(), DeploymentID: accepted.Deployment.ID, AcceptanceID: accepted.Intent.ID, AcceptanceDigest: accepted.Intent.CanonicalDigest,
		SpecDigest: digest, Region: region.Name, NomadRegion: region.NomadRegion, Endpoint: endpoint, FleetTarget: target,
		Inventory: *inventory, Generation: 1, RenderedRoute: rendered}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	reservationValue, err := json.Marshal(initialFleetRouteReservation{IntentID: intent.ID, Generation: intent.Generation})
	if err != nil {
		return nil, err
	}
	comparisons := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.Value(s.appLockKey(spec.App)), "=", lock.Fence()),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(target.App, target.ControlEnvironment)), "=", targetRevision),
		clientv3.Compare(clientv3.ModRevision(s.operationAcceptanceIndexKey(claim.OperationID())), "=", index.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(acceptanceKey), "=", loadedAcceptance.revision),
		clientv3.Compare(clientv3.CreateRevision(activeRouteKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(reservationKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(intentKey), "=", 0),
	}
	comparisons = append(comparisons, inventoryCompares...)
	txn, err := s.kv.Txn(ctx).If(comparisons...).Then(clientv3.OpPut(intentKey, string(encoded)), clientv3.OpPut(reservationKey, string(reservationValue))).Commit()
	if err != nil {
		return nil, err
	}
	if !txn.Succeeded {
		return nil, fmt.Errorf("initial Fleet route authority changed before intent commit")
	}
	return &intent, nil
}

func (s *V3OperationStore) replayInitialFleetRouteIntent(ctx context.Context, reservationRaw []byte, intentKey string, accepted store.AcceptedOperation, endpoint model.FleetRouteEndpoint, rendered ingress.RenderedRoute) (*InitialFleetRouteIntent, int64, error) {
	var reservation initialFleetRouteReservation
	if err := decodeV3Record(reservationRaw, &reservation); err != nil || reservation.IntentID == "" || reservation.Generation != 1 {
		return nil, 0, fmt.Errorf("Fleet route reservation is invalid")
	}
	response, err := s.kv.Get(ctx, intentKey)
	if err != nil || len(response.Kvs) != 1 {
		return nil, 0, fmt.Errorf("Fleet route reservation has no matching intent")
	}
	var intent InitialFleetRouteIntent
	if err := decodeV3Record(response.Kvs[0].Value, &intent); err != nil || intent.SchemaVersion != initialFleetRouteIntentSchema || intent.ID != reservation.IntentID || intent.Generation != reservation.Generation ||
		intent.OperationID != accepted.Operation.ID || intent.DeploymentID != accepted.Deployment.ID || intent.AcceptanceID != accepted.Intent.ID || intent.AcceptanceDigest != accepted.Intent.CanonicalDigest || intent.SpecDigest != accepted.Deployment.SpecDigest ||
		intent.Endpoint != endpoint || intent.App != accepted.Operation.App || intent.ControlEnvironment != accepted.Deployment.Environment || intent.Region != accepted.Regions[0].Name || intent.NomadRegion != accepted.Regions[0].NomadRegion ||
		intent.RenderedRoute.SHA256 != rendered.SHA256 || !bytes.Equal(intent.RenderedRoute.YAML, rendered.YAML) || intent.RenderedRoute.RouterName != rendered.RouterName || intent.RenderedRoute.ServiceName != rendered.ServiceName || intent.RenderedRoute.EndpointHost != rendered.EndpointHost || !slices.Equal(intent.RenderedRoute.BackendNames, rendered.BackendNames) ||
		intent.Inventory.Cluster != accepted.FleetAppTarget.Cluster || intent.Inventory.Environment != accepted.FleetAppTarget.FleetEnvironment || intent.Inventory.ActivePointerRevision <= 0 || intent.Inventory.ActiveClusterEpochRevision <= 0 || len(intent.Inventory.Nodes) < 2 || !sameFleetAppTarget(intent.FleetTarget, *accepted.FleetAppTarget) {
		return nil, 0, fmt.Errorf("Fleet route reservation conflicts with signed deployment")
	}
	return &intent, response.Kvs[0].ModRevision, nil
}
