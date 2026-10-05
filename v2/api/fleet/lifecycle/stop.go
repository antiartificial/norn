package lifecycle

import (
	"net/url"
	"time"
)

// StopEvidence is server-observed GitHub terminal evidence for a predecessor
// runner attempt's protected workflow run. store.FleetRunnerPredecessorStopEvidence
// is a type alias for this definition, so its JSON shape and identity are
// unchanged.
type StopEvidence struct {
	PredecessorID       string    `json:"predecessorId"`
	SourceDispatchRunID string    `json:"sourceDispatchRunId"`
	RunAttempt          int64     `json:"runAttempt"`
	WorkflowURL         string    `json:"workflowUrl"`
	Status              string    `json:"status"`
	Conclusion          string    `json:"conclusion"`
	ObservedAt          time.Time `json:"observedAt"`
}

// ValidStopEvidence reports whether e proves that the named predecessor's
// workflow run terminated: it must name the expected predecessor and source
// run, carry a positive run attempt, a completed status with a failure,
// cancelled or timed_out conclusion, an observation timestamp within five
// minutes in the past (and no more than one minute in the future of now),
// and an https://github.com workflow URL. Callers pass time.Now() (m4).
func ValidStopEvidence(e StopEvidence, predecessorID, sourceRunID string, now time.Time) bool {
	if e.PredecessorID != predecessorID || e.SourceDispatchRunID != sourceRunID || e.RunAttempt <= 0 || e.Status != "completed" ||
		(e.Conclusion != "failure" && e.Conclusion != "cancelled" && e.Conclusion != "timed_out") ||
		e.ObservedAt.IsZero() || now.Sub(e.ObservedAt) > 5*time.Minute || e.ObservedAt.After(now.UTC().Add(time.Minute)) {
		return false
	}
	parsed, err := url.Parse(e.WorkflowURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host == "github.com" && parsed.User == nil && len(e.WorkflowURL) <= 2048
}
