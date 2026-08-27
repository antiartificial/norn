package handler

import (
	"strings"
	"testing"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

func TestFleetRunnerStartValidation(t *testing.T) {
	valid := fleet.RunnerAttemptStartRequest{
		SchemaVersion:   model.FleetRunnerAttemptSchemaVersion,
		RunnerAttemptID: "github-run-123", CommitSHA: strings.Repeat("a", 40),
		PlanSHA256: strings.Repeat("b", 64), WorkflowURL: "https://github.com/example/fleet/actions/runs/123",
		HeartbeatTimeoutSeconds: 120,
	}
	if err := validateFleetRunnerStart(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.WorkflowURL = "https://token@example.test/run"
	if err := validateFleetRunnerStart(invalid); err == nil {
		t.Fatal("embedded workflow credentials were accepted")
	}
	invalid = valid
	invalid.WorkflowURL = "https://github.com/example/fleet/actions/runs/123?token=secret"
	if err := validateFleetRunnerStart(invalid); err == nil {
		t.Fatal("workflow URL query credentials were accepted")
	}
	invalid = valid
	invalid.HeartbeatTimeoutSeconds = 10
	if err := validateFleetRunnerStart(invalid); err == nil {
		t.Fatal("unsafe heartbeat timeout was accepted")
	}
}

func TestFleetRunnerPhaseAdvanceIsOrderedAndTerminal(t *testing.T) {
	for index, phase := range fleetReconciliationPhases {
		next, terminal, err := nextFleetRunnerPhase(phase, true)
		if err != nil {
			t.Fatal(err)
		}
		if index == len(fleetReconciliationPhases)-1 {
			if !terminal || next != "complete" {
				t.Fatalf("terminal advance = %q, %v", next, terminal)
			}
			continue
		}
		if terminal || next != fleetReconciliationPhases[index+1] {
			t.Fatalf("%s advance = %q, %v", phase, next, terminal)
		}
	}
	if _, _, err := nextFleetRunnerPhase("invented", true); err == nil {
		t.Fatal("unknown phase was accepted")
	}
	next, terminal, err := nextFleetRunnerPhase("readiness_verified", false)
	if err != nil || terminal || next != "complete" {
		t.Fatalf("non-drain advance = %q, %v, %v", next, terminal, err)
	}
	if _, _, err := nextFleetRunnerPhase("old_nodes_drained", false); err == nil {
		t.Fatal("non-drain plan accepted a drain phase")
	}
}

func TestFleetRunnerBindingCannotChangeAcrossCheckpoints(t *testing.T) {
	commit := strings.Repeat("a", 40)
	plan := strings.Repeat("b", 64)
	checkpoints := []model.Operation{{Payload: map[string]interface{}{"commitSha": commit, "planSha256": plan}}}
	if err := validateFleetRunnerBinding(checkpoints, commit, plan); err != nil {
		t.Fatal(err)
	}
	if err := validateFleetRunnerBinding(checkpoints, strings.Repeat("c", 40), plan); err == nil {
		t.Fatal("changed commit binding was accepted")
	}
}

func TestFleetRunnerResumesAtFirstIncompletePhase(t *testing.T) {
	plan := &model.Operation{Payload: map[string]interface{}{"action": "scale", "current": map[string]interface{}{"desired": 3.0}, "proposed": map[string]interface{}{"desired": 2.0}}}
	checkpoints := []model.Operation{
		{Status: model.OperationSucceeded, Payload: map[string]interface{}{"phase": "infrastructure_applied"}},
		{Status: model.OperationSucceeded, Payload: map[string]interface{}{"phase": "inventory_generated"}},
		{Status: model.OperationFailed, Payload: map[string]interface{}{"phase": "nodes_configured"}},
	}
	phase, complete := firstIncompleteFleetRunnerPhase(plan, checkpoints)
	if complete || phase != "nodes_configured" {
		t.Fatalf("resume phase = %q, complete=%v", phase, complete)
	}
	for _, phase := range fleetRunnerPhases(true)[2:] {
		checkpoints = append(checkpoints, model.Operation{Status: model.OperationSucceeded, Payload: map[string]interface{}{"phase": phase}})
	}
	phase, complete = firstIncompleteFleetRunnerPhase(plan, checkpoints)
	if !complete || phase != "complete" {
		t.Fatalf("completed phase = %q, complete=%v", phase, complete)
	}
}
