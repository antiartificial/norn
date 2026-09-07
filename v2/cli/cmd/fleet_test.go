package cmd

import (
	"testing"

	"norn/v2/cli/api"
)

func TestFleetCLILeavesProtectedRunnerMutationsToGitHubWorkflows(t *testing.T) {
	var attemptsVisible, mutationGroupVisible bool
	for _, command := range fleetCmd.Commands() {
		switch command.Name() {
		case "attempts":
			attemptsVisible = true
		case "attempt":
			mutationGroupVisible = true
		}
	}
	if !attemptsVisible {
		t.Fatal("fleet attempts read-only status command is missing")
	}
	if mutationGroupVisible {
		t.Fatal("human CLI exposed protected runner mutation commands")
	}
}

func TestFleetAttemptTimingDisplayShowsAdvisoryRangeOnlyWhenAvailable(t *testing.T) {
	elapsed, eta, confidence := fleetAttemptTimingDisplay(&api.FleetRunnerTiming{
		Availability: "available", ElapsedMs: 2_000, Confidence: "low",
		EstimatedRemaining: &api.FleetTimingRange{LowMs: 900_000, HighMs: 1_800_000},
	})
	if elapsed != "2s" || eta != "15m0s–30m0s" || confidence != "low" {
		t.Fatalf("display = %q, %q, %q", elapsed, eta, confidence)
	}
	_, eta, confidence = fleetAttemptTimingDisplay(&api.FleetRunnerTiming{Availability: "unavailable", ElapsedMs: 2_000, Confidence: "none"})
	if eta != "—" || confidence != "none" {
		t.Fatalf("unavailable display = %q, %q", eta, confidence)
	}
	_, eta, _ = fleetAttemptTimingDisplay(&api.FleetRunnerTiming{
		Availability: "available", EstimatedRemaining: &api.FleetTimingRange{}, Confidence: "low",
	})
	if eta != "0s" {
		t.Fatalf("equal endpoint eta = %q", eta)
	}
}
