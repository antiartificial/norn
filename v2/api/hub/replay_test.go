package hub

import "testing"

func TestEvaluateReplayUsesPersistentCompactionWatermark(t *testing.T) {
	bounds := EventBounds{OldestCursor: 0, LatestCursor: 10, PrunedThroughCursor: 10, RetainedEvents: 0}
	decision := EvaluateReplay(bounds, 4)
	if decision.Replayable || decision.ResyncCursor != 10 {
		t.Fatalf("fully-pruned cursor decision = %#v", decision)
	}
	if decision := EvaluateReplay(bounds, 10); !decision.Replayable {
		t.Fatalf("stream head must remain replayable: %#v", decision)
	}
}
