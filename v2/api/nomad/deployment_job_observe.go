package nomad

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"
)

var ErrDeploymentJobLookupIndeterminate = errors.New("deployment job lookup is indeterminate")

type DeploymentJobObservationState string

const (
	DeploymentJobNotFound      DeploymentJobObservationState = "not-found"
	DeploymentJobFound         DeploymentJobObservationState = "found"
	DeploymentJobIndeterminate DeploymentJobObservationState = "indeterminate"
)

// DeploymentJobObservation proves the current Nomad revision, its exact
// submitted source digest, and a no-diff Nomad plan for that source. It does
// not prove allocation health or authorize release of the app effect gate.
type DeploymentJobObservation struct {
	State          DeploymentJobObservationState
	JobModifyIndex uint64
	Version        uint64
}

// LookupDeploymentJobRevision treats only a 404 as absence. Any changed or
// malformed revision remains unresolved, so recovery cannot submit again.
func (c *Client) LookupDeploymentJobRevision(ctx context.Context, request CASDeploymentJobRequest) (DeploymentJobObservation, error) {
	indeterminate := func() (DeploymentJobObservation, error) {
		return DeploymentJobObservation{State: DeploymentJobIndeterminate}, ErrDeploymentJobLookupIndeterminate
	}
	if c == nil || c.api == nil || strings.TrimSpace(request.App) == "" || strings.TrimSpace(request.Region) == "" ||
		strings.TrimSpace(request.DeploymentID) == "" || strings.TrimSpace(request.SpecDigest) == "" ||
		strings.TrimSpace(request.OperationID) == "" || strings.TrimSpace(request.ExecutionID) == "" ||
		strings.TrimSpace(request.JobDigest) == "" {
		return indeterminate()
	}
	query := (&nomadapi.QueryOptions{Region: request.Region}).WithContext(ctx)
	job, _, err := c.api.Jobs().Info(request.App, query)
	if err != nil {
		if nomadHTTPStatus(err) == http.StatusNotFound {
			return DeploymentJobObservation{State: DeploymentJobNotFound}, nil
		}
		return indeterminate()
	}
	if !matchesDeploymentJobRevision(job, request) {
		return indeterminate()
	}
	submission, _, err := c.api.Jobs().Submission(request.App, int(*job.Version), query)
	if err != nil || submission == nil || submission.Format != "json" || submission.Source == "" {
		return indeterminate()
	}
	var submitted nomadapi.Job
	if err := json.Unmarshal([]byte(submission.Source), &submitted); err != nil || !matchesDeploymentSubmission(&submitted, request) {
		return indeterminate()
	}
	digest, err := DigestDeploymentJob(&submitted)
	if err != nil || digest != request.JobDigest {
		return indeterminate()
	}
	plan, _, err := c.api.Jobs().Plan(&submitted, true, (&nomadapi.WriteOptions{Region: request.Region}).WithContext(ctx))
	if err != nil || plan == nil || plan.JobModifyIndex != *job.JobModifyIndex || plan.Diff == nil || plan.Diff.Type != "None" {
		return indeterminate()
	}
	current, _, err := c.api.Jobs().Info(request.App, query)
	if err != nil || !matchesDeploymentJobRevision(current, request) || current.Version == nil ||
		*current.Version != *job.Version || *current.JobModifyIndex != *job.JobModifyIndex {
		return indeterminate()
	}
	return DeploymentJobObservation{State: DeploymentJobFound, JobModifyIndex: *job.JobModifyIndex, Version: *job.Version}, nil
}

func matchesDeploymentSubmission(job *nomadapi.Job, request CASDeploymentJobRequest) bool {
	return job != nil && job.ID != nil && *job.ID == request.App && job.Region != nil && *job.Region == request.Region &&
		job.Meta[DeploymentIDMeta] == request.DeploymentID && job.Meta[SpecDigestMeta] == request.SpecDigest &&
		job.Meta[DeploymentOperationIDMeta] == request.OperationID && job.Meta[DeploymentExecutionIDMeta] == request.ExecutionID &&
		job.Meta[DeploymentJobDigestMeta] == request.JobDigest
}

func matchesDeploymentJobRevision(job *nomadapi.Job, request CASDeploymentJobRequest) bool {
	return job != nil && job.ID != nil && *job.ID == request.App && job.Region != nil && *job.Region == request.Region &&
		job.JobModifyIndex != nil && *job.JobModifyIndex > request.ExpectedJobModifyIndex && job.Version != nil &&
		job.Meta[DeploymentIDMeta] == request.DeploymentID && job.Meta[SpecDigestMeta] == request.SpecDigest &&
		job.Meta[DeploymentOperationIDMeta] == request.OperationID && job.Meta[DeploymentExecutionIDMeta] == request.ExecutionID &&
		job.Meta[DeploymentJobDigestMeta] == request.JobDigest
}
