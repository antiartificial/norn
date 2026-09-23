// Package memstore holds in-memory implementations of the Norn v3 control-store
// boundaries. They are faithful second implementations of the same interfaces
// the PostgreSQL adapter satisfies, used to prove the conformance suites are
// backend-neutral (roadmap P5's acceptance mechanism: a second backend passing
// the same invariant suite) and usable as test doubles and a local,
// explicitly-non-durable backend for development.
package memstore

import (
	"context"
	"sync"

	"norn/v2/api/hub"
)

// EventStore is an in-memory implementation of hub.EventStore. Cursors are a
// monotonic counter, mirroring the PostgreSQL serial-id contract: values are
// opaque and strictly increasing, and consumers page forward with
// ListHubEventsAfter.
type EventStore struct {
	mu     sync.Mutex
	seq    int64
	events []hub.Event
}

// NewEventStore returns an empty in-memory event log.
func NewEventStore() *EventStore { return &EventStore{} }

// AppendHubEvent assigns the next monotonic cursor and stores a copy of the
// event. It mirrors the adapter contract by writing the assigned id back onto
// the caller's event.
func (s *EventStore) AppendHubEvent(_ context.Context, event *hub.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	event.ID = s.seq
	s.events = append(s.events, *event)
	return nil
}

// ListHubEventsAfter returns events with a cursor strictly greater than after,
// in ascending cursor order, capped at limit (default/ceiling 500/1000 to match
// the PostgreSQL adapter).
func (s *EventStore) ListHubEventsAfter(_ context.Context, after int64, limit int) ([]hub.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []hub.Event{}
	for _, event := range s.events { // stored in ascending-cursor order
		if event.ID <= after {
			continue
		}
		out = append(out, event)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// LatestHubEventID returns the highest assigned cursor, or 0 when empty.
func (s *EventStore) LatestHubEventID(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq, nil
}

// HubEventBounds reports the retained cursor range and timestamp span.
func (s *EventStore) HubEventBounds(_ context.Context) (hub.EventBounds, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bounds := hub.EventBounds{RetainedEvents: int64(len(s.events))}
	if len(s.events) == 0 {
		return bounds, nil
	}
	oldestTS, latestTS := s.events[0].Timestamp, s.events[0].Timestamp
	bounds.OldestCursor, bounds.LatestCursor = s.events[0].ID, s.events[0].ID
	for _, event := range s.events {
		if event.ID < bounds.OldestCursor {
			bounds.OldestCursor = event.ID
		}
		if event.ID > bounds.LatestCursor {
			bounds.LatestCursor = event.ID
		}
		if event.Timestamp.Before(oldestTS) {
			oldestTS = event.Timestamp
		}
		if event.Timestamp.After(latestTS) {
			latestTS = event.Timestamp
		}
	}
	bounds.OldestTimestamp = &oldestTS
	bounds.LatestTimestamp = &latestTS
	return bounds, nil
}

// Compile-time proof that the in-memory adapter satisfies the event boundary.
var _ hub.EventStore = (*EventStore)(nil)
