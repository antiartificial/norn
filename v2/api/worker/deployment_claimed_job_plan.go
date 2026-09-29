package worker

import (
	"encoding/json"
	"fmt"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/effect"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// ClaimedFleetDeploymentJobPlan is inert. Managed Nomad inputs must be
// staged and read back before EnsureDeploymentJobEffect may submit this job.
type ClaimedFleetDeploymentJobPlan struct {
	Job         *nomadapi.Job
	Input       nomad.DeploymentJobEffectInput
	Reservation effect.Reservation
}

// BuildClaimedFleetDeploymentJobPlan binds one managed revision to the
// signed deployment, database target set and live claim. It does not write
// Nomad variables or submit a job.
func BuildClaimedFleetDeploymentJobPlan(source ClaimedFleetDeploymentSource, claim store.OperationClaim, authority string) (ClaimedFleetDeploymentJobPlan, error) {
	accepted := source.Managed.Accepted
	if source.Spec == nil || accepted.Deployment == nil || accepted.FleetAppTarget == nil || authority == "" || claim.OperationID() == "" || claim.OwnerID() == "" || claim.Generation() <= 0 ||
		accepted.Operation.ID != claim.OperationID() || accepted.Operation.Kind != "app.deploy" || accepted.Operation.App != source.Spec.App || len(accepted.Regions) != 1 ||
		accepted.Regions[0].TrafficWeight != 100 || source.Route.OperationID != claim.OperationID() || source.Route.DeploymentID != accepted.Deployment.ID ||
		source.Route.AcceptanceID != accepted.Intent.ID || source.Route.AcceptanceDigest != accepted.Intent.CanonicalDigest ||
		source.Route.Region != accepted.Regions[0].Name || source.Route.SpecDigest != accepted.Deployment.SpecDigest ||
		source.Route.FleetCluster != accepted.FleetAppTarget.Cluster || source.Route.FleetEnvironment != accepted.FleetAppTarget.FleetEnvironment ||
		source.Route.TargetGeneration != accepted.FleetAppTarget.Generation || source.Route.NomadRegion != accepted.Regions[0].NomadRegion {
		return ClaimedFleetDeploymentJobPlan{}, fmt.Errorf("claimed Fleet deployment job source is incomplete")
	}
	region := accepted.Regions[0]
	input, err := nomad.BuildManagedDeploymentJobEffectInput(accepted.Deployment, source.Spec, region, source.Managed.CatalogRevision, source.Managed.RuntimeTargets)
	if err != nil {
		return ClaimedFleetDeploymentJobPlan{}, err
	}
	job, err := nomad.TranslateManagedDeploymentForRegionAt(source.Spec, accepted.Deployment.ImageTag, nil, region, accepted.Deployment.ID, source.Managed.CatalogRevision)
	if err != nil {
		return ClaimedFleetDeploymentJobPlan{}, err
	}
	if err := pipeline.BindDeploymentJobProvenance(job, accepted.Deployment.ID, accepted.Deployment.SpecDigest, accepted.Operation.Payload); err != nil {
		return ClaimedFleetDeploymentJobPlan{}, err
	}
	if job.Meta == nil {
		job.Meta = map[string]string{}
	}
	if existing := job.Meta[nomad.DeploymentOperationIDMeta]; existing != "" && existing != claim.OperationID() {
		return ClaimedFleetDeploymentJobPlan{}, fmt.Errorf("managed Nomad job has conflicting operation marker")
	}
	job.Meta[nomad.DeploymentOperationIDMeta] = claim.OperationID()
	input.JobDigest, err = nomad.DigestDeploymentJob(job)
	if err != nil {
		return ClaimedFleetDeploymentJobPlan{}, err
	}
	executionID := nomad.DeploymentEffectExecutionID(claim.OperationID(), region.Name, input.JobDigest)
	job.Meta[nomad.DeploymentExecutionIDMeta] = executionID
	job.Meta[nomad.DeploymentJobDigestMeta] = input.JobDigest
	if err := nomad.ValidateManagedJobInputPlan(job, *input.ManagedInputs); err != nil {
		return ClaimedFleetDeploymentJobPlan{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return ClaimedFleetDeploymentJobPlan{}, err
	}
	reservation := effect.Reservation{Authority: authority, Resource: "app/" + source.Spec.App + "/deploy/" + region.Name,
		OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()},
		Stage:          "app.deploy.nomad.submit", Supervisor: "nomad-deployment", SupervisorExecutionID: executionID, LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		return ClaimedFleetDeploymentJobPlan{}, err
	}
	return ClaimedFleetDeploymentJobPlan{Job: job, Input: input, Reservation: reservation}, nil
}
