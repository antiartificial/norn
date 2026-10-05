package controller

import (
	"testing"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
)

const convergeCommit = "cccccccccccccccccccccccccccccccccccccccc"

func convergeInput(now time.Time, mode string, plan *ConvergePlanFacts) Input {
	in := baseInput(now)
	in.Resource.ExecutionMode = mode
	in.Resource.Desired = DesiredRevision{Generation: 1, PlanID: "plan-c", CommitSHA: convergeCommit, Verification: VerificationGitHubProtectedMain}
	in.Converge = &ConvergeFacts{Plan: plan}
	return in
}

func executablePlan() *ConvergePlanFacts {
	return &ConvergePlanFacts{PlanID: "plan-c", CommitSHA: convergeCommit, BaseCommitSHA: "dddddddddddddddddddddddddddddddddddddddd", PlanSHA256: "ab", RequiresApproval: true}
}

func TestDeriveApproveConverge(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()

	// Executable plan, controller mode, no approve: approve_converge + approval blocker.
	in := convergeInput(now, executor.ModeController, executablePlan())
	status := DeriveStatus(in)
	if status.NextAction != NextActionApproveConverge {
		t.Fatalf("NextAction = %s, want approve_converge", status.NextAction)
	}
	d := DeriveConverge(in.Resource, status, in.Converge)
	if d.Blocker == nil || d.Blocker.Kind != BlockerKindApproval || d.Blocker.Reason != BlockerReasonApprovalRequired || d.Blocker.PlanID != "plan-c" {
		t.Fatalf("blocker = %+v, want approval/approval_required", d.Blocker)
	}

	// Approved: no action, no blocker.
	in = convergeInput(now, executor.ModeController, executablePlan())
	in.Converge.Plan.Approved, in.Converge.Plan.ApprovedBy = true, "op"
	status = DeriveStatus(in)
	if status.NextAction == NextActionApproveConverge {
		t.Fatalf("approved plan still asks for approval")
	}
	if d := DeriveConverge(in.Resource, status, in.Converge); d.Blocker != nil {
		t.Fatalf("approved plan blocker = %+v", d.Blocker)
	}

	// Config plan over a base commit auto-applies.
	cfg := executablePlan()
	cfg.RequiresApproval = false
	in = convergeInput(now, executor.ModeController, cfg)
	if got := DeriveStatus(in).NextAction; got == NextActionApproveConverge {
		t.Fatalf("config plan asked for approval")
	}

	// The first converge (no base commit) needs approval even for config.
	first := *cfg
	first.BaseCommitSHA = ""
	in = convergeInput(now, executor.ModeController, &first)
	if got := DeriveStatus(in).NextAction; got != NextActionApproveConverge {
		t.Fatalf("first converge NextAction = %s, want approve_converge", got)
	}

	// A held fence outranks approval: await_runner / resolve_uncertain stay.
	in = convergeInput(now, executor.ModeController, executablePlan())
	in.Fence = heldFence()
	in.Holder = lifecycle.HolderFacts{DispatchState: "bound", Attempts: []fleet.RunnerAttempt{
		{ID: "attempt-1", PlanID: "plan-1", Attempt: 1, Status: "running", HeartbeatExpiresAt: now.Add(time.Hour)},
	}}
	if got := DeriveStatus(in).NextAction; got == NextActionApproveConverge {
		t.Fatalf("approve_converge outranked a held fence")
	}

	// Already applied: nothing to approve.
	in = convergeInput(now, executor.ModeController, executablePlan())
	in.Fence = lifecycle.FenceFacts{TargetID: "tgt_1", Generation: 2, AuthorityEpoch: 1,
		LastRelease: &lifecycle.Release{PlanID: "plan-c", Reason: "succeeded", At: now.Add(-time.Minute)}}
	in.LastReleaseAttempts = []fleet.RunnerAttempt{{ID: "a", PlanID: "plan-c", Attempt: 1, Status: "succeeded", CommitSHA: convergeCommit}}
	if got := DeriveStatus(in).NextAction; got == NextActionApproveConverge {
		t.Fatalf("applied plan asked for approval")
	}

	// github mode never asks the executor lane for approval.
	in = convergeInput(now, executor.ModeGitHub, executablePlan())
	if got := DeriveStatus(in).NextAction; got == NextActionApproveConverge {
		t.Fatalf("github mode asked for approval")
	}
}

func TestDeriveConvergeBlockers(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	derive := func(in Input) *Blocker {
		return DeriveConverge(in.Resource, DeriveStatus(in), in.Converge).Blocker
	}

	in := convergeInput(now, executor.ModeGitHub, executablePlan())
	if b := derive(in); b == nil || b.Reason != BlockerReasonModeMismatch || b.Kind != BlockerKindExecution {
		t.Fatalf("github mode with a converge plan: %+v", b)
	}
	in = convergeInput(now, executor.ModeController, nil)
	if b := derive(in); b == nil || b.Reason != BlockerReasonModeMismatch {
		t.Fatalf("controller mode with a non-converge desired: %+v", b)
	}

	refused := executablePlan()
	refused.Refused, refused.Approved = true, true
	in = convergeInput(now, executor.ModeController, refused)
	if b := derive(in); b == nil || b.Reason != BlockerReasonDestructive {
		t.Fatalf("refused plan: %+v", b)
	}
	if got := DeriveStatus(in).NextAction; got == NextActionApproveConverge {
		t.Fatalf("a refused plan must not ask for approval")
	}

	for trigger, want := range map[string]string{TriggerHistoryRewritten: BlockerReasonHistoryRewritten, TriggerUnverified: BlockerReasonTriggerUnverified, TriggerPending: BlockerReasonTriggerPending} {
		in = convergeInput(now, executor.ModeController, nil)
		in.Resource.Desired = DesiredRevision{}
		in.Converge.Trigger = trigger
		b := derive(in)
		if b == nil || b.Reason != want || b.Kind != BlockerKindTrigger {
			t.Fatalf("trigger %s: %+v", trigger, b)
		}
	}
	// The watcher's verdict is meaningless in github mode.
	in = convergeInput(now, executor.ModeGitHub, nil)
	in.Resource.Desired = DesiredRevision{}
	in.Converge.Trigger = TriggerUnverified
	if b := derive(in); b != nil {
		t.Fatalf("github mode trigger blocker: %+v", b)
	}
	// Nil facts derive nothing (WP11 behaviour).
	in = baseInput(now)
	if b := DeriveConverge(in.Resource, DeriveStatus(in), nil).Blocker; b != nil {
		t.Fatalf("nil facts blocker: %+v", b)
	}
}

func TestDeriveResumeApproved(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	held := func(resume bool) Input {
		in := convergeInput(now, executor.ModeController, executablePlan())
		in.Fence = heldFence()
		in.Holder = lifecycle.HolderFacts{DispatchState: "bound", Attempts: []fleet.RunnerAttempt{
			{ID: "attempt-1", PlanID: "plan-1", Attempt: 1, Status: "running", HeartbeatExpiresAt: now.Add(-time.Hour)},
		}}
		in.Converge.ResumeApproved = resume
		return in
	}
	if s := DeriveStatus(held(true)); s.Active == nil || !s.Active.ResumeApproved {
		t.Fatalf("Active = %+v, want resumeApproved", s.Active)
	}
	if s := DeriveStatus(held(false)); s.Active == nil || s.Active.ResumeApproved {
		t.Fatalf("Active = %+v, want resumeApproved false", s.Active)
	}
	// Free fence: there is nothing to resume, whatever the facts say.
	in := convergeInput(now, executor.ModeController, executablePlan())
	in.Converge.ResumeApproved = true
	if s := DeriveStatus(in); s.Active != nil {
		t.Fatalf("free fence Active = %+v", s.Active)
	}
	// Resume does not hide the uncertain outcome.
	if got := DeriveStatus(held(true)).NextAction; got != NextActionResolveUncertainOutcome {
		t.Fatalf("NextAction = %s", got)
	}
}

func TestDeriveWithoutConvergeFactsIsUnchanged(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := baseInput(now)
	a := DeriveStatus(in)
	in.Converge = &ConvergeFacts{}
	in.Resource.ExecutionMode = executor.ModeController
	b := DeriveStatus(in)
	if !StatusEquivalent(a, b) {
		t.Fatalf("empty converge facts changed the status:\n%+v\n%+v", a, b)
	}
}

func TestValidateDesiredAcceptsProtectedMain(t *testing.T) {
	t.Parallel()
	d := DesiredRevision{Verification: VerificationGitHubProtectedMain, PlanID: "p", Repository: "o/r", CommitSHA: convergeCommit, AcceptedBy: "w", AcceptedAt: time.Now()}
	if err := ValidateDesired(d); err != nil {
		t.Fatal(err)
	}
	d.PlanID = ""
	if ValidateDesired(d) == nil {
		t.Fatal("protected-main desired without a plan must be refused")
	}
}
