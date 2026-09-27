package nomad

import (
	"context"
	"errors"
	"sort"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

var ErrDeploymentJobHealthIndeterminate = errors.New("deployment job health is indeterminate")

type DeploymentJobHealthState string

const (
	DeploymentJobHealthPending       DeploymentJobHealthState = "pending"
	DeploymentJobHealthReady         DeploymentJobHealthState = "ready"
	DeploymentJobHealthIndeterminate DeploymentJobHealthState = "indeterminate"
)

type DeploymentJobHealthObservation struct {
	State          DeploymentJobHealthState
	JobVersion     uint64
	JobModifyIndex uint64
	AllocationIDs  []string
}

// ObserveDeploymentJobHealth proves that every declared service group has
// exactly its desired number of healthy, running allocations from the same
// versioned job whose source and scheduler diff were already verified.
// Pending placement is not failure evidence; unknown or changed provenance
// is indeterminate and must keep the deployment effect gate.
func (c *Client) ObserveDeploymentJobHealth(ctx context.Context, request CASDeploymentJobRequest) (DeploymentJobHealthObservation, error) {
	indeterminate := func() (DeploymentJobHealthObservation, error) {
		return DeploymentJobHealthObservation{State: DeploymentJobHealthIndeterminate}, ErrDeploymentJobHealthIndeterminate
	}
	if c == nil || c.api == nil || !model.IsContentAddressedImage(request.ImageTag) {
		return indeterminate()
	}
	identity, err := c.LookupDeploymentJobRevision(ctx, request)
	if err != nil || identity.State != DeploymentJobFound {
		return indeterminate()
	}
	query := (&nomadapi.QueryOptions{Region: request.Region}).WithContext(ctx)
	job, _, err := c.api.Jobs().Info(request.EffectiveJobID(), query)
	if err != nil || !exactDeploymentHealthJob(job, request, identity) {
		return indeterminate()
	}
	stubs, _, err := c.api.Jobs().Allocations(request.EffectiveJobID(), true, query)
	if err != nil {
		return indeterminate()
	}
	counts := make(map[string]int, len(job.TaskGroups))
	ids := make([]string, 0, len(stubs))
	seenIDs := make(map[string]bool, len(stubs))
	pending := false
	expected := make(map[string]bool, len(job.TaskGroups))
	for _, group := range job.TaskGroups {
		expected[*group.Name] = true
	}
	for _, stub := range stubs {
		if stub == nil || stub.ID == "" || stub.JobID != request.EffectiveJobID() || seenIDs[stub.ID] {
			return indeterminate()
		}
		seenIDs[stub.ID] = true
		if casStopTerminal(stub.ClientStatus) {
			continue
		}
		if !expected[stub.TaskGroup] {
			return indeterminate()
		}
		if stub.JobVersion != identity.Version || stub.DesiredStatus != nomadapi.AllocDesiredStatusRun ||
			stub.ClientStatus != nomadapi.AllocClientStatusRunning || stub.DeploymentStatus == nil ||
			stub.DeploymentStatus.Healthy == nil || !*stub.DeploymentStatus.Healthy {
			pending = true
			continue
		}
		allocation, _, err := c.api.Allocations().Info(stub.ID, query)
		if err != nil || !runningAllocationImage(stub, allocation, job, request.ImageTag, expected) ||
			!matchesDeploymentJobRevision(allocation.Job, request) || allocation.Job.JobModifyIndex == nil ||
			*allocation.Job.JobModifyIndex != identity.JobModifyIndex {
			return indeterminate()
		}
		counts[stub.TaskGroup]++
		ids = append(ids, stub.ID)
	}
	for _, group := range job.TaskGroups {
		if counts[*group.Name] != *group.Count {
			pending = true
		}
	}
	current, _, err := c.api.Jobs().Info(request.EffectiveJobID(), query)
	if err != nil || !exactDeploymentHealthJob(current, request, identity) {
		return indeterminate()
	}
	if pending {
		return DeploymentJobHealthObservation{State: DeploymentJobHealthPending, JobVersion: identity.Version, JobModifyIndex: identity.JobModifyIndex}, nil
	}
	sort.Strings(ids)
	return DeploymentJobHealthObservation{State: DeploymentJobHealthReady, JobVersion: identity.Version, JobModifyIndex: identity.JobModifyIndex, AllocationIDs: ids}, nil
}

func exactDeploymentHealthJob(job *nomadapi.Job, request CASDeploymentJobRequest, identity DeploymentJobObservation) bool {
	if !matchesDeploymentJobRevision(job, request) || job.Version == nil || *job.Version != identity.Version ||
		job.JobModifyIndex == nil || *job.JobModifyIndex != identity.JobModifyIndex || job.Type == nil || *job.Type != "service" ||
		job.Stop == nil || *job.Stop || len(job.TaskGroups) == 0 {
		return false
	}
	seen := make(map[string]bool, len(job.TaskGroups))
	for _, group := range job.TaskGroups {
		if group == nil || group.Name == nil || *group.Name == "" || seen[*group.Name] || group.Count == nil || *group.Count < 1 ||
			(group.Update != nil && group.Update.Canary != nil && *group.Update.Canary > 0) ||
			len(group.Tasks) != 1 || group.Tasks[0] == nil || group.Tasks[0].Name != *group.Name ||
			group.Tasks[0].Driver != "docker" || group.Tasks[0].Config["image"] != request.ImageTag {
			return false
		}
		seen[*group.Name] = true
	}
	return true
}
