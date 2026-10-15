package githubapp

import (
	"strings"
	"testing"
)

func TestScaleUpApprovalInputMustMatchProtectedBinding(t *testing.T) {
	bound := &Dispatch{PlanRunID: 91, PlanSHA: strings.Repeat("a", 64), PilotRunID: "pilot261012a", ScaleUpApprovalRunID: 12345}
	run := &applyRun{Inputs: map[string]string{
		"fleet_environment": "disposable/fleet/nyc3", "plan_run_id": "91",
		"plan_sha256": bound.PlanSHA, "norn_plan_id": "plan-1", "allow_destructive": "false",
		"dispatch_nonce": strings.Repeat("b", 64), "pilot_run_id": bound.PilotRunID,
		"scale_up_approval_run_id": "12345",
	}}
	if err := verifyObservedApplyInputs(run, "plan-1", "disposable/fleet/nyc3", false, bound, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	for _, got := range []string{"", "12346"} {
		run.Inputs["scale_up_approval_run_id"] = got
		if err := verifyObservedApplyInputs(run, "plan-1", "disposable/fleet/nyc3", false, bound, strings.Repeat("b", 64)); err == nil {
			t.Fatalf("accepted scale-up approval run ID %q", got)
		}
	}
}
