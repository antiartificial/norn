package model

import (
	"testing"
	"time"
)

func TestDeploymentStatuses(t *testing.T) {
	statuses := []DeployStatus{
		StatusQueued, StatusBuilding, StatusTesting, StatusSnapshot,
		StatusMigrating, StatusDeploying, StatusDeployed, StatusFailed,
		StatusRolledBack,
	}

	seen := map[DeployStatus]bool{}
	for _, s := range statuses {
		if seen[s] {
			t.Errorf("duplicate status: %q", s)
		}
		seen[s] = true
		if string(s) == "" {
			t.Error("empty status string")
		}
	}
}

func TestDeploymentFields(t *testing.T) {
	now := time.Now()
	d := Deployment{
		ID:        "test-123",
		App:       "my-app",
		CommitSHA: "abc123def456",
		ImageTag:  "my-app:abc123def456",
		Status:    StatusQueued,
		Steps:     []StepLog{},
		StartedAt: now,
	}

	if d.ID != "test-123" {
		t.Errorf("ID = %q", d.ID)
	}
	if d.Status != StatusQueued {
		t.Errorf("Status = %q", d.Status)
	}
	if d.FinishedAt != nil {
		t.Errorf("FinishedAt should be nil for new deployment")
	}
}

func TestStepLog(t *testing.T) {
	step := StepLog{
		Step:       "build",
		Status:     StatusDeployed,
		DurationMs: 1500,
		Output:     "build output",
	}

	if step.Step != "build" {
		t.Errorf("Step = %q", step.Step)
	}
	if step.DurationMs != 1500 {
		t.Errorf("DurationMs = %d", step.DurationMs)
	}
}
