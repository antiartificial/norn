package hub

import "context"

// This file adds the ADR 0001 / M2 event-replay expiry contract: when a client
// presents a cursor whose next event was already compacted away, replay cannot
// silently skip the gap — the client is told explicitly to resync from a
// snapshot and resume live at the latest cursor.

// ResyncDecision reports whether a client's cursor can still be replayed. When it
// cannot, ResyncCursor is where the client should resume live after taking a
// fresh snapshot, so no event is silently missed.
type ResyncDecision struct {
	Replayable   bool   `json:"replayable"`
	ResyncCursor int64  `json:"resyncCursor,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// EvaluateReplay decides whether a cursor is still replayable against the current
// retained-event bounds. Retained events occupy [OldestCursor, LatestCursor]; a
// client with cursor C needs events with id > C, so the earliest it needs is
// C+1. If C+1 was pruned (C+1 < OldestCursor) there is a gap and replay is
// refused.
func EvaluateReplay(bounds EventBounds, cursor int64) ResyncDecision {
	// Nothing retained: the client is already current, no gap possible.
	if bounds.RetainedEvents == 0 {
		return ResyncDecision{Replayable: true}
	}
	if cursor+1 < bounds.OldestCursor {
		return ResyncDecision{
			Replayable:   false,
			ResyncCursor: bounds.LatestCursor,
			Reason:       "cursor expired: events after it were compacted; resync from a snapshot",
		}
	}
	return ResyncDecision{Replayable: true}
}

// ReplayEvents returns the events after cursor, or an explicit resync decision
// when the cursor has expired. On expiry it returns no events and Replayable
// false, never a silently truncated stream.
func ReplayEvents(ctx context.Context, s EventStore, cursor int64, limit int) ([]Event, ResyncDecision, error) {
	bounds, err := s.HubEventBounds(ctx)
	if err != nil {
		return nil, ResyncDecision{}, err
	}
	decision := EvaluateReplay(bounds, cursor)
	if !decision.Replayable {
		return nil, decision, nil
	}
	events, err := s.ListHubEventsAfter(ctx, cursor, limit)
	if err != nil {
		return nil, decision, err
	}
	return events, decision, nil
}
