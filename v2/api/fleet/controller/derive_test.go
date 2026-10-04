package controller

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
)

func conditionsByType(status Status) map[string]Condition {
	out := make(map[string]Condition, len(status.Conditions))
	for _, c := range status.Conditions {
		out[c.Type] = c
	}
	return out
}

func mustCondition(t *testing.T, status Status, conditionType string) Condition {
	t.Helper()
	c, ok := conditionsByType(status)[conditionType]
	if !ok {
		t.Fatalf("no %s condition in %+v", conditionType, status.Conditions)
	}
	return c
}

// healthyObservations returns a full set of fresh, all-green observations at
// `now` for the three sources, plus matching watermarks.
func healthyObservations(now time.Time) (map[string]Watermark, map[string]Observation) {
	provider := Observation{Sequence: 1, Source: SourceProvider, ObservedAt: now, ReceivedAt: now,
		Facts: ObservationFacts{factEnrolledNodes: 3.0, factExpectedNodes: 3.0}}
	state := Observation{Sequence: 2, Source: SourceState, ObservedAt: now, ReceivedAt: now,
		Facts: ObservationFacts{factDrift: false}}
	runtime := Observation{Sequence: 3, Source: SourceRuntime, ObservedAt: now, ReceivedAt: now,
		Facts: ObservationFacts{factReady: true, factIngressReady: true}}
	watermarks := map[string]Watermark{
		SourceProvider: {ObservedAt: now, Sequence: provider.Sequence},
		SourceState:    {ObservedAt: now, Sequence: state.Sequence},
		SourceRuntime:  {ObservedAt: now, Sequence: runtime.Sequence},
	}
	latest := map[string]Observation{SourceProvider: provider, SourceState: state, SourceRuntime: runtime}
	return watermarks, latest
}

func baseInput(now time.Time) Input {
	watermarks, latest := healthyObservations(now)
	return Input{
		Resource: Resource{
			SchemaVersion: SchemaVersion, Name: "fleet-1", TargetID: "tgt_1",
			Watermarks: watermarks,
		},
		Fence:              lifecycle.FenceFacts{},
		Holder:             lifecycle.HolderFacts{},
		AuthorityEpoch:     1,
		LatestObservations: latest,
		TiedObservations:   map[string][]Observation{},
		Now:                now,
	}
}

func assertAllGreen(t *testing.T, status Status) {
	t.Helper()
	for _, ct := range []string{ConditionProviderStateKnown, ConditionNodesEnrolled, ConditionRuntimeReady, ConditionIngressReady} {
		if c := mustCondition(t, status, ct); c.Status != StatusTrue {
			t.Fatalf("%s = %s/%s, want True", ct, c.Status, c.Reason)
		}
	}
	if c := mustCondition(t, status, ConditionDriftDetected); c.Status != StatusFalse {
		t.Fatalf("DriftDetected = %s/%s, want False", c.Status, c.Reason)
	}
}

func TestDeriveSuccessfulDeployment(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	commitX := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitY := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	succeededFence := lifecycle.FenceFacts{
		TargetID: "tgt_1", Generation: 2, Held: false, AuthorityEpoch: 1,
		LastRelease: &lifecycle.Release{PlanID: "plan-1", Reason: "succeeded", At: now.Add(-time.Minute)},
	}
	// The fence is free after a succeeded release, so Holder is empty; the
	// released plan's attempts arrive as LastReleaseAttempts.
	released := []fleet.RunnerAttempt{
		{ID: "attempt-1", PlanID: "plan-1", Attempt: 1, Status: "succeeded", CommitSHA: commitX},
	}

	t.Run("generation at the applied commit", func(t *testing.T) {
		in := baseInput(now)
		in.Resource.Desired = DesiredRevision{Generation: 2, PlanID: "plan-1", CommitSHA: commitX}
		in.Fence, in.LastReleaseAttempts = succeededFence, released

		status := DeriveStatus(in)
		assertAllGreen(t, status)
		if status.LastAppliedGeneration != 2 {
			t.Fatalf("LastAppliedGeneration = %d, want 2", status.LastAppliedGeneration)
		}
		if status.LastApplied == nil || status.LastApplied.CommitSHA != commitX || status.LastApplied.PlanID != "plan-1" || status.LastApplied.Reason != "" {
			t.Fatalf("LastApplied = %+v, want commit %s with no reason", status.LastApplied, commitX)
		}
		rr := mustCondition(t, status, ConditionReconciliationRequired)
		if rr.Status != StatusFalse || rr.Reason != ReasonUpToDate {
			t.Fatalf("ReconciliationRequired = %s/%s, want False/UpToDate", rr.Status, rr.Reason)
		}
		if status.NextAction != NextActionNone {
			t.Fatalf("NextAction = %s, want none", status.NextAction)
		}
	})

	t.Run("later desired generation not yet applied", func(t *testing.T) {
		in := baseInput(now)
		in.Resource.Desired = DesiredRevision{Generation: 3, PlanID: "plan-2", CommitSHA: commitY}
		in.Resource.DesiredHistory = []DesiredRevision{{Generation: 2, PlanID: "plan-1", CommitSHA: commitX}}
		in.Fence, in.LastReleaseAttempts = succeededFence, released

		status := DeriveStatus(in)
		if status.LastAppliedGeneration != 2 {
			t.Fatalf("LastAppliedGeneration = %d, want 2 (unaffected by the newer desired generation)", status.LastAppliedGeneration)
		}
		if status.ObservedGeneration != 3 {
			t.Fatalf("ObservedGeneration = %d, want 3", status.ObservedGeneration)
		}
		rr := mustCondition(t, status, ConditionReconciliationRequired)
		if rr.Status != StatusTrue || rr.Reason != ReasonDesiredNotApplied {
			t.Fatalf("ReconciliationRequired = %s/%s, want True/DesiredNotApplied", rr.Status, rr.Reason)
		}
		if status.NextAction != NextActionNone {
			t.Fatalf("NextAction = %s, want none (no automatic repair)", status.NextAction)
		}
	})
}

func TestDeriveDrift(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 0}
	in.LatestObservations[SourceState] = Observation{Sequence: 9, Source: SourceState, ObservedAt: now, ReceivedAt: now,
		Facts: ObservationFacts{factDrift: true}}

	status := DeriveStatus(in)
	drift := mustCondition(t, status, ConditionDriftDetected)
	if drift.Status != StatusTrue || drift.Reason != ReasonObservedDrift {
		t.Fatalf("DriftDetected = %s/%s, want True/ObservedDrift", drift.Status, drift.Reason)
	}
	rr := mustCondition(t, status, ConditionReconciliationRequired)
	if rr.Status != StatusTrue || rr.Reason != ReasonDriftReconciliation {
		t.Fatalf("ReconciliationRequired = %s/%s, want True/DriftDetected", rr.Status, rr.Reason)
	}
	if status.NextAction != NextActionInvestigateDrift {
		t.Fatalf("NextAction = %s, want investigate_drift", status.NextAction)
	}
}

func TestDeriveStaleIsUnknown(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	stale := now.Add(-ObservationFreshness - time.Minute)
	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 0}
	for source, obs := range in.LatestObservations {
		obs.ObservedAt = stale
		in.LatestObservations[source] = obs
	}
	in.Resource.Watermarks = map[string]Watermark{
		SourceProvider: {ObservedAt: stale, Sequence: 1},
		SourceState:    {ObservedAt: stale, Sequence: 2},
		SourceRuntime:  {ObservedAt: stale, Sequence: 3},
	}

	status := DeriveStatus(in)
	for _, ct := range []string{ConditionProviderStateKnown, ConditionNodesEnrolled, ConditionRuntimeReady, ConditionIngressReady, ConditionDriftDetected} {
		if c := mustCondition(t, status, ct); c.Status != StatusUnknown || c.Reason != ReasonObservationStale {
			t.Fatalf("%s = %s/%s, want Unknown/ObservationStale", ct, c.Status, c.Reason)
		}
	}
	// Nothing is desired and the fence is free, but without a fresh no-drift
	// reading UpToDate would be a stale False.
	rr := mustCondition(t, status, ConditionReconciliationRequired)
	if rr.Status != StatusUnknown || rr.Reason != ReasonObservationStale {
		t.Fatalf("ReconciliationRequired = %s/%s, want Unknown/ObservationStale", rr.Status, rr.Reason)
	}
	if status.NextAction != NextActionRefreshObservations {
		t.Fatalf("NextAction = %s, want refresh_observations", status.NextAction)
	}
}

func TestReadTimeFreshnessDowngrade(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 0}
	status := DeriveStatus(in)
	assertAllGreen(t, status)

	t.Run("still within the window", func(t *testing.T) {
		later := DowngradeStale(status, now.Add(10*time.Minute))
		assertAllGreen(t, later)
	})

	t.Run("past the window", func(t *testing.T) {
		later := DowngradeStale(status, now.Add(ObservationFreshness+time.Minute))
		for _, ct := range []string{ConditionProviderStateKnown, ConditionNodesEnrolled, ConditionRuntimeReady, ConditionIngressReady, ConditionDriftDetected} {
			if c := mustCondition(t, later, ct); c.Status != StatusUnknown || c.Reason != ReasonObservationStale {
				t.Fatalf("%s = %s/%s, want Unknown/ObservationStale at read time", ct, c.Status, c.Reason)
			}
		}
		// UpToDate rested on the now-stale drift reading.
		rr := mustCondition(t, later, ConditionReconciliationRequired)
		if rr.Status != StatusUnknown || rr.Reason != ReasonObservationStale {
			t.Fatalf("ReconciliationRequired = %s/%s, want Unknown/ObservationStale at read time", rr.Status, rr.Reason)
		}
		if later.NextAction != NextActionRefreshObservations {
			t.Fatalf("NextAction = %s, want refresh_observations at read time", later.NextAction)
		}
		// The derive-time status must not be mutated.
		assertAllGreen(t, status)
	})

	t.Run("occupancy-driven values are kept", func(t *testing.T) {
		held := baseInput(now)
		held.Fence = heldFence()
		held.Holder = lifecycle.HolderFacts{DispatchState: "dispatched"}
		derived := DeriveStatus(held)
		later := DowngradeStale(derived, now.Add(time.Hour))
		if later.NextAction != NextActionResolveUncertainOutcome {
			t.Fatalf("NextAction = %s, want resolve_uncertain_outcome kept", later.NextAction)
		}
		if rr := mustCondition(t, later, ConditionReconciliationRequired); rr.Reason != ReasonOutcomeUncertain {
			t.Fatalf("ReconciliationRequired reason = %s, want OutcomeUncertain kept", rr.Reason)
		}
	})
}

// TestDeriveFreshnessBoundary pins the 15-minute window against the passed
// now, at derive time and at read time: exactly ObservationFreshness old is
// still fresh, one nanosecond more is stale.
func TestDeriveFreshnessBoundary(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{{ObservationFreshness, StatusTrue}, {ObservationFreshness + time.Nanosecond, StatusUnknown}} {
		in := baseInput(observed)
		in.Now = observed.Add(tc.age)
		if c := mustCondition(t, DeriveStatus(in), ConditionRuntimeReady); c.Status != tc.want {
			t.Fatalf("derive at age %s: RuntimeReady = %s, want %s", tc.age, c.Status, tc.want)
		}
		read := DowngradeStale(DeriveStatus(baseInput(observed)), observed.Add(tc.age))
		if c := mustCondition(t, read, ConditionRuntimeReady); c.Status != tc.want {
			t.Fatalf("read at age %s: RuntimeReady = %s, want %s", tc.age, c.Status, tc.want)
		}
	}
}

func heldFence() lifecycle.FenceFacts {
	return lifecycle.FenceFacts{TargetID: "tgt_1", Generation: 1, Held: true, HolderPlanID: "plan-1", HolderNonceSHA256: "nonce-1", AuthorityEpoch: 1, Revision: 1}
}

func TestDeriveNoAttemptIsUncertain(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 0}
	in.Fence = heldFence()
	in.Holder = lifecycle.HolderFacts{DispatchState: "dispatched", DispatchCreatedAt: now.Add(-lifecycle.AbandonMinimumAge - time.Minute)}

	status := DeriveStatus(in)
	if status.Active == nil || status.Active.Occupancy != string(lifecycle.OccupancyUncertain) || status.Active.OccupancyReason != lifecycle.ReasonNoLiveAttempt {
		t.Fatalf("Active = %+v, want Uncertain/NoLiveAttempt", status.Active)
	}
	rr := mustCondition(t, status, ConditionReconciliationRequired)
	if rr.Status != StatusTrue || rr.Reason != ReasonOutcomeUncertain {
		t.Fatalf("ReconciliationRequired = %s/%s, want True/OutcomeUncertain", rr.Status, rr.Reason)
	}
	if !strings.Contains(rr.Message, "abandon allowed now") {
		t.Fatalf("ReconciliationRequired message %q must surface that abandon is allowed", rr.Message)
	}
	if status.NextAction != NextActionResolveUncertainOutcome {
		t.Fatalf("NextAction = %s, want resolve_uncertain_outcome", status.NextAction)
	}
}

func TestDeriveInterruptedExecutionIsUncertain(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 0}
	in.Fence = heldFence()
	in.Holder = lifecycle.HolderFacts{DispatchState: "dispatched", Attempts: []fleet.RunnerAttempt{
		{ID: "attempt-1", PlanID: "plan-1", Attempt: 1, Status: "running", HeartbeatExpiresAt: now.Add(-time.Hour)},
	}}

	status := DeriveStatus(in)
	if status.Active == nil || status.Active.Occupancy != string(lifecycle.OccupancyUncertain) || status.Active.OccupancyReason != lifecycle.ReasonAttemptTerminalWithoutSuccess {
		t.Fatalf("Active = %+v, want Uncertain/AttemptTerminalWithoutSuccess", status.Active)
	}
	if status.Active.PlanID != "plan-1" || len(status.Active.AttemptIDs) != 1 || status.Active.AttemptIDs[0] != "attempt-1" {
		t.Fatalf("Active refs must come straight from the input, got %+v", status.Active)
	}
	rr := mustCondition(t, status, ConditionReconciliationRequired)
	if rr.Status != StatusTrue || rr.Reason != ReasonOutcomeUncertain {
		t.Fatalf("ReconciliationRequired = %s/%s, want True/OutcomeUncertain", rr.Status, rr.Reason)
	}
}

func TestDeriveDispatchAmbiguous(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 0}
	in.Fence = heldFence()
	in.Holder = lifecycle.HolderFacts{DispatchState: "submitting"}

	status := DeriveStatus(in)
	if status.Active == nil || status.Active.Occupancy != string(lifecycle.OccupancyUncertain) || status.Active.OccupancyReason != lifecycle.ReasonDispatchSubmissionUnresolved {
		t.Fatalf("Active = %+v, want Uncertain/DispatchSubmissionUnresolved", status.Active)
	}
	if status.NextAction != NextActionResolveUncertainOutcome {
		t.Fatalf("NextAction = %s, want resolve_uncertain_outcome", status.NextAction)
	}
}

func TestDeriveDesiredChangedDuringExecution(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := baseInput(now)
	live := fleet.RunnerAttempt{ID: "attempt-7", PlanID: "plan-1", Attempt: 1, Status: "running",
		RunnerAttemptID: "github-actions:acme/norn-fleet:555:1", HeartbeatExpiresAt: now.Add(time.Hour)}
	in.Fence = heldFence()
	in.Holder = lifecycle.HolderFacts{DispatchState: "dispatched", Attempts: []fleet.RunnerAttempt{live}}
	in.HolderDispatchRunID = 555
	// A new desired revision lands while the attempt above is still running.
	in.Resource.Desired = DesiredRevision{Generation: 9, PlanID: "plan-9", CommitSHA: "cccccccccccccccccccccccccccccccccccccccc"}

	status := DeriveStatus(in)
	if status.Active == nil || status.Active.PlanID != "plan-1" || status.Active.FenceGeneration != 1 ||
		len(status.Active.AttemptIDs) != 1 || status.Active.AttemptIDs[0] != "attempt-7" ||
		status.Active.DispatchRunID != "555" {
		t.Fatalf("Active must keep the original binding untouched by the desired change, got %+v", status.Active)
	}
	if status.Active.Occupancy != string(lifecycle.OccupancyActive) {
		t.Fatalf("Occupancy = %s, want Active", status.Active.Occupancy)
	}
	if status.ObservedGeneration != 9 {
		t.Fatalf("ObservedGeneration = %d, want 9", status.ObservedGeneration)
	}
	rr := mustCondition(t, status, ConditionReconciliationRequired)
	if rr.Status != StatusUnknown || rr.Reason != ReasonExecutionInProgress {
		t.Fatalf("ReconciliationRequired = %s/%s, want Unknown/ExecutionInProgress", rr.Status, rr.Reason)
	}
	if status.NextAction != NextActionAwaitRunner {
		t.Fatalf("NextAction = %s, want await_runner", status.NextAction)
	}
}

func TestDeriveOlderObservationCannotClearFailure(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()

	run := func(latestReady, tiedReady bool) Condition {
		in := baseInput(now)
		in.Resource.Desired = DesiredRevision{Generation: 0}
		in.LatestObservations[SourceRuntime] = Observation{Sequence: 10, Source: SourceRuntime, ObservedAt: now, ReceivedAt: now,
			Facts: ObservationFacts{factReady: latestReady, factIngressReady: true}}
		in.TiedObservations[SourceRuntime] = []Observation{
			{Sequence: 11, Source: SourceRuntime, ObservedAt: now, ReceivedAt: now, Facts: ObservationFacts{factReady: tiedReady}},
		}
		return mustCondition(t, DeriveStatus(in), ConditionRuntimeReady)
	}

	if c := run(true, false); c.Status != StatusFalse || c.Reason != ReasonObservedNotReady || c.ObservationSequence != 11 {
		t.Fatalf("watermark true + tie false = %s/%s seq %d, want False/ObservedNotReady citing seq 11 (failure wins)", c.Status, c.Reason, c.ObservationSequence)
	}
	if c := run(false, true); c.Status != StatusFalse || c.Reason != ReasonObservedNotReady {
		t.Fatalf("watermark false + tie true = %s/%s, want False/ObservedNotReady (order must not matter)", c.Status, c.Reason)
	}

	t.Run("older success arriving after a newer failure", func(t *testing.T) {
		failure := Observation{Sequence: 20, Source: SourceRuntime, ObservedAt: now, ReceivedAt: now,
			Facts: ObservationFacts{factReady: false, factIngressReady: false}}
		applied, wm := ApplyObservation(Watermark{}, false, failure.ObservedAt, failure.Sequence)
		if !applied {
			t.Fatal("the first failure must apply")
		}
		applied, wm2 := ApplyObservation(wm, true, now.Add(-time.Second), 21)
		if applied || !reflect.DeepEqual(wm, wm2) {
			t.Fatalf("an older success must not apply or move the watermark: applied=%v %+v", applied, wm2)
		}
		in := baseInput(now)
		in.Resource.Watermarks[SourceRuntime] = wm2
		in.LatestObservations[SourceRuntime] = failure // storage reads only the watermark row
		for _, ct := range []string{ConditionRuntimeReady, ConditionIngressReady} {
			if c := mustCondition(t, DeriveStatus(in), ct); c.Status != StatusFalse {
				t.Fatalf("%s = %s, want False: the older success cannot clear it", ct, c.Status)
			}
		}
	})
}

func TestDeriveEqualTimestampFailureWins(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 0}
	in.LatestObservations[SourceState] = Observation{Sequence: 20, Source: SourceState, ObservedAt: now, ReceivedAt: now,
		Facts: ObservationFacts{factDrift: false}}
	in.TiedObservations[SourceState] = []Observation{
		{Sequence: 21, Source: SourceState, ObservedAt: now, ReceivedAt: now, Facts: ObservationFacts{factDrift: true}},
	}

	status := DeriveStatus(in)
	drift := mustCondition(t, status, ConditionDriftDetected)
	if drift.Status != StatusTrue || drift.Reason != ReasonObservedDrift {
		t.Fatalf("on an equal-timestamp tie the failure (drift=true) must win, got %s/%s", drift.Status, drift.Reason)
	}
}

func TestDeriveTargetIdentityMismatch(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 0}
	in.Resource.TargetID = "tgt_1"
	provider := in.LatestObservations[SourceProvider]
	provider.Facts = ObservationFacts{factTargetID: "tgt_other", factEnrolledNodes: 3.0, factExpectedNodes: 3.0}
	in.LatestObservations[SourceProvider] = provider

	status := DeriveStatus(in)
	c := mustCondition(t, status, ConditionProviderStateKnown)
	if c.Status != StatusFalse || c.Reason != ReasonTargetIdentityMismatch {
		t.Fatalf("ProviderStateKnown = %s/%s, want False/TargetIdentityMismatch", c.Status, c.Reason)
	}
}

func TestDeriveEpochSuperseded(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 0}
	fence := heldFence()
	fence.AuthorityEpoch = 1
	in.Fence = fence
	in.Holder = lifecycle.HolderFacts{DispatchState: "dispatched", Attempts: []fleet.RunnerAttempt{
		{ID: "attempt-1", PlanID: "plan-1", Attempt: 1, Status: "running", HeartbeatExpiresAt: now.Add(time.Hour)},
	}}
	in.AuthorityEpoch = 2 // the current epoch has advanced past the fence's
	in.HolderDispatchRunID = 77

	status := DeriveStatus(in)
	if status.Active.PlanID != "plan-1" || status.Active.DispatchRunID != "77" {
		t.Fatalf("Active must still name the superseded holder, got %+v", status.Active)
	}
	// The attempt's lease is still live, so abandon is not yet allowed.
	want := "abandon allowed from " + now.Add(time.Hour+lifecycle.AbandonMinimumAge).UTC().Format(time.RFC3339)
	if rr := mustCondition(t, status, ConditionReconciliationRequired); !strings.Contains(rr.Message, want) {
		t.Fatalf("ReconciliationRequired message %q, want it to contain %q", rr.Message, want)
	}
	if status.Active == nil || status.Active.Occupancy != string(lifecycle.OccupancyUncertain) || status.Active.OccupancyReason != lifecycle.ReasonAuthoritySuperseded {
		t.Fatalf("Active = %+v, want Uncertain/AuthoritySuperseded", status.Active)
	}
	rr := mustCondition(t, status, ConditionReconciliationRequired)
	if rr.Status != StatusTrue || rr.Reason != ReasonOutcomeUncertain {
		t.Fatalf("ReconciliationRequired = %s/%s, want True/OutcomeUncertain", rr.Status, rr.Reason)
	}
	if status.NextAction != NextActionResolveUncertainOutcome {
		t.Fatalf("NextAction = %s, want resolve_uncertain_outcome", status.NextAction)
	}
}

func TestDeriveGenerationOutOfHistory(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	commit := "dddddddddddddddddddddddddddddddddddddddd"
	in := baseInput(now)
	// The desired revision and its retained history no longer include the
	// commit that actually succeeded: it aged out of the 20-entry window.
	in.Resource.Desired = DesiredRevision{Generation: 25, PlanID: "plan-25", CommitSHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}
	in.Fence = lifecycle.FenceFacts{
		TargetID: "tgt_1", Generation: 1, Held: false, AuthorityEpoch: 1,
		LastRelease: &lifecycle.Release{PlanID: "plan-1", Reason: "succeeded", At: now.Add(-time.Hour)},
	}
	in.LastReleaseAttempts = []fleet.RunnerAttempt{
		{ID: "attempt-1", PlanID: "plan-1", Attempt: 1, Status: "succeeded", CommitSHA: commit},
	}
	for g := int64(24); g >= 5; g-- {
		in.Resource.DesiredHistory = append(in.Resource.DesiredHistory,
			DesiredRevision{Generation: g, CommitSHA: strings.Repeat("0", 38) + strconv.FormatInt(g, 10)})
	}

	status := DeriveStatus(in)
	if status.LastAppliedGeneration != 0 {
		t.Fatalf("LastAppliedGeneration = %d, want 0 for an aged-out commit", status.LastAppliedGeneration)
	}
	if status.LastApplied == nil || status.LastApplied.CommitSHA != commit || status.LastApplied.PlanID != "plan-1" || status.LastApplied.Reason != ReasonGenerationOutOfHistory {
		t.Fatalf("LastApplied = %+v, want commit %s with Reason=GenerationOutOfHistory", status.LastApplied, commit)
	}

	t.Run("commit never desired", func(t *testing.T) {
		in.Resource.DesiredHistory = in.Resource.DesiredHistory[:3]
		status := DeriveStatus(in)
		if status.LastAppliedGeneration != 0 || status.LastApplied == nil || status.LastApplied.Reason != ReasonCommitNotDesired {
			t.Fatalf("LastApplied = %+v, want Reason=CommitNotDesired when history never dropped an entry", status.LastApplied)
		}
		if rr := mustCondition(t, status, ConditionReconciliationRequired); rr.Reason != ReasonDesiredNotApplied {
			t.Fatalf("ReconciliationRequired reason = %s, want DesiredNotApplied", rr.Reason)
		}
	})
}

// TestDeriveLastAppliedSurvivesLaterBadHealth exercises the historical rule
// directly: a later reconcile pass, where runtime health has regressed and
// the fence has since been released for a different (non-succeeded) reason,
// must not erase the earlier success recorded in Resource.Status.
func TestDeriveLastAppliedSurvivesLaterBadHealth(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	commit := "ffffffffffffffffffffffffffffffffffffffff"

	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 4, PlanID: "plan-4", CommitSHA: commit}
	// This reconcile pass's own fence state is a later terminal release for
	// a different plan -- not a success -- so deriveLastApplied cannot
	// recompute from it and must fall back to the persisted cache.
	in.Fence = lifecycle.FenceFacts{
		TargetID: "tgt_1", Held: false, AuthorityEpoch: 1,
		LastRelease: &lifecycle.Release{PlanID: "plan-5", Reason: "released_terminal", At: now},
	}
	in.Resource.Status = Status{
		LastAppliedGeneration: 4,
		LastApplied:           &AppliedRef{Generation: 4, CommitSHA: commit, PlanID: "plan-4"},
	}
	// Runtime health has regressed.
	runtime := in.LatestObservations[SourceRuntime]
	runtime.Facts = ObservationFacts{factReady: false}
	in.LatestObservations[SourceRuntime] = runtime

	status := DeriveStatus(in)
	if status.LastAppliedGeneration != 4 || status.LastApplied == nil || status.LastApplied.CommitSHA != commit {
		t.Fatalf("LastApplied must survive later bad health, got generation=%d applied=%+v", status.LastAppliedGeneration, status.LastApplied)
	}
	if c := mustCondition(t, status, ConditionRuntimeReady); c.Status != StatusFalse {
		t.Fatalf("RuntimeReady = %s, want False (health really did regress)", c.Status)
	}
}

func TestDeriveDeterministic(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 0}
	in.Fence = heldFence()
	in.Holder = lifecycle.HolderFacts{DispatchState: "dispatched", Attempts: []fleet.RunnerAttempt{
		{ID: "attempt-2", PlanID: "plan-1", Attempt: 2, Status: "running", HeartbeatExpiresAt: now.Add(time.Hour)},
		{ID: "attempt-1", PlanID: "plan-1", Attempt: 1, Status: "failed", HeartbeatExpiresAt: now.Add(-time.Hour)},
	}}

	first := DeriveStatus(in)
	second := DeriveStatus(in)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("status differs across identical calls:\n%+v\n%+v", first, second)
	}
	for i := 1; i < len(first.Conditions); i++ {
		if first.Conditions[i-1].Type >= first.Conditions[i].Type {
			t.Fatalf("conditions are not sorted by Type: %s then %s", first.Conditions[i-1].Type, first.Conditions[i].Type)
		}
	}
	if len(first.Active.AttemptIDs) != 2 || first.Active.AttemptIDs[0] != "attempt-1" || first.Active.AttemptIDs[1] != "attempt-2" {
		t.Fatalf("AttemptIDs must be sorted, got %v", first.Active.AttemptIDs)
	}
}

// TestDeriveControllerRestart: a restarted controller reads the persisted
// status back (JSON round trip) as Resource.Status and re-derives from the
// same facts; it must reach exactly the same status, LastApplied included.
func TestDeriveControllerRestart(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	commit := "abababababababababababababababababababab"
	in := baseInput(now)
	in.Resource.Desired = DesiredRevision{Generation: 3, PlanID: "plan-3", CommitSHA: commit}
	in.Fence = lifecycle.FenceFacts{TargetID: "tgt_1", Generation: 3, AuthorityEpoch: 1,
		LastRelease: &lifecycle.Release{PlanID: "plan-3", Reason: "succeeded", At: now.Add(-time.Minute)}}
	in.LastReleaseAttempts = []fleet.RunnerAttempt{{ID: "a-1", PlanID: "plan-3", Attempt: 1, Status: "succeeded", CommitSHA: commit}}

	before := DeriveStatus(in)
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Status
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	restarted := in
	restarted.Resource.Status = persisted
	after := DeriveStatus(restarted)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("restart changed the status:\n%+v\n%+v", before, after)
	}
	if after.LastAppliedGeneration != 3 {
		t.Fatalf("LastAppliedGeneration = %d, want 3", after.LastAppliedGeneration)
	}
}
