package logcollect

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"
)

// LokiStats are the shipper's counters.
type LokiStats struct {
	ShippedLines  int64     `json:"shippedLines"`
	DroppedLines  int64     `json:"droppedLines"`
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

// Flush ships everything pending once. Any failure ends the pass and is
// returned; batches Loki rejects as invalid are dropped instead (see
// rejected), so they cannot stall a stream.
func (l *LokiShipper) Flush(ctx context.Context) error {
	for _, state := range l.Spool.StreamStates() {
		if state.Idle() {
			continue
		}
		if err := l.shipStream(ctx, state); err != nil {
			return err
		}
	}
	return nil
}

// rejected reports a push Loki refused for its content (e.g. entries too
// old or out of order, or too large). Retrying cannot succeed, so the
// batch is dropped and counted like any other diagnostic loss (ADR 0001).
// Auth and routing failures (401/403/404) are configuration errors and keep
// retrying, so fixing the configuration loses nothing.
func rejected(err error) bool {
	var status *LokiStatusError
	return errors.As(err, &status) && (status.Code == http.StatusBadRequest || status.Code == http.StatusRequestEntityTooLarge)
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
				if !rejected(err) {
					return l.fail(err)
				}
				log.Printf("loki shipping: dropped %d lines of %s/%s/%s: %v", len(lines), labels.App, labels.AllocID, labels.Stream, err)
				l.mu.Lock()
				l.stats.DroppedLines += int64(len(lines))
				l.stats.LastError, l.stats.LastErrorTime = err.Error(), time.Now()
				l.mu.Unlock()
				lines = nil
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
