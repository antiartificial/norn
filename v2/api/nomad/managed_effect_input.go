package nomad

import (
	"encoding/json"
	"fmt"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// BuildManagedDeploymentJobEffectInput derives the secret-free worker
// descriptor from one accepted deployment, its exact source spec and the
// accepted database targets. The caller adds JobDigest after rendering and
// binding the operation/execution provenance to the private job.
func BuildManagedDeploymentJobEffectInput(deployment *model.Deployment, spec *model.InfraSpec, region model.ResolvedRegion, revision int64, targets map[string]database.TargetIdentity) (DeploymentJobEffectInput, error) {
	if deployment == nil || spec == nil || deployment.ID == "" || deployment.App == "" || deployment.App != spec.App ||
		region.Name == "" || region.NomadRegion == "" || !model.IsContentAddressedImage(deployment.ImageTag) {
		return DeploymentJobEffectInput{}, fmt.Errorf("managed deployment source is incomplete")
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil || digest != deployment.SpecDigest {
		return DeploymentJobEffectInput{}, fmt.Errorf("managed deployment spec differs from accepted digest")
	}
	found := false
	for _, resolved := range spec.ResolvedRegions() {
		if resolved.Name == region.Name && resolved.NomadRegion == region.NomadRegion {
			found = true
			break
		}
	}
	if !found {
		return DeploymentJobEffectInput{}, fmt.Errorf("managed deployment region differs from source spec")
	}
	if err := model.ValidateNomadVariableFilesForSpec(spec); err != nil {
		return DeploymentJobEffectInput{}, err
	}
	plan, err := PlanManagedJobInputs(spec, region, deployment.ID, revision)
	if err != nil {
		return DeploymentJobEffectInput{}, err
	}
	if len(targets) != len(plan.RuntimeDatabaseNames) {
		return DeploymentJobEffectInput{}, fmt.Errorf("managed deployment database targets are incomplete")
	}
	expected := make(map[string]string, len(targets))
	for _, name := range plan.RuntimeDatabaseNames {
		target, ok := targets[name]
		if !ok || target.ServiceID == "" || target.ServiceGeneration == 0 || target.BindingID == "" || target.BindingGeneration == 0 ||
			target.Engine == "" || target.Database == "" || target.Role == "" {
			return DeploymentJobEffectInput{}, fmt.Errorf("managed deployment database target %s is incomplete", name)
		}
		encoded, err := json.Marshal(target)
		if err != nil {
			return DeploymentJobEffectInput{}, fmt.Errorf("managed deployment database target %s cannot be encoded", name)
		}
		expected[name] = string(encoded)
	}
	return DeploymentJobEffectInput{App: deployment.App, JobID: plan.JobID, DeploymentID: deployment.ID,
		Region: region.Name, NomadRegion: region.NomadRegion, ImageTag: deployment.ImageTag,
		SpecDigest: deployment.SpecDigest, ManagedInputs: &plan, ExpectedDatabaseTargets: expected}, nil
}
