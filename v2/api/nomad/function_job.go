package nomad

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
)

import nomadapi "github.com/hashicorp/nomad/api"

type FunctionInvocationJobIdentity struct{ JobID string }
type FunctionInvocationJobState string

const (
	FunctionInvocationJobNotFound      FunctionInvocationJobState = "not-found"
	FunctionInvocationJobFound         FunctionInvocationJobState = "found"
	FunctionInvocationJobIndeterminate FunctionInvocationJobState = "indeterminate"
)

// FunctionInvocationJobObservation is the redacted, complete proof the
// worker needs to reconcile a reserved function job. It contains no task
// environment, config, template data, or Nomad response errors.
type FunctionInvocationJobObservation struct {
	State           FunctionInvocationJobState
	JobID           string
	OwnerMarker     string
	JobSpecDigest   string
	JobVersion      *uint64
	ModifyIndex     uint64
	EvaluationIDs   []string
	AllocationIDs   []string
	HistoryComplete bool
}

var (
	ErrFunctionJobIdentity            = errors.New("function job identity is invalid")
	ErrFunctionJobLookupIndeterminate = errors.New("function job lookup is indeterminate")
)
var functionInvocationJobID = regexp.MustCompile(`^norn-fn-[0-9a-f]{40}$`)

// LookupFunctionInvocationJob reads one exact function job and accepts it
// only when Nomad proves its complete single-version history. The returned
// digest comes from ProjectFunctionInvocationJob, which rejects all workload
// shapes outside the closed dialect. A 404 from Info is the sole absence
// proof. Every malformed response, changed history, or failed follow-up query
// is indeterminate and carries no server response details.
func (c *Client) LookupFunctionInvocationJob(ctx context.Context, region string, identity FunctionInvocationJobIdentity) (FunctionInvocationJobObservation, error) {
	if !functionInvocationJobID.MatchString(identity.JobID) || region != "global" {
		return indeterminateFunctionJob(ErrFunctionJobIdentity)
	}
	if c == nil || c.api == nil {
		return indeterminateFunctionJob(ErrFunctionJobLookupIndeterminate)
	}
	query := (&nomadapi.QueryOptions{Region: region}).WithContext(ctx)
	job, _, err := c.api.Jobs().Info(identity.JobID, query)
	if err != nil {
		if nomadHTTPStatus(err) == http.StatusNotFound {
			return FunctionInvocationJobObservation{State: FunctionInvocationJobNotFound}, nil
		}
		return indeterminateFunctionJob(ErrFunctionJobLookupIndeterminate)
	}
	digest, err := ProjectFunctionInvocationJob(job)
	if err != nil || job == nil || job.ID == nil || *job.ID != identity.JobID || job.Version == nil || job.ModifyIndex == nil || job.JobModifyIndex == nil || *job.ModifyIndex == 0 || *job.JobModifyIndex == 0 {
		return indeterminateFunctionJob(ErrFunctionJobLookupIndeterminate)
	}
	versions, _, _, err := c.api.Jobs().Versions(identity.JobID, false, query)
	if err != nil || !exactFunctionJobHistory(identity.JobID, job, digest, versions) {
		return indeterminateFunctionJob(ErrFunctionJobLookupIndeterminate)
	}
	evaluations, _, err := c.api.Jobs().Evaluations(identity.JobID, query)
	// Nomad's general ModifyIndex advances as the batch job is evaluated and
	// allocated. Evaluations retain the immutable JobModifyIndex of the
	// submitted definition, which is the lineage this read must prove.
	evaluationIDs, ok := exactFunctionJobEvaluations(identity.JobID, *job.JobModifyIndex, evaluations)
	if err != nil || !ok {
		return indeterminateFunctionJob(ErrFunctionJobLookupIndeterminate)
	}
	allocations, _, err := c.api.Jobs().Allocations(identity.JobID, true, query)
	allocationIDs, ok := exactFunctionJobAllocations(identity.JobID, *job.Version, allocations)
	if err != nil || !ok {
		return indeterminateFunctionJob(ErrFunctionJobLookupIndeterminate)
	}
	current, _, err := c.api.Jobs().Info(identity.JobID, query)
	if err != nil || current == nil || current.Version == nil || current.ModifyIndex == nil || *current.Version != *job.Version || *current.ModifyIndex != *job.ModifyIndex {
		return indeterminateFunctionJob(ErrFunctionJobLookupIndeterminate)
	}
	currentDigest, err := ProjectFunctionInvocationJob(current)
	if err != nil || currentDigest != digest {
		return indeterminateFunctionJob(ErrFunctionJobLookupIndeterminate)
	}
	return FunctionInvocationJobObservation{
		State:           FunctionInvocationJobFound,
		JobID:           identity.JobID,
		OwnerMarker:     job.Meta[functionInvocationOwnerMeta],
		JobSpecDigest:   digest.Digest,
		JobVersion:      uint64Pointer(*job.Version),
		ModifyIndex:     *job.ModifyIndex,
		EvaluationIDs:   evaluationIDs,
		AllocationIDs:   allocationIDs,
		HistoryComplete: true,
	}, nil
}

func indeterminateFunctionJob(err error) (FunctionInvocationJobObservation, error) {
	return FunctionInvocationJobObservation{State: FunctionInvocationJobIndeterminate}, err
}

func exactFunctionJobHistory(jobID string, current *nomadapi.Job, digest FunctionInvocationJobDigest, versions []*nomadapi.Job) bool {
	if len(versions) != 1 || versions[0] == nil || current.Version == nil || current.ModifyIndex == nil {
		return false
	}
	history := versions[0]
	historyDigest, err := ProjectFunctionInvocationJob(history)
	return err == nil && historyDigest == digest && history.ID != nil && *history.ID == jobID && history.Version != nil && *history.Version == *current.Version && history.ModifyIndex != nil && *history.ModifyIndex == *current.ModifyIndex
}

func exactFunctionJobEvaluations(jobID string, modifyIndex uint64, evaluations []*nomadapi.Evaluation) ([]string, bool) {
	if len(evaluations) == 0 {
		return nil, false
	}
	ids := make([]string, 0, len(evaluations))
	seen := make(map[string]struct{}, len(evaluations))
	for _, evaluation := range evaluations {
		if evaluation == nil || evaluation.ID == "" || evaluation.JobID != jobID || evaluation.JobModifyIndex != modifyIndex {
			return nil, false
		}
		if _, exists := seen[evaluation.ID]; exists {
			return nil, false
		}
		seen[evaluation.ID] = struct{}{}
		ids = append(ids, evaluation.ID)
	}
	sort.Strings(ids)
	return ids, true
}

func exactFunctionJobAllocations(jobID string, version uint64, allocations []*nomadapi.AllocationListStub) ([]string, bool) {
	ids := make([]string, 0, len(allocations))
	seen := make(map[string]struct{}, len(allocations))
	for _, allocation := range allocations {
		if allocation == nil || allocation.ID == "" || allocation.JobID != jobID || allocation.JobVersion != version {
			return nil, false
		}
		if _, exists := seen[allocation.ID]; exists {
			return nil, false
		}
		seen[allocation.ID] = struct{}{}
		ids = append(ids, allocation.ID)
	}
	sort.Strings(ids)
	return ids, true
}

func uint64Pointer(value uint64) *uint64 { return &value }
