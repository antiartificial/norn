package model

import (
	"testing"
	"time"
)

func TestFleetRunnerTimingFiveNodeColdStartProjectsConfiguredRange(t *testing.T) {
	started := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	attempt := &FleetRunnerAttempt{Status: FleetRunnerAttemptRunning, CurrentPhase: "infrastructure_applied", StartedAt: started, PhaseStartedAt: started.Add(2 * time.Minute)}
	timing := NewFleetRunnerTiming(attempt, FleetTimingClassification{OperationClass: FleetTimingColdStart, CreatedNodeCount: 5}, started.Add(10*time.Minute))
	if timing.Availability != FleetTimingAvailable || timing.Confidence != FleetTimingLow || timing.ElapsedMs != (10*time.Minute).Milliseconds() {
		t.Fatalf("timing = %#v", timing)
	}
	if timing.EstimatedRemaining == nil || timing.EstimatedRemaining.LowMs != (5*time.Minute).Milliseconds() || timing.EstimatedRemaining.HighMs != (20*time.Minute).Milliseconds() {
		t.Fatalf("remaining = %#v", timing.EstimatedRemaining)
	}
	if timing.EstimatedCompletion == nil || !timing.EstimatedCompletion.EarliestAt.Equal(started.Add(15*time.Minute)) || !timing.EstimatedCompletion.LatestAt.Equal(started.Add(30*time.Minute)) {
		t.Fatalf("completion = %#v", timing.EstimatedCompletion)
	}
	if len(timing.Phases) != 1 || timing.Phases[0].State != "active" || timing.Phases[0].ElapsedMs != (8*time.Minute).Milliseconds() {
		t.Fatalf("phases = %#v", timing.Phases)
	}
}

func TestFleetRunnerTimingClampsAndDoesNotEstimateUngroundedOrFailedWork(t *testing.T) {
	started := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	cold := &FleetRunnerAttempt{Status: FleetRunnerAttemptRunning, CurrentPhase: "nodes_configured", StartedAt: started, PhaseStartedAt: started}
	clamped := NewFleetRunnerTiming(cold, FleetTimingClassification{OperationClass: FleetTimingColdStart, CreatedNodeCount: 5}, started.Add(35*time.Minute))
	if clamped.EstimatedRemaining == nil || clamped.EstimatedRemaining.LowMs != 0 || clamped.EstimatedRemaining.HighMs != 0 {
		t.Fatalf("remaining was not clamped: %#v", clamped.EstimatedRemaining)
	}
	unknown := NewFleetRunnerTiming(cold, FleetTimingClassification{OperationClass: FleetTimingUnclassified, CreatedNodeCount: 1000}, started.Add(time.Minute))
	if unknown.Availability != FleetTimingUnavailable || unknown.OperationClass != FleetTimingUnclassified || unknown.EstimatedRemaining != nil {
		t.Fatalf("ungrounded timing = %#v", unknown)
	}
	finished := started.Add(7 * time.Minute)
	failed := &FleetRunnerAttempt{Status: FleetRunnerAttemptFailed, CurrentPhase: "nodes_configured", StartedAt: started, PhaseStartedAt: started, FinishedAt: &finished}
	failureTiming := NewFleetRunnerTiming(failed, FleetTimingClassification{OperationClass: FleetTimingColdStart, CreatedNodeCount: 5}, started.Add(20*time.Minute))
	if failureTiming.Availability != FleetTimingAvailable || failureTiming.Confidence != FleetTimingLow || failureTiming.EstimatedTotal == nil || failureTiming.EstimatedRemaining != nil || failureTiming.EstimatedCompletion != nil || failureTiming.Phases[0].State != "terminal" {
		t.Fatalf("failed timing = %#v", failureTiming)
	}
	succeeded := *failed
	succeeded.Status = FleetRunnerAttemptSucceeded
	successTiming := NewFleetRunnerTiming(&succeeded, FleetTimingClassification{OperationClass: FleetTimingColdStart, CreatedNodeCount: 5}, started.Add(20*time.Minute))
	if successTiming.ElapsedMs != (7*time.Minute).Milliseconds() || successTiming.Phases[0].State != "complete" || successTiming.EstimatedCompletion == nil || !successTiming.EstimatedCompletion.EarliestAt.Equal(finished) {
		t.Fatalf("succeeded timing = %#v", successTiming)
	}
}

func TestFleetTimingExclusionsAreStableCodes(t *testing.T) {
	want := []string{"review_approval", "github_queue", "dns_propagation", "application_migrations"}
	got := FleetTimingExclusions()
	if len(got) != len(want) {
		t.Fatalf("exclusions = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("exclusions = %#v", got)
		}
	}
}
