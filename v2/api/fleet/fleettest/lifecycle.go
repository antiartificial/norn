package fleettest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
)

// PlanRef names a capacity plan a LifecycleHarness seeded for the suite.
type PlanRef struct {
	ID            string
	RequiresDrain bool
}

// DispatchRef is the protected GitHub dispatch binding a harness seeded for a
// plan. RawNonce is handed back because the suite is the party that minted the
// dispatch; D10 (raw nonce at rest) is about what each backend's own storage
// retains, which this field does not touch.
type DispatchRef struct {
	RunID           int64
	RunAttempt      int
	RawNonce        string
	ApprovedHeadSHA string
	PlanSHA256      string
}

// RunRef is the GitHub Actions run identity a runner presents to Start or
// Recover.
type RunRef struct {
	RunID      int64
	RunAttempt int
	Intent     string // "apply" | "recover"
	SHA        string
}

// StopEvidence is the shared server-observed predecessor-termination proof
// (plan.md D6). PG legacy's harness ignores it; etcd's harness requires it for
// every recovery.
type StopEvidence = lifecycle.StopEvidence

// Resp is a backend-neutral HTTP-boundary result. Code is the control-plane
// problem code (empty on success). Attempt is populated whenever the call
// returns an attempt representation; it is nil for Checkpoint, which returns
// an operation receipt the suite does not need.
type Resp struct {
	HTTPStatus int
	Code       string
	Attempt    *fleet.RunnerAttempt
}

// LifecycleHarness drives one backend's live HTTP routes: the PG legacy
// handlers production registers in main.go, or EtcdFleetRunnerHandler. This
// fixes plan-review.md finding B3 (the suite previously would have targeted
// the unrouted PG V3 path): every case here goes through an actual route.
type LifecycleHarness interface {
	Profile() lifecycle.Profile
	SeedPlan(action string) PlanRef
	SeedDispatch(plan PlanRef) DispatchRef
	Start(plan PlanRef, run RunRef) Resp
	Recover(plan PlanRef, run RunRef, stop *StopEvidence) Resp
	Heartbeat(attempt *fleet.RunnerAttempt, sequence, revision int64) Resp
	Advance(attempt *fleet.RunnerAttempt, expectedPhase string, revision int64) Resp
	Cancel(attempt *fleet.RunnerAttempt, revision int64, reason string) Resp
	Checkpoint(attempt *fleet.RunnerAttempt, phase, status string) Resp
	Get(attempt *fleet.RunnerAttempt) Resp
	// ExpireAttempt forces attempt's heartbeat lease to be in the past, as
	// observed by the next read or mutation against it.
	ExpireAttempt(attempt *fleet.RunnerAttempt)
}

// RunFleetLifecycleConformance runs the shared lifecycle conformance cases
// against h. Each case's assertions are identical across backends except
// where plan.md §1.6 (D1-D12) documents a preserved difference; those are
// branched explicitly on h.Profile(), never weakened into a shared check.
func RunFleetLifecycleConformance(t *testing.T, h LifecycleHarness) {
	t.Run("StartReplaySameIdentity", func(t *testing.T) { testStartReplaySameIdentity(t, h) })
	t.Run("StartRejectsDispatchMismatch", func(t *testing.T) { testStartRejectsDispatchMismatch(t, h) })
	t.Run("HeartbeatOrderingAndRevision", func(t *testing.T) { testHeartbeatOrderingAndRevision(t, h) })
	t.Run("HeartbeatRejectedAfterExpiry", func(t *testing.T) { testHeartbeatRejectedAfterExpiry(t, h) })
	t.Run("AdvanceRequiresCurrentPhaseEvidence", func(t *testing.T) { testAdvanceRequiresCurrentPhaseEvidence(t, h) })
	t.Run("PhaseSequencePerProfile", func(t *testing.T) { testPhaseSequencePerProfile(t, h) })
	t.Run("AdvanceCompleteTerminal", func(t *testing.T) { testAdvanceCompleteTerminal(t, h) })
	t.Run("CheckpointRejectsWrongPhaseOrInactiveAttempt", func(t *testing.T) { testCheckpointRejectsWrongPhaseOrInactiveAttempt(t, h) })
	t.Run("CheckpointReplayAfterLostResponse", func(t *testing.T) { testCheckpointReplayAfterLostResponse(t, h) })
	t.Run("RecoveryLineageAndLimit", func(t *testing.T) { testRecoveryLineageAndLimit(t, h) })
	t.Run("RecoveryStopProofPerProfile", func(t *testing.T) { testRecoveryStopProofPerProfile(t, h) })
	t.Run("ReadsDoNotWriteExceptLegacyExpiry", func(t *testing.T) { testReadsDoNotWriteExceptLegacyExpiry(t, h) })
}

func seedApplyRun(h LifecycleHarness, action string) (PlanRef, DispatchRef, RunRef) {
	plan := h.SeedPlan(action)
	dispatch := h.SeedDispatch(plan)
	run := RunRef{RunID: dispatch.RunID, RunAttempt: dispatch.RunAttempt, Intent: "apply", SHA: dispatch.ApprovedHeadSHA}
	return plan, dispatch, run
}

func requireAttempt(t *testing.T, resp Resp, wantStatus int) *fleet.RunnerAttempt {
	t.Helper()
	if resp.HTTPStatus != wantStatus || resp.Attempt == nil {
		t.Fatalf("status=%d code=%q attempt=%v, want status=%d with an attempt", resp.HTTPStatus, resp.Code, resp.Attempt, wantStatus)
	}
	return resp.Attempt
}

func requireRejected(t *testing.T, resp Resp, wantStatus int, wantCode string) {
	t.Helper()
	if resp.HTTPStatus != wantStatus || resp.Code != wantCode {
		t.Fatalf("status=%d code=%q, want status=%d code=%q", resp.HTTPStatus, resp.Code, wantStatus, wantCode)
	}
}

func stopEvidenceFor(predecessorID string, runID int64, runAttempt int64) *StopEvidence {
	return &StopEvidence{
		PredecessorID: predecessorID, SourceDispatchRunID: fmt.Sprintf("%d", runID), RunAttempt: runAttempt,
		WorkflowURL: fmt.Sprintf("https://github.com/acme/norn-fleet/actions/runs/%d", runID),
		Status:      "completed", Conclusion: "failure", ObservedAt: time.Now().UTC(),
	}
}

func testStartReplaySameIdentity(t *testing.T, h LifecycleHarness) {
	plan, _, run := seedApplyRun(h, "scale")
	first := requireAttempt(t, h.Start(plan, run), http.StatusCreated)
	replay := requireAttempt(t, h.Start(plan, run), http.StatusOK)
	if replay.ID != first.ID || replay.RunnerAttemptID != first.RunnerAttemptID {
		t.Fatalf("replay of the same identity diverged: first=%+v replay=%+v", first, replay)
	}
}

func testStartRejectsDispatchMismatch(t *testing.T, h LifecycleHarness) {
	plan, _, run := seedApplyRun(h, "scale")
	mismatched := run
	mismatched.SHA = strings.Repeat("9", 40)
	resp := h.Start(plan, mismatched)
	if resp.Attempt != nil {
		t.Fatalf("a start that does not match the protected dispatch still created an attempt: %+v", resp.Attempt)
	}
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		requireRejected(t, resp, http.StatusForbidden, "fleet_runner_dispatch_binding_mismatch")
	default:
		requireRejected(t, resp, http.StatusForbidden, "fleet_runner_attempt_dispatch_mismatch")
	}
}

func testHeartbeatOrderingAndRevision(t *testing.T, h LifecycleHarness) {
	plan, _, run := seedApplyRun(h, "scale")
	attempt := requireAttempt(t, h.Start(plan, run), http.StatusCreated)
	first := requireAttempt(t, h.Heartbeat(attempt, attempt.HeartbeatSequence+1, attempt.Revision), http.StatusOK)
	if first.HeartbeatSequence != attempt.HeartbeatSequence+1 || first.Revision != attempt.Revision+1 {
		t.Fatalf("heartbeat did not advance sequence/revision: before=%+v after=%+v", attempt, first)
	}
	replayed := h.Heartbeat(attempt, attempt.HeartbeatSequence+1, attempt.Revision)
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		// D12: a replay at the same sequence, naming the revision that
		// recorded it, succeeds and returns the current attempt unchanged.
		replay := requireAttempt(t, replayed, http.StatusOK)
		if replay.ID != first.ID || replay.Revision != first.Revision || replay.HeartbeatSequence != first.HeartbeatSequence {
			t.Fatalf("legacy same-sequence heartbeat replay diverged: want=%+v got=%+v", first, replay)
		}
	default:
		// etcd is strict: naming a revision that is no longer current is
		// refused outright, with no replay carve-out.
		requireRejected(t, replayed, http.StatusConflict, "fleet_runner_attempt_stale")
	}
}

func testHeartbeatRejectedAfterExpiry(t *testing.T, h LifecycleHarness) {
	plan, _, run := seedApplyRun(h, "scale")
	attempt := requireAttempt(t, h.Start(plan, run), http.StatusCreated)
	h.ExpireAttempt(attempt)
	resp := h.Heartbeat(attempt, attempt.HeartbeatSequence+1, attempt.Revision)
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		// The route's own read writes abandoned first (D8), so the guarded
		// UPDATE then misses on revision/status.
		requireRejected(t, resp, http.StatusConflict, "fleet_runner_attempt_revision_conflict")
	default:
		requireRejected(t, resp, http.StatusConflict, "fleet_runner_attempt_stale")
	}
	after := requireAttempt(t, h.Get(attempt), http.StatusOK)
	if after.Status != "abandoned" || after.HeartbeatSequence != attempt.HeartbeatSequence {
		t.Fatalf("a refused heartbeat after expiry revived or advanced the attempt: before=%+v after=%+v", attempt, after)
	}
}

func testAdvanceRequiresCurrentPhaseEvidence(t *testing.T, h LifecycleHarness) {
	plan, _, run := seedApplyRun(h, "scale")
	attempt := requireAttempt(t, h.Start(plan, run), http.StatusCreated)
	noEvidence := h.Advance(attempt, attempt.CurrentPhase, attempt.Revision)
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		// D9: PG legacy proves evidence with an atomic SQL EXISTS gate.
		requireRejected(t, noEvidence, http.StatusConflict, "fleet_runner_advance_unproven")
	default:
		// D9: etcd proves evidence with an atomic CAS; an unproven advance
		// collapses into the same generic staleness code as every other
		// unmet precondition on this route.
		requireRejected(t, noEvidence, http.StatusConflict, "fleet_runner_attempt_stale")
	}
	checkpoint := h.Checkpoint(attempt, attempt.CurrentPhase, "succeeded")
	if checkpoint.HTTPStatus != http.StatusCreated {
		t.Fatalf("checkpoint for the current phase was rejected: status=%d code=%q", checkpoint.HTTPStatus, checkpoint.Code)
	}
	advanced := requireAttempt(t, h.Advance(attempt, attempt.CurrentPhase, attempt.Revision), http.StatusOK)
	if advanced.CurrentPhase == attempt.CurrentPhase {
		t.Fatalf("advance with signed checkpoint evidence did not move phase: %+v", advanced)
	}
}

// walkToTerminal drives attempt through every phase lifecycle.Phases reports
// for h's profile, checkpointing and advancing one phase at a time, and
// returns the final terminal attempt.
func walkToTerminal(t *testing.T, h LifecycleHarness, plan PlanRef, attempt *fleet.RunnerAttempt) *fleet.RunnerAttempt {
	t.Helper()
	for _, phase := range lifecycle.Phases(h.Profile(), plan.RequiresDrain) {
		if attempt.Status == "succeeded" {
			// V3/etcd finalizes status the moment CurrentPhase becomes
			// "complete" (etcdstore's advance case), one phase earlier than
			// PG legacy, which only finalizes on an explicit advance call
			// made *at* "complete" (nextFleetRunnerPhase is terminal only
			// when called with current=="complete"). Either way, the walk
			// is done once the attempt reports success.
			break
		}
		if attempt.CurrentPhase != phase {
			t.Fatalf("walk reached phase %q, want %q", attempt.CurrentPhase, phase)
		}
		checkpoint := h.Checkpoint(attempt, phase, "succeeded")
		if checkpoint.HTTPStatus != http.StatusCreated {
			t.Fatalf("checkpoint for phase %q was rejected: status=%d code=%q", phase, checkpoint.HTTPStatus, checkpoint.Code)
		}
		attempt = requireAttempt(t, h.Advance(attempt, phase, attempt.Revision), http.StatusOK)
	}
	return attempt
}

func testPhaseSequencePerProfile(t *testing.T, h LifecycleHarness) {
	plan, _, run := seedApplyRun(h, "scale")
	attempt := requireAttempt(t, h.Start(plan, run), http.StatusCreated)
	phases := lifecycle.Phases(h.Profile(), plan.RequiresDrain)
	// D3/D4: PG legacy starts at infrastructure_applied with six non-drain
	// phases; every V3 profile starts at prechange_verified with the
	// canonical nine, regardless of drain.
	if len(phases) == 0 || attempt.CurrentPhase != phases[0] {
		t.Fatalf("initial phase %q does not match lifecycle.Phases(%v, %v)=%v", attempt.CurrentPhase, h.Profile(), plan.RequiresDrain, phases)
	}
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		if len(phases) != 6 {
			t.Fatalf("PG legacy non-drain phase count = %d, want 6", len(phases))
		}
	default:
		if len(phases) != 9 {
			t.Fatalf("V3 phase count = %d, want 9", len(phases))
		}
	}
	final := walkToTerminal(t, h, plan, attempt)
	if final.CurrentPhase != "complete" {
		t.Fatalf("walk did not reach the terminal phase: %+v", final)
	}
}

func testAdvanceCompleteTerminal(t *testing.T, h LifecycleHarness) {
	plan, _, run := seedApplyRun(h, "scale")
	attempt := requireAttempt(t, h.Start(plan, run), http.StatusCreated)
	final := walkToTerminal(t, h, plan, attempt)
	if final.Status != "succeeded" {
		t.Fatalf("terminal attempt is not succeeded: %+v", final)
	}
	// A *new* checkpoint identity (status differs, so this is not a replay of
	// the one that just completed the walk) must still be rejected: the
	// attempt is no longer live.
	again := h.Checkpoint(final, "complete", "failed")
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		requireRejected(t, again, http.StatusConflict, "fleet_runner_attempt_binding_conflict")
	default:
		requireRejected(t, again, http.StatusConflict, "fleet_reconciliation_attempt_not_current")
	}
	if after := requireAttempt(t, h.Get(final), http.StatusOK); after.Status != "succeeded" || after.Revision != final.Revision {
		t.Fatalf("a refused checkpoint changed the terminal attempt: before=%+v after=%+v", final, after)
	}
}

func testCheckpointRejectsWrongPhaseOrInactiveAttempt(t *testing.T, h LifecycleHarness) {
	plan, _, run := seedApplyRun(h, "scale")
	attempt := requireAttempt(t, h.Start(plan, run), http.StatusCreated)
	phases := lifecycle.Phases(h.Profile(), plan.RequiresDrain)
	wrongPhase := phases[len(phases)-1]
	if wrongPhase == attempt.CurrentPhase {
		t.Fatalf("fixture error: wrong phase equals the current phase")
	}
	wrong := h.Checkpoint(attempt, wrongPhase, "succeeded")
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		requireRejected(t, wrong, http.StatusConflict, "fleet_runner_attempt_binding_conflict")
	default:
		requireRejected(t, wrong, http.StatusConflict, "fleet_reconciliation_attempt_not_current")
	}

	inactivePlan, _, inactiveRun := seedApplyRun(h, "scale")
	inactive := requireAttempt(t, h.Start(inactivePlan, inactiveRun), http.StatusCreated)
	canceled := requireAttempt(t, h.Cancel(inactive, inactive.Revision, "operator requested stop"), http.StatusOK)
	afterCancel := h.Checkpoint(canceled, canceled.CurrentPhase, "succeeded")
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		requireRejected(t, afterCancel, http.StatusConflict, "fleet_runner_attempt_binding_conflict")
	default:
		requireRejected(t, afterCancel, http.StatusConflict, "fleet_reconciliation_attempt_not_current")
	}
}

func testCheckpointReplayAfterLostResponse(t *testing.T, h LifecycleHarness) {
	plan, _, run := seedApplyRun(h, "scale")
	attempt := requireAttempt(t, h.Start(plan, run), http.StatusCreated)
	first := h.Checkpoint(attempt, attempt.CurrentPhase, "succeeded")
	if first.HTTPStatus != http.StatusCreated {
		t.Fatalf("initial checkpoint was rejected: status=%d code=%q", first.HTTPStatus, first.Code)
	}
	// Simulate the executor crashing after the checkpoint committed but
	// before it observed the response: it retries the identical request.
	replay := h.Checkpoint(attempt, attempt.CurrentPhase, "succeeded")
	if replay.HTTPStatus != http.StatusOK {
		t.Fatalf("checkpoint replay after a lost response was not idempotent: status=%d code=%q", replay.HTTPStatus, replay.Code)
	}
	advanced := requireAttempt(t, h.Advance(attempt, attempt.CurrentPhase, attempt.Revision), http.StatusOK)
	if advanced.CurrentPhase == attempt.CurrentPhase {
		t.Fatalf("advance after a checkpoint replay did not progress: %+v", advanced)
	}
}

func testRecoveryLineageAndLimit(t *testing.T, h LifecycleHarness) {
	plan, dispatch, run := seedApplyRun(h, "scale")
	root := requireAttempt(t, h.Start(plan, run), http.StatusCreated)
	stop := stopEvidenceFor(root.ID, dispatch.RunID, 1)
	recoverRun := RunRef{RunID: dispatch.RunID + 1, RunAttempt: 1, Intent: "recover", SHA: dispatch.ApprovedHeadSHA}
	recovered := requireAttempt(t, h.Recover(plan, recoverRun, stop), http.StatusCreated)
	if recovered.RetryOf != root.ID || recovered.RootAttemptID != root.RootAttemptID || recovered.Attempt != root.Attempt+1 {
		t.Fatalf("recovery lineage is wrong: root=%+v recovered=%+v", root, recovered)
	}
	if err := lifecycle.ValidateLineage([]fleet.RunnerAttempt{*root, *recovered}); err != nil {
		t.Fatalf("recovery chain failed the shared lineage validator: %v", err)
	}
	rootAfter := requireAttempt(t, h.Get(root), http.StatusOK)
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		// D7: a superseded source becomes canceled on PG legacy.
		if rootAfter.Status != "canceled" {
			t.Fatalf("D7: superseded source did not become canceled: %+v", rootAfter)
		}
	default:
		// D7: a superseded source becomes failed on etcd.
		if rootAfter.Status != "failed" {
			t.Fatalf("D7: superseded source did not become failed: %+v", rootAfter)
		}
	}

	secondStop := stopEvidenceFor(recovered.ID, dispatch.RunID+1, 1)
	secondRecoverRun := RunRef{RunID: dispatch.RunID + 2, RunAttempt: 1, Intent: "recover", SHA: dispatch.ApprovedHeadSHA}
	second := h.Recover(plan, secondRecoverRun, secondStop)
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		// D5: PG legacy allows unlimited sequential recoveries.
		again := requireAttempt(t, second, http.StatusCreated)
		if again.RetryOf != recovered.ID {
			t.Fatalf("second legacy recovery did not chain off the first: %+v", again)
		}
	default:
		// D5: etcd allows exactly one recovery successor. The harness supplies
		// a stop observation (secondStop != nil), so this refusal comes from
		// the handler's len(attempts) != 1 gate, which reports the limit under
		// the stop-proof code; the store's fleet_runner_attempt_recovery_limit
		// is not reachable over HTTP.
		requireRejected(t, second, http.StatusConflict, "fleet_runner_attempt_external_stop_unproven")
		if second.Attempt != nil {
			t.Fatalf("etcd returned an attempt for a refused second recovery: %+v", second.Attempt)
		}
		if after := requireAttempt(t, h.Get(recovered), http.StatusOK); after.Status != recovered.Status || after.Revision != recovered.Revision {
			t.Fatalf("a refused second recovery superseded the first successor: before=%+v after=%+v", recovered, after)
		}
	}
}

func testRecoveryStopProofPerProfile(t *testing.T, h LifecycleHarness) {
	plan, dispatch, run := seedApplyRun(h, "scale")
	root := requireAttempt(t, h.Start(plan, run), http.StatusCreated)
	recoverRun := RunRef{RunID: dispatch.RunID + 1, RunAttempt: 1, Intent: "recover", SHA: dispatch.ApprovedHeadSHA}
	unproven := h.Recover(plan, recoverRun, nil)
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		// D6 / Q11 (plan.md §5, recorded as a gap on unregistered targets,
		// not fixed by this plan): legacy recovery requires no server-observed
		// predecessor stop proof.
		recovered := requireAttempt(t, unproven, http.StatusCreated)
		if recovered.RetryOf != root.ID {
			t.Fatalf("unproven legacy recovery did not chain off the source attempt: %+v", recovered)
		}
	default:
		// D6: etcd refuses recovery without a server-observed stop proof...
		requireRejected(t, unproven, http.StatusConflict, "fleet_runner_attempt_external_stop_unproven")
		// ...and accepts it once that proof is supplied.
		proven := stopEvidenceFor(root.ID, dispatch.RunID, 1)
		recovered := requireAttempt(t, h.Recover(plan, recoverRun, proven), http.StatusCreated)
		if recovered.RetryOf != root.ID {
			t.Fatalf("proven etcd recovery did not chain off the source attempt: %+v", recovered)
		}
	}
}

func testReadsDoNotWriteExceptLegacyExpiry(t *testing.T, h LifecycleHarness) {
	plan, _, run := seedApplyRun(h, "scale")
	attempt := requireAttempt(t, h.Start(plan, run), http.StatusCreated)
	before := requireAttempt(t, h.Get(attempt), http.StatusOK)
	if before.Revision != attempt.Revision {
		t.Fatalf("an ordinary read mutated the attempt: before=%d after=%d", attempt.Revision, before.Revision)
	}
	h.ExpireAttempt(attempt)
	expired := requireAttempt(t, h.Get(attempt), http.StatusOK)
	if expired.Status != "abandoned" {
		t.Fatalf("an expired attempt was not reported abandoned: %+v", expired)
	}
	again := requireAttempt(t, h.Get(attempt), http.StatusOK)
	switch h.Profile() {
	case lifecycle.PostgresLegacy:
		// D8: PG legacy writes abandoned durably, exactly once, on read.
		if expired.Revision != attempt.Revision+1 || again.Revision != expired.Revision {
			t.Fatalf("D8: legacy expiry-on-read did not write exactly once: before=%d expired=%d again=%d", attempt.Revision, expired.Revision, again.Revision)
		}
	default:
		// D8: every other profile only projects expiry; the read never writes.
		if expired.Revision != attempt.Revision || again.Revision != attempt.Revision {
			t.Fatalf("D8: a projected read also wrote: before=%d expired=%d again=%d", attempt.Revision, expired.Revision, again.Revision)
		}
	}
}
