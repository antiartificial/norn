package nomad

import (
	"context"
	"errors"
	"math"

	nomadapi "github.com/hashicorp/nomad/api"
)

var ErrMySQLSourceStoppedObservation = errors.New("exact stopped MySQL source job observation is unavailable")

// ObserveStoppedMySQLSourceJob confirms that the signed source revision was
// stopped by the guarded CAS path and that no allocation for the job is live.
// It performs no Nomad mutation. A changed job revision or unknown live
// allocation fails closed, including a direct restart outside Norn's gate.
func (c *Client) ObserveStoppedMySQLSourceJob(ctx context.Context, request CASStopJobRequest) error {
	if c == nil || c.api == nil || !validCASStopRequest(request) || request.JobVersion == math.MaxUint64 {
		return ErrMySQLSourceStoppedObservation
	}
	query := (&nomadapi.QueryOptions{Region: request.Region}).WithContext(ctx)
	job, _, err := c.api.Jobs().Info(request.JobID, query)
	if err != nil || job == nil || job.JobModifyIndex == nil || *job.JobModifyIndex <= request.JobModifyIndex ||
		!exactCASStopJob(job, request, request.JobVersion+1, *job.JobModifyIndex, true) {
		return ErrMySQLSourceStoppedObservation
	}
	stubs, _, err := c.api.Jobs().Allocations(request.JobID, true, query)
	if err != nil || !knownCASStopAllocations(request, stubs) || !allCASStopTerminal(stubs) {
		return ErrMySQLSourceStoppedObservation
	}
	seen := make(map[string]bool, len(stubs))
	for _, stub := range stubs {
		seen[stub.ID] = true
	}
	for _, id := range request.AllocationIDs {
		if !seen[id] {
			return ErrMySQLSourceStoppedObservation
		}
	}
	return nil
}
