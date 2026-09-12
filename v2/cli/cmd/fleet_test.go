package cmd

import (
	"os"
	"path/filepath"
	"strings"
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

func TestFleetExecuteNonceReadsExactPrivateInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatch-nonce")
	nonce := strings.Repeat("a", 64) + "\n"
	if err := os.WriteFile(path, []byte(nonce), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPrivateFleetDispatchNonce(path)
	if err != nil || got != strings.TrimSpace(nonce) {
		t.Fatalf("private nonce = %q, %v", got, err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFleetDispatchNonce(path); err == nil {
		t.Fatal("group-readable nonce accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "nonce-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFleetDispatchNonce(link); err == nil {
		t.Fatal("symlinked nonce accepted")
	}
	copy := filepath.Join(t.TempDir(), "nonce-hardlink")
	if err := os.Link(path, copy); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFleetDispatchNonce(path); err == nil {
		t.Fatal("hardlinked nonce accepted")
	}
}

func TestFleetGitHubPrepareResetIsExplicit(t *testing.T) {
	var found bool
	for _, command := range fleetGitHubCmd.Commands() {
		if command.Name() == "prepare-reset" {
			found = true
			if command.Flags().Lookup("confirm-lost-nonce") == nil {
				t.Fatal("prepare reset lacks explicit lost-nonce confirmation")
			}
		}
	}
	if !found {
		t.Fatal("fleet GitHub prepare-reset command is missing")
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
