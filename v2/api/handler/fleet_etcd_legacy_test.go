package handler

import (
	"norn/v2/api/fleet"
	"strings"
	"testing"
)

func TestFleetReplayWorkloadSHALegacyRootOnly(t *testing.T) {
	sha := strings.Repeat("a", 40)
	if got := fleetReplayWorkloadSHA(&fleet.RunnerAttempt{Attempt: 1, CommitSHA: sha}); got != sha {
		t.Fatalf("legacy root=%q", got)
	}
	if got := fleetReplayWorkloadSHA(&fleet.RunnerAttempt{Attempt: 2, CommitSHA: sha}); got != "" {
		t.Fatalf("legacy successor=%q", got)
	}
}
