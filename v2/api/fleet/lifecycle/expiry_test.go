package lifecycle

import (
	"testing"
	"time"

	"norn/v2/api/fleet"
)

func TestProjectExpiryNeverMutatesTerminal(t *testing.T) {
	t.Parallel()
	now := time.Now()

	terminal := fleet.RunnerAttempt{Status: "succeeded", HeartbeatExpiresAt: now.Add(-time.Hour)}
	if got := ProjectExpiry(terminal, now); got.Status != "succeeded" || got.LastError != "" || got.FinishedAt != nil {
		t.Fatalf("a terminal attempt must not be projected: %+v", got)
	}

	live := fleet.RunnerAttempt{Status: "running", HeartbeatExpiresAt: now.Add(time.Hour)}
	if got := ProjectExpiry(live, now); got.Status != "running" {
		t.Fatalf("a live attempt with an unexpired lease must not be projected: %+v", got)
	}

	expired := fleet.RunnerAttempt{Status: "queued", HeartbeatExpiresAt: now.Add(-time.Minute)}
	got := ProjectExpiry(expired, now)
	if got.Status != "abandoned" || got.LastError == "" || got.FinishedAt == nil || !got.FinishedAt.Equal(expired.HeartbeatExpiresAt) {
		t.Fatalf("an expired live attempt must project to abandoned: %+v", got)
	}
	if expired.Status != "queued" {
		t.Fatalf("ProjectExpiry must not mutate its argument: %+v", expired)
	}
}
