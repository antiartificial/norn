package lifecycle

import (
	"testing"
	"time"
)

func TestValidStopEvidenceRejectsStaleFutureWrongRun(t *testing.T) {
	t.Parallel()
	now := time.Now()
	valid := StopEvidence{
		PredecessorID:       "pred-1",
		SourceDispatchRunID: "run-1",
		RunAttempt:          1,
		WorkflowURL:         "https://github.com/acme/repo/actions/runs/1",
		Status:              "completed",
		Conclusion:          "failure",
		ObservedAt:          now.Add(-time.Minute),
	}
	if !ValidStopEvidence(valid, "pred-1", "run-1", now) {
		t.Fatal("expected a well-formed, fresh proof to be valid")
	}

	wrongPredecessor := valid
	if ValidStopEvidence(wrongPredecessor, "other-pred", "run-1", now) {
		t.Fatal("a proof naming a different predecessor must be rejected")
	}

	wrongRun := valid
	if ValidStopEvidence(wrongRun, "pred-1", "other-run", now) {
		t.Fatal("a proof naming a different source run must be rejected")
	}

	stale := valid
	stale.ObservedAt = now.Add(-10 * time.Minute)
	if ValidStopEvidence(stale, "pred-1", "run-1", now) {
		t.Fatal("a proof observed more than five minutes ago must be rejected")
	}

	future := valid
	future.ObservedAt = now.Add(5 * time.Minute)
	if ValidStopEvidence(future, "pred-1", "run-1", now) {
		t.Fatal("a proof observed too far in the future must be rejected")
	}

	wrongConclusion := valid
	wrongConclusion.Conclusion = "success"
	if ValidStopEvidence(wrongConclusion, "pred-1", "run-1", now) {
		t.Fatal("a non-terminal conclusion must be rejected")
	}

	wrongHost := valid
	wrongHost.WorkflowURL = "https://evil.example/acme/repo/actions/runs/1"
	if ValidStopEvidence(wrongHost, "pred-1", "run-1", now) {
		t.Fatal("a workflow URL not on github.com must be rejected")
	}

	zeroAttempt := valid
	zeroAttempt.RunAttempt = 0
	if ValidStopEvidence(zeroAttempt, "pred-1", "run-1", now) {
		t.Fatal("a non-positive run attempt must be rejected")
	}
}
