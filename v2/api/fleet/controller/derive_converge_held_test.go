package controller

import (
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
)

// heldRecoveryInput is a controller-mode resource whose fence is held by the
// converge plan "plan-1" through an attempt that is no longer heartbeating
// (the executor died), the shape the P2/P3 hand-offs derive from.
func heldRecoveryInput(now time.Time) Input {
	plan := executablePlan()
	plan.PlanID, plan.Approved = "plan-1", true
	in := convergeInput(now, executor.ModeController, plan)
	in.Resource.Name = "staging"
	in.Fence = heldFence()
	in.Holder = lifecycle.HolderFacts{DispatchState: "bound", Attempts: []fleet.RunnerAttempt{
		{ID: "attempt-2", PlanID: "plan-1", Attempt: 2, Status: "running", HeartbeatExpiresAt: now.Add(-time.Hour)},
	}}
	return in
}

// TestDeriveStoppedAttemptNeedsResumeApprove: a recovery that was itself
// killed has consumed its resume approve, so while the fence is held the
// record must say a new --resume approve is needed, through nextAction and
// the blocker, outranking await_runner and resolve_uncertain_outcome.
func TestDeriveStoppedAttemptNeedsResumeApprove(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := heldRecoveryInput(now)
	in.Converge.ResumeNeededAttemptID = "attempt-2"
	status := DeriveStatus(in)
	if status.NextAction != NextActionApproveConverge {
		t.Fatalf("NextAction = %s, want approve_converge", status.NextAction)
	}
	d := DeriveConverge(in.Resource, status, in.Converge)
	if !d.ResumeNeeded || d.Blocker == nil || !d.Blocker.WhileHeld || d.Blocker.Kind != BlockerKindApproval || d.Blocker.Reason != BlockerReasonApprovalRequired ||
		!strings.Contains(d.Blocker.Message, "norn fleet converge staging --resume") || !strings.Contains(d.Blocker.Message, "attempt-2") {
		t.Fatalf("derivation = %+v / %+v", d, d.Blocker)
	}

	// Without the fact the held fence keeps its usual next action.
	in = heldRecoveryInput(now)
	if got := DeriveStatus(in).NextAction; got != NextActionResolveUncertainOutcome {
		t.Fatalf("NextAction = %s, want resolve_uncertain_outcome", got)
	}
	// A free fence has nothing to resume, whatever the fact says.
	in = convergeInput(now, executor.ModeController, executablePlan())
	in.Converge.ResumeNeededAttemptID = "attempt-2"
	if d := DeriveConverge(in.Resource, DeriveStatus(in), in.Converge); d.ResumeNeeded {
		t.Fatalf("free fence: %+v", d)
	}
}

// TestDeriveBlockersStayVisibleWhileHeld: the destructive remainder of the
// held plan and a foreign state lock are not hidden behind the held fence's
// execution blocker (P3 hand-off); a refusal of another plan is.
func TestDeriveBlockersStayVisibleWhileHeld(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()

	in := heldRecoveryInput(now)
	in.Converge.Plan.Refused = true
	d := DeriveConverge(in.Resource, DeriveStatus(in), in.Converge)
	if d.Blocker == nil || !d.Blocker.WhileHeld || d.Blocker.Reason != BlockerReasonDestructive || d.Blocker.PlanID != "plan-1" || d.NextAction == NextActionApproveConverge {
		t.Fatalf("destructive remainder while held: %+v", d)
	}
	// The holder's own refusal stays visible after a newer commit moved the
	// desired revision (and so Plan) to another plan.
	in = heldRecoveryInput(now)
	in.Converge.Plan.PlanID, in.Converge.HolderRefused = "plan-newer", true
	if d := DeriveConverge(in.Resource, DeriveStatus(in), in.Converge); d.Blocker == nil || !d.Blocker.WhileHeld ||
		d.Blocker.Reason != BlockerReasonDestructive || d.Blocker.PlanID != "plan-1" {
		t.Fatalf("holder refusal with a newer desired plan: %+v", d.Blocker)
	}
	// A refusal of a plan other than the holder is not marked to outrank the
	// held fence (the resource view keeps the execution blocker).
	in = heldRecoveryInput(now)
	in.Converge.Plan.PlanID, in.Converge.Plan.Refused = "plan-other", true
	if d := DeriveConverge(in.Resource, DeriveStatus(in), in.Converge); d.Blocker != nil && d.Blocker.WhileHeld {
		t.Fatalf("another plan's refusal must not outrank a held fence: %+v", d.Blocker)
	}

	in = heldRecoveryInput(now)
	in.Converge.StateLock = &StateLockFacts{SourceAttemptID: "attempt-2", LockID: "lock-9", Who: "ops@host"}
	d = DeriveConverge(in.Resource, DeriveStatus(in), in.Converge)
	if d.Blocker == nil || !d.Blocker.WhileHeld || d.Blocker.Kind != BlockerKindExecution || d.Blocker.Reason != BlockerReasonStateLockForeign || !strings.Contains(d.Blocker.Message, "lock-9") {
		t.Fatalf("state_lock_foreign: %+v", d.Blocker)
	}
	// It is not a refusal of the plan, and it needs no approve.
	if d.NextAction == NextActionApproveConverge {
		t.Fatalf("state_lock_foreign must not ask for approval")
	}
	// Priority: destructive, then state lock, then a stopped attempt.
	in.Converge.ResumeNeededAttemptID = "attempt-2"
	if d := DeriveConverge(in.Resource, DeriveStatus(in), in.Converge); d.Blocker.Reason != BlockerReasonStateLockForeign || d.ResumeNeeded {
		t.Fatalf("state lock outranks a resume: %+v", d)
	}
	in.Converge.Plan.Refused = true
	if d := DeriveConverge(in.Resource, DeriveStatus(in), in.Converge); d.Blocker.Reason != BlockerReasonDestructive {
		t.Fatalf("destructive outranks the state lock: %+v", d.Blocker)
	}
	// A state lock is only meaningful while the fence is held.
	free := convergeInput(now, executor.ModeController, executablePlan())
	free.Converge.StateLock = &StateLockFacts{SourceAttemptID: "attempt-2"}
	if d := DeriveConverge(free.Resource, DeriveStatus(free), free.Converge); d.Blocker != nil && d.Blocker.Reason == BlockerReasonStateLockForeign {
		t.Fatalf("free fence reported a state lock: %+v", d.Blocker)
	}
}
