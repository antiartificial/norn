package nomad

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"
	"norn/v2/api/model"
)

var ErrDeploymentLineageUnavailable = errors.New("active deployment lineage is unavailable")

// ActiveDeploymentLineage is a redacted, read-only projection of the exact
// currently serving Nomad deployment. It contains no submitted job source,
// environment, or secret material.
type ActiveDeploymentLineage struct {
	App           string
	DeploymentID  string
	OperationID   string
	Region        string
	NomadRegion   string
	Namespace     string
	SpecDigest    string
	ImageTag      string
	JobID         string
	JobDigest     string
	JobVersion    uint64
	ModifyIndex   uint64
	Process       string
	Replicas      int
	AllocationIDs []string
}

type ActiveDeploymentLineageObserver interface {
	ObserveActiveDeploymentLineage(context.Context, string, string, string, string, string, string, string, string, string, string) (ActiveDeploymentLineage, error)
}

// ObserveActiveDeploymentLineage derives scheduler coordinates from the
// managed deployment's signed identity and exact Nomad readback. Callers
// provide control-plane deployment identity only; version, modify index,
// digest, replica count, and allocation IDs are never caller-selected.
func (c *Client) ObserveActiveDeploymentLineage(ctx context.Context, app, placementRegion, nomadRegion, namespace,
	deploymentID, operationID, specDigest, expectedJobDigest, imageTag, process string) (ActiveDeploymentLineage, error) {
	failed := func(cause error) (ActiveDeploymentLineage, error) {
		return ActiveDeploymentLineage{}, fmt.Errorf("%w: %v", ErrDeploymentLineageUnavailable, cause)
	}
	if c == nil || c.api == nil || strings.TrimSpace(app) == "" || strings.TrimSpace(placementRegion) == "" ||
		strings.TrimSpace(nomadRegion) == "" || strings.TrimSpace(deploymentID) == "" ||
		strings.TrimSpace(operationID) == "" || strings.TrimSpace(process) == "" ||
		!validDeploymentSpecDigest(specDigest) || !validDeploymentJobDigest(expectedJobDigest) || !model.IsContentAddressedImage(imageTag) {
		return failed(fmt.Errorf("control-owned deployment identity is incomplete"))
	}
	jobID, err := ManagedDeploymentJobID(app, placementRegion, deploymentID)
	if err != nil {
		return failed(err)
	}
	query := (&nomadapi.QueryOptions{Region: nomadRegion, Namespace: effectiveNomadNamespace(namespace)}).WithContext(ctx)
	job, _, err := c.api.Jobs().Info(jobID, query)
	if err != nil || !validActiveDeploymentJob(job, jobID, nomadRegion, namespace, deploymentID, operationID, specDigest, process, imageTag) {
		return failed(fmt.Errorf("current managed Nomad job differs from the accepted deployment"))
	}
	if job.Version == nil || *job.Version == 0 {
		return failed(fmt.Errorf("current managed Nomad job has no version"))
	}
	submission, _, err := c.api.Jobs().Submission(jobID, int(*job.Version), query)
	if err != nil || submission == nil || submission.Format != "json" || submission.Source == "" {
		return failed(fmt.Errorf("current managed Nomad job submission is unavailable"))
	}
	var submitted nomadapi.Job
	if err := json.Unmarshal([]byte(submission.Source), &submitted); err != nil ||
		!validActiveDeploymentJob(&submitted, jobID, nomadRegion, namespace, deploymentID, operationID, specDigest, process, imageTag) {
		return failed(fmt.Errorf("current managed Nomad job submission is invalid"))
	}
	jobDigest, err := DigestDeploymentJob(&submitted)
	if err != nil || jobDigest != expectedJobDigest || job.Meta[DeploymentJobDigestMeta] != expectedJobDigest ||
		job.Meta[DeploymentExecutionIDMeta] != DeploymentEffectExecutionID(operationID, placementRegion, jobDigest) {
		return failed(fmt.Errorf("current managed Nomad job digest or execution marker differs"))
	}
	group, ok := activeDeploymentTaskGroup(&submitted, process)
	if !ok || group.Count == nil || *group.Count < 1 {
		return failed(fmt.Errorf("serving process is absent from the managed Nomad job"))
	}
	request := CASDeploymentJobRequest{App: app, JobID: jobID, PlacementRegion: placementRegion,
		Region: nomadRegion, Namespace: effectiveNomadNamespace(namespace), ExpectedJobModifyIndex: 0,
		DeploymentID: deploymentID, SpecDigest: specDigest, OperationID: operationID,
		ExecutionID: DeploymentEffectExecutionID(operationID, placementRegion, jobDigest), JobDigest: jobDigest, ImageTag: imageTag}
	identity, err := c.LookupDeploymentJobRevision(ctx, request)
	if err != nil || identity.State != DeploymentJobFound {
		return failed(fmt.Errorf("exact managed Nomad revision readback failed"))
	}
	health, err := c.ObserveDeploymentJobHealth(ctx, request)
	if err != nil || health.State != DeploymentJobHealthReady || health.JobVersion != identity.Version ||
		health.JobModifyIndex != identity.JobModifyIndex {
		return failed(fmt.Errorf("managed Nomad deployment is not fully healthy"))
	}
	allocationIDs := make([]string, 0, *group.Count)
	for _, allocation := range health.Allocations {
		if allocation.TaskGroup == process && allocation.Healthy {
			allocationIDs = append(allocationIDs, allocation.AllocationID)
		}
	}
	if len(allocationIDs) != *group.Count {
		return failed(fmt.Errorf("serving process allocation count differs from its job group"))
	}
	return ActiveDeploymentLineage{App: app, DeploymentID: deploymentID, OperationID: operationID,
		Region: placementRegion, NomadRegion: nomadRegion, Namespace: effectiveNomadNamespace(namespace),
		SpecDigest: specDigest, ImageTag: imageTag, JobID: jobID, JobDigest: jobDigest,
		JobVersion: identity.Version, ModifyIndex: identity.JobModifyIndex, Process: process,
		Replicas: *group.Count, AllocationIDs: allocationIDs}, nil
}

func validDeploymentJobDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validDeploymentSpecDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validActiveDeploymentJob(job *nomadapi.Job, jobID, region, namespace, deploymentID, operationID,
	specDigest, process, imageTag string) bool {
	if job == nil || job.ID == nil || *job.ID != jobID || job.Region == nil || *job.Region != region ||
		jobNamespace(job.Namespace) != effectiveNomadNamespace(namespace) || job.Type == nil || *job.Type != "service" ||
		job.Stop == nil || *job.Stop || job.Meta[DeploymentIDMeta] != deploymentID ||
		job.Meta[SpecDigestMeta] != specDigest || job.Meta[DeploymentOperationIDMeta] != operationID {
		return false
	}
	group, ok := activeDeploymentTaskGroup(job, process)
	if !ok || group.Count == nil || *group.Count < 1 {
		return false
	}
	for _, taskGroup := range job.TaskGroups {
		if taskGroup == nil || taskGroup.Name == nil || taskGroup.Count == nil || *taskGroup.Count < 1 || len(taskGroup.Tasks) != 1 ||
			taskGroup.Tasks[0] == nil || taskGroup.Tasks[0].Name != *taskGroup.Name || taskGroup.Tasks[0].Driver != "docker" ||
			taskGroup.Tasks[0].Config["image"] != imageTag {
			return false
		}
	}
	return true
}

func activeDeploymentTaskGroup(job *nomadapi.Job, process string) (*nomadapi.TaskGroup, bool) {
	if job == nil {
		return nil, false
	}
	var selected *nomadapi.TaskGroup
	for _, group := range job.TaskGroups {
		if group == nil || group.Name == nil {
			return nil, false
		}
		if *group.Name == process {
			if selected != nil {
				return nil, false
			}
			selected = group
		}
	}
	return selected, selected != nil
}
