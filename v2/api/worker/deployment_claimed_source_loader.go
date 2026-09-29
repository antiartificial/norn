package worker

import (
	"context"
	"fmt"

	"norn/v2/api/model"
)

// ClaimedFleetDeploymentSource is the deployable local InfraSpec after it has
// been bound to one signed, claimed Fleet deployment. It is the source input
// for the future normal etcd deploy executor, never a request body.
type ClaimedFleetDeploymentSource struct {
	Spec    *model.InfraSpec
	Managed VerifiedManagedDeployment
	Route   VerifiedFleetRouteSource
}

// LoadClaimedFleetDeploymentSource refuses disabled, duplicated or changed
// local app source before any Nomad input or route publication is prepared.
func LoadClaimedFleetDeploymentSource(ctx context.Context, verifier ClaimedDeploymentVerifier, claimed model.Operation, appsDir string) (ClaimedFleetDeploymentSource, error) {
	if verifier == nil || claimed.Kind != "app.deploy" || claimed.App == "" || appsDir == "" {
		return ClaimedFleetDeploymentSource{}, fmt.Errorf("claimed Fleet deployment source is unavailable")
	}
	specs, err := model.DiscoverApps(appsDir)
	if err != nil {
		return ClaimedFleetDeploymentSource{}, err
	}
	var spec *model.InfraSpec
	for _, candidate := range specs {
		if candidate.App == claimed.App {
			if spec != nil {
				return ClaimedFleetDeploymentSource{}, fmt.Errorf("claimed Fleet app has duplicate deployable InfraSpecs")
			}
			spec = candidate
		}
	}
	if spec == nil {
		return ClaimedFleetDeploymentSource{}, fmt.Errorf("claimed Fleet app has no deployable InfraSpec")
	}
	managed, err := VerifyClaimedManagedDeployment(ctx, verifier, claimed, spec)
	if err != nil {
		return ClaimedFleetDeploymentSource{}, err
	}
	if managed.Accepted.FleetAppTarget == nil || len(managed.Accepted.Regions) != 1 || managed.Accepted.Regions[0].TrafficWeight != 100 {
		return ClaimedFleetDeploymentSource{}, fmt.Errorf("claimed first Fleet route requires one signed 100-percent region")
	}
	route, err := VerifyClaimedFleetRouteSource(ctx, verifier, claimed, spec, managed.Accepted.Regions[0].Name)
	if err != nil || route.OperationID != managed.Accepted.Operation.ID || route.DeploymentID != managed.Accepted.Deployment.ID || route.AcceptanceID != managed.Accepted.Intent.ID || route.AcceptanceDigest != managed.Accepted.Intent.CanonicalDigest || route.SpecDigest != managed.Accepted.Deployment.SpecDigest ||
		route.FleetCluster != managed.Accepted.FleetAppTarget.Cluster || route.FleetEnvironment != managed.Accepted.FleetAppTarget.FleetEnvironment || route.TargetGeneration != managed.Accepted.FleetAppTarget.Generation {
		return ClaimedFleetDeploymentSource{}, fmt.Errorf("claimed Fleet route source changed during verification: %v", err)
	}
	return ClaimedFleetDeploymentSource{Spec: spec, Managed: managed, Route: route}, nil
}
