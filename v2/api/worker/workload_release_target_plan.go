package worker

import (
	"fmt"
	"reflect"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
)

// WorkloadReleaseTargetJobPlan is a deterministic, create-only Nomad target
// derived from the immutable release plan and its exact app source. It carries
// no secret values; those are staged separately into the job's Nomad Variable.
type WorkloadReleaseTargetJobPlan struct {
	Job     *nomadapi.Job
	Request nomad.CASDeploymentJobRequest
	Inputs  *nomad.ManagedJobInputRequirements
}

// BuildWorkloadReleaseTargetJobPlan builds the private target job before
// submission. This first release path is intentionally database-free: a
// database-backed app needs an immutable catalog revision in the plan before
// it can use this builder.
func BuildWorkloadReleaseTargetJobPlan(plan pipeline.WorkloadReleasePlan, spec *model.InfraSpec) (WorkloadReleaseTargetJobPlan, error) {
	invalid := func() (WorkloadReleaseTargetJobPlan, error) {
		return WorkloadReleaseTargetJobPlan{}, fmt.Errorf("workload release target source differs from the immutable plan")
	}
	if err := plan.Validate(); err != nil {
		return WorkloadReleaseTargetJobPlan{}, fmt.Errorf("workload release plan is invalid: %w", err)
	}
	if spec == nil || spec.App != plan.Intent.App || spec.NamedDatabases() ||
		(spec.Infrastructure != nil && spec.Infrastructure.Postgres != nil) {
		return invalid()
	}
	intent := plan.Intent
	specDigest, err := model.InfraSpecDigest(spec)
	if err != nil || specDigest != intent.TargetSpecDigest || spec.NomadNamespace != intent.Namespace {
		return invalid()
	}
	var region model.ResolvedRegion
	regionFound := false
	for _, candidate := range spec.ResolvedRegions() {
		if candidate.Name == intent.Region && candidate.NomadRegion == intent.NomadRegion && reflect.DeepEqual(candidate.Datacenters, intent.FleetDatacenters) {
			region, regionFound = candidate, true
			break
		}
	}
	process, processFound := spec.Processes[intent.Process]
	if !regionFound || !processFound || process.Schedule != "" || process.Port <= 0 || !spec.ProcessRunsInRegion(process, intent.Region) {
		return invalid()
	}
	endpointFound := false
	for _, endpoint := range spec.Endpoints {
		if endpoint.URL != intent.Endpoint || (endpoint.Region != "" && endpoint.Region != intent.Region) || (endpoint.Process != "" && endpoint.Process != intent.Process) {
			continue
		}
		if endpoint.TrafficProbe == nil || endpoint.TrafficProbe.Path != intent.TrafficProbePath || endpoint.TrafficProbe.BodySHA256 != intent.TrafficProbeBodySHA256 || endpoint.RestrictedIngress == nil ||
			!reflect.DeepEqual(endpoint.RestrictedIngress.PublicPaths, intent.PublicPaths) ||
			!reflect.DeepEqual(endpoint.RestrictedIngress.RetryGETPaths, intent.RetryGETPaths) ||
			endpoint.RestrictedIngress.PrivateReadinessPath != intent.PrivateReadinessPath {
			return invalid()
		}
		endpointFound = true
		break
	}
	if !endpointFound || !model.IsContentAddressedImage(intent.TargetImage) {
		return invalid()
	}
	job, err := nomad.TranslateManagedDeploymentForRegionAt(spec, intent.TargetImage, nil, region, intent.TargetDeploymentID, 0)
	if err != nil || job == nil || job.ID == nil || *job.ID != intent.TargetJobID {
		return invalid()
	}
	nomad.ApplyDesiredReplicaCounts(job, map[string]int{intent.Process: intent.TargetReplicas})
	if job.Meta == nil {
		job.Meta = map[string]string{}
	}
	job.Meta[nomad.DeploymentOperationIDMeta] = intent.TargetOperationID
	job.Meta[nomad.DeploymentExecutionIDMeta] = intent.TargetExecutionID
	digest, err := nomad.DigestDeploymentJob(job)
	if err != nil || "sha256:"+digest != intent.TargetJobDigest {
		return invalid()
	}
	job.Meta[nomad.DeploymentJobDigestMeta] = digest
	inputs, err := nomad.PlanManagedJobInputs(spec, region, intent.TargetDeploymentID, 0)
	if err != nil || nomad.ValidateManagedJobInputPlan(job, inputs) != nil {
		return invalid()
	}
	request := nomad.CASDeploymentJobRequest{Job: job, App: intent.App, JobID: intent.TargetJobID,
		PlacementRegion: intent.Region, Region: intent.NomadRegion, Namespace: intent.Namespace,
		ExpectedJobModifyIndex: 0, DeploymentID: intent.TargetDeploymentID, SpecDigest: intent.TargetSpecDigest,
		OperationID: intent.TargetOperationID, ExecutionID: intent.TargetExecutionID,
		JobDigest: digest, ImageTag: intent.TargetImage}
	if strings.TrimSpace(request.OperationID) == "" || strings.TrimSpace(request.ExecutionID) == "" {
		return invalid()
	}
	return WorkloadReleaseTargetJobPlan{Job: job, Request: request, Inputs: &inputs}, nil
}
