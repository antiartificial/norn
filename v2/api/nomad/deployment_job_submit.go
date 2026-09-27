package nomad

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"
)

const (
	DeploymentExecutionIDMeta = "norn.executionId"
	DeploymentOperationIDMeta = "norn.operationId"
	DeploymentJobDigestMeta   = "norn.jobDigest"
)

var (
	ErrDeploymentJobSubmitConflict      = errors.New("deployment job revision changed")
	ErrDeploymentJobSubmitIndeterminate = errors.New("deployment job submission is indeterminate")
)

type CASDeploymentJobRequest struct {
	Job                    *nomadapi.Job
	App                    string
	JobID                  string
	PlacementRegion        string
	Region                 string
	ExpectedJobModifyIndex uint64
	DeploymentID           string
	SpecDigest             string
	OperationID            string
	ExecutionID            string
	JobDigest              string
	ImageTag               string
}

func (r CASDeploymentJobRequest) EffectiveJobID() string {
	if r.JobID != "" {
		return r.JobID
	}
	return r.App
}

func (r CASDeploymentJobRequest) validJobIdentity() bool {
	if r.JobID == "" {
		return r.PlacementRegion == ""
	}
	if r.PlacementRegion == "" || r.ExpectedJobModifyIndex != 0 {
		return false
	}
	want, err := ManagedDeploymentJobID(r.App, r.PlacementRegion, r.DeploymentID)
	return err == nil && r.JobID == want
}

// RegisterDeploymentJobCAS performs one guarded Nomad registration. A lost
// response is never permission to submit again; the caller must read back the
// exact execution marker and job revision before completing its effect.
func (c *Client) RegisterDeploymentJobCAS(ctx context.Context, request CASDeploymentJobRequest) (string, error) {
	jobDigest, digestErr := hex.DecodeString(request.JobDigest)
	if c == nil || c.api == nil || request.Job == nil || request.Job.ID == nil || request.Job.Region == nil ||
		strings.TrimSpace(request.Region) == "" || *request.Job.Region != request.Region ||
		strings.TrimSpace(request.App) == "" || !request.validJobIdentity() || *request.Job.ID != request.EffectiveJobID() ||
		strings.TrimSpace(request.DeploymentID) == "" || strings.TrimSpace(request.SpecDigest) == "" ||
		strings.TrimSpace(request.OperationID) == "" || strings.TrimSpace(request.ExecutionID) == "" || digestErr != nil || len(jobDigest) != sha256.Size || hex.EncodeToString(jobDigest) != request.JobDigest ||
		*request.Job.ID == "" || request.Job.Meta[DeploymentIDMeta] != request.DeploymentID ||
		request.Job.Meta[SpecDigestMeta] != request.SpecDigest ||
		request.Job.Meta[DeploymentOperationIDMeta] != request.OperationID ||
		request.Job.Meta[DeploymentExecutionIDMeta] != request.ExecutionID ||
		request.Job.Meta[DeploymentJobDigestMeta] != request.JobDigest {
		return "", fmt.Errorf("deployment CAS job identity is incomplete")
	}
	actualDigest, err := DigestDeploymentJob(request.Job)
	if err != nil || actualDigest != request.JobDigest {
		return "", fmt.Errorf("deployment CAS job digest differs from the submitted job")
	}
	source, err := json.Marshal(request.Job)
	if err != nil {
		return "", fmt.Errorf("encode deployment job submission")
	}
	response, _, err := c.api.Jobs().RegisterOpts(request.Job, &nomadapi.RegisterOptions{
		EnforceIndex: true,
		ModifyIndex:  request.ExpectedJobModifyIndex,
		Submission:   &nomadapi.JobSubmission{Source: string(source), Format: "json"},
	}, (&nomadapi.WriteOptions{Region: request.Region}).WithContext(ctx))
	if err != nil {
		if strings.Contains(err.Error(), nomadapi.RegisterEnforceIndexErrPrefix) {
			return "", ErrDeploymentJobSubmitConflict
		}
		return "", ErrDeploymentJobSubmitIndeterminate
	}
	if response == nil || response.JobModifyIndex == 0 {
		return "", ErrDeploymentJobSubmitIndeterminate
	}
	return response.EvalID, nil
}
