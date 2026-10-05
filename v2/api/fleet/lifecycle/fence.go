package lifecycle

import (
	"fmt"
	"time"

	"norn/v2/api/fleet"
)

// AbandonMinimumAge is the minimum time a holder must be silent before a
// break-glass abandon release is allowed (H5, accepted 2026-10-04).
const AbandonMinimumAge = 30 * time.Minute

// RevalidationSkew is the window after any release other than
// dispatch_not_submitted during which a plan started before the release
// must be refused (Q2/M4).
const RevalidationSkew = 5 * time.Minute

// Error codes returned by the Decide* functions below. Each is the exact
// string plan.md §2.5 documents for the corresponding HTTP route; callers
// map FenceError.Code to a status and JSON body without reinterpreting it.
const (
	CodeFleetTargetExecutionOccupied             = "fleet_target_execution_occupied"
	CodeFleetTargetUnregistered                  = "fleet_target_unregistered"
	CodeFleetTargetAliasConflict                 = "fleet_target_alias_conflict"
	CodeFleetTargetAuthoritySuperseded           = "fleet_target_authority_superseded"
	CodeFleetPlanRevalidationRequired            = "fleet_plan_revalidation_required"
	CodeFleetTargetHolderAbandoned               = "fleet_target_holder_abandoned"
	CodeFleetTargetRecoveryRequiresStoppedSource = "fleet_target_recovery_requires_stopped_source"
	CodeFleetTargetRegistrationInFlight          = "fleet_target_registration_in_flight"

	// DecideRelease refusals (the fence-release route, plan.md §2.5).
	CodeFleetTargetFenceNotHeld            = "fleet_target_fence_not_held"
	CodeFleetTargetHasLiveAttempt          = "fleet_target_has_live_attempt"
	CodeFleetTargetTerminalProofIncomplete = "fleet_target_terminal_proof_incomplete"
	CodeFleetTargetAbandonTooSoon          = "fleet_target_abandon_too_soon"
	CodeFleetTargetAbandonSnapshotRequired = "fleet_target_abandon_snapshot_required"
	CodeFleetTargetReleaseEvidenceMismatch = "fleet_target_release_evidence_mismatch"
)

// FenceError reports a fence or epoch decision's refusal by its stable code.
type FenceError struct {
	Code   string
	Reason string
}

func (e *FenceError) Error() string {
	if e.Reason == "" {
		return e.Code
	}
	return e.Reason
}

// Release records one past release of a target's fence. Reason is one of
// "succeeded", "dispatch_not_submitted", "released_terminal" or "abandoned".
type Release struct {
	PlanID string
	Reason string
	At     time.Time
}

// FenceFacts is the durable state of one target's mutual-exclusion fence. It
// stores no attempt ID (m18): holder attempts are always re-derived from the
// attempt store. A zero-value FenceFacts (Held=false) means the target has
// never been acquired.
type FenceFacts struct {
	TargetID          string
	Generation        int64
	Held              bool
	HolderPlanID      string
	HolderNonceSHA256 string
	AuthorityEpoch    int64
	LastRelease       *Release
	Revision          int64
}

// HolderFacts is everything about the current holder plan that Outcome and
// DecideRelease need, re-derived fresh by the caller rather than cached.
type HolderFacts struct {
	// DispatchState is the PG dispatch_state, or etcd's "prepared"/"bound".
	DispatchState       string
	DispatchCreatedAt   time.Time
	SubmissionStartedAt time.Time
	Attempts            []fleet.RunnerAttempt
	Abandoned           bool
}

// Occupancy is the derived, admission-never-reads display cache (M3).
type Occupancy string

const (
	OccupancyFree      Occupancy = "Free"
	OccupancyActive    Occupancy = "Active"
	OccupancyUncertain Occupancy = "Uncertain"
)

// Occupancy reasons, used only when Occupancy is Uncertain.
const (
	ReasonDispatchSubmissionUnresolved  = "DispatchSubmissionUnresolved"
	ReasonNoLiveAttempt                 = "NoLiveAttempt"
	ReasonAttemptTerminalWithoutSuccess = "AttemptTerminalWithoutSuccess"
	ReasonAuthoritySuperseded           = "AuthoritySuperseded"
)

// latestAttempt returns the attempt with the highest Attempt number,
// projected to now so an expired lease is reported abandoned (D8/ProjectExpiry).
func latestAttempt(attempts []fleet.RunnerAttempt, now time.Time) (fleet.RunnerAttempt, bool) {
	if len(attempts) == 0 {
		return fleet.RunnerAttempt{}, false
	}
	latest := attempts[0]
	for _, a := range attempts[1:] {
		if a.Attempt > latest.Attempt {
			latest = a
		}
	}
	return ProjectExpiry(latest, now), true
}

func isLive(a fleet.RunnerAttempt) bool {
	return a.Status == "queued" || a.Status == "running"
}

// Outcome derives occupancy from fence and holder facts (M3): Active holds
// iff the fence is held and a live, unexpired holder attempt exists; every
// other held state is Uncertain with a specific reason. Heartbeat expiry
// alone never frees a target — it only ever produces Uncertain, never Free.
func Outcome(f FenceFacts, h HolderFacts, currentEpoch int64, now time.Time) (Occupancy, string) {
	if !f.Held {
		return OccupancyFree, ""
	}
	if f.AuthorityEpoch < currentEpoch {
		return OccupancyUncertain, ReasonAuthoritySuperseded
	}
	if h.DispatchState != "" && h.DispatchState != "dispatched" && h.DispatchState != "bound" {
		return OccupancyUncertain, ReasonDispatchSubmissionUnresolved
	}
	latest, ok := latestAttempt(h.Attempts, now)
	if !ok {
		return OccupancyUncertain, ReasonNoLiveAttempt
	}
	if isLive(latest) {
		return OccupancyActive, ""
	}
	return OccupancyUncertain, ReasonAttemptTerminalWithoutSuccess
}

// DecideAcquire is the fence transition behind dispatch submit and rerun
// submit. The fence must be free, or already held by the same plan and
// nonce. A same-holder acquire (the rerun path, M5) additionally requires
// the fence to be at the current authority epoch: it never silently adopts a
// newer epoch, since only bind re-binds (M13) and an epoch change must
// always come with a generation bump. Revalidation (M4) refuses a plan
// started too soon after any release other than dispatch_not_submitted.
// Abandonment (B1) is not visible here: callers must check the abandoned
// plans record in the same transaction before calling.
func DecideAcquire(f FenceFacts, planID, nonceSHA256 string, planStartedAt time.Time, currentEpoch int64) (FenceFacts, error) {
	sameHolder := f.Held && f.HolderPlanID == planID && f.HolderNonceSHA256 == nonceSHA256
	if f.Held && !sameHolder {
		return f, &FenceError{Code: CodeFleetTargetExecutionOccupied}
	}
	if sameHolder && f.AuthorityEpoch != currentEpoch {
		return f, &FenceError{Code: CodeFleetTargetAuthoritySuperseded}
	}
	if f.LastRelease != nil && f.LastRelease.Reason != "dispatch_not_submitted" {
		if planStartedAt.Before(f.LastRelease.At.Add(RevalidationSkew)) {
			return f, &FenceError{Code: CodeFleetPlanRevalidationRequired}
		}
	}
	next := f
	if !sameHolder {
		next.Generation = f.Generation + 1
	}
	next.Held = true
	next.HolderPlanID = planID
	next.HolderNonceSHA256 = nonceSHA256
	next.AuthorityEpoch = currentEpoch
	next.Revision = f.Revision + 1
	return next, nil
}

// DecideBind is the fence transition behind both the first attempt bind and
// recovery bind (M13: neither is stranded by an epoch advance). The fence
// must already be held by this plan and nonce. If it was acquired or last
// bound under an older epoch, bind re-binds it: Generation+1, current epoch.
func DecideBind(f FenceFacts, planID, nonceSHA256 string, currentEpoch int64) (FenceFacts, error) {
	if !f.Held || f.HolderPlanID != planID || f.HolderNonceSHA256 != nonceSHA256 {
		return f, &FenceError{Code: CodeFleetTargetExecutionOccupied}
	}
	next := f
	if f.AuthorityEpoch < currentEpoch {
		next.Generation = f.Generation + 1
		next.AuthorityEpoch = currentEpoch
	}
	next.Revision = f.Revision + 1
	return next, nil
}

// EvidenceKind names the write DecideEvidenceWrite is gating.
type EvidenceKind string

const (
	EvidenceHeartbeat         EvidenceKind = "heartbeat"
	EvidenceAdvance           EvidenceKind = "advance"
	EvidenceSuccessCheckpoint EvidenceKind = "success-checkpoint"
	EvidenceFailedCheckpoint  EvidenceKind = "failed-checkpoint"
	EvidenceCancel            EvidenceKind = "cancel"
)

// DecideEvidenceWrite gates heartbeat, advance and checkpoint writes against
// the fence's authority epoch (Q10). An unregistered target (f.TargetID=="")
// behaves exactly as before this change. Once the fence's recorded epoch
// falls behind currentEpoch, heartbeat, advance and a succeeded checkpoint
// are refused; a failed checkpoint or a cancel is still allowed, since both
// only ever narrow what the superseded attempt can do.
func DecideEvidenceWrite(f FenceFacts, planID string, currentEpoch int64, kind EvidenceKind) error {
	_ = planID
	if f.TargetID == "" || f.AuthorityEpoch >= currentEpoch {
		return nil
	}
	if kind == EvidenceFailedCheckpoint || kind == EvidenceCancel {
		return nil
	}
	return &FenceError{Code: CodeFleetTargetAuthoritySuperseded}
}

// ReleaseMode names why a fence is being freed.
type ReleaseMode string

const (
	ReleaseModeSucceeded            ReleaseMode = "succeeded"
	ReleaseModeDispatchNotSubmitted ReleaseMode = "dispatch_not_submitted"
	ReleaseModeTerminal             ReleaseMode = "terminal"
	ReleaseModeAbandon              ReleaseMode = "abandon"
)

// TerminalProof is the GitHub evidence, gathered by the WP5 observers, that
// a terminal or abandon release rests on (B1/B2). The caller fills it only
// from observations it actually made; DecideRelease checks that it covers
// every run the holder ever used.
type TerminalProof struct {
	// BoundApplyRunCompleted is true when the dispatch's bound apply run is
	// observed completed (ObserveApplyRunByNonceHash).
	BoundApplyRunCompleted bool
	// CompletedRunnerAttemptIDs holds the RunnerAttemptID
	// ("github-actions:<repo>:<runID>:<runAttempt>") of every run attempt
	// observed completed (the apply run, or ObserveRecoverRun). Terminal
	// release requires every holder attempt's RunnerAttemptID to be here.
	CompletedRunnerAttemptIDs map[string]bool
	// ListingSnapshotSHA256 is the digest of the GitHub run listing snapshot
	// (including "absent") stored in the release operation's payload.
	// Abandon requires it.
	ListingSnapshotSHA256 string
}

// DecideRelease's refusals. Each is a *FenceError carrying a stable Code, so
// callers map it like every other fence refusal; errors.Is and == against
// these sentinels keep working because DecideRelease returns them as-is.
var (
	// ErrFleetTargetNotHeld is returned by DecideRelease when the fence is
	// already free.
	ErrFleetTargetNotHeld error = &FenceError{Code: CodeFleetTargetFenceNotHeld, Reason: "fleet target fence is not held"}
	// ErrFleetTargetHasLiveAttempt is returned by DecideRelease when a live,
	// unexpired holder attempt still exists.
	ErrFleetTargetHasLiveAttempt error = &FenceError{Code: CodeFleetTargetHasLiveAttempt, Reason: "fleet target fence has a live holder attempt"}
	// ErrFleetTargetTerminalProofRequired is returned by DecideRelease for
	// mode terminal without a satisfied TerminalProof.
	ErrFleetTargetTerminalProofRequired error = &FenceError{Code: CodeFleetTargetTerminalProofIncomplete, Reason: "fleet target terminal release requires proof every run completed"}
	// ErrFleetTargetAbandonTooSoon is returned by DecideRelease for mode
	// abandon before AbandonMinimumAge has elapsed.
	ErrFleetTargetAbandonTooSoon error = &FenceError{Code: CodeFleetTargetAbandonTooSoon, Reason: "fleet target abandon release requires the minimum age"}
	// ErrFleetTargetAbandonSnapshotRequired is returned by DecideRelease for
	// mode abandon without a GitHub listing snapshot.
	ErrFleetTargetAbandonSnapshotRequired error = &FenceError{Code: CodeFleetTargetAbandonSnapshotRequired, Reason: "fleet target abandon release requires a GitHub listing snapshot"}
	// ErrFleetTargetReleaseEvidenceMismatch is returned by DecideRelease
	// when the holder's attempts contradict the release mode: succeeded
	// without a succeeded latest attempt, or dispatch_not_submitted after
	// an attempt registered.
	ErrFleetTargetReleaseEvidenceMismatch error = &FenceError{Code: CodeFleetTargetReleaseEvidenceMismatch, Reason: "fleet target release mode contradicts the holder's attempts"}
)

// releaseReason maps a release mode to the durable last_release_reason
// plan.md §2.1b names.
var releaseReason = map[ReleaseMode]string{
	ReleaseModeSucceeded:            "succeeded",
	ReleaseModeDispatchNotSubmitted: "dispatch_not_submitted",
	ReleaseModeTerminal:             "released_terminal",
	ReleaseModeAbandon:              "abandoned",
}

// DecideRelease frees f's fence. Every mode first requires that no live,
// unexpired holder attempt exists (a live attempt means execution may still
// be running, so freeing the fence now would double-admit). Heartbeat expiry
// alone never frees anything: every mode is an explicit transition.
//   - succeeded: the latest holder attempt is succeeded (callers pass the
//     attempts as written by the advance-to-complete in the same txn).
//   - dispatch_not_submitted: no attempt was ever registered. It is not an
//     execution, so an earlier real release stays the recorded LastRelease
//     and keeps driving revalidation (M4).
//   - terminal: proof that the bound apply run and every holder run attempt
//     completed (B2).
//   - abandon: a GitHub listing snapshot, and AbandonMinimumAge since the
//     holder's last activity (submission or dispatch creation, or any
//     attempt's heartbeat lease expiry, whichever is latest).
func DecideRelease(f FenceFacts, h HolderFacts, mode ReleaseMode, proof *TerminalProof, now time.Time) (FenceFacts, error) {
	if !f.Held {
		return f, ErrFleetTargetNotHeld
	}
	latest, hasAttempt := latestAttempt(h.Attempts, now)
	if hasAttempt && isLive(latest) {
		return f, ErrFleetTargetHasLiveAttempt
	}
	switch mode {
	case ReleaseModeSucceeded:
		if !hasAttempt || latest.Status != "succeeded" {
			return f, ErrFleetTargetReleaseEvidenceMismatch
		}
	case ReleaseModeDispatchNotSubmitted:
		if len(h.Attempts) > 0 {
			return f, ErrFleetTargetReleaseEvidenceMismatch
		}
	case ReleaseModeTerminal:
		if proof == nil || !proof.BoundApplyRunCompleted {
			return f, ErrFleetTargetTerminalProofRequired
		}
		for _, a := range h.Attempts {
			if a.RunnerAttemptID == "" || !proof.CompletedRunnerAttemptIDs[a.RunnerAttemptID] {
				return f, ErrFleetTargetTerminalProofRequired
			}
		}
	case ReleaseModeAbandon:
		if proof == nil || proof.ListingSnapshotSHA256 == "" {
			return f, ErrFleetTargetAbandonSnapshotRequired
		}
		baseline := h.SubmissionStartedAt
		if h.DispatchCreatedAt.After(baseline) {
			baseline = h.DispatchCreatedAt
		}
		for _, a := range h.Attempts {
			if a.HeartbeatExpiresAt.After(baseline) {
				baseline = a.HeartbeatExpiresAt
			}
		}
		if baseline.IsZero() || now.Sub(baseline) < AbandonMinimumAge {
			return f, ErrFleetTargetAbandonTooSoon
		}
	default:
		return f, fmt.Errorf("fleet fence: unknown release mode %q", mode)
	}
	next := f
	if mode != ReleaseModeDispatchNotSubmitted || f.LastRelease == nil || f.LastRelease.Reason == releaseReason[ReleaseModeDispatchNotSubmitted] {
		next.LastRelease = &Release{PlanID: f.HolderPlanID, Reason: releaseReason[mode], At: now}
	}
	next.Held = false
	next.HolderPlanID = ""
	next.HolderNonceSHA256 = ""
	next.Revision = f.Revision + 1
	return next, nil
}
