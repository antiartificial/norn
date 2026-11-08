package nomad

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"
)

var ErrStoppedDeploymentObservation = errors.New("exact stopped deployment observation is unavailable")
var ErrDeploymentStopConflict = errors.New("old deployment revision changed before stop")
var ErrDeploymentStopIndeterminate = errors.New("old deployment stop outcome is indeterminate")

// StoppedDeploymentRequest binds terminal-allocation readback to the original
// deployment revision and the post-stop revision returned by its durable
// effect. This method is read-only; callers must obtain the latter pins from
// the effect receipt, never by adopting whatever revision currently exists.
type StoppedDeploymentRequest struct {
	JobID                  string
	Region                 string
	Namespace              string
	DeploymentID           string
	StopOperationID        string
	IntentDigest           string
	SpecDigest             string
	JobDigest              string
	ImageDigest            string
	TaskGroup              string
	OriginalJobVersion     uint64
	OriginalJobModifyIndex uint64
	StoppedJobVersion      uint64
	StoppedJobModifyIndex  uint64
	AllocationIDs          []string
}

type StoppedDeploymentObservation struct {
	JobID                string
	DeploymentID         string
	JobVersion           uint64
	JobModifyIndex       uint64
	DrainedAllocationIDs []string
}

type DeploymentStopReceipt struct {
	JobID          string `json:"jobId"`
	DeploymentID   string `json:"deploymentId"`
	OperationID    string `json:"operationId"`
	IntentDigest   string `json:"intentDigest"`
	JobVersion     uint64 `json:"jobVersion"`
	JobModifyIndex uint64 `json:"jobModifyIndex"`
}

// StopDeploymentCAS stops only the pinned old deployment revision and stamps
// an immutable operation marker in the same Nomad registration. A lost
// response is reconciled with ObserveStoppedDeploymentByIntent; callers must
// never repeat the registration merely because this method returned unknown.
func (c *Client) StopDeploymentCAS(ctx context.Context, request StoppedDeploymentRequest) (DeploymentStopReceipt, error) {
	if c == nil || c.api == nil || !validDeploymentStopRequest(request) || request.OriginalJobVersion == ^uint64(0) {
		return DeploymentStopReceipt{}, fmt.Errorf("old deployment stop request is incomplete")
	}
	if err := c.requireAtomicJobCAS(); err != nil {
		return DeploymentStopReceipt{}, err
	}
	query := (&nomadapi.QueryOptions{Region: request.Region, Namespace: effectiveNomadNamespace(request.Namespace), AllowStale: false}).WithContext(ctx)
	job, _, err := c.api.Jobs().Info(request.JobID, query)
	if err != nil || job == nil || job.ID == nil || *job.ID != request.JobID || job.Region == nil || *job.Region != request.Region ||
		jobNamespace(job.Namespace) != effectiveNomadNamespace(request.Namespace) || job.Version == nil || *job.Version != request.OriginalJobVersion ||
		job.JobModifyIndex == nil || *job.JobModifyIndex != request.OriginalJobModifyIndex || job.Stop == nil || *job.Stop ||
		job.Meta[DeploymentIDMeta] != request.DeploymentID || job.Meta[SpecDigestMeta] != request.SpecDigest || job.Meta[DeploymentJobDigestMeta] != request.JobDigest ||
		!stoppedDeploymentJobMatches(job, request) {
		if err == nil {
			return DeploymentStopReceipt{}, ErrDeploymentStopConflict
		}
		return DeploymentStopReceipt{}, ErrDeploymentStopIndeterminate
	}
	stubs, _, err := c.api.Jobs().Allocations(request.JobID, true, query)
	if err != nil || !exactDeploymentStopAllocations(request, stubs) {
		return DeploymentStopReceipt{}, ErrDeploymentStopIndeterminate
	}
	stopped := true
	copy := *job
	copy.Stop = &stopped
	if copy.Meta == nil {
		copy.Meta = make(map[string]string)
	}
	copy.Meta[DeploymentStopOperationMeta] = request.StopOperationID
	copy.Meta[DeploymentStopIntentMeta] = request.IntentDigest
	registered, _, err := c.api.Jobs().RegisterOpts(&copy, &nomadapi.RegisterOptions{EnforceIndex: true, ModifyIndex: request.OriginalJobModifyIndex}, (&nomadapi.WriteOptions{Region: request.Region, Namespace: effectiveNomadNamespace(request.Namespace)}).WithContext(ctx))
	if err != nil {
		if strings.Contains(err.Error(), nomadapi.RegisterEnforceIndexErrPrefix) {
			return DeploymentStopReceipt{}, ErrDeploymentStopConflict
		}
		return DeploymentStopReceipt{}, ErrDeploymentStopIndeterminate
	}
	if registered == nil || registered.JobModifyIndex <= request.OriginalJobModifyIndex {
		return DeploymentStopReceipt{}, ErrDeploymentStopIndeterminate
	}
	return DeploymentStopReceipt{JobID: request.JobID, DeploymentID: request.DeploymentID, OperationID: request.StopOperationID,
		IntentDigest: request.IntentDigest, JobVersion: request.OriginalJobVersion + 1, JobModifyIndex: registered.JobModifyIndex}, nil
}

func validDeploymentStopRequest(request StoppedDeploymentRequest) bool {
	request.StoppedJobVersion = request.OriginalJobVersion + 1
	request.StoppedJobModifyIndex = request.OriginalJobModifyIndex + 1
	return validStoppedDeploymentRequest(request)
}

func exactDeploymentStopAllocations(request StoppedDeploymentRequest, stubs []*nomadapi.AllocationListStub) bool {
	want := make(map[string]struct{}, len(request.AllocationIDs))
	for _, id := range request.AllocationIDs {
		want[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(stubs))
	live := make(map[string]struct{}, len(want))
	for _, stub := range stubs {
		if stub == nil || stub.ID == "" || stub.JobID != request.JobID {
			return false
		}
		if _, duplicate := seen[stub.ID]; duplicate {
			return false
		}
		seen[stub.ID] = struct{}{}
		if casStopTerminal(stub.ClientStatus) {
			continue
		}
		if stub.JobVersion != request.OriginalJobVersion || stub.TaskGroup != request.TaskGroup || stub.DesiredStatus != nomadapi.AllocDesiredStatusRun {
			return false
		}
		if _, expected := want[stub.ID]; !expected {
			return false
		}
		live[stub.ID] = struct{}{}
	}
	if len(live) != len(want) {
		return false
	}
	for id := range want {
		if _, ok := live[id]; !ok {
			return false
		}
	}
	return true
}

// ObserveStoppedDeployment proves that the exact old deployment job is at
// the stop effect's resulting revision and every allocation named by the
// original intent is terminal. Any unnamed live allocation or missing,
// duplicated, or substituted identity fails closed.
func (c *Client) ObserveStoppedDeployment(ctx context.Context, request StoppedDeploymentRequest) (StoppedDeploymentObservation, error) {
	failed := func() (StoppedDeploymentObservation, error) {
		return StoppedDeploymentObservation{}, ErrStoppedDeploymentObservation
	}
	if c == nil || c.api == nil || !validStoppedDeploymentRequest(request) || request.OriginalJobVersion == ^uint64(0) ||
		request.StoppedJobVersion != request.OriginalJobVersion+1 || request.StoppedJobModifyIndex <= request.OriginalJobModifyIndex {
		return failed()
	}
	query := (&nomadapi.QueryOptions{Region: request.Region, Namespace: effectiveNomadNamespace(request.Namespace), AllowStale: false}).WithContext(ctx)
	job, _, err := c.api.Jobs().Info(request.JobID, query)
	if err != nil || job == nil || job.ID == nil || *job.ID != request.JobID || job.Region == nil || *job.Region != request.Region ||
		jobNamespace(job.Namespace) != effectiveNomadNamespace(request.Namespace) || job.Version == nil || *job.Version != request.StoppedJobVersion ||
		job.JobModifyIndex == nil || *job.JobModifyIndex != request.StoppedJobModifyIndex || job.Stop == nil || !*job.Stop ||
		job.Meta[DeploymentIDMeta] != request.DeploymentID || job.Meta[SpecDigestMeta] != request.SpecDigest || job.Meta[DeploymentJobDigestMeta] != request.JobDigest ||
		job.Meta[DeploymentStopOperationMeta] != request.StopOperationID || job.Meta[DeploymentStopIntentMeta] != request.IntentDigest ||
		!stoppedDeploymentJobMatches(job, request) {
		return failed()
	}
	stubs, _, err := c.api.Jobs().Allocations(request.JobID, true, query)
	if err != nil {
		return failed()
	}
	want := make(map[string]struct{}, len(request.AllocationIDs))
	for _, id := range request.AllocationIDs {
		want[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(stubs))
	terminal := make(map[string]struct{}, len(want))
	for _, stub := range stubs {
		if stub == nil || stub.ID == "" || stub.JobID != request.JobID {
			return failed()
		}
		if _, duplicate := seen[stub.ID]; duplicate {
			return failed()
		}
		seen[stub.ID] = struct{}{}
		_, expected := want[stub.ID]
		if !casStopTerminal(stub.ClientStatus) {
			return failed()
		}
		if expected {
			if stub.JobVersion != request.OriginalJobVersion || stub.TaskGroup != request.TaskGroup || stub.DesiredStatus != nomadapi.AllocDesiredStatusStop {
				return failed()
			}
			terminal[stub.ID] = struct{}{}
		}
	}
	if len(terminal) != len(want) {
		return failed()
	}
	ids := append([]string(nil), request.AllocationIDs...)
	sort.Strings(ids)
	return StoppedDeploymentObservation{JobID: request.JobID, DeploymentID: request.DeploymentID,
		JobVersion: request.StoppedJobVersion, JobModifyIndex: request.StoppedJobModifyIndex, DrainedAllocationIDs: ids}, nil
}

// ObserveStoppedDeploymentByIntent rediscovers the post-stop revision from the
// immutable effect marker after a lost write response. It never writes and
// never adopts an unmarked or differently marked revision.
func (c *Client) ObserveStoppedDeploymentByIntent(ctx context.Context, request StoppedDeploymentRequest) (StoppedDeploymentObservation, error) {
	receipt, err := c.LookupStoppedDeploymentByIntent(ctx, request)
	if err != nil {
		return StoppedDeploymentObservation{}, err
	}
	request.StoppedJobVersion = receipt.JobVersion
	request.StoppedJobModifyIndex = receipt.JobModifyIndex
	return c.ObserveStoppedDeployment(ctx, request)
}

// LookupStoppedDeploymentByIntent proves only the post-stop job revision and
// marker. Allocation drain is a separate observation because Nomad may still
// be terminating the pinned allocations after accepting the stop.
func (c *Client) LookupStoppedDeploymentByIntent(ctx context.Context, request StoppedDeploymentRequest) (DeploymentStopReceipt, error) {
	if c == nil || c.api == nil || !validStoppedDeploymentBaseRequest(request) || request.OriginalJobVersion == ^uint64(0) {
		return DeploymentStopReceipt{}, ErrStoppedDeploymentObservation
	}
	query := (&nomadapi.QueryOptions{Region: request.Region, Namespace: effectiveNomadNamespace(request.Namespace), AllowStale: false}).WithContext(ctx)
	job, _, err := c.api.Jobs().Info(request.JobID, query)
	if err != nil || job == nil || job.ID == nil || *job.ID != request.JobID || job.Region == nil || *job.Region != request.Region ||
		jobNamespace(job.Namespace) != effectiveNomadNamespace(request.Namespace) || job.Version == nil || *job.Version != request.OriginalJobVersion+1 ||
		job.JobModifyIndex == nil || *job.JobModifyIndex <= request.OriginalJobModifyIndex || job.Stop == nil || !*job.Stop ||
		job.Meta[DeploymentIDMeta] != request.DeploymentID || job.Meta[SpecDigestMeta] != request.SpecDigest || job.Meta[DeploymentJobDigestMeta] != request.JobDigest ||
		job.Meta[DeploymentStopOperationMeta] != request.StopOperationID || job.Meta[DeploymentStopIntentMeta] != request.IntentDigest ||
		!stoppedDeploymentJobMatches(job, request) {
		return DeploymentStopReceipt{}, ErrStoppedDeploymentObservation
	}
	return DeploymentStopReceipt{JobID: request.JobID, DeploymentID: request.DeploymentID,
		OperationID: request.StopOperationID, IntentDigest: request.IntentDigest,
		JobVersion: *job.Version, JobModifyIndex: *job.JobModifyIndex}, nil
}

func validStoppedDeploymentRequest(request StoppedDeploymentRequest) bool {
	return validStoppedDeploymentBaseRequest(request) && request.StoppedJobVersion != 0 && request.StoppedJobModifyIndex != 0
}

func validStoppedDeploymentBaseRequest(request StoppedDeploymentRequest) bool {
	if strings.TrimSpace(request.JobID) == "" || strings.TrimSpace(request.Region) == "" || strings.TrimSpace(request.DeploymentID) == "" ||
		strings.TrimSpace(request.StopOperationID) == "" || !validDeploymentDigest(request.IntentDigest) ||
		!validDeploymentDigest(request.SpecDigest) || !validDeploymentDigest(request.JobDigest) || !validDeploymentDigest(request.ImageDigest) ||
		strings.TrimSpace(request.TaskGroup) == "" || request.OriginalJobVersion == 0 || request.OriginalJobModifyIndex == 0 || len(request.AllocationIDs) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(request.AllocationIDs))
	for _, id := range request.AllocationIDs {
		if strings.TrimSpace(id) == "" {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func validDeploymentDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range strings.TrimPrefix(value, "sha256:") {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

func stoppedDeploymentJobMatches(job *nomadapi.Job, request StoppedDeploymentRequest) bool {
	for _, group := range job.TaskGroups {
		if group == nil || group.Name == nil || *group.Name != request.TaskGroup {
			continue
		}
		if len(group.Tasks) != 1 || group.Tasks[0] == nil {
			return false
		}
		image, _ := group.Tasks[0].Config["image"].(string)
		return contentAddressedImageDigest(image) == request.ImageDigest
	}
	return false
}
