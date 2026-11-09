package nomad

import (
	"context"
	"errors"
	"sort"
	"strings"

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
	Allocations    []DeploymentJobAllocationHealthObservation
}

// DeploymentJobAllocationHealthObservation is a redacted projection of one
// exact allocation from a verified submitted job revision.
type DeploymentJobAllocationHealthObservation struct {
	AllocationID   string
	NodeID         string
	JobID          string
	Namespace      string
	TaskGroup      string
	JobVersion     uint64
	JobModifyIndex uint64
	ImageDigest    string
	Healthy        bool
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
	query := (&nomadapi.QueryOptions{Region: request.Region, Namespace: effectiveNomadNamespace(request.Namespace)}).WithContext(ctx)
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
	allocationObservations := make([]DeploymentJobAllocationHealthObservation, 0, len(stubs))
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
		if stub.JobVersion != identity.Version {
			pending = true
			continue
		}
		allocation, _, err := c.api.Allocations().Info(stub.ID, query)
		if err != nil || !matchesDeploymentHealthAllocation(stub, allocation, request, identity, expected) {
			return indeterminate()
		}
		healthy := stub.DesiredStatus == nomadapi.AllocDesiredStatusRun &&
			stub.ClientStatus == nomadapi.AllocClientStatusRunning && stub.DeploymentStatus != nil &&
			stub.DeploymentStatus.Healthy != nil && *stub.DeploymentStatus.Healthy &&
			runningAllocationImage(stub, allocation, job, request.ImageTag, expected)
		allocationObservations = append(allocationObservations, DeploymentJobAllocationHealthObservation{
			AllocationID: stub.ID, NodeID: stub.NodeID, JobID: stub.JobID,
			Namespace: effectiveNomadNamespace(request.Namespace), TaskGroup: stub.TaskGroup,
			JobVersion: stub.JobVersion, JobModifyIndex: identity.JobModifyIndex,
			ImageDigest: contentAddressedImageDigest(request.ImageTag), Healthy: healthy,
		})
		if healthy {
			counts[stub.TaskGroup]++
			ids = append(ids, stub.ID)
		}
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
		sort.Strings(ids)
		sort.Slice(allocationObservations, func(i, j int) bool {
			return allocationObservations[i].AllocationID < allocationObservations[j].AllocationID
		})
		return DeploymentJobHealthObservation{State: DeploymentJobHealthPending, JobVersion: identity.Version, JobModifyIndex: identity.JobModifyIndex, AllocationIDs: ids, Allocations: allocationObservations}, nil
	}
	sort.Strings(ids)
	sort.Slice(allocationObservations, func(i, j int) bool {
		return allocationObservations[i].AllocationID < allocationObservations[j].AllocationID
	})
	return DeploymentJobHealthObservation{State: DeploymentJobHealthReady, JobVersion: identity.Version, JobModifyIndex: identity.JobModifyIndex, AllocationIDs: ids, Allocations: allocationObservations}, nil
}

func matchesDeploymentHealthAllocation(stub *nomadapi.AllocationListStub, allocation *nomadapi.Allocation, request CASDeploymentJobRequest, identity DeploymentJobObservation, expected map[string]bool) bool {
	return stub != nil && allocation != nil && stub.ID != "" && stub.NodeID != "" && stub.JobID == request.EffectiveJobID() &&
		stub.JobVersion == identity.Version && expected[stub.TaskGroup] && allocation.ID == stub.ID && allocation.NodeID == stub.NodeID &&
		allocation.JobID == stub.JobID && allocation.TaskGroup == stub.TaskGroup &&
		allocation.Namespace == effectiveNomadNamespace(request.Namespace) && allocation.Job != nil &&
		allocation.Job.JobModifyIndex != nil && *allocation.Job.JobModifyIndex == identity.JobModifyIndex &&
		exactDeploymentHealthJob(allocation.Job, request, identity)
}

func contentAddressedImageDigest(image string) string {
	separator := strings.LastIndex(image, "@sha256:")
	if separator < 0 {
		return ""
	}
	return image[separator+1:]
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
