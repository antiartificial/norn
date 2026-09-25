package nomad

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

var ErrJobStopVerificationIndeterminate = errors.New("Nomad job stop verification is indeterminate")

// CASStopJobRequest binds a stop to the exact deployed job revision and the
// allocation set observed by its caller. Allocation IDs are never discovered
// after the mutation because that could adopt a competing deployment.
type CASStopJobRequest struct {
	JobID          string
	Region         string
	JobModifyIndex uint64
	AllocationIDs  []string
}

// StopJobCAS sets Stop with Nomad's guarded registration endpoint, then proves
// that the exact observed allocation set contains no live or substituted work.
// A caller controls the bounded wait through ctx.
func (c *Client) StopJobCAS(ctx context.Context, request CASStopJobRequest) error {
	if c == nil || c.api == nil || !validCASStopRequest(request) {
		return fmt.Errorf("CAS job stop requires an exact job ID, region, revision, and allocation IDs")
	}
	if err := c.requireAtomicJobCAS(); err != nil {
		return err
	}
	query := (&nomadapi.QueryOptions{Region: request.Region}).WithContext(ctx)
	job, _, err := c.api.Jobs().Info(request.JobID, query)
	if err != nil || job == nil || job.ID == nil || *job.ID != request.JobID || job.Region == nil || *job.Region != request.Region || job.JobModifyIndex == nil || *job.JobModifyIndex != request.JobModifyIndex {
		if err == nil {
			return fmt.Errorf("%w for %s", ErrJobRevisionChanged, request.JobID)
		}
		return fmt.Errorf("read job %s for CAS stop: %w", request.JobID, err)
	}
	stubs, _, err := c.api.Jobs().Allocations(request.JobID, true, query)
	if err != nil || !exactCASStopAllocations(request, stubs) {
		return ErrJobStopVerificationIndeterminate
	}
	stopped := true
	copy := *job
	copy.Stop = &stopped
	_, _, err = c.api.Jobs().RegisterOpts(&copy, &nomadapi.RegisterOptions{EnforceIndex: true, ModifyIndex: request.JobModifyIndex}, (&nomadapi.WriteOptions{Region: request.Region}).WithContext(ctx))
	if err != nil {
		if strings.Contains(err.Error(), nomadapi.RegisterEnforceIndexErrPrefix) {
			return fmt.Errorf("%w for %s: %v", ErrJobRevisionChanged, request.JobID, err)
		}
		return fmt.Errorf("CAS stop job %s: %w", request.JobID, err)
	}
	return c.verifyCASStopped(ctx, request)
}

func validCASStopRequest(request CASStopJobRequest) bool {
	if strings.TrimSpace(request.JobID) == "" || strings.TrimSpace(request.Region) == "" || request.JobModifyIndex == 0 || len(request.AllocationIDs) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(request.AllocationIDs))
	for _, id := range request.AllocationIDs {
		if strings.TrimSpace(id) == "" {
			return false
		}
		if _, ok := seen[id]; ok {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func exactCASStopAllocations(request CASStopJobRequest, stubs []*nomadapi.AllocationListStub) bool {
	got := make([]string, 0, len(stubs))
	for _, stub := range stubs {
		if stub == nil || stub.ID == "" || stub.JobID != request.JobID {
			return false
		}
		got = append(got, stub.ID)
	}
	want := append([]string(nil), request.AllocationIDs...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func (c *Client) verifyCASStopped(ctx context.Context, request CASStopJobRequest) error {
	query := (&nomadapi.QueryOptions{Region: request.Region}).WithContext(ctx)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, _, err := c.api.Jobs().Info(request.JobID, query)
		if err != nil || job == nil || job.ID == nil || *job.ID != request.JobID || job.Region == nil || *job.Region != request.Region || job.Stop == nil || !*job.Stop {
			return ErrJobStopVerificationIndeterminate
		}
		stubs, _, err := c.api.Jobs().Allocations(request.JobID, true, query)
		if err == nil && ((len(stubs) == 0) || (exactCASStopAllocations(request, stubs) && allCASStopTerminal(stubs))) {
			return nil
		}
		if err != nil || !exactCASStopAllocations(request, stubs) {
			return ErrJobStopVerificationIndeterminate
		}
		select {
		case <-ctx.Done():
			return ErrJobStopVerificationIndeterminate
		case <-ticker.C:
		}
	}
}

func allCASStopTerminal(stubs []*nomadapi.AllocationListStub) bool {
	for _, stub := range stubs {
		if stub == nil {
			return false
		}
		switch stub.ClientStatus {
		case nomadapi.AllocClientStatusComplete, nomadapi.AllocClientStatusFailed, nomadapi.AllocClientStatusLost:
		default:
			return false
		}
	}
	return true
}
