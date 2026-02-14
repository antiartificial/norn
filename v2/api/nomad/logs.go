package nomad

import (
	"fmt"
	"io"

	nomadapi "github.com/hashicorp/nomad/api"
)

// StreamLogs streams stdout and stderr from the latest allocation of a job.
func (c *Client) StreamLogs(jobID string, follow bool) (io.ReadCloser, error) {
	allocs, err := c.JobAllocations(jobID)
	if err != nil {
		return nil, err
	}
	if len(allocs) == 0 {
		return nil, fmt.Errorf("no allocations for job %s", jobID)
	}

	// Find the most recent running allocation
	var target *nomadapi.AllocationListStub
	for _, a := range allocs {
		if a.ClientStatus == "running" {
			target = a
			break
		}
	}
	if target == nil {
		target = allocs[0]
	}

	alloc, _, err := c.api.Allocations().Info(target.ID, nil)
	if err != nil {
		return nil, fmt.Errorf("get allocation: %w", err)
	}

	var taskName string
	for _, tg := range alloc.Job.TaskGroups {
		if len(tg.Tasks) > 0 {
			taskName = tg.Tasks[0].Name
			break
		}
	}
	if taskName == "" {
		return nil, fmt.Errorf("no tasks found in allocation %s", target.ID)
	}

	cancel := make(chan struct{})

	stdoutFrames, stdoutErr := c.api.AllocFS().Logs(alloc, follow, taskName, "stdout", "start", 0, cancel, nil)
	stderrFrames, stderrErr := c.api.AllocFS().Logs(alloc, follow, taskName, "stderr", "start", 0, cancel, nil)

	r, w := io.Pipe()
	go func() {
		defer w.Close()
		stdoutDone := false
		stderrDone := false
		for !stdoutDone || !stderrDone {
			select {
			case frame, ok := <-stdoutFrames:
				if !ok {
					stdoutDone = true
					continue
				}
				if frame != nil && len(frame.Data) > 0 {
					w.Write(frame.Data)
				}
			case frame, ok := <-stderrFrames:
				if !ok {
					stderrDone = true
					continue
				}
				if frame != nil && len(frame.Data) > 0 {
					w.Write(frame.Data)
				}
			case err := <-stdoutErr:
				if err != nil {
					w.CloseWithError(err)
					return
				}
				stdoutDone = true
			case err := <-stderrErr:
				if err != nil {
					w.CloseWithError(err)
					return
				}
				stderrDone = true
			}
		}
	}()

	return r, nil
}
