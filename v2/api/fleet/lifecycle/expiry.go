package lifecycle

import (
	"time"

	"norn/v2/api/fleet"
)

// ProjectExpiry returns a's projected state as of now: a live (queued or
// running) attempt whose heartbeat lease has passed is reported abandoned,
// with FinishedAt set to the lease expiry. It never mutates a, and it never
// changes an already-terminal attempt. This is the etcd V3 projection (D8);
// PG legacy instead writes abandoned on read and stays backend-local.
func ProjectExpiry(a fleet.RunnerAttempt, now time.Time) fleet.RunnerAttempt {
	if (a.Status == "queued" || a.Status == "running") && a.HeartbeatExpiresAt.Before(now) {
		a.Status, a.LastError = "abandoned", "heartbeat lease expired; external execution termination unproven"
		finished := a.HeartbeatExpiresAt
		a.FinishedAt = &finished
	}
	return a
}
