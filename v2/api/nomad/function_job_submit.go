package nomad

import (
	"context"
	"errors"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"
)

var (
	ErrFunctionJobCreateConflict      = errors.New("function job already exists")
	ErrFunctionJobCreateIndeterminate = errors.New("function job create is indeterminate")
)

// CreateFunctionInvocationJob attempts one create-only registration. The
// caller must durably record SubmitAttempted under its live claim before this
// method is called. An ambiguous response is never permission to submit again.
// Evaluation IDs from the response are deliberately not recovery evidence;
// the caller must reconcile a complete read-back before observing allocations.
func (c *Client) CreateFunctionInvocationJob(ctx context.Context, region string, job *nomadapi.Job, expected FunctionInvocationJobDigest) error {
	if c == nil || c.api == nil || region != "global" || job == nil || job.ID == nil || !functionInvocationJobID.MatchString(*job.ID) {
		return ErrFunctionJobIdentity
	}
	actual, err := ProjectFunctionInvocationJob(job)
	if err != nil || actual != expected {
		return ErrFunctionJobIdentity
	}
	_, _, err = c.api.Jobs().RegisterOpts(job, &nomadapi.RegisterOptions{
		EnforceIndex: true,
		ModifyIndex:  0,
	}, (&nomadapi.WriteOptions{Region: region}).WithContext(ctx))
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), nomadapi.RegisterEnforceIndexErrPrefix) {
		return ErrFunctionJobCreateConflict
	}
	return ErrFunctionJobCreateIndeterminate
}
