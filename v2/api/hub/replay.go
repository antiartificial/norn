package hub

// ResyncDecision explains whether a cursor can replay every event it has not
// observed. A client receiving Replayable=false must refresh authoritative
// state, then reconnect at ResyncCursor.
type ResyncDecision struct {
	Replayable   bool   `json:"replayable"`
	ResyncCursor int64  `json:"resyncCursor,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// EvaluateReplay evaluates a cursor against retained events and the durable
// compaction watermark. The watermark survives an empty retained table, so an
// old client is never told that a fully-pruned stream is a valid replay.
func EvaluateReplay(bounds EventBounds, cursor int64) ResyncDecision {
	if cursor < bounds.PrunedThroughCursor {
		return ResyncDecision{
			Replayable:   false,
			ResyncCursor: bounds.LatestCursor,
			Reason:       "cursor expired: events after it were compacted; refresh authoritative state before reconnecting",
		}
	}
	return ResyncDecision{Replayable: true}
}
