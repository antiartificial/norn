package nomad

import (
	"context"
	"fmt"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// PrepareManagedDeploymentJob builds a private revision job and delivers its
// inputs. The caller must supply connection material from a probed resolver
// and targets recorded in signed acceptance. This method validates all local
// inputs before either Variable write, then reads delivery back. It does not
// reserve an effect or submit the job.
func (c *Client) PrepareManagedDeploymentJob(ctx context.Context, deployment *model.Deployment, spec *model.InfraSpec,
	region model.ResolvedRegion, revision int64, targets map[string]database.TargetIdentity, databaseItems map[string]string,
	secrets ManagedJobSecretSource) (*nomadapi.Job, DeploymentJobEffectInput, error) {
	if c == nil || c.api == nil {
		return nil, DeploymentJobEffectInput{}, fmt.Errorf("managed Nomad preparation is unavailable")
	}
	input, err := BuildManagedDeploymentJobEffectInput(deployment, spec, region, revision, targets)
	if err != nil {
		return nil, DeploymentJobEffectInput{}, err
	}
	job, err := TranslateManagedDeploymentForRegionAt(spec, deployment.ImageTag, nil, region, deployment.ID, revision)
	if err != nil {
		return nil, DeploymentJobEffectInput{}, err
	}
	plan := *input.ManagedInputs
	if err := ValidateManagedJobInputPlan(job, plan); err != nil {
		return nil, DeploymentJobEffectInput{}, err
	}
	secretItems, err := loadManagedJobSecrets(deployment.App, plan, secrets)
	if err != nil {
		return nil, DeploymentJobEffectInput{}, err
	}
	if err := validateManagedJobSecretItems(plan, secretItems); err != nil {
		return nil, DeploymentJobEffectInput{}, err
	}
	if err := validateManagedJobDatabaseItems(plan, databaseItems, input.ExpectedDatabaseTargets); err != nil {
		return nil, DeploymentJobEffectInput{}, err
	}
	if err := c.StageManagedJobSecretInputs(region.NomadRegion, plan, secretItems); err != nil {
		return nil, DeploymentJobEffectInput{}, err
	}
	if err := c.StageManagedJobDatabaseInputs(region.NomadRegion, plan, databaseItems, input.ExpectedDatabaseTargets); err != nil {
		return nil, DeploymentJobEffectInput{}, err
	}
	if err := c.CheckManagedJobInputs(ctx, region.NomadRegion, plan, input.ExpectedDatabaseTargets); err != nil {
		return nil, DeploymentJobEffectInput{}, err
	}
	return job, input, nil
}
