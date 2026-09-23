package hub

import (
	"context"
	"testing"
)

type replayFakeStore struct {
	bounds EventBounds
	after  []Event
}

func (s *replayFakeStore) AppendHubEvent(context.Context, *Event) error { return nil }
func (s *replayFakeStore) ListHubEventsAfter(_ context.Context, after int64, _ int) ([]Event, error) {
	return s.after, nil
}
func (s *replayFakeStore) LatestHubEventID(context.Context) (int64, error) {
	return s.bounds.LatestCursor, nil
}
func (s *replayFakeStore) HubEventBounds(context.Context) (EventBounds, error) {
	return s.bounds, nil
}

func TestEvaluateReplay(t *testing.T) {
	cases := []struct {
		name       string
		bounds     EventBounds
		cursor     int64
		replayable bool
		resyncFrom int64
	}{
		{"empty store is current", EventBounds{RetainedEvents: 0}, 0, true, 0},
		{"cursor within window", EventBounds{OldestCursor: 5, LatestCursor: 10, RetainedEvents: 6}, 7, true, 0},
		{"cursor at oldest-1 has no gap", EventBounds{OldestCursor: 5, LatestCursor: 10, RetainedEvents: 6}, 4, true, 0},
		{"cursor before window expired", EventBounds{OldestCursor: 5, LatestCursor: 10, RetainedEvents: 6}, 2, false, 10},
		{"fresh cursor with pruned history expires", EventBounds{OldestCursor: 5, LatestCursor: 10, RetainedEvents: 6}, 0, false, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := EvaluateReplay(tc.bounds, tc.cursor)
			if d.Replayable != tc.replayable {
				t.Fatalf("replayable = %v, want %v", d.Replayable, tc.replayable)
			}
			if !d.Replayable && d.ResyncCursor != tc.resyncFrom {
				t.Fatalf("resync cursor = %d, want %d", d.ResyncCursor, tc.resyncFrom)
			}
		})
	}
}

func TestReplayEventsReturnsResyncOnExpiry(t *testing.T) {
	ctx := context.Background()
	store := &replayFakeStore{
		bounds: EventBounds{OldestCursor: 5, LatestCursor: 10, RetainedEvents: 6},
		after:  []Event{{ID: 6}, {ID: 7}},
	}
	// Expired cursor: no events, explicit resync.
	events, decision, err := ReplayEvents(ctx, store, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Replayable || len(events) != 0 || decision.ResyncCursor != 10 {
		t.Fatalf("expected resync with no events, got events=%d decision=%+v", len(events), decision)
	}
	// Valid cursor: events flow.
	events, decision, err = ReplayEvents(ctx, store, 5, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Replayable || len(events) != 2 {
		t.Fatalf("expected replay with events, got events=%d decision=%+v", len(events), decision)
	}
}
