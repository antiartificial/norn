package nomad

import (
	"context"
	"fmt"
	"io"

	nomadapi "github.com/hashicorp/nomad/api"
)

// StreamLogs streams stdout and stderr from the latest allocation of a job.
// The upstream Nomad requests are bound to ctx and to the returned reader:
// cancelling ctx or closing the reader aborts both requests, so a departed
// client does not leave followers reading from Nomad indefinitely.
func (c *Client) StreamLogs(ctx context.Context, jobID string, follow bool) (io.ReadCloser, error) {
	query := (&nomadapi.QueryOptions{}).WithContext(ctx)
	allocs, _, err := c.api.Jobs().Allocations(jobID, false, query)
	if err != nil {
		return nil, fmt.Errorf("list allocations for %s: %w", jobID, err)
	}
	if len(allocs) == 0 {
		return nil, fmt.Errorf("no allocations for job %s", jobID)
	}

	target := latestLogAllocation(allocs)
	if target == nil {
		return nil, fmt.Errorf("no usable allocations for job %s", jobID)
	}

	alloc, _, err := c.api.Allocations().Info(target.ID, query)
	if err != nil {
		return nil, fmt.Errorf("get allocation: %w", err)
	}

	// The task comes from the allocation's own task group, not whichever
	// group the job lists first.
	var taskName string
	if alloc.Job != nil {
		for _, tg := range alloc.Job.TaskGroups {
			if tg.Name != nil && *tg.Name == alloc.TaskGroup && len(tg.Tasks) > 0 {
				taskName = tg.Tasks[0].Name
				break
			}
		}
	}
	if taskName == "" {
		return nil, fmt.Errorf("no tasks found in allocation %s", target.ID)
	}

	streamCtx, stop := context.WithCancel(ctx)
	streamQuery := (&nomadapi.QueryOptions{}).WithContext(streamCtx)
	stdoutFrames, stdoutErr := c.api.AllocFS().Logs(alloc, follow, taskName, "stdout", "start", 0, streamCtx.Done(), streamQuery)
	stderrFrames, stderrErr := c.api.AllocFS().Logs(alloc, follow, taskName, "stderr", "start", 0, streamCtx.Done(), streamQuery)

	r, w := io.Pipe()
	finished := make(chan struct{})
	// A client that stops reading leaves the writer blocked in w.Write,
	// where it cannot observe cancellation; closing the read side on
	// request cancellation unblocks it. A clean end is left untouched.
	go func() {
		select {
		case <-ctx.Done():
			r.CloseWithError(ctx.Err())
		case <-finished:
		}
	}()
	go func() {
		defer close(finished)
		defer func() {
			stop()
			// A Nomad decoder may be blocked handing us a frame; drain
			// until it observes the cancelled request and exits.
			drainLogFrames(stdoutFrames, stdoutErr)
			drainLogFrames(stderrFrames, stderrErr)
		}()
		// A stream is live until its frames close (end of log) or its
		// error channel reports (including a request that failed to start).
		for stdoutErr != nil || stderrErr != nil {
			var frame *nomadapi.StreamFrame
			var ok bool
			select {
			case <-streamCtx.Done():
				w.CloseWithError(streamCtx.Err())
				return
			case frame, ok = <-stdoutFrames:
				if !ok {
					stdoutFrames, stdoutErr = nil, nil
					continue
				}
			case frame, ok = <-stderrFrames:
				if !ok {
					stderrFrames, stderrErr = nil, nil
					continue
				}
			case err := <-stdoutErr:
				stdoutFrames, stdoutErr = nil, nil
				if err != nil {
					w.CloseWithError(err)
					return
				}
				continue
			case err := <-stderrErr:
				stderrFrames, stderrErr = nil, nil
				if err != nil {
					w.CloseWithError(err)
					return
				}
				continue
			}
			if frame != nil && len(frame.Data) > 0 {
				if _, err := w.Write(frame.Data); err != nil {
					return // reader closed
				}
			}
		}
		w.Close()
	}()

	return &logStream{PipeReader: r, stop: stop}, nil
}

func latestLogAllocation(allocs []*nomadapi.AllocationListStub) *nomadapi.AllocationListStub {
	var best *nomadapi.AllocationListStub
	for _, candidate := range allocs {
		if candidate == nil || candidate.ID == "" {
			continue
		}
		if best == nil ||
			(candidate.ClientStatus == "running" && best.ClientStatus != "running") ||
			(candidate.ClientStatus == "running") == (best.ClientStatus == "running") &&
				(candidate.CreateTime > best.CreateTime ||
					candidate.CreateTime == best.CreateTime && (candidate.CreateIndex > best.CreateIndex ||
						candidate.CreateIndex == best.CreateIndex && candidate.ID > best.ID)) {
			best = candidate
		}
	}
	return best
}

func drainLogFrames(frames <-chan *nomadapi.StreamFrame, errs <-chan error) {
	if frames == nil && errs == nil {
		return
	}
	go func() {
		for {
			select {
			case _, ok := <-frames:
				if !ok {
					return
				}
			case <-errs:
				return
			}
		}
	}()
}

type logStream struct {
	*io.PipeReader
	stop context.CancelFunc
}

func (s *logStream) Close() error {
	s.stop()
	return s.PipeReader.Close()
}
