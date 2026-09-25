package nomad

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	nomadapi "github.com/hashicorp/nomad/api"
)

type FunctionInvocationJobIdentity struct{ JobID string }
type FunctionInvocationJobState string

const (
	FunctionInvocationJobNotFound      FunctionInvocationJobState = "not-found"
	FunctionInvocationJobIndeterminate FunctionInvocationJobState = "indeterminate"
)

type FunctionInvocationJobObservation struct{ State FunctionInvocationJobState }

var (
	ErrFunctionJobIdentity            = errors.New("function job identity is invalid")
	ErrFunctionJobLookupIndeterminate = errors.New("function job lookup is indeterminate")
)
var functionInvocationJobID = regexp.MustCompile(`^norn-fn-[0-9a-f]{40}$`)

// LookupFunctionInvocationJob reads one exact function-job ID. Only a 404 is
// conclusive absence. Existing-job details remain unavailable until M1 defines
// a complete redacted canonicalization contract for Nomad-normalized jobs.
func (c *Client) LookupFunctionInvocationJob(ctx context.Context, region string, identity FunctionInvocationJobIdentity) (FunctionInvocationJobObservation, error) {
	if !functionInvocationJobID.MatchString(identity.JobID) {
		return FunctionInvocationJobObservation{State: FunctionInvocationJobIndeterminate}, ErrFunctionJobIdentity
	}
	if c == nil || c.api == nil {
		return FunctionInvocationJobObservation{State: FunctionInvocationJobIndeterminate}, ErrFunctionJobLookupIndeterminate
	}
	_, _, err := c.api.Jobs().Info(identity.JobID, (&nomadapi.QueryOptions{Region: region}).WithContext(ctx))
	if err != nil {
		if nomadHTTPStatus(err) == http.StatusNotFound {
			return FunctionInvocationJobObservation{State: FunctionInvocationJobNotFound}, nil
		}
		return FunctionInvocationJobObservation{State: FunctionInvocationJobIndeterminate}, ErrFunctionJobLookupIndeterminate
	}
	return FunctionInvocationJobObservation{State: FunctionInvocationJobIndeterminate}, ErrFunctionJobLookupIndeterminate
}
