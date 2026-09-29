package nomad

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

// ManagedBackendServiceName is the Consul Catalog backend identity for one
// process in one accepted deployment. The public hostname belongs to the
// separately applied ingress route, not this service registration.
func ManagedBackendServiceName(app, process, region, deploymentID string) (string, error) {
	if app == "" || process == "" || region == "" || deploymentID == "" {
		return "", fmt.Errorf("managed backend identity is incomplete")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%d:%s%d:%s%d:%s", len(app), app, len(process), process, len(region), region, len(deploymentID), deploymentID)))
	return "norn-" + hex.EncodeToString(digest[:16]), nil
}

// ManagedDeploymentJobID identifies one deployment revision's service job.
// Keeping old and new jobs separate allows both backends to remain allocated
// while a weighted ingress transition is observed and rolled back if needed.
func ManagedDeploymentJobID(app, region, deploymentID string) (string, error) {
	if app == "" || region == "" || deploymentID == "" {
		return "", fmt.Errorf("managed deployment job identity is incomplete")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%d:%s%d:%s", len(app), app, len(region), region, len(deploymentID), deploymentID)))
	return "norn-job-" + hex.EncodeToString(digest[:16]), nil
}

// TranslateForManagedDeployment separates each deployment revision's Nomad
// job and Consul backend. The backend has only
// an unroutable reserved hostname until an ingress controller publishes and
// verifies the public file-provider route. Ordinary v2 translation is unchanged.
func TranslateForManagedDeployment(spec *model.InfraSpec, imageTag string, env map[string]string, region model.ResolvedRegion, deploymentID string) (*nomadapi.Job, error) {
	return TranslateManagedDeploymentForRegionAt(spec, imageTag, env, region, deploymentID, 0)
}

// TranslateManagedDeploymentForRegionAt binds runtime database templates to
// the exact staged catalog revision in this deployment's private job variable.
// A database-backed job without a staged revision is rejected before submit.
func TranslateManagedDeploymentForRegionAt(spec *model.InfraSpec, imageTag string, env map[string]string, region model.ResolvedRegion, deploymentID string, databaseRevision int64) (*nomadapi.Job, error) {
	if err := model.ValidateNomadVariableFilesForSpec(spec); err != nil {
		return nil, err
	}
	inputs, err := PlanManagedJobInputs(spec, region, deploymentID, databaseRevision)
	if err != nil {
		return nil, err
	}
	job := translateForRegionAtWithJobID(spec, imageTag, env, region, databaseRevision, inputs.JobID)
	// This job ID is unique to the accepted deployment. A wall-clock deploy
	// marker would change the effect digest across a retry or successor claim.
	delete(job.Meta, "deploy_ts")
	// Service processes originate in a map; the effect digest and Nomad CAS
	// readback require the same task-group sequence on every reconstruction.
	sort.Slice(job.TaskGroups, func(i, j int) bool { return *job.TaskGroups[i].Name < *job.TaskGroups[j].Name })
	for process, definition := range spec.Processes {
		if definition.Port <= 0 || !spec.ProcessRunsInRegion(definition, region.Name) {
			continue
		}
		legacyName := fmt.Sprintf("%s-%s", spec.App, process)
		for _, group := range job.TaskGroups {
			if group.Name == nil || *group.Name != process {
				continue
			}
			for _, service := range group.Services {
				if service.Name != legacyName {
					continue
				}
				name, err := ManagedBackendServiceName(spec.App, process, region.Name, deploymentID)
				if err != nil {
					return nil, err
				}
				service.Name = name
				service.Tags = servicePlacementTags(spec, region)
				if managedRegionHasEndpoint(spec, region.Name) {
					service.Tags = append(service.Tags,
						"traefik.enable=true",
						fmt.Sprintf("traefik.http.routers.%s.entrypoints=web", name),
						fmt.Sprintf("traefik.http.routers.%s.rule=Host(`%s.norn.invalid`)", name, name),
						"norn.deployment="+deploymentID,
					)
				}
			}
		}
	}
	return job, nil
}

func managedRegionHasEndpoint(spec *model.InfraSpec, region string) bool {
	for _, endpoint := range spec.Endpoints {
		if endpoint.Region != "" && endpoint.Region != region {
			continue
		}
		parsed, err := url.Parse(endpoint.URL)
		if err == nil && parsed.Hostname() != "" {
			return true
		}
	}
	return false
}
