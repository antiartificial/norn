package logcollect

import (
	"bytes"
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// LokiStats are the shipper's counters.
type LokiStats struct {
	ShippedLines  int64     `json:"shippedLines"`
	FailedPushes  int64     `json:"failedPushes"`
	LastError     string    `json:"lastError,omitempty"`
	LastErrorTime time.Time `json:"lastErrorTime,omitzero"`
	LastSuccess   time.Time `json:"lastSuccess,omitzero"`
}

// LokiShipper ships the spool to Loki from outside the collector, so a slow
// or down Loki never blocks collection. Each stream has a durable shipped
// cursor in the spool, advanced only after Loki answers 2xx: a restart
// neither re-ships nor skips what the spool still holds. Output the spool
// dropped before it was shipped is gone (already counted by the spool).
type LokiShipper struct {
	Spool  *Spool
	Client *LokiClient
	// Interval between passes (backoff doubles it up to MaxBackoff on failure).
	Interval   time.Duration
	MaxBackoff time.Duration
	// MaxBatchBytes and MaxBatchLines bound one push; a single spooled
	// record is never split, so it may exceed them.
	MaxBatchBytes int
	MaxBatchLines int

	mu    sync.Mutex
	stats LokiStats
}

// Stats returns a snapshot of the counters.
func (l *LokiShipper) Stats() LokiStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stats
}

// Run ships until ctx ends.
func (l *LokiShipper) Run(ctx context.Context) {
	delay := l.Interval
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if err := l.Flush(ctx); err != nil {
			delay = min(max(delay, l.Interval)*2, l.MaxBackoff)
		} else {
			delay = l.Interval
		}
	}
}

// Flush ships everything pending once. Streams Loki rejects permanently
// (4xx other than 408/429) do not stop other streams; any other failure ends
// the pass and the first error is returned.
func (l *LokiShipper) Flush(ctx context.Context) error {
	var firstErr error
	for _, state := range l.Spool.StreamStates() {
		if state.Idle() {
			continue
		}
		err := l.shipStream(ctx, state)
		if err == nil {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
		var status *LokiStatusError
		if !errors.As(err, &status) || status.Retryable() {
			break
		}
	}
	return firstErr
}

func (l *LokiShipper) shipStream(ctx context.Context, state StreamState) error {
	labels, cursor := state.Labels, state.Shipped
	for ctx.Err() == nil {
		records, err := l.Spool.ReadAfter(labels, cursor, l.MaxBatchBytes)
		if err != nil {
			return l.fail(err)
		}
		if len(records) == 0 {
			return nil
		}
		var lines []LokiLine
		var size, taken int
		for _, record := range records {
			recordLines := lokiLines(labels, record)
			if taken > 0 && (len(lines)+len(recordLines) > l.MaxBatchLines || size+recordBytes(recordLines) > l.MaxBatchBytes) {
				break
			}
			lines = append(lines, recordLines...)
			size += recordBytes(recordLines)
			taken++
			cursor = record.Past()
		}
		if len(lines) > 0 {
			if err := l.Client.Push(ctx, lokiLabels(labels), lines); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return l.fail(err)
			}
		}
		if err := l.Spool.SetShipped(labels, cursor); err != nil {
			return l.fail(err)
		}
		l.mu.Lock()
		l.stats.ShippedLines += int64(len(lines))
		l.stats.LastSuccess = time.Now()
		l.mu.Unlock()
	}
	return ctx.Err()
}

func (l *LokiShipper) fail(err error) error {
	l.mu.Lock()
	l.stats.FailedPushes++
	l.stats.LastError, l.stats.LastErrorTime = err.Error(), time.Now()
	l.mu.Unlock()
	log.Printf("loki shipping: %v (will retry; the spool keeps the output)", err)
	return err
}

func recordBytes(lines []LokiLine) int {
	total := 0
	for _, line := range lines {
		total += len(line.Line) + 64
	}
	return total
}

func lokiLabels(labels Labels) map[string]string {
	return map[string]string{"source": "norn", "app": labels.App, "job": labels.JobID, "task_group": labels.TaskGroup, "task": labels.Task, "stream": labels.Stream, "node": labels.NodeName}
}

// lokiLines turns a record into Loki lines. Chunks are split at newlines
// (the empty piece after a trailing newline is dropped); each line gets its
// own nanosecond so Loki does not deduplicate repeated identical lines.
func lokiLines(labels Labels, record Record) []LokiLine {
	metadata := map[string]string{"alloc_id": labels.AllocID, "node_id": labels.NodeID}
	if record.Gap != "" {
		metadata["norn_gap"] = "true"
		return []LokiLine{{Time: record.Time, Line: record.Gap, Metadata: metadata}}
	}
	pieces := bytes.Split(record.Data, []byte("\n"))
	if len(pieces) > 0 && len(pieces[len(pieces)-1]) == 0 {
		pieces = pieces[:len(pieces)-1]
	}
	lines := make([]LokiLine, 0, len(pieces))
	for i, piece := range pieces {
		lines = append(lines, LokiLine{Time: record.Time.Add(time.Duration(i)), Line: string(bytes.TrimSuffix(piece, []byte("\r"))), Metadata: metadata})
	}
	return lines
}
