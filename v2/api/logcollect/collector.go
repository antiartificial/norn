package logcollect

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/nomad"
)

// Job is one Nomad job whose allocations are collected for an app.
type Job struct {
	App   string
	JobID string
}

// Source lists allocations and follows one task stream from the oldest
// retained log file.
type Source interface {
	Allocations(ctx context.Context, jobID string) ([]nomad.LogAllocation, error)
	Follow(ctx context.Context, allocation nomad.LogAllocation, task, stream string) (<-chan *nomadapi.StreamFrame, <-chan error)
}

// NomadSource adapts the Nomad client.
type NomadSource struct{ Client *nomad.Client }

func (s NomadSource) Allocations(ctx context.Context, jobID string) ([]nomad.LogAllocation, error) {
	return s.Client.LogAllocations(ctx, jobID)
}

func (s NomadSource) Follow(ctx context.Context, allocation nomad.LogAllocation, task, stream string) (<-chan *nomadapi.StreamFrame, <-chan error) {
	return s.Client.FollowLogs(ctx, allocation, task, stream, true)
}

// Collector keeps one follower per (allocation, task, stream) of the
// listed jobs, resumes each from the spool's own recorded position (so a
// restart neither duplicates nor silently skips output Nomad still
// retains), records explicit gap markers for output rotated away before it
// could be collected, and stops followers of allocations that disappear.
type Collector struct {
	Source Source
	Spool  *Spool
	Jobs   func() []Job
	// MaxFollowers bounds concurrent upstream streams; excess streams wait
	// for the next reconciliation.
	MaxFollowers int

	mu        sync.Mutex
	followers map[string]*follower
	wg        sync.WaitGroup
	// Skipped counts streams not followed because of MaxFollowers.
	Skipped int
}

type follower struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Run reconciles every interval until ctx ends, then waits for followers.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := c.Reconcile(ctx); err != nil {
			log.Printf("log collection: %v", err)
		}
		select {
		case <-ctx.Done():
			c.Wait()
			return
		case <-ticker.C:
		}
	}
}

// Wait blocks until all followers have exited.
func (c *Collector) Wait() { c.wg.Wait() }

// Reconcile starts followers for new streams and stops followers of
// allocations no longer listed.
func (c *Collector) Reconcile(ctx context.Context) error {
	c.mu.Lock()
	if c.followers == nil {
		c.followers = map[string]*follower{}
	}
	c.mu.Unlock()
	wanted := map[string]bool{}
	var firstErr error
	for _, job := range c.Jobs() {
		allocations, err := c.Source.Allocations(ctx, job.JobID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, allocation := range allocations {
			for _, task := range allocation.Tasks {
				for _, stream := range []string{"stdout", "stderr"} {
					labels := Labels{App: job.App, JobID: job.JobID, NodeID: allocation.NodeID, NodeName: allocation.NodeName, AllocID: allocation.ID,
						TaskGroup: allocation.TaskGroup, Task: task, Stream: stream}
					key := allocation.ID + "/" + task + "/" + stream
					wanted[key] = true
					c.start(ctx, key, labels, allocation)
				}
			}
		}
	}
	c.mu.Lock()
	for key, running := range c.followers {
		if !wanted[key] {
			running.cancel()
			delete(c.followers, key)
		}
	}
	c.mu.Unlock()
	return firstErr
}

func (c *Collector) start(ctx context.Context, key string, labels Labels, allocation nomad.LogAllocation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if running, ok := c.followers[key]; ok {
		select {
		case <-running.done:
			delete(c.followers, key) // ended; may be restarted below
		default:
			return
		}
	}
	if c.MaxFollowers > 0 && len(c.followers) >= c.MaxFollowers {
		c.Skipped++
		return
	}
	position, err := c.Spool.Stream(labels)
	if err != nil {
		log.Printf("log collection %s: %v", key, err)
		return
	}
	if position.Complete {
		return
	}
	followCtx, cancel := context.WithCancel(ctx)
	running := &follower{cancel: cancel, done: make(chan struct{})}
	c.followers[key] = running
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(running.done)
		defer cancel()
		if err := c.follow(followCtx, labels, allocation, position); err != nil && followCtx.Err() == nil {
			log.Printf("log collection %s: %v", key, err)
		}
	}()
}

// logFileIndex parses the rotation index from a Nomad log file name such as
// "alloc/logs/web.stdout.3".
func logFileIndex(name string) (int, bool) {
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return 0, false
	}
	index, err := strconv.Atoi(name[dot+1:])
	return index, err == nil && index >= 0
}

// follow streams one task stream into the spool from the recorded position.
func (c *Collector) follow(ctx context.Context, labels Labels, allocation nomad.LogAllocation, position Position) error {
	frames, errs := c.Source.Follow(ctx, allocation, labels.Task, labels.Stream)
	// seenCurrent: this session has seen output of the position's file, so
	// moving to the next file is an ordinary rotation rather than a gap.
	seenCurrent := !position.Known
	first := true
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-errs:
			if ok && err != nil {
				return err
			}
			errs = nil
		case frame, ok := <-frames:
			if !ok {
				if allocation.Terminal() {
					return c.Spool.MarkComplete(labels)
				}
				return nil
			}
			if frame == nil || len(frame.Data) == 0 {
				continue
			}
			index, valid := logFileIndex(frame.File)
			if !valid {
				return fmt.Errorf("log frame without a rotation index (%q)", frame.File)
			}
			data, start := frame.Data, frame.Offset
			end := start + int64(len(data))
			now := time.Now().UTC()
			var records []Record
			switch {
			case first && !position.Known && (index > 0 || start > 0):
				records = append(records, Record{Time: now, File: index, Offset: start, Gap: fmt.Sprintf("collection began at log file %d offset %d; earlier output had already rotated away", index, start)})
			case !position.Known:
			case index < position.File || (index == position.File && end <= position.Offset):
				seenCurrent = seenCurrent || index == position.File
				first = false
				continue // already collected
			case index == position.File && start < position.Offset:
				data, start = data[position.Offset-start:], position.Offset
				seenCurrent = true
			case index == position.File && start > position.Offset:
				records = append(records, Record{Time: now, File: index, Offset: start, Gap: fmt.Sprintf("log file %d bytes %d-%d were not collected", index, position.Offset, start)})
			case index > position.File && !(seenCurrent && index == position.File+1 && start == 0):
				records = append(records, Record{Time: now, File: index, Offset: start, Gap: fmt.Sprintf("log files %d..%d rotated away before collection resumed (from file %d offset %d)", position.File, index-1, position.File, position.Offset)})
			}
			first = false
			records = append(records, Record{Time: now, File: index, Offset: start, Data: data})
			if err := c.Spool.Append(labels, records...); err != nil {
				return err
			}
			position = Position{Known: true, File: index, Offset: start + int64(len(data))}
			seenCurrent = true
		}
	}
}
