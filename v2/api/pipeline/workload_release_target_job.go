package pipeline

import (
	"fmt"
	"reflect"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

// WorkloadReleaseTargetJobPlan is a deterministic, create-only Nomad target
// derived from the immutable release plan and its exact app source. It carries
// no secret values; those are staged separately into the job's Nomad Variable.
type WorkloadReleaseTargetJobPlan struct {
	Job     *nomadapi.Job
	Request nomad.CASDeploymentJobRequest
	Inputs  *nomad.ManagedJobInputRequirements
}

// BuildWorkloadReleaseTargetJobPlan rebuilds a target from a complete accepted
// plan and verifies its digest. Admission and execution share this translator.
func BuildWorkloadReleaseTargetJobPlan(plan WorkloadReleasePlan, spec *model.InfraSpec) (WorkloadReleaseTargetJobPlan, error) {
	return buildWorkloadReleaseTargetJobPlan(plan, spec, false)
}

// DeriveWorkloadReleaseTargetJobDigest computes the only acceptable job digest
// for an admission candidate. The intent must not already contain a digest;
// callers cannot select or override the scheduler target job contents.
func DeriveWorkloadReleaseTargetJobDigest(intent WorkloadReleaseIntent, spec *model.InfraSpec) (string, error) {
	if intent.TargetJobDigest != "" {
		return "", fmt.Errorf("workload release admission cannot supply a target job digest")
	}
	// Plan validation requires a complete intent. This reserved placeholder is
	// only used to validate the non-digest fields; the job translator does not
	// consume it, and the derived digest is returned from the exact job request.
	intent.TargetJobDigest = "sha256:" + strings.Repeat("0", 64)
	plan := WorkloadReleasePlan{SchemaVersion: WorkloadReleasePlanSchema, Intent: intent}
	target, err := buildWorkloadReleaseTargetJobPlan(plan, spec, true)
	if err != nil {
		return "", err
	}
	return "sha256:" + target.Request.JobDigest, nil
}

func buildWorkloadReleaseTargetJobPlan(plan WorkloadReleasePlan, spec *model.InfraSpec, deriveDigest bool) (WorkloadReleaseTargetJobPlan, error) {
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
	if err != nil || (!deriveDigest && "sha256:"+digest != intent.TargetJobDigest) {
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
