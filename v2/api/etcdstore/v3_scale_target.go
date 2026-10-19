package etcdstore

import (
	"context"
	"fmt"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

// ActiveFleetScaleTarget resolves a managed Nomad job only from the published
// route and its completed deployment. The logical app name is not a Nomad job
// ID for normal Fleet deployments.
func (s *V3OperationStore) ActiveFleetScaleTarget(ctx context.Context, app, environment, region string) (string, string, error) {
	if s == nil || s.kv == nil || app == "" || environment == "" || region == "" {
		return "", "", fmt.Errorf("active Fleet scale target is unavailable")
	}
	activeResponse, err := s.kv.Get(ctx, s.fleetActiveRouteKey(app, environment, region))
	if err != nil || len(activeResponse.Kvs) != 1 {
		return "", "", fmt.Errorf("active Fleet route is unavailable: %v", err)
	}
	var active activeFleetRoute
	if decodeV3Record(activeResponse.Kvs[0].Value, &active) != nil || active.IntentID == "" || active.DeploymentID == "" || active.Generation == 0 || active.RouteSHA256 == "" {
		return "", "", fmt.Errorf("active Fleet route is invalid")
	}
	index, err := s.kv.Get(ctx, s.initialFleetRouteIDKey(active.IntentID))
	if err != nil || len(index.Kvs) != 1 {
		return "", "", fmt.Errorf("active Fleet route intent index is unavailable: %v", err)
	}
	intentKey := s.initialFleetRouteKey(app, environment, region, active.DeploymentID)
	if string(index.Kvs[0].Value) != intentKey {
		return "", "", fmt.Errorf("active Fleet route intent index changed")
	}
	intentResponse, err := s.kv.Get(ctx, intentKey)
	if err != nil || len(intentResponse.Kvs) != 1 {
		return "", "", fmt.Errorf("active Fleet route intent is unavailable: %v", err)
	}
	var intent InitialFleetRouteIntent
	if decodeV3Record(intentResponse.Kvs[0].Value, &intent) != nil || intent.SchemaVersion != initialFleetRouteIntentSchema || intent.ID != active.IntentID ||
		intent.App != app || intent.ControlEnvironment != environment || intent.Region != region || intent.DeploymentID != active.DeploymentID ||
		intent.Generation != active.Generation || intent.RenderedRoute.SHA256 != active.RouteSHA256 {
		return "", "", fmt.Errorf("active Fleet route differs from its intent")
	}
	target, _, err := s.loadFleetAppTarget(ctx, app, environment)
	if err != nil || !sameFleetAppTarget(intent.FleetTarget, target) || target.Region != region || target.NomadRegion != intent.NomadRegion {
		return "", "", fmt.Errorf("active Fleet target differs from its route")
	}
	operation, err := s.GetOperation(ctx, intent.OperationID)
	if err != nil || operation == nil || operation.Kind != "app.deploy" || operation.Status != model.OperationSucceeded || operation.App != app || operation.Payload["deploymentId"] != active.DeploymentID {
		return "", "", fmt.Errorf("active Fleet deployment receipt is unavailable")
	}
	jobID, err := nomad.ManagedDeploymentJobID(app, region, active.DeploymentID)
	if err != nil {
		return "", "", err
	}
	return jobID, active.DeploymentID, nil
}
