package nomad

import (
	"context"
	"errors"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

// ErrRunningAppImageUnproven means Nomad cannot prove that every live service
// allocation is healthy and uses the deployment's content-addressed image.
var ErrRunningAppImageUnproven = errors.New("running application image is unproven")

// VerifyRunningAppImage checks the actual allocation job snapshots, not only
// the mutable job definition. Function-only apps have no service allocation;
// their deployment image is checked by the caller's recorded provenance.
func (c *Client) VerifyRunningAppImage(ctx context.Context, spec *model.InfraSpec, image string) error {
	if c == nil || c.api == nil || spec == nil || !model.IsContentAddressedImage(image) {
		return ErrRunningAppImageUnproven
	}
	seenRegions := make(map[string]bool)
	for _, region := range spec.ResolvedRegions() {
		expected := make(map[string]bool)
		for name, process := range spec.Processes {
			if process.Schedule == "" && process.Function == nil && spec.ProcessRunsInRegion(process, region.Name) {
				expected[name] = true
			}
		}
		if len(expected) == 0 {
			continue
		}
		if seenRegions[region.NomadRegion] {
			return ErrRunningAppImageUnproven
		}
		seenRegions[region.NomadRegion] = true
		query := (&nomadapi.QueryOptions{Region: region.NomadRegion}).WithContext(ctx)
		job, _, err := c.api.Jobs().Info(spec.App, query)
		if err != nil || !runningJobImage(job, spec.App, region.NomadRegion, image, expected) {
			return ErrRunningAppImageUnproven
		}
		stubs, _, err := c.api.Jobs().Allocations(spec.App, true, query)
		if err != nil {
			return ErrRunningAppImageUnproven
		}
		counts := make(map[string]int)
		for _, stub := range stubs {
			if stub == nil || stub.ID == "" || stub.JobID != spec.App {
				return ErrRunningAppImageUnproven
			}
			if stub.ClientStatus == nomadapi.AllocClientStatusComplete || stub.ClientStatus == nomadapi.AllocClientStatusFailed || stub.ClientStatus == nomadapi.AllocClientStatusLost {
				continue
			}
			allocation, _, err := c.api.Allocations().Info(stub.ID, query)
			if err != nil || !runningAllocationImage(stub, allocation, job, image, expected) {
				return ErrRunningAppImageUnproven
			}
			counts[stub.TaskGroup]++
		}
		for _, group := range job.TaskGroups {
			if group.Count == nil || *group.Count < 1 || counts[*group.Name] != *group.Count {
				return ErrRunningAppImageUnproven
			}
		}
		current, _, err := c.api.Jobs().Info(spec.App, query)
		if err != nil || current == nil || current.Version == nil || current.JobModifyIndex == nil || *current.Version != *job.Version || *current.JobModifyIndex != *job.JobModifyIndex {
			return ErrRunningAppImageUnproven
		}
	}
	return nil
}

func runningJobImage(job *nomadapi.Job, app, region, image string, expected map[string]bool) bool {
	if job == nil || job.ID == nil || *job.ID != app || job.Region == nil || *job.Region != region || job.Type == nil || *job.Type != "service" || (job.Stop != nil && *job.Stop) || job.Version == nil || job.JobModifyIndex == nil || *job.JobModifyIndex == 0 || len(job.TaskGroups) != len(expected) {
		return false
	}
	seen := make(map[string]bool)
	for _, group := range job.TaskGroups {
		if group == nil || group.Name == nil || !expected[*group.Name] || seen[*group.Name] || len(group.Tasks) != 1 || group.Tasks[0] == nil || group.Tasks[0].Name != *group.Name || group.Tasks[0].Driver != "docker" || group.Tasks[0].Config["image"] != image {
			return false
		}
		seen[*group.Name] = true
	}
	return len(seen) == len(expected)
}

func runningAllocationImage(stub *nomadapi.AllocationListStub, allocation *nomadapi.Allocation, job *nomadapi.Job, image string, expected map[string]bool) bool {
	if stub == nil || allocation == nil || job == nil || job.Version == nil || !expected[stub.TaskGroup] || stub.JobVersion != *job.Version || stub.ClientStatus != nomadapi.AllocClientStatusRunning || stub.DesiredStatus != nomadapi.AllocDesiredStatusRun || stub.DeploymentStatus == nil || stub.DeploymentStatus.Healthy == nil || !*stub.DeploymentStatus.Healthy || allocation.ID != stub.ID || allocation.JobID != stub.JobID || allocation.TaskGroup != stub.TaskGroup || allocation.ClientStatus != stub.ClientStatus || allocation.DesiredStatus != stub.DesiredStatus || allocation.Job == nil || allocation.Job.Version == nil || *allocation.Job.Version != stub.JobVersion || len(allocation.TaskStates) != 1 {
		return false
	}
	state := allocation.TaskStates[stub.TaskGroup]
	return state != nil && state.State == "running" && runningJobImage(allocation.Job, stub.JobID, *job.Region, image, expected)
}
