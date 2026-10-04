package fleettest

import (
	"net/http"
	"testing"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
)

// GitHubOutcome tells a FenceHarness's Dispatch call what the backend's fake
// GitHub client should report for that submission, so the suite can drive
// both the normal path and the ambiguous-outcome path (plan.md §2.2's
// "etcd race loser" note and the M6/H4 pre-existing defect) through the same
// real submit route.
type GitHubOutcome string

const (
	// GitHubOutcomeSubmitted binds a run: the submission reaches
	// "dispatched" (PG) / "bound" (etcd) with a real run ID.
	GitHubOutcomeSubmitted GitHubOutcome = "submitted"
	// GitHubOutcomeAmbiguous leaves the submission fenced (PG
	// "submitting", or etcd's unbound preparation) with no bound run,
	// exactly the outcome the submit route already reports as 409
	// fleet_github_dispatch_ambiguous when GitHub's result is unknown.
	GitHubOutcomeAmbiguous GitHubOutcome = "ambiguous"
)

// TargetResp is the HTTP-boundary result of a target registration call.
// TargetID is populated whenever registration succeeds (new or idempotent
// replay); it is empty on any refusal.
type TargetResp struct {
	HTTPStatus int
	Code       string
	TargetID   string
}

// FenceHarness extends LifecycleHarness (WP6) with the target, fence and
// authority-epoch operations RunFleetFenceConformance needs, so every case
// still drives one backend's real HTTP routes rather than its storage
// directly. Only AgeHolder, FenceFacts and SetRunTerminal may touch storage
// or fakes directly. Implementations land in WP8a (PG legacy) and WP8b
// (etcd); WP7 only stubs them (fleet_conformance_{pg,etcd}_test.go).
type FenceHarness interface {
	LifecycleHarness

	// RegisterTarget calls the signed registration route for identity and
	// aliases ("cluster:<name>" / "environment:<lane>", plan.md §2.2).
	RegisterTarget(identity lifecycle.TargetIdentity, aliases []string) TargetResp

	// SeedPlanOnCluster seeds a capacity plan exactly like SeedPlan, except
	// the plan's cluster and (if non-empty) its dispatch's FleetEnvironment
	// lane are cluster and environment rather than the fixed defaults
	// SeedPlan uses.
	SeedPlanOnCluster(action, cluster, environment string) PlanRef

	// Dispatch sets plan's StartedAt to startedAt (a direct storage write,
	// so revalidation (M4) is exercised without a real wait; startedAt may
	// be in the future), then calls the real dispatch submit route for plan
	// (the fence-acquire transition, plan.md §2.2's "Acquire" row). outcome
	// configures the backend's fake GitHub client. It returns the submit
	// call's own result plus the bound dispatch, which is zero-valued unless
	// the call bound a run. Calling it again for an already-dispatched plan
	// is the dispatch re-POST / finish path abandonment must refuse.
	Dispatch(plan PlanRef, startedAt time.Time, outcome GitHubOutcome) (Resp, DispatchRef)

	// Epoch returns the current authority epoch.
	Epoch() int64
	// AdvanceEpoch CASes the authority epoch forward from expected.
	AdvanceEpoch(expected int64, reason string) (next int64, err error)

	// Release calls the signed fence-release route (plan.md §2.5) for
	// targetID with mode (terminal or abandon only; succeeded and
	// dispatch_not_submitted are side effects of other routes) and
	// expectedGeneration. The request carries no proof: as in production,
	// the backend gathers lifecycle.TerminalProof itself from its WP5
	// observers (configured by SetRunTerminal) and, for abandon, takes the
	// run-listing snapshot from its fake GitHub client, which must always
	// return a listing (possibly "absent"). A refusal's Code is the
	// lifecycle.FenceError code DecideRelease (or another Decide*) returned.
	Release(targetID string, mode lifecycle.ReleaseMode, expectedGeneration int64) Resp

	// AbandonPlan calls the signed, admin-only break-glass abandon keyed by
	// plan rather than target (H7), which works whether or not plan's
	// cluster is registered. It applies the same lifecycle.AbandonMinimumAge
	// and snapshot rules as Release in abandon mode and, like Release, does
	// nothing to satisfy the minimum age itself: the suite calls AgeHolder.
	AbandonPlan(plan PlanRef) Resp

	// AgeHolder makes plan look silent for at least by, so abandon's
	// lifecycle.AbandonMinimumAge can be satisfied without waiting. It
	// subtracts by from every timestamp DecideRelease's abandon baseline
	// reads for plan, by direct storage write: the dispatch's
	// SubmissionStartedAt and created-at (PG) or the preparation's and
	// binding's CreatedAt (etcd), and every one of plan's attempts'
	// HeartbeatExpiresAt (which also expires any live attempt). It must
	// never sleep, and must touch nothing else (in particular not the
	// plan's StartedAt or any fence row).
	AgeHolder(plan PlanRef, by time.Duration)

	// Occupancy reads targetID's derived occupancy and reason through the
	// target read route (plan.md §2.2's M3; display only, never admission).
	Occupancy(targetID string) (occupancy lifecycle.Occupancy, reason string, err error)
	// FenceFacts reads targetID's fence row directly, so cases can assert
	// generation, holder, epoch and release reason exactly.
	FenceFacts(targetID string) lifecycle.FenceFacts

	// SetRunTerminal configures the backend's fake GitHub observers so run
	// is (or is not) reported completed by ObserveApplyRunByNonceHash (an
	// "apply" run) or ObserveRecoverRun (a "recover" run) (WP5). A run never
	// passed to SetRunTerminal is reported not completed.
	SetRunTerminal(run RunRef, completed bool)
}

// RunFleetFenceConformance runs the WP7 fence conformance cases. Every case
// calls newHarness(t) for its own harness, which must sit on a fresh,
// isolated backend bound to t's lifetime (a new integrationtest.PG schema or
// integrationtest.Etcd prefix): an empty target registry, no plans, and the
// initial authority epoch 1. No case depends on another or on their order.
// The returned value is the same harness type RunFleetLifecycleConformance
// takes, so one implementation serves both suites.
func RunFleetFenceConformance(t *testing.T, newHarness func(t *testing.T) FenceHarness) {
	cases := []struct {
		name string
		run  func(*testing.T, FenceHarness)
	}{
		{"EmptyRegistryPreservesLegacyBehavior", testEmptyRegistryPreservesLegacyBehavior},
		{"SecondPlanBlockedWhileHeld", testSecondPlanBlockedWhileHeld},
		{"AliasDoesNotBypass", testAliasDoesNotBypass},
		{"UnregisteredClusterRefusedWhenRegistryNonEmpty", testUnregisteredClusterRefusedWhenRegistryNonEmpty},
		{"RegisterWhileInFlightRefused", testRegisterWhileInFlightRefused},
		{"CompleteReleasesFence", testCompleteReleasesFence},
		{"ExpiryDoesNotRelease", testExpiryDoesNotRelease},
		{"NoAttemptAfterDispatchIsUncertain", testNoAttemptAfterDispatchIsUncertain},
		{"DispatchAmbiguousKeepsOccupiedThenAbandonResolves", testDispatchAmbiguousKeepsOccupiedThenAbandonResolves},
		{"AbandonedPlanCannotStartRecoverOrFinish", testAbandonedPlanCannotStartRecoverOrFinish},
		{"TerminalReleaseRequiresEveryRunTerminal", testTerminalReleaseRequiresEveryRunTerminal},
		{"RecoverySamePlanKeepsFence", testRecoverySamePlanKeepsFence},
		{"EpochAdvanceRefusesHeartbeatAdvanceSuccessCheckpoint", testEpochAdvanceRefusesHeartbeatAdvanceSuccessCheckpoint},
		{"FailedCheckpointAndCancelAllowedAfterEpochAdvance", testFailedCheckpointAndCancelAllowedAfterEpochAdvance},
		{"EpochAdvanceBeforeFirstAttemptRebinds", testEpochAdvanceBeforeFirstAttemptRebinds},
		{"RevalidationAfterAnyRelease", testRevalidationAfterAnyRelease},
		{"RecoveryRefusedWhileSourceRunNotTerminal", testRecoveryRefusedWhileSourceRunNotTerminal},
		{"AbandonUnregisteredPlanUnblocksRegistration", testAbandonUnregisteredPlanUnblocksRegistration},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			if epoch := h.Epoch(); epoch != 1 {
				t.Fatalf("newHarness must return a fresh backend at authority epoch 1, got %d", epoch)
			}
			c.run(t, h)
		})
	}
}

// abandonAge is comfortably past lifecycle.AbandonMinimumAge.
const abandonAge = lifecycle.AbandonMinimumAge + time.Minute

func fenceTargetIdentity(name string) lifecycle.TargetIdentity {
	return lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: name, StateBackend: "s3://fence-conformance-" + name + "/state"}
}

func registerTarget(t *testing.T, h FenceHarness, name string, aliases ...string) string {
	t.Helper()
	reg := h.RegisterTarget(fenceTargetIdentity(name), aliases)
	if reg.HTTPStatus != http.StatusCreated || reg.TargetID == "" {
		t.Fatalf("register target %s: status=%d code=%q", name, reg.HTTPStatus, reg.Code)
	}
	return reg.TargetID
}

// dispatchBound submits plan with a bound GitHub run and returns the binding.
func dispatchBound(t *testing.T, h FenceHarness, plan PlanRef, startedAt time.Time) DispatchRef {
	t.Helper()
	resp, dispatch := h.Dispatch(plan, startedAt, GitHubOutcomeSubmitted)
	if resp.HTTPStatus >= http.StatusBadRequest || dispatch.RunID == 0 {
		t.Fatalf("dispatch submit for plan %s: status=%d code=%q runID=%d", plan.ID, resp.HTTPStatus, resp.Code, dispatch.RunID)
	}
	return dispatch
}

func dispatchAmbiguous(t *testing.T, h FenceHarness, plan PlanRef) {
	t.Helper()
	resp, _ := h.Dispatch(plan, time.Now().UTC(), GitHubOutcomeAmbiguous)
	requireRejected(t, resp, http.StatusConflict, "fleet_github_dispatch_ambiguous")
}

// boundAttempt registers, dispatches and starts the first attempt of a plan
// on cluster.
func boundAttempt(t *testing.T, h FenceHarness, cluster string) (PlanRef, DispatchRef, *fleet.RunnerAttempt) {
	t.Helper()
	plan := h.SeedPlanOnCluster("scale", cluster, "")
	dispatch := dispatchBound(t, h, plan, time.Now().UTC())
	attempt := requireAttempt(t, h.Start(plan, runRefFor(dispatch, "apply")), http.StatusCreated)
	return plan, dispatch, attempt
}

func runRefFor(dispatch DispatchRef, intent string) RunRef {
	return RunRef{RunID: dispatch.RunID, RunAttempt: dispatch.RunAttempt, Intent: intent, SHA: dispatch.ApprovedHeadSHA}
}

func recoverRunFor(dispatch DispatchRef) RunRef {
	return RunRef{RunID: dispatch.RunID + 1, RunAttempt: 1, Intent: "recover", SHA: dispatch.ApprovedHeadSHA}
}

func requireHeldBy(t *testing.T, h FenceHarness, targetID, planID string) lifecycle.FenceFacts {
	t.Helper()
	facts := h.FenceFacts(targetID)
	if !facts.Held || facts.HolderPlanID != planID {
		t.Fatalf("fence %s must be held by plan %s: %+v", targetID, planID, facts)
	}
	return facts
}

func requireUnchangedFence(t *testing.T, h FenceHarness, targetID string, before lifecycle.FenceFacts) {
	t.Helper()
	after := h.FenceFacts(targetID)
	if after.Held != before.Held || after.HolderPlanID != before.HolderPlanID || after.Generation != before.Generation || after.AuthorityEpoch != before.AuthorityEpoch || after.Revision != before.Revision {
		t.Fatalf("a refused call must not change the fence: before=%+v after=%+v", before, after)
	}
}

func requireReleased(t *testing.T, h FenceHarness, targetID, planID, reason string) lifecycle.FenceFacts {
	t.Helper()
	facts := h.FenceFacts(targetID)
	if facts.Held || facts.HolderPlanID != "" || facts.LastRelease == nil || facts.LastRelease.PlanID != planID || facts.LastRelease.Reason != reason {
		t.Fatalf("fence %s must be free with last_release={%s,%s}: %+v", targetID, planID, reason, facts)
	}
	return facts
}

func requireOccupancy(t *testing.T, h FenceHarness, targetID string, want lifecycle.Occupancy, wantReason string) {
	t.Helper()
	occ, reason, err := h.Occupancy(targetID)
	if err != nil {
		t.Fatal(err)
	}
	if occ != want || reason != wantReason {
		t.Fatalf("occupancy of %s = %q/%q, want %q/%q", targetID, occ, reason, want, wantReason)
	}
}

// afterSkew is a plan StartedAt that clears revalidation for facts'
// LastRelease, so a later refusal can only come from abandonment.
func afterSkew(facts lifecycle.FenceFacts) time.Time {
	return facts.LastRelease.At.Add(lifecycle.RevalidationSkew + time.Minute)
}

// testEmptyRegistryPreservesLegacyBehavior: with no target registered, the
// ordinary start path and the dispatch route on any cluster behave exactly
// as before the fence domain existed (Q1/M1).
func testEmptyRegistryPreservesLegacyBehavior(t *testing.T, h FenceHarness) {
	plan, _, run := seedApplyRun(h, "scale")
	requireAttempt(t, h.Start(plan, run), http.StatusCreated)

	routed := h.SeedPlanOnCluster("scale", "fence-unregistered", "")
	dispatch := dispatchBound(t, h, routed, time.Now().UTC())
	requireAttempt(t, h.Start(routed, runRefFor(dispatch, "apply")), http.StatusCreated)
}

// testSecondPlanBlockedWhileHeld covers "two distinct plans, including root
// aliases": a second plan on the same cluster name, and a third on a second
// cluster alias (a renamed root) of the same target, are both refused.
func testSecondPlanBlockedWhileHeld(t *testing.T, h FenceHarness) {
	target := registerTarget(t, h, "held", "cluster:fence-held", "cluster:fence-held-renamed")
	planA := h.SeedPlanOnCluster("scale", "fence-held", "")
	dispatchBound(t, h, planA, time.Now().UTC())
	before := requireHeldBy(t, h, target, planA.ID)
	if before.Generation != 1 || before.AuthorityEpoch != 1 {
		t.Fatalf("first acquire must be generation 1 at epoch 1: %+v", before)
	}

	planB := h.SeedPlanOnCluster("scale", "fence-held", "")
	second, _ := h.Dispatch(planB, time.Now().UTC(), GitHubOutcomeSubmitted)
	requireRejected(t, second, http.StatusConflict, lifecycle.CodeFleetTargetExecutionOccupied)
	planC := h.SeedPlanOnCluster("scale", "fence-held-renamed", "")
	third, _ := h.Dispatch(planC, time.Now().UTC(), GitHubOutcomeSubmitted)
	requireRejected(t, third, http.StatusConflict, lifecycle.CodeFleetTargetExecutionOccupied)
	requireUnchangedFence(t, h, target, before)
}

// testAliasDoesNotBypass registers X on cluster:A and Y on cluster:B plus
// environment:E. A plan whose cluster is A but whose dispatch lane is E is
// refused as an alias conflict, and neither fence is touched.
func testAliasDoesNotBypass(t *testing.T, h FenceHarness) {
	targetX := registerTarget(t, h, "bypass-x", "cluster:fence-bypass-a")
	targetY := registerTarget(t, h, "bypass-y", "cluster:fence-bypass-b", "environment:fence-bypass-env")
	plan := h.SeedPlanOnCluster("scale", "fence-bypass-a", "fence-bypass-env")
	resp, _ := h.Dispatch(plan, time.Now().UTC(), GitHubOutcomeSubmitted)
	requireRejected(t, resp, http.StatusConflict, lifecycle.CodeFleetTargetAliasConflict)
	if x, y := h.FenceFacts(targetX), h.FenceFacts(targetY); x.Held || y.Held {
		t.Fatalf("an alias-conflict refusal must not acquire either fence: X=%+v Y=%+v", x, y)
	}
}

func testUnregisteredClusterRefusedWhenRegistryNonEmpty(t *testing.T, h FenceHarness) {
	target := registerTarget(t, h, "nonempty", "cluster:fence-nonempty")
	plan := h.SeedPlanOnCluster("scale", "fence-unregistered", "")
	resp, _ := h.Dispatch(plan, time.Now().UTC(), GitHubOutcomeSubmitted)
	requireRejected(t, resp, http.StatusConflict, lifecycle.CodeFleetTargetUnregistered)
	if facts := h.FenceFacts(target); facts.Held {
		t.Fatalf("a refused unregistered plan must not touch another target's fence: %+v", facts)
	}
}

func testRegisterWhileInFlightRefused(t *testing.T, h FenceHarness) {
	plan := h.SeedPlanOnCluster("scale", "fence-inflight", "")
	dispatchAmbiguous(t, h, plan)
	reg := h.RegisterTarget(fenceTargetIdentity("inflight"), []string{"cluster:fence-inflight"})
	if reg.HTTPStatus != http.StatusConflict || reg.Code != lifecycle.CodeFleetTargetRegistrationInFlight || reg.TargetID != "" {
		t.Fatalf("registering a cluster with an in-flight plan must refuse as registration-in-flight with no target ID: %+v", reg)
	}
}

func testCompleteReleasesFence(t *testing.T, h FenceHarness) {
	target := registerTarget(t, h, "complete", "cluster:fence-complete")
	plan, _, attempt := boundAttempt(t, h, "fence-complete")
	held := requireHeldBy(t, h, target, plan.ID)
	if held.Generation != 1 {
		t.Fatalf("a same-epoch first bind must keep acquire's generation 1: %+v", held)
	}
	requireOccupancy(t, h, target, lifecycle.OccupancyActive, "")
	if final := walkToTerminal(t, h, plan, attempt); final.Status != "succeeded" {
		t.Fatalf("walk did not reach succeeded: %+v", final)
	}
	released := requireReleased(t, h, target, plan.ID, "succeeded")
	if released.Generation != held.Generation {
		t.Fatalf("release must not bump generation: before=%d after=%d", held.Generation, released.Generation)
	}
	requireOccupancy(t, h, target, lifecycle.OccupancyFree, "")

	again := h.Release(target, lifecycle.ReleaseModeTerminal, released.Generation)
	requireRejected(t, again, http.StatusConflict, lifecycle.CodeFleetTargetFenceNotHeld)
	requireUnchangedFence(t, h, target, released)
}

// testExpiryDoesNotRelease covers "provider mutation succeeds, checkpoint
// lost": the executor stops reporting after a mutation, its lease expires,
// and the target must stay held and keep refusing a second plan.
func testExpiryDoesNotRelease(t *testing.T, h FenceHarness) {
	target := registerTarget(t, h, "expiry", "cluster:fence-expiry")
	plan, _, attempt := boundAttempt(t, h, "fence-expiry")
	before := requireHeldBy(t, h, target, plan.ID)
	h.ExpireAttempt(attempt)
	requireOccupancy(t, h, target, lifecycle.OccupancyUncertain, lifecycle.ReasonAttemptTerminalWithoutSuccess)

	second := h.SeedPlanOnCluster("scale", "fence-expiry", "")
	resp, _ := h.Dispatch(second, time.Now().UTC(), GitHubOutcomeSubmitted)
	requireRejected(t, resp, http.StatusConflict, lifecycle.CodeFleetTargetExecutionOccupied)
	after := requireHeldBy(t, h, target, plan.ID)
	if after.Generation != before.Generation || after.LastRelease != nil {
		t.Fatalf("expiry must not release the fence: before=%+v after=%+v", before, after)
	}
}

func testNoAttemptAfterDispatchIsUncertain(t *testing.T, h FenceHarness) {
	target := registerTarget(t, h, "noattempt", "cluster:fence-noattempt")
	plan := h.SeedPlanOnCluster("scale", "fence-noattempt", "")
	dispatchBound(t, h, plan, time.Now().UTC())
	requireHeldBy(t, h, target, plan.ID)
	requireOccupancy(t, h, target, lifecycle.OccupancyUncertain, lifecycle.ReasonNoLiveAttempt)
}

// testDispatchAmbiguousKeepsOccupiedThenAbandonResolves covers B1 and the
// "GitHub dispatch outcome is ambiguous" matrix row.
func testDispatchAmbiguousKeepsOccupiedThenAbandonResolves(t *testing.T, h FenceHarness) {
	target := registerTarget(t, h, "ambiguous", "cluster:fence-ambiguous")
	plan := h.SeedPlanOnCluster("scale", "fence-ambiguous", "")
	dispatchAmbiguous(t, h, plan)
	held := requireHeldBy(t, h, target, plan.ID)
	requireOccupancy(t, h, target, lifecycle.OccupancyUncertain, lifecycle.ReasonDispatchSubmissionUnresolved)

	tooSoon := h.Release(target, lifecycle.ReleaseModeAbandon, held.Generation)
	requireRejected(t, tooSoon, http.StatusConflict, lifecycle.CodeFleetTargetAbandonTooSoon)
	requireUnchangedFence(t, h, target, held)

	h.AgeHolder(plan, abandonAge)
	if resp := h.Release(target, lifecycle.ReleaseModeAbandon, held.Generation); resp.HTTPStatus >= http.StatusBadRequest {
		t.Fatalf("abandon past the minimum age was refused: status=%d code=%q", resp.HTTPStatus, resp.Code)
	}
	released := requireReleased(t, h, target, plan.ID, "abandoned")
	requireOccupancy(t, h, target, lifecycle.OccupancyFree, "")

	// A GitHub result that finally arrives cannot finish the abandoned
	// dispatch (FinishFleetGitHubDispatch refuses abandoned plans).
	late, _ := h.Dispatch(plan, afterSkew(released), GitHubOutcomeSubmitted)
	requireRejected(t, late, http.StatusConflict, lifecycle.CodeFleetTargetHolderAbandoned)
	requireUnchangedFence(t, h, target, released)
}

// testAbandonedPlanCannotStartRecoverOrFinish: plan B (bound, no attempt
// yet) is abandoned, then refused a first attempt and a dispatch re-POST;
// plan R (one canceled attempt) is abandoned, then refused recovery.
func testAbandonedPlanCannotStartRecoverOrFinish(t *testing.T, h FenceHarness) {
	targetB := registerTarget(t, h, "abandoned-b", "cluster:fence-abandoned-b")
	planB := h.SeedPlanOnCluster("scale", "fence-abandoned-b", "")
	dispatchB := dispatchBound(t, h, planB, time.Now().UTC())
	heldB := requireHeldBy(t, h, targetB, planB.ID)
	h.AgeHolder(planB, abandonAge)
	if resp := h.Release(targetB, lifecycle.ReleaseModeAbandon, heldB.Generation); resp.HTTPStatus >= http.StatusBadRequest {
		t.Fatalf("abandon of plan B was refused: status=%d code=%q", resp.HTTPStatus, resp.Code)
	}
	releasedB := requireReleased(t, h, targetB, planB.ID, "abandoned")
	requireRejected(t, h.Start(planB, runRefFor(dispatchB, "apply")), http.StatusConflict, lifecycle.CodeFleetTargetHolderAbandoned)
	finish, _ := h.Dispatch(planB, afterSkew(releasedB), GitHubOutcomeSubmitted)
	requireRejected(t, finish, http.StatusConflict, lifecycle.CodeFleetTargetHolderAbandoned)
	requireUnchangedFence(t, h, targetB, releasedB)

	targetR := registerTarget(t, h, "abandoned-r", "cluster:fence-abandoned-r")
	planR, dispatchR, root := boundAttempt(t, h, "fence-abandoned-r")
	requireAttempt(t, h.Cancel(root, root.Revision, "abandon fixture"), http.StatusOK)
	h.SetRunTerminal(runRefFor(dispatchR, "apply"), true)
	heldR := requireHeldBy(t, h, targetR, planR.ID)
	h.AgeHolder(planR, abandonAge)
	if resp := h.Release(targetR, lifecycle.ReleaseModeAbandon, heldR.Generation); resp.HTTPStatus >= http.StatusBadRequest {
		t.Fatalf("abandon of plan R was refused: status=%d code=%q", resp.HTTPStatus, resp.Code)
	}
	releasedR := requireReleased(t, h, targetR, planR.ID, "abandoned")
	stop := stopEvidenceFor(root.ID, dispatchR.RunID, int64(dispatchR.RunAttempt))
	requireRejected(t, h.Recover(planR, recoverRunFor(dispatchR), stop), http.StatusConflict, lifecycle.CodeFleetTargetHolderAbandoned)
	requireUnchangedFence(t, h, targetR, releasedR)
}

// testTerminalReleaseRequiresEveryRunTerminal covers B2: terminal release
// needs no live attempt, the bound apply run proven completed, and every
// run that ever hosted an attempt (here a recover run) proven completed. It
// then permanently abandons the holder plan (m16).
func testTerminalReleaseRequiresEveryRunTerminal(t *testing.T, h FenceHarness) {
	target := registerTarget(t, h, "terminal", "cluster:fence-terminal")
	plan, dispatch, root := boundAttempt(t, h, "fence-terminal")
	applyRun, recoverRun := runRefFor(dispatch, "apply"), recoverRunFor(dispatch)
	held := requireHeldBy(t, h, target, plan.ID)

	live := h.Release(target, lifecycle.ReleaseModeTerminal, held.Generation)
	requireRejected(t, live, http.StatusConflict, lifecycle.CodeFleetTargetHasLiveAttempt)
	requireUnchangedFence(t, h, target, held)

	h.SetRunTerminal(applyRun, true)
	stop := stopEvidenceFor(root.ID, dispatch.RunID, int64(dispatch.RunAttempt))
	recovered := requireAttempt(t, h.Recover(plan, recoverRun, stop), http.StatusCreated)
	requireAttempt(t, h.Cancel(recovered, recovered.Revision, "terminal release fixture"), http.StatusOK)
	held = requireHeldBy(t, h, target, plan.ID)

	recoverUnproven := h.Release(target, lifecycle.ReleaseModeTerminal, held.Generation)
	requireRejected(t, recoverUnproven, http.StatusConflict, lifecycle.CodeFleetTargetTerminalProofIncomplete)
	requireUnchangedFence(t, h, target, held)

	h.SetRunTerminal(recoverRun, true)
	h.SetRunTerminal(applyRun, false)
	applyUnproven := h.Release(target, lifecycle.ReleaseModeTerminal, held.Generation)
	requireRejected(t, applyUnproven, http.StatusConflict, lifecycle.CodeFleetTargetTerminalProofIncomplete)
	requireUnchangedFence(t, h, target, held)

	h.SetRunTerminal(applyRun, true)
	if resp := h.Release(target, lifecycle.ReleaseModeTerminal, held.Generation); resp.HTTPStatus >= http.StatusBadRequest {
		t.Fatalf("terminal release with every run proven completed was refused: status=%d code=%q", resp.HTTPStatus, resp.Code)
	}
	released := requireReleased(t, h, target, plan.ID, "released_terminal")
	if released.Generation != held.Generation {
		t.Fatalf("release must not bump generation: before=%d after=%d", held.Generation, released.Generation)
	}
	requireOccupancy(t, h, target, lifecycle.OccupancyFree, "")
	again, _ := h.Dispatch(plan, afterSkew(released), GitHubOutcomeSubmitted)
	requireRejected(t, again, http.StatusConflict, lifecycle.CodeFleetTargetHolderAbandoned)
}

// testRecoverySamePlanKeepsFence: a same-epoch recovery bind (DecideBind)
// keeps the holder, generation and epoch.
func testRecoverySamePlanKeepsFence(t *testing.T, h FenceHarness) {
	target := registerTarget(t, h, "recover", "cluster:fence-recover")
	plan, dispatch, root := boundAttempt(t, h, "fence-recover")
	before := requireHeldBy(t, h, target, plan.ID)
	// Q11: on PG legacy a registered target's recovery needs the source run
	// proven completed; etcd ignores this and needs the stop evidence below.
	h.SetRunTerminal(runRefFor(dispatch, "apply"), true)
	stop := stopEvidenceFor(root.ID, dispatch.RunID, int64(dispatch.RunAttempt))
	recovered := requireAttempt(t, h.Recover(plan, recoverRunFor(dispatch), stop), http.StatusCreated)
	if recovered.RetryOf != root.ID {
		t.Fatalf("recovery did not chain off the source attempt: %+v", recovered)
	}
	after := requireHeldBy(t, h, target, plan.ID)
	if after.Generation != before.Generation || after.AuthorityEpoch != before.AuthorityEpoch {
		t.Fatalf("a same-epoch recovery bind must keep generation and epoch: before=%+v after=%+v", before, after)
	}
	requireOccupancy(t, h, target, lifecycle.OccupancyActive, "")
}

// testEpochAdvanceRefusesHeartbeatAdvanceSuccessCheckpoint (Q10, "authority
// restore invalidates old ownership"). Attempt A already holds its current
// phase's succeeded checkpoint, so only the epoch can refuse its advance;
// attempt B has none, so its succeeded checkpoint is a new identity rather
// than a replay.
func testEpochAdvanceRefusesHeartbeatAdvanceSuccessCheckpoint(t *testing.T, h FenceHarness) {
	targetA := registerTarget(t, h, "epoch-refuse-a", "cluster:fence-epoch-refuse-a")
	registerTarget(t, h, "epoch-refuse-b", "cluster:fence-epoch-refuse-b")
	planA, _, attemptA := boundAttempt(t, h, "fence-epoch-refuse-a")
	_, _, attemptB := boundAttempt(t, h, "fence-epoch-refuse-b")
	if resp := h.Checkpoint(attemptA, attemptA.CurrentPhase, "succeeded"); resp.HTTPStatus != http.StatusCreated {
		t.Fatalf("fixture checkpoint before the epoch advance was refused: status=%d code=%q", resp.HTTPStatus, resp.Code)
	}

	if _, err := h.AdvanceEpoch(1, "fence-conformance-advance"); err != nil {
		t.Fatalf("advancing the epoch failed: %v", err)
	}

	requireRejected(t, h.Heartbeat(attemptA, attemptA.HeartbeatSequence+1, attemptA.Revision), http.StatusConflict, lifecycle.CodeFleetTargetAuthoritySuperseded)
	requireRejected(t, h.Advance(attemptA, attemptA.CurrentPhase, attemptA.Revision), http.StatusConflict, lifecycle.CodeFleetTargetAuthoritySuperseded)
	requireRejected(t, h.Checkpoint(attemptB, attemptB.CurrentPhase, "succeeded"), http.StatusConflict, lifecycle.CodeFleetTargetAuthoritySuperseded)

	for _, before := range []*fleet.RunnerAttempt{attemptA, attemptB} {
		after := requireAttempt(t, h.Get(before), http.StatusOK)
		if after.Revision != before.Revision || after.CurrentPhase != before.CurrentPhase || after.HeartbeatSequence != before.HeartbeatSequence {
			t.Fatalf("refused writes under a superseded epoch changed the attempt: before=%+v after=%+v", before, after)
		}
	}
	facts := requireHeldBy(t, h, targetA, planA.ID)
	if facts.AuthorityEpoch != 1 {
		t.Fatalf("the fence keeps its recorded epoch until re-bind or release, got %d", facts.AuthorityEpoch)
	}
	requireOccupancy(t, h, targetA, lifecycle.OccupancyUncertain, lifecycle.ReasonAuthoritySuperseded)
}

// testFailedCheckpointAndCancelAllowedAfterEpochAdvance (Q10) uses separate
// attempts so a failed checkpoint's own effect on its attempt cannot mask
// the cancel check.
func testFailedCheckpointAndCancelAllowedAfterEpochAdvance(t *testing.T, h FenceHarness) {
	registerTarget(t, h, "epoch-allow-a", "cluster:fence-epoch-allow-a")
	registerTarget(t, h, "epoch-allow-b", "cluster:fence-epoch-allow-b")
	_, _, attemptA := boundAttempt(t, h, "fence-epoch-allow-a")
	_, _, attemptB := boundAttempt(t, h, "fence-epoch-allow-b")
	if _, err := h.AdvanceEpoch(1, "fence-conformance-advance-allow"); err != nil {
		t.Fatalf("advancing the epoch failed: %v", err)
	}

	if resp := h.Checkpoint(attemptA, attemptA.CurrentPhase, "failed"); resp.HTTPStatus != http.StatusCreated {
		t.Fatalf("a failed checkpoint must stay allowed after an epoch advance: status=%d code=%q", resp.HTTPStatus, resp.Code)
	}
	canceled := requireAttempt(t, h.Cancel(attemptB, attemptB.Revision, "epoch advanced"), http.StatusOK)
	if canceled.Status != "canceled" {
		t.Fatalf("cancel must stay allowed after an epoch advance: %+v", canceled)
	}
}

// testEpochAdvanceBeforeFirstAttemptRebinds covers M13: the epoch advances
// between dispatch (acquire) and the first attempt; the first bind re-binds
// (Generation+1, current epoch) instead of being stranded.
func testEpochAdvanceBeforeFirstAttemptRebinds(t *testing.T, h FenceHarness) {
	target := registerTarget(t, h, "rebind", "cluster:fence-rebind")
	plan := h.SeedPlanOnCluster("scale", "fence-rebind", "")
	dispatch := dispatchBound(t, h, plan, time.Now().UTC())
	before := requireHeldBy(t, h, target, plan.ID)
	newEpoch, err := h.AdvanceEpoch(1, "fence-conformance-rebind")
	if err != nil {
		t.Fatalf("advancing the epoch failed: %v", err)
	}
	requireOccupancy(t, h, target, lifecycle.OccupancyUncertain, lifecycle.ReasonAuthoritySuperseded)

	requireAttempt(t, h.Start(plan, runRefFor(dispatch, "apply")), http.StatusCreated)
	after := requireHeldBy(t, h, target, plan.ID)
	if after.AuthorityEpoch != newEpoch || after.Generation != before.Generation+1 {
		t.Fatalf("first bind across an epoch advance must re-bind to epoch %d at generation %d: %+v", newEpoch, before.Generation+1, after)
	}
	requireOccupancy(t, h, target, lifecycle.OccupancyActive, "")
}

// testRevalidationAfterAnyRelease covers M4/Q2: a succeeded release and a
// non-success (terminal) release both start the revalidation skew.
func testRevalidationAfterAnyRelease(t *testing.T, h FenceHarness) {
	targetS := registerTarget(t, h, "revalidate-s", "cluster:fence-revalidate-s")
	planS, _, attemptS := boundAttempt(t, h, "fence-revalidate-s")
	if final := walkToTerminal(t, h, planS, attemptS); final.Status != "succeeded" {
		t.Fatalf("fixture error: plan did not reach succeeded: %+v", final)
	}
	requireRevalidation(t, h, targetS, "fence-revalidate-s", requireReleased(t, h, targetS, planS.ID, "succeeded"))

	targetT := registerTarget(t, h, "revalidate-t", "cluster:fence-revalidate-t")
	planT, dispatchT, attemptT := boundAttempt(t, h, "fence-revalidate-t")
	requireAttempt(t, h.Cancel(attemptT, attemptT.Revision, "failed apply fixture"), http.StatusOK)
	h.SetRunTerminal(runRefFor(dispatchT, "apply"), true)
	heldT := requireHeldBy(t, h, targetT, planT.ID)
	if resp := h.Release(targetT, lifecycle.ReleaseModeTerminal, heldT.Generation); resp.HTTPStatus >= http.StatusBadRequest {
		t.Fatalf("fixture terminal release was refused: status=%d code=%q", resp.HTTPStatus, resp.Code)
	}
	requireRevalidation(t, h, targetT, "fence-revalidate-t", requireReleased(t, h, targetT, planT.ID, "released_terminal"))
}

func requireRevalidation(t *testing.T, h FenceHarness, targetID, cluster string, released lifecycle.FenceFacts) {
	t.Helper()
	stale := h.SeedPlanOnCluster("scale", cluster, "")
	refused, _ := h.Dispatch(stale, released.LastRelease.At.Add(time.Minute), GitHubOutcomeSubmitted)
	requireRejected(t, refused, http.StatusConflict, lifecycle.CodeFleetPlanRevalidationRequired)
	requireUnchangedFence(t, h, targetID, released)

	fresh := h.SeedPlanOnCluster("scale", cluster, "")
	dispatchBound(t, h, fresh, afterSkew(released))
	requireHeldBy(t, h, targetID, fresh.ID)
}

// testRecoveryRefusedWhileSourceRunNotTerminal covers Q11 (PG legacy,
// registered targets). etcd's D6 stop-proof requirement is unconditional
// and does not change with registration, so it is re-asserted instead.
func testRecoveryRefusedWhileSourceRunNotTerminal(t *testing.T, h FenceHarness) {
	registerTarget(t, h, "q11", "cluster:fence-q11")
	plan, dispatch, root := boundAttempt(t, h, "fence-q11")
	run := runRefFor(dispatch, "apply")
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		h.SetRunTerminal(run, false)
		requireRejected(t, h.Recover(plan, recoverRunFor(dispatch), nil), http.StatusConflict, lifecycle.CodeFleetTargetRecoveryRequiresStoppedSource)
		h.SetRunTerminal(run, true)
		recovered := requireAttempt(t, h.Recover(plan, recoverRunFor(dispatch), nil), http.StatusCreated)
		if recovered.RetryOf != root.ID {
			t.Fatalf("recovery once the source run is proven terminal did not chain off the source: %+v", recovered)
		}
	default:
		requireRejected(t, h.Recover(plan, recoverRunFor(dispatch), nil), http.StatusConflict, "fleet_runner_attempt_external_stop_unproven")
	}
}

// testAbandonUnregisteredPlanUnblocksRegistration covers H7: a failed plan
// (dispatched, attempt not succeeded) on an unregistered cluster blocks
// registration until a signed plan-keyed abandon, after which registration
// succeeds and the abandoned plan can never recover or finish.
func testAbandonUnregisteredPlanUnblocksRegistration(t *testing.T, h FenceHarness) {
	plan, dispatch, root := boundAttempt(t, h, "fence-h7")
	requireAttempt(t, h.Cancel(root, root.Revision, "failed plan fixture"), http.StatusOK)

	blocked := h.RegisterTarget(fenceTargetIdentity("h7"), []string{"cluster:fence-h7"})
	if blocked.HTTPStatus != http.StatusConflict || blocked.Code != lifecycle.CodeFleetTargetRegistrationInFlight {
		t.Fatalf("a failed, non-abandoned plan must keep registration refused as in-flight: %+v", blocked)
	}
	requireRejected(t, h.AbandonPlan(plan), http.StatusConflict, lifecycle.CodeFleetTargetAbandonTooSoon)

	h.AgeHolder(plan, abandonAge)
	if resp := h.AbandonPlan(plan); resp.HTTPStatus >= http.StatusBadRequest {
		t.Fatalf("abandoning a plan on an unregistered cluster must succeed (H7): status=%d code=%q", resp.HTTPStatus, resp.Code)
	}
	target := registerTarget(t, h, "h7", "cluster:fence-h7")

	h.SetRunTerminal(runRefFor(dispatch, "apply"), true)
	stop := stopEvidenceFor(root.ID, dispatch.RunID, int64(dispatch.RunAttempt))
	requireRejected(t, h.Recover(plan, recoverRunFor(dispatch), stop), http.StatusConflict, lifecycle.CodeFleetTargetHolderAbandoned)
	finish, _ := h.Dispatch(plan, time.Now().UTC(), GitHubOutcomeSubmitted)
	requireRejected(t, finish, http.StatusConflict, lifecycle.CodeFleetTargetHolderAbandoned)
	if facts := h.FenceFacts(target); facts.Held {
		t.Fatalf("the abandoned plan must never acquire the newly registered fence: %+v", facts)
	}
}
