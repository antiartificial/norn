// Package controller holds the Fleet resource's durable shape: the desired
// revision, the derived status cache and the observation stream that feeds
// it (docs/v3/fleet-controller/plan.md §2.3). Storage lives in
// store/fleet_resources.go (PG, WP10) and etcdstore/v3_fleet_resources.go
// (etcd, WP10); the pure derivation function (controller.DeriveStatus) and
// the reconciler are later work packages (WP11/WP12) and are not defined
// here. This package is pure Go: besides fleet and model (which
// fleet/lifecycle already allows) it also imports fleet/lifecycle for the
// fence and holder facts a reconcile read gathers.
package controller

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
)

// SchemaVersion is the Resource's stored and wire schema version.
const SchemaVersion = "norn.fleet-resource/v1"

// DesiredHistoryLimit bounds Resource.DesiredHistory (plan.md §2.3).
const DesiredHistoryLimit = 20

// ObservationFreshness is the constant window (Q5, accepted 2026-10-04) past
// which a condition derived from an observation becomes Unknown/
// ObservationStale, both at derive time and at read time (M9).
const ObservationFreshness = 15 * time.Minute

// ObservationLimit bounds retained observations per resource ("bounded
// storage"). Pruning never deletes a row any watermark still references
// (M10).
const ObservationLimit = 100

// MaxObservationFactsBytes and MaxObservationEvidenceRefs are the per-
// observation bounds plan.md §2.3's "Bounds" paragraph lists.
const (
	MaxObservationFactsBytes   = 16 * 1024
	MaxObservationEvidenceRefs = 20
)

// MaxObservationStringBytes bounds each evidence ref and the reporter, and
// MaxDesiredStringBytes each DesiredRevision string, so the resource
// document and every observation stay bounded in size.
const (
	MaxObservationStringBytes = 512
	MaxDesiredStringBytes     = 512
)

// MaxWatermarkTies bounds how many further observations with exactly the
// watermark's ObservedAt are also applied (plan.md §2.3: "On equal
// ObservedAt, the failure/false value wins" -- so derive must see every tied
// row, not just the first one stored). A tie beyond this bound is stored
// unapplied. Tied rows are watermark-referenced and never pruned.
const MaxWatermarkTies = 4

// Verification values for DesiredRevision (Q8, accepted 2026-10-04).
const (
	VerificationGitHubMergedPlan = "github-merged-plan"
	VerificationOperatorDeclared = "operator-declared"
)

// Observation source names plan.md §2.3 uses for Watermarks' keys.
const (
	SourceProvider = "provider"
	SourceState    = "state"
	SourceRuntime  = "runtime"
)

// Resource is the durable Fleet resource: one per Fleet target's managed
// infrastructure. Status is a derived cache that admission never reads
// (mirroring §2.2's fence "lock, not a ledger" rule): it stores no second
// operation ledger -- Active below only ever names plan, dispatch and
// attempt IDs that already live in the target fence, dispatch and attempt
// stores, and ReconcileFleetResource re-derives them fresh on every write.
type Resource struct {
	SchemaVersion       string
	Name                string
	TargetID            string
	Desired             DesiredRevision
	DesiredHistory      []DesiredRevision
	Policy              ApprovalPolicy
	Watermarks          map[string]Watermark
	Status              Status
	Revision            int64
	AuthorityEpoch      int64
	ObservationSequence int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// DesiredRevision is one accepted desired state (Q8): operator-declared and
// tied to a merged plan's approved commit through ResolveApprovedPlan, or
// explicitly operator-declared when the GitHub App is not configured. A
// pending proposal never becomes this by itself -- only a successful
// POST .../desired writes one.
type DesiredRevision struct {
	Generation   int64
	PlanID       string
	CommitSHA    string
	Repository   string
	Verification string
	AcceptedAt   time.Time
	AcceptedBy   string
}

// Watermark is the latest *applied* observation's identity for one source
// (M10): {ObservedAt, Sequence}. TiedSequences lists further applied
// observations whose ObservedAt equals the watermark's exactly (bounded by
// MaxWatermarkTies). Pruning never deletes a fleet_observations row a
// watermark names, tied rows included.
type Watermark struct {
	ObservedAt    time.Time
	Sequence      int64
	TiedSequences []int64 `json:",omitempty"`
}

// Sequences returns every observation sequence w references.
func (w Watermark) Sequences() []int64 {
	return append([]int64{w.Sequence}, w.TiedSequences...)
}

// ApplyObservation is the one monotonicity rule both backends use (M10):
// an observation applies iff its ObservedAt is strictly after the source's
// watermark, or equal to it while fewer than MaxWatermarkTies ties are
// recorded. It returns whether the observation applied and the watermark to
// store; an unapplied observation leaves the watermark unchanged.
func ApplyObservation(current Watermark, hasWatermark bool, observedAt time.Time, sequence int64) (bool, Watermark) {
	switch {
	case !hasWatermark || observedAt.After(current.ObservedAt):
		return true, Watermark{ObservedAt: observedAt, Sequence: sequence}
	case observedAt.Equal(current.ObservedAt) && len(current.TiedSequences) < MaxWatermarkTies:
		next := current
		next.TiedSequences = append(append([]int64(nil), current.TiedSequences...), sequence)
		return true, next
	default:
		return false, current
	}
}

// ValidSource reports whether source is one of the three plan.md §2.3
// sources. Restricting it keeps Watermarks (and so the unprunable rows it
// pins) bounded at three entries.
func ValidSource(source string) bool {
	return source == SourceProvider || source == SourceState || source == SourceRuntime
}

// ObservationWithinBounds applies plan.md §2.3's
// ReceivedAt-24h <= ObservedAt <= ReceivedAt+1m bound: a reporter whose clock
// runs ahead can pin its source's watermark at most one minute past server
// time.
func ObservationWithinBounds(observedAt, receivedAt time.Time) bool {
	return !observedAt.Before(receivedAt.Add(-24*time.Hour)) && !observedAt.After(receivedAt.Add(time.Minute))
}

// ValidResourceName reports whether name is usable as both a PG key and an
// etcd key segment (no "/", so one resource's observation prefix can never
// contain another's).
func ValidResourceName(name string) bool {
	return name != "" && len(name) <= 253 && !strings.Contains(name, "/")
}

var commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidateDesired enforces DesiredRevision's shape at the storage boundary
// (Q8): a known Verification, a full lowercase commit SHA, an accepting
// principal, and -- for github-merged-plan -- the plan and repository the
// approved commit was resolved from. Storage cannot itself call
// ResolveApprovedPlan; the caller (WP13) must, and must never pass a pending
// proposal's head as github-merged-plan.
func ValidateDesired(next DesiredRevision) error {
	switch next.Verification {
	case VerificationGitHubMergedPlan:
		if next.PlanID == "" || next.Repository == "" {
			return fmt.Errorf("github-merged-plan desired revision requires planId and repository")
		}
	case VerificationOperatorDeclared:
	default:
		return fmt.Errorf("desired revision verification %q is not recognized", next.Verification)
	}
	if !commitSHAPattern.MatchString(next.CommitSHA) {
		return fmt.Errorf("desired revision commitSha must be a 40-character lowercase hex SHA")
	}
	if next.AcceptedBy == "" || next.AcceptedAt.IsZero() {
		return fmt.Errorf("desired revision requires acceptedBy and acceptedAt")
	}
	for _, value := range []string{next.PlanID, next.Repository, next.AcceptedBy} {
		if len(value) > MaxDesiredStringBytes {
			return fmt.Errorf("desired revision field exceeds %d bytes", MaxDesiredStringBytes)
		}
	}
	return nil
}

// ValidateObservationStrings bounds evidence refs and the reporter.
func ValidateObservationStrings(evidenceRefs []string, reporter string) bool {
	if len(evidenceRefs) > MaxObservationEvidenceRefs || len(reporter) > MaxObservationStringBytes {
		return false
	}
	for _, ref := range evidenceRefs {
		if len(ref) > MaxObservationStringBytes {
			return false
		}
	}
	return true
}

// ApprovalPolicy is server-derived and reported for operator visibility. It
// never gates anything itself; it only echoes which checks actually ran.
type ApprovalPolicy struct {
	ProtectedBranch       bool
	MergedPullRequest     bool
	PlanWorkflowSucceeded bool
	BoundPlanDigest       bool
	AuthorizedDispatch    bool
	OwnerApprovalEnvelope bool
	EnvironmentReviewers  string // "not_enforced"
	IndependentReview     string // "not_enforced"
}

// AppliedRef names the commit the fence's last succeeded release ran (M11),
// the plan that ran it, and the retained desired entry with that CommitSHA.
// Reason is empty when such an entry was found. Otherwise Generation is 0
// and Reason says why: ReasonGenerationOutOfHistory when DesiredHistory is
// full (the entry may have aged out, plan.md §2.3), or
// ReasonCommitNotDesired when it is not (the commit was never desired).
type AppliedRef struct {
	Generation int64
	CommitSHA  string
	PlanID     string
	Reason     string `json:",omitempty"`
}

// ActiveRefs is the derived, admission-never-reads execution snapshot (M3):
// it only ever names IDs that already exist in the fence, dispatch and
// attempt stores, never a second copy of their state.
type ActiveRefs struct {
	PlanID          string
	DispatchRunID   string
	AttemptIDs      []string
	FenceGeneration int64
	Occupancy       string
	OccupancyReason string
}

// Status is the resource's derived cache. Never read by admission.
type Status struct {
	ObservedGeneration    int64
	LastAppliedGeneration int64
	LastApplied           *AppliedRef
	Active                *ActiveRefs
	Conditions            []Condition
	NextAction            string
	EvaluatedAt           time.Time
}

// Condition is one derived readiness fact (controller.DeriveStatus, WP11).
type Condition struct {
	Type                string
	Status              string // "True" | "False" | "Unknown"
	Reason              string
	Message             string
	ObservedAt          time.Time
	EvidenceRefs        []string
	ObservationSequence int64
}

// ObservationFacts is the bounded, source-specific payload one observation
// carries. It is free-form (stored and compared as JSON) because provider,
// state and runtime sources report different shapes; storage enforces only
// the size bound (MaxObservationFactsBytes), never its internal shape.
type ObservationFacts map[string]interface{}

// Observation is one source's report, server-sequenced on ingestion.
type Observation struct {
	Sequence     int64
	Source       string
	ObservedAt   time.Time
	ReceivedAt   time.Time
	Facts        ObservationFacts
	EvidenceRefs []string
	Reporter     string
	Applied      bool
}

// Input is everything ReconcileFleetResource gathers in one atomic read (PG:
// one transaction; etcd: one consistent set of reads whose ModRevisions the
// commit Txn then compares, retried on a lost race) before calling the
// caller's derive function (plan.md §2.3's "Reconcile write (M9)"; WP11's
// DeriveStatus is the real derive function -- WP10's own conformance suite
// uses small stand-ins to exercise the storage mechanics only).
//
// Fence and epoch facts are never added to a *fingerprinted* struct (T8);
// Input is not fingerprinted (it is never signed or hashed into an
// admission), so T8 does not constrain it.
type Input struct {
	Resource Resource
	// Fence and Holder are zero-value when Resource.TargetID is "" (the
	// resource's target was never registered, or the registry was empty at
	// desired-set time): every derive function must treat a zero FenceFacts
	// (Held=false) as "no mutation fence applies", exactly as lifecycle.Outcome
	// already does.
	Fence  lifecycle.FenceFacts
	Holder lifecycle.HolderFacts
	// HolderDispatchRunID is the holder plan's bound GitHub dispatch run ID
	// (PG fleet_github_dispatches.run_id; etcd FleetRunnerDispatchBinding.
	// RunID), or 0 when the fence is free or the dispatch is not yet bound.
	// lifecycle.HolderFacts does not carry it. Storage (store/
	// fleet_resources.go, etcdstore/v3_fleet_resources.go) must populate it
	// from the same holder reads; until it does, ActiveRefs.DispatchRunID
	// stays empty.
	HolderDispatchRunID int64
	// LastReleaseAttempts holds the runner attempts of Fence.LastRelease's
	// plan when that release's reason is "succeeded" (nil otherwise). Holder
	// is read only while the fence is held, and a succeeded release frees the
	// fence in the same transaction that completes the attempt, so without
	// this LastApplied (M11) could never be derived. Storage must populate it
	// and, on etcd, compare those attempt keys too.
	LastReleaseAttempts []fleet.RunnerAttempt
	// AuthorityEpoch is the current authority epoch, read in the same atomic
	// unit as every other input (Resource.AuthorityEpoch is only the epoch
	// stamped by the previous write). The write stamps exactly this value.
	AuthorityEpoch int64
	// LatestObservations holds exactly the watermark row for each source in
	// Resource.Watermarks -- the newest *applied* observation, never merely
	// the newest inserted one (M10). TiedObservations holds that source's
	// tied rows (Watermark.TiedSequences): derive must let a failure among
	// LatestObservations[s] and TiedObservations[s] win (plan.md §2.3).
	LatestObservations map[string]Observation
	TiedObservations   map[string][]Observation
	Now                time.Time
}
