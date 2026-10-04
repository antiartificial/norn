// DeriveStatus (docs/v3/fleet-controller/plan.md §2.3, WP11) is the pure
// function that turns one Input snapshot into a Status. It performs no I/O
// and takes `now` as a parameter: every "fresh" decision is a function of
// in.Now, never of the wall clock. The same Input always produces a
// byte-identical Status (every collection DeriveStatus itself builds --
// Conditions, AttemptIDs -- is sorted).
//
// Checkpoint evidence never makes a readiness condition True: the five
// observation-backed conditions below (ProviderStateKnown, NodesEnrolled,
// RuntimeReady, IngressReady, DriftDetected) read only in.LatestObservations
// and in.TiedObservations, never in.Holder or in.Fence. Execution facts
// (Fence/Holder) only ever drive Status.Active and the ReconciliationRequired/
// NextAction case for an in-progress or uncertain mutation.
package controller

import (
	"sort"
	"strconv"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
)

// Condition type names (plan.md §2.3).
const (
	ConditionProviderStateKnown     = "ProviderStateKnown"
	ConditionNodesEnrolled          = "NodesEnrolled"
	ConditionRuntimeReady           = "RuntimeReady"
	ConditionIngressReady           = "IngressReady"
	ConditionDriftDetected          = "DriftDetected"
	ConditionReconciliationRequired = "ReconciliationRequired"
)

// Condition.Status values.
const (
	StatusTrue    = "True"
	StatusFalse   = "False"
	StatusUnknown = "Unknown"
)

// Reasons shared by every observation-backed condition's Unknown branch:
// no observation has ever arrived for the source, or the latest one is
// older than ObservationFreshness relative to the evaluation time (derive
// time here; DowngradeStale applies the same rule again at read time, M9).
const (
	ReasonNoObservation    = "NoObservation"
	ReasonObservationStale = "ObservationStale"
)

// ProviderStateKnown reasons.
const (
	ReasonProviderFresh          = "Fresh"
	ReasonProviderError          = "ProviderError"
	ReasonTargetIdentityMismatch = "TargetIdentityMismatch"
)

// NodesEnrolled reasons.
const (
	ReasonEnrolledMatchesExpected = "EnrolledMatchesExpected"
	ReasonEnrolledBelowExpected   = "EnrolledBelowExpected"
	ReasonCountsAbsent            = "CountsAbsent"
)

// RuntimeReady / IngressReady reasons.
const (
	ReasonObservedReady    = "ObservedReady"
	ReasonObservedNotReady = "ObservedNotReady"
)

// DriftDetected reasons.
const (
	ReasonObservedDrift   = "ObservedDrift"
	ReasonNoDriftObserved = "NoDriftObserved"
)

// ReconciliationRequired reasons (plan.md §2.3's table).
const (
	ReasonDesiredNotApplied   = "DesiredNotApplied"
	ReasonDriftReconciliation = "DriftDetected"
	ReasonOutcomeUncertain    = "OutcomeUncertain"
	ReasonUpToDate            = "UpToDate"
	ReasonExecutionInProgress = "ExecutionInProgress"
)

// ReasonGenerationOutOfHistory is AppliedRef.Reason when the fence's last
// succeeded release's bound commit no longer has a retained desired-history
// entry and DesiredHistory is full (M11): the commit is still shown, with
// LastAppliedGeneration=0. ReasonCommitNotDesired is the same case with a
// history that never dropped an entry, so the commit was never desired (a
// plan executed outside the desired revision).
const (
	ReasonGenerationOutOfHistory = "GenerationOutOfHistory"
	ReasonCommitNotDesired       = "CommitNotDesired"
)

// NextAction values, in priority order (plan.md §2.3). dispatch_plan and
// review_plan are cut (M11/review disposition): there is no automatic
// repair, so DesiredNotApplied alone never produces an action here.
const (
	NextActionResolveUncertainOutcome = "resolve_uncertain_outcome"
	NextActionAwaitRunner             = "await_runner"
	NextActionRefreshObservations     = "refresh_observations"
	NextActionInvestigateDrift        = "investigate_drift"
	NextActionNone                    = "none"
)

// Observation fact keys DeriveStatus reads. ObservationFacts is otherwise
// free-form (types.go); this is the schema DeriveStatus itself defines:
// ProviderStateKnown and NodesEnrolled read the "provider" source,
// DriftDetected reads "state", and RuntimeReady/IngressReady read "runtime".
const (
	factTargetID      = "targetId"
	factError         = "error"
	factEnrolledNodes = "enrolledNodes"
	factExpectedNodes = "expectedNodes"
	factReady         = "ready"
	factIngressReady  = "ingressReady"
	factDrift         = "drift"
)

// DeriveStatus is the pure derive function WP10's ReconcileFleetResource
// calls (plan.md §2.3's "Reconcile write (M9)").
func DeriveStatus(in Input) Status {
	providerStateKnown := deriveProviderStateKnown(in)
	nodesEnrolled := deriveNodesEnrolled(in)
	runtimeReady := deriveRuntimeLike(in, ConditionRuntimeReady, factReady)
	ingressReady := deriveRuntimeLike(in, ConditionIngressReady, factIngressReady)
	driftDetected := deriveDriftDetected(in)

	active, occupancy := deriveActive(in)
	lastAppliedGeneration, lastApplied := deriveLastApplied(in)

	desiredApplied := in.Resource.Desired.Generation == 0 ||
		(lastApplied != nil && in.Resource.Desired.CommitSHA != "" && lastApplied.CommitSHA == in.Resource.Desired.CommitSHA)
	reconciliationRequired := deriveReconciliationRequired(occupancy, desiredApplied, driftDetected, in.Now)
	if occupancy == lifecycle.OccupancyUncertain && active != nil {
		reconciliationRequired.Message = uncertainMessage(in, active)
	}

	conditions := []Condition{providerStateKnown, nodesEnrolled, runtimeReady, ingressReady, driftDetected, reconciliationRequired}
	sort.Slice(conditions, func(i, j int) bool { return conditions[i].Type < conditions[j].Type })

	return Status{
		ObservedGeneration:    in.Resource.Desired.Generation,
		LastAppliedGeneration: lastAppliedGeneration,
		LastApplied:           lastApplied,
		Active:                active,
		Conditions:            conditions,
		NextAction:            deriveNextAction(occupancy, conditions, driftDetected),
		EvaluatedAt:           in.Now,
	}
}

// DowngradeStale re-applies the freshness rule to an already-derived Status
// at read time (M9): any observation-backed condition whose ObservedAt is now
// older than ObservationFreshness is downgraded to Unknown/ObservationStale,
// whatever it held at derive time. It never changes a condition that is
// already Unknown and never upgrades one. It then re-applies the two derive
// rules that depend on those conditions: a ReconciliationRequired resting on
// the drift reading (UpToDate or DriftDetected) becomes Unknown once that
// reading is Unknown, and a NextAction below refresh_observations in the
// priority order becomes refresh_observations. Occupancy-driven values
// (OutcomeUncertain, ExecutionInProgress, DesiredNotApplied and their
// actions) are left as derived. It is pure and deterministic.
func DowngradeStale(status Status, now time.Time) Status {
	next := status
	conditions := make([]Condition, len(status.Conditions))
	copy(conditions, status.Conditions)
	anyUnknown := false
	drift := Condition{Status: StatusUnknown, Reason: ReasonNoObservation}
	for i, c := range conditions {
		if c.Type == ConditionReconciliationRequired {
			continue
		}
		if c.Status != StatusUnknown && (c.ObservedAt.IsZero() || !isFresh(c.ObservedAt, now)) {
			conditions[i].Status = StatusUnknown
			conditions[i].Reason = ReasonObservationStale
		}
		if conditions[i].Status == StatusUnknown {
			anyUnknown = true
		}
		if c.Type == ConditionDriftDetected {
			drift = conditions[i]
		}
	}
	for i, c := range conditions {
		if c.Type == ConditionReconciliationRequired && drift.Status == StatusUnknown &&
			(c.Reason == ReasonUpToDate || c.Reason == ReasonDriftReconciliation) {
			conditions[i].Status, conditions[i].Reason, conditions[i].Message = StatusUnknown, drift.Reason, ""
		}
	}
	if anyUnknown && (next.NextAction == NextActionNone || next.NextAction == NextActionInvestigateDrift) {
		next.NextAction = NextActionRefreshObservations
	}
	next.Conditions = conditions
	return next
}

// --- observation plumbing -------------------------------------------------

// observationRows returns the source's watermark observation (if any) and
// the full tied set (the watermark row plus every row sharing its exact
// ObservedAt, M10) that a tie-break must consider.
func observationRows(in Input, source string) (latest Observation, hasLatest bool, rows []Observation) {
	latest, hasLatest = in.LatestObservations[source]
	if !hasLatest {
		return latest, false, nil
	}
	rows = make([]Observation, 0, 1+len(in.TiedObservations[source]))
	rows = append(rows, latest)
	rows = append(rows, in.TiedObservations[source]...)
	return latest, true, rows
}

func isFresh(observedAt, now time.Time) bool {
	return !now.After(observedAt.Add(ObservationFreshness))
}

func factBool(o Observation, key string) (bool, bool) {
	v, ok := o.Facts[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func factNumber(o Observation, key string) (float64, bool) {
	v, ok := o.Facts[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func factString(o Observation, key string) string {
	s, _ := o.Facts[key].(string)
	return s
}

// resolveTieBool reports the fact at key across rows, with problem winning a
// tie (plan.md §2.3: "On equal ObservedAt, the failure/false value wins" --
// problem names whichever boolean value is the failure value for this
// condition, since that is not always literally false, e.g. drift=true).
//
// It also returns the deciding row, so the condition's evidence names the
// observation that actually set its value (a tied failure, not the watermark
// row it overrode).
func resolveTieBool(rows []Observation, key string, problem bool) (value, present bool, decided Observation) {
	for _, row := range rows {
		b, ok := factBool(row, key)
		if !ok {
			continue
		}
		if !present || (b == problem && value != problem) {
			present, value, decided = true, b, row
		}
	}
	return value, present, decided
}

// cite points c's evidence at the observation that decided its value.
func cite(c *Condition, row Observation) {
	c.ObservationSequence = row.Sequence
	c.EvidenceRefs = row.EvidenceRefs
}

func baseCondition(conditionType string, latest Observation, hasLatest bool) (Condition, bool) {
	c := Condition{Type: conditionType}
	if !hasLatest {
		c.Status, c.Reason = StatusUnknown, ReasonNoObservation
		return c, false
	}
	c.ObservedAt = latest.ObservedAt
	c.ObservationSequence = latest.Sequence
	c.EvidenceRefs = latest.EvidenceRefs
	return c, true
}

// --- individual conditions -------------------------------------------------

func deriveProviderStateKnown(in Input) Condition {
	latest, hasLatest, rows := observationRows(in, SourceProvider)
	c, ok := baseCondition(ConditionProviderStateKnown, latest, hasLatest)
	if !ok {
		return c
	}
	if !isFresh(latest.ObservedAt, in.Now) {
		c.Status, c.Reason = StatusUnknown, ReasonObservationStale
		return c
	}
	for _, row := range rows {
		if msg := factString(row, factError); msg != "" {
			c.Status, c.Reason, c.Message = StatusFalse, ReasonProviderError, msg
			cite(&c, row)
			return c
		}
	}
	if in.Resource.TargetID != "" {
		for _, row := range rows {
			reported := factString(row, factTargetID)
			if reported != "" && reported != in.Resource.TargetID {
				c.Status, c.Reason = StatusFalse, ReasonTargetIdentityMismatch
				c.Message = "observed target " + reported + " does not match resource target " + in.Resource.TargetID
				cite(&c, row)
				return c
			}
		}
	}
	c.Status, c.Reason = StatusTrue, ReasonProviderFresh
	return c
}

func deriveNodesEnrolled(in Input) Condition {
	latest, hasLatest, rows := observationRows(in, SourceProvider)
	c, ok := baseCondition(ConditionNodesEnrolled, latest, hasLatest)
	if !ok {
		return c
	}
	if !isFresh(latest.ObservedAt, in.Now) {
		c.Status, c.Reason = StatusUnknown, ReasonObservationStale
		return c
	}
	present, below := false, false
	for _, row := range rows {
		enrolled, ok1 := factNumber(row, factEnrolledNodes)
		expected, ok2 := factNumber(row, factExpectedNodes)
		if !ok1 || !ok2 {
			continue
		}
		if !present || (!below && enrolled < expected) {
			cite(&c, row)
		}
		present = true
		if enrolled < expected {
			below = true
		}
	}
	switch {
	case !present:
		c.Status, c.Reason = StatusUnknown, ReasonCountsAbsent
	case below:
		c.Status, c.Reason = StatusFalse, ReasonEnrolledBelowExpected
	default:
		c.Status, c.Reason = StatusTrue, ReasonEnrolledMatchesExpected
	}
	return c
}

// deriveRuntimeLike derives RuntimeReady or IngressReady: both read the
// "runtime" source, with "not ready" as the tie-break problem value.
func deriveRuntimeLike(in Input, conditionType, factKey string) Condition {
	latest, hasLatest, rows := observationRows(in, SourceRuntime)
	c, ok := baseCondition(conditionType, latest, hasLatest)
	if !ok {
		return c
	}
	if !isFresh(latest.ObservedAt, in.Now) {
		c.Status, c.Reason = StatusUnknown, ReasonObservationStale
		return c
	}
	value, present, decided := resolveTieBool(rows, factKey, false)
	if present {
		cite(&c, decided)
	}
	switch {
	case !present:
		c.Status, c.Reason = StatusUnknown, ReasonNoObservation
	case value:
		c.Status, c.Reason = StatusTrue, ReasonObservedReady
	default:
		c.Status, c.Reason = StatusFalse, ReasonObservedNotReady
	}
	return c
}

func deriveDriftDetected(in Input) Condition {
	latest, hasLatest, rows := observationRows(in, SourceState)
	c, ok := baseCondition(ConditionDriftDetected, latest, hasLatest)
	if !ok {
		return c
	}
	if !isFresh(latest.ObservedAt, in.Now) {
		c.Status, c.Reason = StatusUnknown, ReasonObservationStale
		return c
	}
	value, present, decided := resolveTieBool(rows, factDrift, true)
	if present {
		cite(&c, decided)
	}
	switch {
	case !present:
		c.Status, c.Reason = StatusUnknown, ReasonNoObservation
	case value:
		c.Status, c.Reason = StatusTrue, ReasonObservedDrift
	default:
		c.Status, c.Reason = StatusFalse, ReasonNoDriftObserved
	}
	return c
}

// --- execution snapshot, M11 linkage and the rollup -----------------------

// deriveActive builds Status.Active straight from in.Fence/in.Holder (never
// from Resource.Desired, so a desired change mid-execution leaves it
// untouched) and returns the occupancy DeriveStatus's other computations
// need. Active is nil exactly when the fence is free.
func deriveActive(in Input) (*ActiveRefs, lifecycle.Occupancy) {
	occupancy, reason := lifecycle.Outcome(in.Fence, in.Holder, in.AuthorityEpoch, in.Now)
	if !in.Fence.Held {
		return nil, occupancy
	}
	ids := make([]string, 0, len(in.Holder.Attempts))
	for _, a := range in.Holder.Attempts {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	dispatchRunID := ""
	// The bound GitHub dispatch run, never an attempt's RunnerAttemptID (a
	// per-run-attempt identity that recovery runs also carry).
	if in.HolderDispatchRunID > 0 {
		dispatchRunID = strconv.FormatInt(in.HolderDispatchRunID, 10)
	}
	return &ActiveRefs{
		PlanID:          in.Fence.HolderPlanID,
		DispatchRunID:   dispatchRunID,
		AttemptIDs:      ids,
		FenceGeneration: in.Fence.Generation,
		Occupancy:       string(occupancy),
		OccupancyReason: reason,
	}, occupancy
}

// succeededCommit finds planID's succeeded attempt (DecideRelease's succeeded
// mode requires the latest one to be succeeded) and returns the commit it
// ran. Attempt admission requires CommitSHA == the dispatch binding's
// ApprovedHeadSHA on both backends, so this is M11 step 2's commit.
func succeededCommit(planID string, attempts []fleet.RunnerAttempt) (string, bool) {
	best := fleet.RunnerAttempt{}
	found := false
	for _, a := range attempts {
		if a.PlanID != planID || a.Status != "succeeded" {
			continue
		}
		if !found || a.Attempt > best.Attempt {
			best, found = a, true
		}
	}
	return best.CommitSHA, found
}

// findDesiredEntry looks for commitSHA in the current desired revision and
// its bounded history (M11).
func findDesiredEntry(r Resource, commitSHA string) (DesiredRevision, bool) {
	if r.Desired.CommitSHA == commitSHA {
		return r.Desired, true
	}
	for _, entry := range r.DesiredHistory {
		if entry.CommitSHA == commitSHA {
			return entry, true
		}
	}
	return DesiredRevision{}, false
}

// deriveLastApplied implements M11's linkage and its historical-only rule:
// LastApplied/LastAppliedGeneration only ever advances from a succeeded
// attempt bound to a generation, and a later non-succeeded release (or a
// later bad health reading, which never reaches this function at all) never
// undoes it. in.Resource.Status is the previously persisted cache, so
// whenever the fence's current last release is not itself "succeeded" --
// including the common case where it was never released at all -- this
// simply carries that cache forward unchanged.
func deriveLastApplied(in Input) (int64, *AppliedRef) {
	prevGeneration, prevApplied := in.Resource.Status.LastAppliedGeneration, in.Resource.Status.LastApplied
	if in.Fence.LastRelease == nil || in.Fence.LastRelease.Reason != "succeeded" {
		return prevGeneration, prevApplied
	}
	planID := in.Fence.LastRelease.PlanID
	commitSHA, ok := succeededCommit(planID, in.LastReleaseAttempts)
	if !ok {
		return prevGeneration, prevApplied
	}
	if entry, found := findDesiredEntry(in.Resource, commitSHA); found {
		return entry.Generation, &AppliedRef{Generation: entry.Generation, CommitSHA: commitSHA, PlanID: planID}
	}
	reason := ReasonCommitNotDesired
	if len(in.Resource.DesiredHistory) >= DesiredHistoryLimit {
		reason = ReasonGenerationOutOfHistory
	}
	return 0, &AppliedRef{CommitSHA: commitSHA, PlanID: planID, Reason: reason}
}

// uncertainMessage names the supported, explicit ways out of an Uncertain
// holder (there is no automatic repair) and when the admin-only abandon
// release (H5/H7) becomes allowed, using DecideRelease's own baseline: the
// latest of submission start, dispatch creation and every attempt's lease
// expiry, plus AbandonMinimumAge.
func uncertainMessage(in Input, active *ActiveRefs) string {
	msg := "holder plan " + active.PlanID + " is Uncertain (" + active.OccupancyReason +
		"); resolve explicitly: recovery, terminal release with proof every run completed, or admin abandon"
	baseline := in.Holder.SubmissionStartedAt
	if in.Holder.DispatchCreatedAt.After(baseline) {
		baseline = in.Holder.DispatchCreatedAt
	}
	for _, a := range in.Holder.Attempts {
		if a.HeartbeatExpiresAt.After(baseline) {
			baseline = a.HeartbeatExpiresAt
		}
	}
	if baseline.IsZero() {
		return msg + " (abandon refused: no holder activity timestamp)"
	}
	eligibleAt := baseline.Add(lifecycle.AbandonMinimumAge)
	if in.Now.Before(eligibleAt) {
		return msg + " (abandon allowed from " + eligibleAt.UTC().Format(time.RFC3339) + ")"
	}
	return msg + " (abandon allowed now)"
}

func deriveReconciliationRequired(occupancy lifecycle.Occupancy, desiredApplied bool, drift Condition, now time.Time) Condition {
	c := Condition{Type: ConditionReconciliationRequired, ObservedAt: now}
	switch {
	case occupancy == lifecycle.OccupancyActive:
		c.Status, c.Reason = StatusUnknown, ReasonExecutionInProgress
	case occupancy == lifecycle.OccupancyUncertain:
		c.Status, c.Reason = StatusTrue, ReasonOutcomeUncertain
	case !desiredApplied:
		c.Status, c.Reason = StatusTrue, ReasonDesiredNotApplied
	case drift.Status == StatusTrue:
		c.Status, c.Reason = StatusTrue, ReasonDriftReconciliation
	case drift.Status == StatusUnknown:
		// UpToDate needs a fresh no-drift reading; a stale or missing one
		// must not read as a False "nothing to do".
		c.Status, c.Reason = StatusUnknown, drift.Reason
	default:
		c.Status, c.Reason = StatusFalse, ReasonUpToDate
	}
	return c
}

// deriveNextAction applies plan.md §2.3's fixed priority order. There is no
// automatic repair: DesiredNotApplied alone (no drift, nothing stale, no
// active or uncertain execution) falls through to "none".
func deriveNextAction(occupancy lifecycle.Occupancy, conditions []Condition, drift Condition) string {
	switch occupancy {
	case lifecycle.OccupancyUncertain:
		return NextActionResolveUncertainOutcome
	case lifecycle.OccupancyActive:
		return NextActionAwaitRunner
	}
	for _, c := range conditions {
		if c.Type != ConditionReconciliationRequired && c.Status == StatusUnknown {
			return NextActionRefreshObservations
		}
	}
	if drift.Status == StatusTrue {
		return NextActionInvestigateDrift
	}
	return NextActionNone
}
