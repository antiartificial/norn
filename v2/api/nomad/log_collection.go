package nomad

import (
	"context"
	"fmt"
	"sort"

	nomadapi "github.com/hashicorp/nomad/api"
)

// LogAllocation is one allocation with the labels collected logs keep.
type LogAllocation struct {
	ID           string
	JobID        string
	NodeID       string
	NodeName     string
	TaskGroup    string
	ClientStatus string
	// Tasks are the allocation's own task group's tasks (from its task
	// states), not the first task group of the job.
	Tasks []string
}

// Terminal reports whether the allocation can no longer produce output.
func (a LogAllocation) Terminal() bool {
	return a.ClientStatus == "complete" || a.ClientStatus == "failed" || a.ClientStatus == "lost"
}

// LogAllocations lists a job's allocations with node, task group and task
// labels. The query is bound to ctx.
func (c *Client) LogAllocations(ctx context.Context, jobID string) ([]LogAllocation, error) {
	stubs, _, err := c.api.Jobs().Allocations(jobID, true, (&nomadapi.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("list allocations for %s: %w", jobID, err)
	}
	allocations := make([]LogAllocation, 0, len(stubs))
	for _, stub := range stubs {
		allocation := LogAllocation{ID: stub.ID, JobID: stub.JobID, NodeID: stub.NodeID, NodeName: stub.NodeName, TaskGroup: stub.TaskGroup, ClientStatus: stub.ClientStatus}
		for task := range stub.TaskStates {
			allocation.Tasks = append(allocation.Tasks, task)
		}
		sort.Strings(allocation.Tasks)
		allocations = append(allocations, allocation)
	}
	return allocations, nil
}

// FollowLogs streams one task stream of one allocation from the oldest
// retained log file. Frames carry the log file name and offset within it.
// Cancelling ctx aborts the upstream request.
func (c *Client) FollowLogs(ctx context.Context, allocation LogAllocation, task, stream string, follow bool) (<-chan *nomadapi.StreamFrame, <-chan error) {
	target := &nomadapi.Allocation{ID: allocation.ID, NodeID: allocation.NodeID}
	return c.api.AllocFS().Logs(target, follow, task, stream, "start", 0, ctx.Done(), (&nomadapi.QueryOptions{}).WithContext(ctx))
}
