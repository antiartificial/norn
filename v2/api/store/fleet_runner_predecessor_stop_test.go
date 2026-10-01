package store

import (
	"testing"
	"time"
)

func TestFleetRunnerPredecessorStopEvidenceRejectsForgedOrStaleProof(t *testing.T) {
	proof := FleetRunnerPredecessorStopEvidence{
		PredecessorID:       "4b5b5555-1111-4222-8333-444444444444",
		SourceDispatchRunID: "37",
		RunAttempt:          2,
		WorkflowURL:         "https://github.com/acme/fleet/actions/runs/37",
		Status:              "completed",
		Conclusion:          "timed_out",
		ObservedAt:          time.Now().UTC(),
	}
	if !validFleetRunnerPredecessorStop(proof, proof.PredecessorID, proof.SourceDispatchRunID) {
		t.Fatal("valid server observation rejected")
	}
	for name, change := range map[string]func(*FleetRunnerPredecessorStopEvidence){
		"success":           func(p *FleetRunnerPredecessorStopEvidence) { p.Conclusion = "success" },
		"active":            func(p *FleetRunnerPredecessorStopEvidence) { p.Status, p.Conclusion = "in_progress", "" },
		"wrong-predecessor": func(p *FleetRunnerPredecessorStopEvidence) { p.PredecessorID = "different" },
		"stale":             func(p *FleetRunnerPredecessorStopEvidence) { p.ObservedAt = time.Now().UTC().Add(-6 * time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := proof
			change(&candidate)
			if validFleetRunnerPredecessorStop(candidate, proof.PredecessorID, proof.SourceDispatchRunID) {
				t.Fatalf("invalid proof accepted: %#v", candidate)
			}
		})
	}
}
