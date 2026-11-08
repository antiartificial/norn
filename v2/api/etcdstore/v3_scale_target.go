package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
)

// FleetScaleTarget is the immutable deployment identity accepted for a native
// scale mutation. SpecDigest binds the scheduler target to the exact InfraSpec
// that produced the active Fleet route.
type FleetScaleTarget struct {
	JobID          string
	DeploymentID   string
	SpecDigest     string
	NomadNamespace string
}

// ActiveFleetScaleTarget resolves a managed Nomad job only from the published
// route and its completed deployment. The logical app name is not a Nomad job
// ID for normal Fleet deployments.
func (s *V3OperationStore) ActiveFleetScaleTarget(ctx context.Context, app, environment, region string) (FleetScaleTarget, error) {
	if s == nil || s.kv == nil || app == "" || environment == "" || region == "" {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet scale target is unavailable")
	}
	activeResponse, err := s.kv.Get(ctx, s.fleetActiveRouteKey(app, environment, region))
	if err != nil || len(activeResponse.Kvs) != 1 {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet route is unavailable: %v", err)
	}
	var active activeFleetRoute
	if decodeV3Record(activeResponse.Kvs[0].Value, &active) != nil || active.IntentID == "" || active.DeploymentID == "" || active.Generation == 0 || active.RouteSHA256 == "" {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet route is invalid")
	}
	index, err := s.kv.Get(ctx, s.initialFleetRouteIDKey(active.IntentID))
	if err != nil {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet route intent index is unavailable: %v", err)
	}
	if len(index.Kvs) == 0 {
		return s.activeWorkloadReleaseScaleTarget(ctx, app, environment, region, active, activeResponse.Kvs[0].ModRevision)
	}
	if len(index.Kvs) != 1 {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet route intent index is invalid")
	}
	intentKey := s.initialFleetRouteKey(app, environment, region, active.DeploymentID)
	if string(index.Kvs[0].Value) != intentKey {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet route intent index changed")
	}
	intentResponse, err := s.kv.Get(ctx, intentKey)
	if err != nil || len(intentResponse.Kvs) != 1 {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet route intent is unavailable: %v", err)
	}
	var intent InitialFleetRouteIntent
	if decodeV3Record(intentResponse.Kvs[0].Value, &intent) != nil || intent.SchemaVersion != initialFleetRouteIntentSchema || intent.ID != active.IntentID ||
		intent.App != app || intent.ControlEnvironment != environment || intent.Region != region || intent.DeploymentID != active.DeploymentID ||
		intent.Generation != active.Generation || intent.RenderedRoute.SHA256 != active.RouteSHA256 || strings.TrimSpace(intent.SpecDigest) == "" {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet route differs from its intent")
	}
	reservationKey := s.fleetRouteReservationKey(app, environment, region)
	reservation, err := s.kv.Get(ctx, reservationKey)
	if err != nil || len(reservation.Kvs) != 1 {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet route reservation is unavailable: %v", err)
	}
	var reserved initialFleetRouteReservation
	if decodeV3Record(reservation.Kvs[0].Value, &reserved) != nil || reserved.IntentID != active.IntentID || reserved.Generation != active.Generation {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet route reservation differs from its route")
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, app, environment)
	if err != nil || !sameFleetAppTarget(intent.FleetTarget, target) || target.Region != region || target.NomadRegion != intent.NomadRegion {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet target differs from its route")
	}
	operation, err := s.GetOperation(ctx, intent.OperationID)
	if err != nil || operation == nil || operation.Kind != "app.deploy" || operation.Status != model.OperationSucceeded || operation.App != app || operation.Payload["deploymentId"] != active.DeploymentID {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet deployment receipt is unavailable")
	}
	deployment, err := s.GetDeployment(ctx, active.DeploymentID)
	if err != nil || deployment == nil || deployment.App != app || deployment.Environment != environment || deployment.Status != model.StatusDeployed || deployment.SpecDigest != intent.SpecDigest {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet deployment differs from its signed route: %v", err)
	}
	stable, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.fleetActiveRouteKey(app, environment, region)), "=", activeResponse.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.initialFleetRouteIDKey(active.IntentID)), "=", index.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(intentKey), "=", intentResponse.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(reservationKey), "=", reservation.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(app, environment)), "=", targetRevision),
	).Then(clientv3.OpGet(s.fleetActiveRouteKey(app, environment, region))).Commit()
	if err != nil || !stable.Succeeded {
		return FleetScaleTarget{}, fmt.Errorf("active Fleet scale target changed during verification")
	}
	jobID, err := nomad.ManagedDeploymentJobID(app, region, active.DeploymentID)
	if err != nil {
		return FleetScaleTarget{}, err
	}
	return FleetScaleTarget{JobID: jobID, DeploymentID: active.DeploymentID, SpecDigest: intent.SpecDigest, NomadNamespace: target.NomadNamespace}, nil
}

// activeWorkloadReleaseScaleTarget resolves a route promoted by a completed
// workload release. It replays the signed acceptance and fences the active
// route, reservation, receipt, operation, acceptance, and Fleet target in one
// read-only etcd transaction before exposing the scale target.
func (s *V3OperationStore) activeWorkloadReleaseScaleTarget(ctx context.Context, app, environment, region string, active activeFleetRoute, activeRevision int64) (FleetScaleTarget, error) {
	receiptRecord, err := s.LoadWorkloadReleaseReceipt(ctx, active.IntentID)
	if err != nil || receiptRecord == nil {
		return FleetScaleTarget{}, fmt.Errorf("active workload release receipt is unavailable: %v", err)
	}
	operationID := receiptRecord.OperationID
	indexKey := s.operationAcceptanceIndexKey(operationID)
	index, err := s.kv.Get(ctx, indexKey)
	if err != nil || len(index.Kvs) != 1 {
		return FleetScaleTarget{}, fmt.Errorf("active workload release acceptance index is unavailable: %v", err)
	}
	acceptanceKey := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, acceptanceKey)
	if err != nil || loaded.record.Accepted.Operation.ID != operationID || loaded.record.Identity.Kind != "app.workload-release" ||
		loaded.record.Identity.Resource != "app/"+app {
		return FleetScaleTarget{}, fmt.Errorf("active workload release signed acceptance is invalid: %v", err)
	}
	accepted, err := s.replay(ctx, acceptanceKey, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil || accepted.Operation.ID != operationID || accepted.Operation.Kind != "app.workload-release" ||
		accepted.Operation.Status != model.OperationSucceeded || accepted.Operation.App != app {
		return FleetScaleTarget{}, fmt.Errorf("active workload release operation is not successfully completed: %v", err)
	}
	var intent pipeline.WorkloadReleaseIntent
	intentRaw := effectPayloadString(accepted.Operation.Payload, "workloadReleaseIntent")
	if intentRaw == "" || json.Unmarshal([]byte(intentRaw), &intent) != nil || intent.Validate() != nil || intent.ID == "" ||
		intent.App != app || intent.ControlEnvironment != environment || intent.Region != region || intent.TargetDeploymentID != active.DeploymentID {
		return FleetScaleTarget{}, fmt.Errorf("active workload release intent differs from its route")
	}
	if receiptRecord.OperationID != operationID || intent.ID != active.IntentID {
		return FleetScaleTarget{}, fmt.Errorf("active workload release receipt is unavailable: %v", err)
	}
	receiptKey := s.workloadReleaseReceiptKey(intent.ID)
	storedReceipt, err := s.kv.Get(ctx, receiptKey)
	var receiptEnvelope v3WorkloadReleaseReceipt
	if err != nil || len(storedReceipt.Kvs) != 1 || decodeV3Record(storedReceipt.Kvs[0].Value, &receiptEnvelope) != nil ||
		receiptEnvelope.ReceiptDigest != receiptRecord.ReceiptDigest || !bytes.Equal(receiptEnvelope.Receipt, receiptRecord.Receipt) {
		return FleetScaleTarget{}, fmt.Errorf("active workload release receipt changed during verification")
	}
	var receipt pipeline.WorkloadReleaseReceipt
	if json.Unmarshal(receiptRecord.Receipt, &receipt) != nil || receipt.ValidateAgainst(intent) != nil ||
		receipt.Phase != pipeline.WorkloadReleaseComplete || receipt.RouteGeneration != active.Generation ||
		strings.TrimPrefix(receipt.RouteSHA256, "sha256:") != active.RouteSHA256 || receipt.RouteStep != len(intent.RouteSteps)-1 {
		return FleetScaleTarget{}, fmt.Errorf("active workload release receipt differs from its route")
	}
	target, targetRevision, err := s.loadFleetAppTarget(ctx, app, environment)
	if err != nil || target.Cluster != intent.FleetCluster || target.FleetEnvironment != intent.FleetEnvironment ||
		target.Region != region || target.NomadRegion != intent.NomadRegion || target.NomadNamespace != intent.Namespace ||
		target.Generation != intent.FleetTargetGeneration || !slices.Equal(target.Datacenters, intent.FleetDatacenters) {
		return FleetScaleTarget{}, fmt.Errorf("current Fleet target differs from the completed workload release")
	}
	reservationKey := s.fleetRouteReservationKey(app, environment, region)
	reservation, err := s.kv.Get(ctx, reservationKey)
	if err != nil || len(reservation.Kvs) != 1 {
		return FleetScaleTarget{}, fmt.Errorf("active workload route reservation is unavailable: %v", err)
	}
	var reserved initialFleetRouteReservation
	if decodeV3Record(reservation.Kvs[0].Value, &reserved) != nil || reserved.IntentID != active.IntentID || reserved.Generation != active.Generation {
		return FleetScaleTarget{}, fmt.Errorf("active workload route reservation differs from its route")
	}
	operation, operationRevision, err := s.load(ctx, operationID)
	if err != nil || operation.Operation.Status != model.OperationSucceeded || operation.Operation.Kind != "app.workload-release" {
		return FleetScaleTarget{}, fmt.Errorf("active workload release operation changed")
	}
	wantPayload, err := json.Marshal(accepted.Operation.Payload)
	if err != nil {
		return FleetScaleTarget{}, err
	}
	gotPayload, err := json.Marshal(operation.Operation.Payload)
	if err != nil || !bytes.Equal(wantPayload, gotPayload) {
		return FleetScaleTarget{}, fmt.Errorf("active workload release payload differs from signed acceptance")
	}
	stable, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.fleetActiveRouteKey(app, environment, region)), "=", activeRevision),
		clientv3.Compare(clientv3.CreateRevision(s.initialFleetRouteIDKey(active.IntentID)), "=", 0),
		clientv3.Compare(clientv3.ModRevision(reservationKey), "=", reservation.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(receiptKey), "=", storedReceipt.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(operationID)), "=", operationRevision),
		clientv3.Compare(clientv3.ModRevision(indexKey), "=", index.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(acceptanceKey), "=", loaded.revision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(app, environment)), "=", targetRevision),
	).Then(clientv3.OpGet(s.fleetActiveRouteKey(app, environment, region))).Commit()
	if err != nil || !stable.Succeeded {
		return FleetScaleTarget{}, fmt.Errorf("active workload scale target changed during verification")
	}
	return FleetScaleTarget{JobID: intent.TargetJobID, DeploymentID: intent.TargetDeploymentID,
		SpecDigest: intent.TargetSpecDigest, NomadNamespace: intent.Namespace}, nil
}
