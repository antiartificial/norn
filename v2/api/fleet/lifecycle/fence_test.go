package lifecycle

import (
	"testing"
	"time"

	"norn/v2/api/fleet"
)

func heldFence() FenceFacts {
	return FenceFacts{TargetID: "tgt_1", Generation: 1, Held: true, HolderPlanID: "plan-1", HolderNonceSHA256: "nonce-1", AuthorityEpoch: 1, Revision: 1}
}

func TestOutcomeTable(t *testing.T) {
	t.Parallel()
	now := time.Now()

	if occupancy, reason := Outcome(FenceFacts{}, HolderFacts{}, 1, now); occupancy != OccupancyFree || reason != "" {
		t.Fatalf("an unheld fence must be Free, got %s/%s", occupancy, reason)
	}

	live := fleet.RunnerAttempt{Attempt: 1, Status: "running", HeartbeatExpiresAt: now.Add(time.Minute)}
	if occupancy, reason := Outcome(heldFence(), HolderFacts{DispatchState: "dispatched", Attempts: []fleet.RunnerAttempt{live}}, 1, now); occupancy != OccupancyActive || reason != "" {
		t.Fatalf("held + live attempt must be Active, got %s/%s", occupancy, reason)
	}

	if occupancy, reason := Outcome(heldFence(), HolderFacts{DispatchState: "dispatched"}, 1, now); occupancy != OccupancyUncertain || reason != ReasonNoLiveAttempt {
		t.Fatalf("held + no attempt must be Uncertain/NoLiveAttempt, got %s/%s", occupancy, reason)
	}

	if occupancy, reason := Outcome(heldFence(), HolderFacts{DispatchState: "submitting"}, 1, now); occupancy != OccupancyUncertain || reason != ReasonDispatchSubmissionUnresolved {
		t.Fatalf("held + submitting dispatch must be Uncertain/DispatchSubmissionUnresolved, got %s/%s", occupancy, reason)
	}

	expired := fleet.RunnerAttempt{Attempt: 1, Status: "running", HeartbeatExpiresAt: now.Add(-time.Minute)}
	if occupancy, reason := Outcome(heldFence(), HolderFacts{DispatchState: "dispatched", Attempts: []fleet.RunnerAttempt{expired}}, 1, now); occupancy != OccupancyUncertain || reason != ReasonAttemptTerminalWithoutSuccess {
		t.Fatalf("held + expired attempt must be Uncertain/AttemptTerminalWithoutSuccess, got %s/%s", occupancy, reason)
	}

	oldEpoch := heldFence()
	oldEpoch.AuthorityEpoch = 1
	if occupancy, reason := Outcome(oldEpoch, HolderFacts{DispatchState: "dispatched", Attempts: []fleet.RunnerAttempt{live}}, 2, now); occupancy != OccupancyUncertain || reason != ReasonAuthoritySuperseded {
		t.Fatalf("held + old epoch must be Uncertain/AuthoritySuperseded, got %s/%s", occupancy, reason)
	}
}

func TestDecideAcquireRevalidation(t *testing.T) {
	t.Parallel()
	now := time.Now()

	free := FenceFacts{}
	acquired, err := DecideAcquire(free, "plan-1", "nonce-1", now, 1)
	if err != nil || !acquired.Held || acquired.HolderPlanID != "plan-1" || acquired.Generation != 1 {
		t.Fatalf("expected a free fence to acquire cleanly, got %+v, %v", acquired, err)
	}

	if _, err := DecideAcquire(acquired, "plan-2", "nonce-2", now, 1); err == nil {
		t.Fatal("a fence held by another plan must refuse acquire")
	} else if fe, ok := err.(*FenceError); !ok || fe.Code != CodeFleetTargetExecutionOccupied {
		t.Fatalf("expected fleet_target_execution_occupied, got %v", err)
	}

	replay, err := DecideAcquire(acquired, "plan-1", "nonce-1", now, 1)
	if err != nil || replay.Generation != acquired.Generation {
		t.Fatalf("a replay by the same plan and nonce must not bump generation, got %+v, %v", replay, err)
	}

	for _, reason := range []string{"succeeded", "released_terminal", "abandoned"} {
		released := FenceFacts{LastRelease: &Release{PlanID: "plan-1", Reason: reason, At: now}}
		if _, err := DecideAcquire(released, "plan-2", "nonce-2", now.Add(time.Minute), 1); err == nil {
			t.Fatalf("a plan started within the skew of a %q release must be refused revalidation", reason)
		} else if fe, ok := err.(*FenceError); !ok || fe.Code != CodeFleetPlanRevalidationRequired {
			t.Fatalf("expected fleet_plan_revalidation_required for %q, got %v", reason, err)
		}
		if _, err := DecideAcquire(released, "plan-2", "nonce-2", now.Add(RevalidationSkew+time.Second), 1); err != nil {
			t.Fatalf("a plan started after the skew of a %q release must be admitted, got %v", reason, err)
		}
	}

	// M5: a same-holder acquire (rerun) under a superseded epoch must be
	// refused rather than silently adopting the new epoch.
	if _, err := DecideAcquire(acquired, "plan-1", "nonce-1", now, 2); err == nil {
		t.Fatal("a same-holder acquire under a newer epoch must be refused")
	} else if fe, ok := err.(*FenceError); !ok || fe.Code != CodeFleetTargetAuthoritySuperseded {
		t.Fatalf("expected fleet_target_authority_superseded, got %v", err)
	}

	// dispatch_not_submitted never requires revalidation (it is not a real
	// execution), regardless of timing.
	neverSubmitted := FenceFacts{LastRelease: &Release{PlanID: "plan-1", Reason: "dispatch_not_submitted", At: now}}
	if _, err := DecideAcquire(neverSubmitted, "plan-2", "nonce-2", now, 1); err != nil {
		t.Fatalf("dispatch_not_submitted must never require revalidation, got %v", err)
	}
}

func TestDecideBindRebindsOnNewEpoch(t *testing.T) {
	t.Parallel()

	if _, err := DecideBind(FenceFacts{}, "plan-1", "nonce-1", 1); err == nil {
		t.Fatal("binding an unheld fence must be refused")
	}

	held := heldFence()
	bound, err := DecideBind(held, "plan-1", "nonce-1", held.AuthorityEpoch)
	if err != nil || bound.Generation != held.Generation {
		t.Fatalf("binding at the current epoch must not bump generation, got %+v, %v", bound, err)
	}

	// M13: a first attempt (or recovery) binds even though the epoch
	// advanced after acquire, exactly like recovery does.
	rebound, err := DecideBind(held, "plan-1", "nonce-1", held.AuthorityEpoch+1)
	if err != nil {
		t.Fatalf("bind under a newer epoch must re-bind rather than refuse, got %v", err)
	}
	if rebound.AuthorityEpoch != held.AuthorityEpoch+1 || rebound.Generation != held.Generation+1 {
		t.Fatalf("re-bind must advance to the current epoch and bump generation, got %+v", rebound)
	}

	if _, err := DecideBind(held, "plan-2", "nonce-2", held.AuthorityEpoch); err == nil {
		t.Fatal("binding with a different plan or nonce must be refused")
	}
}

func TestDecideEvidenceWriteUnderOldEpoch(t *testing.T) {
	t.Parallel()
	current := FenceFacts{TargetID: "tgt_1", AuthorityEpoch: 1}
	superseded := FenceFacts{TargetID: "tgt_1", AuthorityEpoch: 1}

	if err := DecideEvidenceWrite(current, "plan-1", 1, EvidenceHeartbeat); err != nil {
		t.Fatalf("evidence at the current epoch must be allowed, got %v", err)
	}
	if err := DecideEvidenceWrite(FenceFacts{}, "plan-1", 2, EvidenceHeartbeat); err != nil {
		t.Fatalf("an unregistered target must behave exactly as before this change, got %v", err)
	}

	for _, kind := range []EvidenceKind{EvidenceHeartbeat, EvidenceAdvance, EvidenceSuccessCheckpoint} {
		if err := DecideEvidenceWrite(superseded, "plan-1", 2, kind); err == nil {
			t.Fatalf("%s under a superseded epoch must be refused", kind)
		} else if fe, ok := err.(*FenceError); !ok || fe.Code != CodeFleetTargetAuthoritySuperseded {
			t.Fatalf("expected fleet_target_authority_superseded for %s, got %v", kind, err)
		}
	}
	for _, kind := range []EvidenceKind{EvidenceFailedCheckpoint, EvidenceCancel} {
		if err := DecideEvidenceWrite(superseded, "plan-1", 2, kind); err != nil {
			t.Fatalf("%s must stay allowed under a superseded epoch (Q10), got %v", kind, err)
		}
	}
}

func TestDecideReleaseAbandonMinimumAge(t *testing.T) {
	t.Parallel()
	now := time.Now()
	held := heldFence()

	if _, err := DecideRelease(FenceFacts{}, HolderFacts{}, ReleaseModeAbandon, nil, now); err != ErrFleetTargetNotHeld {
		t.Fatalf("abandoning an unheld fence must refuse, got %v", err)
	}

	live := HolderFacts{Attempts: []fleet.RunnerAttempt{{Attempt: 1, Status: "running", HeartbeatExpiresAt: now.Add(time.Minute)}}}
	if _, err := DecideRelease(held, live, ReleaseModeAbandon, nil, now); err != ErrFleetTargetHasLiveAttempt {
		t.Fatalf("abandoning with a live attempt must refuse, got %v", err)
	}

	snapshot := &TerminalProof{ListingSnapshotSHA256: "sha-listing"}
	tooSoon := HolderFacts{SubmissionStartedAt: now.Add(-AbandonMinimumAge + time.Minute)}
	if _, err := DecideRelease(held, tooSoon, ReleaseModeAbandon, snapshot, now); err != ErrFleetTargetAbandonTooSoon {
		t.Fatalf("abandoning before the minimum age must refuse, got %v", err)
	}

	oldEnough := HolderFacts{SubmissionStartedAt: now.Add(-AbandonMinimumAge - time.Minute)}
	if _, err := DecideRelease(held, oldEnough, ReleaseModeAbandon, nil, now); err != ErrFleetTargetAbandonSnapshotRequired {
		t.Fatalf("abandoning without a GitHub listing snapshot must refuse, got %v", err)
	}
	released, err := DecideRelease(held, oldEnough, ReleaseModeAbandon, snapshot, now)
	if err != nil {
		t.Fatalf("abandoning after the minimum age must succeed, got %v", err)
	}
	if released.Held || released.LastRelease == nil || released.LastRelease.Reason != "abandoned" {
		t.Fatalf("a successful abandon must free the fence and record reason abandoned, got %+v", released)
	}

	// A later heartbeat extends the baseline even if submission was old.
	recentHeartbeat := HolderFacts{
		SubmissionStartedAt: now.Add(-24 * time.Hour),
		Attempts:            []fleet.RunnerAttempt{{Attempt: 1, Status: "abandoned", HeartbeatExpiresAt: now.Add(-time.Minute)}},
	}
	if _, err := DecideRelease(held, recentHeartbeat, ReleaseModeAbandon, snapshot, now); err != ErrFleetTargetAbandonTooSoon {
		t.Fatalf("a recently expired attempt must extend the abandon baseline, got %v", err)
	}
	// Any attempt's lease extends the baseline, not only the latest one.
	olderAttemptRecent := HolderFacts{
		SubmissionStartedAt: now.Add(-24 * time.Hour),
		Attempts: []fleet.RunnerAttempt{
			{Attempt: 1, Status: "abandoned", HeartbeatExpiresAt: now.Add(-time.Minute)},
			{Attempt: 2, Status: "failed", HeartbeatExpiresAt: now.Add(-2 * time.Hour)},
		},
	}
	if _, err := DecideRelease(held, olderAttemptRecent, ReleaseModeAbandon, snapshot, now); err != ErrFleetTargetAbandonTooSoon {
		t.Fatalf("any attempt's recent lease must extend the abandon baseline, got %v", err)
	}
}

func TestDecideReleaseEvidence(t *testing.T) {
	t.Parallel()
	now := time.Now()
	held := heldFence()
	apply := fleet.RunnerAttempt{Attempt: 1, RunnerAttemptID: "github-actions:o/r:10:1", Status: "failed", HeartbeatExpiresAt: now.Add(-time.Hour)}
	recover := fleet.RunnerAttempt{Attempt: 2, RunnerAttemptID: "github-actions:o/r:11:1", Status: "abandoned", HeartbeatExpiresAt: now.Add(-time.Hour)}
	holder := HolderFacts{DispatchState: "dispatched", Attempts: []fleet.RunnerAttempt{apply, recover}}

	// B2: terminal needs the bound apply run plus every run that hosted an
	// attempt, including a recovery run that is not the bound run.
	for name, proof := range map[string]*TerminalProof{
		"nil":            nil,
		"bound only":     {BoundApplyRunCompleted: true, CompletedRunnerAttemptIDs: map[string]bool{apply.RunnerAttemptID: true}},
		"attempts only":  {CompletedRunnerAttemptIDs: map[string]bool{apply.RunnerAttemptID: true, recover.RunnerAttemptID: true}},
		"recover absent": {BoundApplyRunCompleted: true, CompletedRunnerAttemptIDs: map[string]bool{apply.RunnerAttemptID: true, recover.RunnerAttemptID: false}},
	} {
		if _, err := DecideRelease(held, holder, ReleaseModeTerminal, proof, now); err != ErrFleetTargetTerminalProofRequired {
			t.Fatalf("terminal release with incomplete proof %q must refuse, got %v", name, err)
		}
	}
	complete := &TerminalProof{BoundApplyRunCompleted: true, CompletedRunnerAttemptIDs: map[string]bool{apply.RunnerAttemptID: true, recover.RunnerAttemptID: true}}
	released, err := DecideRelease(held, holder, ReleaseModeTerminal, complete, now)
	if err != nil || released.Held || released.LastRelease == nil || released.LastRelease.Reason != "released_terminal" {
		t.Fatalf("terminal release with complete proof must free with reason released_terminal, got %+v, %v", released, err)
	}

	if _, err := DecideRelease(held, holder, ReleaseModeSucceeded, nil, now); err != ErrFleetTargetReleaseEvidenceMismatch {
		t.Fatalf("succeeded release without a succeeded latest attempt must refuse, got %v", err)
	}
	succeeded := HolderFacts{Attempts: []fleet.RunnerAttempt{{Attempt: 1, Status: "succeeded"}}}
	if released, err := DecideRelease(held, succeeded, ReleaseModeSucceeded, nil, now); err != nil || released.LastRelease.Reason != "succeeded" {
		t.Fatalf("succeeded release must free with reason succeeded, got %+v, %v", released, err)
	}

	if _, err := DecideRelease(held, holder, ReleaseModeDispatchNotSubmitted, nil, now); err != ErrFleetTargetReleaseEvidenceMismatch {
		t.Fatalf("dispatch_not_submitted after an attempt registered must refuse, got %v", err)
	}

	// M4: a not-submitted release must not erase an earlier real release,
	// or a plan started before that real release would skip revalidation.
	prior := heldFence()
	prior.LastRelease = &Release{PlanID: "plan-0", Reason: "released_terminal", At: now.Add(-time.Minute)}
	notSubmitted, err := DecideRelease(prior, HolderFacts{DispatchState: "submitting"}, ReleaseModeDispatchNotSubmitted, nil, now)
	if err != nil || notSubmitted.Held || notSubmitted.LastRelease == nil || *notSubmitted.LastRelease != *prior.LastRelease {
		t.Fatalf("dispatch_not_submitted must free the fence and keep the prior real release, got %+v, %v", notSubmitted, err)
	}
	if _, err := DecideAcquire(notSubmitted, "plan-2", "nonce-2", now.Add(-2*time.Minute), 1); err == nil {
		t.Fatal("a plan started before the prior real release must still require revalidation")
	}
	fresh, err := DecideRelease(heldFence(), HolderFacts{DispatchState: "submitting"}, ReleaseModeDispatchNotSubmitted, nil, now)
	if err != nil || fresh.LastRelease == nil || fresh.LastRelease.Reason != "dispatch_not_submitted" {
		t.Fatalf("dispatch_not_submitted with no prior release must record it, got %+v, %v", fresh, err)
	}
}
