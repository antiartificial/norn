package nomad

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Region                 string
	ExpectedJobModifyIndex uint64
	DeploymentID           string
	SpecDigest             string
	OperationID            string
	ExecutionID            string
	JobDigest              string
}

// RegisterDeploymentJobCAS performs one guarded Nomad registration. A lost
// response is never permission to submit again; the caller must read back the
// exact execution marker and job revision before completing its effect.
func (c *Client) RegisterDeploymentJobCAS(ctx context.Context, request CASDeploymentJobRequest) (string, error) {
	jobDigest, digestErr := hex.DecodeString(request.JobDigest)
	if c == nil || c.api == nil || request.Job == nil || request.Job.ID == nil || request.Job.Region == nil ||
		strings.TrimSpace(request.Region) == "" || *request.Job.Region != request.Region ||
		strings.TrimSpace(request.App) == "" || *request.Job.ID != request.App ||
		strings.TrimSpace(request.DeploymentID) == "" || strings.TrimSpace(request.SpecDigest) == "" ||
		strings.TrimSpace(request.OperationID) == "" || strings.TrimSpace(request.ExecutionID) == "" || digestErr != nil || len(jobDigest) != sha256.Size || hex.EncodeToString(jobDigest) != request.JobDigest ||
		*request.Job.ID == "" || request.Job.Meta[DeploymentIDMeta] != request.DeploymentID ||
		request.Job.Meta[SpecDigestMeta] != request.SpecDigest ||
		request.Job.Meta[DeploymentOperationIDMeta] != request.OperationID ||
		request.Job.Meta[DeploymentExecutionIDMeta] != request.ExecutionID ||
		request.Job.Meta[DeploymentJobDigestMeta] != request.JobDigest {
		return "", fmt.Errorf("deployment CAS job identity is incomplete")
	}
	response, _, err := c.api.Jobs().RegisterOpts(request.Job, &nomadapi.RegisterOptions{
		EnforceIndex: true,
		ModifyIndex:  request.ExpectedJobModifyIndex,
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
