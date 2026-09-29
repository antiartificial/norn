package worker

import (
	"context"
	"fmt"
	"reflect"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

type ClaimedDeploymentInputStager interface {
	PrepareManagedDeploymentJob(context.Context, *model.Deployment, *model.InfraSpec, model.ResolvedRegion, int64,
		map[string]database.TargetIdentity, map[string]string, nomad.ManagedJobSecretSource) (*nomadapi.Job, nomad.DeploymentJobEffectInput, error)
}

var _ ClaimedDeploymentInputStager = (*nomad.Client)(nil)

// PrepareClaimedFleetDeploymentJob stages and reads back private Nomad
// Variable inputs, then checks the returned job against the inert signed plan.
// A mismatched prepared job is never passed to the submit effect boundary.
func PrepareClaimedFleetDeploymentJob(ctx context.Context, source ClaimedFleetDeploymentSource, claim store.OperationClaim, authority string,
	stager ClaimedDeploymentInputStager, databaseItems map[string]string, secrets nomad.ManagedJobSecretSource) (ClaimedFleetDeploymentJobPlan, error) {
	if stager == nil {
		return ClaimedFleetDeploymentJobPlan{}, fmt.Errorf("managed Nomad input stager is unavailable")
	}
	plan, err := BuildClaimedFleetDeploymentJobPlan(source, claim, authority)
	if err != nil {
		return ClaimedFleetDeploymentJobPlan{}, err
	}
	accepted := source.Managed.Accepted
	preparedJob, preparedInput, err := stager.PrepareManagedDeploymentJob(ctx, accepted.Deployment, source.Spec, accepted.Regions[0],
		source.Managed.CatalogRevision, source.Managed.RuntimeTargets, databaseItems, secrets)
	if err != nil {
		return ClaimedFleetDeploymentJobPlan{}, err
	}
	expectedInput := plan.Input
	expectedInput.JobDigest = ""
	if preparedJob == nil || !reflect.DeepEqual(preparedInput, expectedInput) {
		return ClaimedFleetDeploymentJobPlan{}, fmt.Errorf("staged Nomad input descriptor differs from signed plan")
	}
	if err := pipeline.BindDeploymentJobProvenance(preparedJob, accepted.Deployment.ID, accepted.Deployment.SpecDigest, accepted.Operation.Payload); err != nil {
		return ClaimedFleetDeploymentJobPlan{}, err
	}
	if preparedJob.Meta == nil {
		preparedJob.Meta = map[string]string{}
	}
	if existing := preparedJob.Meta[nomad.DeploymentOperationIDMeta]; existing != "" && existing != claim.OperationID() {
		return ClaimedFleetDeploymentJobPlan{}, fmt.Errorf("staged Nomad job has conflicting operation marker")
	}
	preparedJob.Meta[nomad.DeploymentOperationIDMeta] = claim.OperationID()
	digest, err := nomad.DigestDeploymentJob(preparedJob)
	if err != nil || digest != plan.Input.JobDigest {
		return ClaimedFleetDeploymentJobPlan{}, fmt.Errorf("staged Nomad job differs from signed plan")
	}
	preparedJob.Meta[nomad.DeploymentExecutionIDMeta] = plan.Reservation.SupervisorExecutionID
	preparedJob.Meta[nomad.DeploymentJobDigestMeta] = digest
	if !reflect.DeepEqual(preparedJob, plan.Job) {
		return ClaimedFleetDeploymentJobPlan{}, fmt.Errorf("staged Nomad revision differs from signed plan")
	}
	return plan, nil
}
