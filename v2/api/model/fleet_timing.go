package model

import "time"

const FleetTimingSchemaVersion = "norn.fleet-timing/v1"

const (
	FleetTimingAvailable   = "available"
	FleetTimingUnavailable = "unavailable"
	FleetTimingLow         = "low"
	FleetTimingNone        = "none"

	FleetTimingConfiguredRange = "configured_range"
	FleetTimingNoEstimate      = "unavailable"
	FleetTimingColdStart       = "cold_start"
	FleetTimingUnclassified    = "unknown"

	FleetTimingAttemptScope = "runner_attempt"
)

// FleetTimingRange deliberately communicates a range. Fleet provisioning is
// asynchronous external work, so exposing a single predicted duration would
// imply precision the control plane cannot establish.
type FleetTimingRange struct {
	LowMs  int64 `json:"lowMs"`
	HighMs int64 `json:"highMs"`
}

type FleetTimingCompletionRange struct {
	EarliestAt time.Time `json:"earliestAt"`
	LatestAt   time.Time `json:"latestAt"`
}

// FleetTimingProvenance explains whether the estimate comes from configured
// operational evidence or from enough matching completed attempts. It is
// attempt/plan scoped and intentionally contains no runner credentials.
type FleetTimingProvenance struct {
	Method                string            `json:"method"`
	ConfiguredRange       *FleetTimingRange `json:"configuredRange,omitempty"`
	SampleCount           int               `json:"sampleCount"`
	SuccessfulSampleCount int               `json:"successfulSampleCount"`
	Exclusions            []string          `json:"exclusions"`
}

type FleetTimingPhase struct {
	Name               string            `json:"name"`
	State              string            `json:"state"`
	ElapsedMs          int64             `json:"elapsedMs"`
	EstimatedRemaining *FleetTimingRange `json:"estimatedRemaining,omitempty"`
}

// FleetTimingClassification is submitted only by the protected runner after it
// has derived a summary from the reviewed provider plan. Norn stores the
// sanitized values as attempt metadata; no client may infer cold start from a
// desired-pool document, which cannot prove provider emptiness.
type FleetTimingClassification struct {
	OperationClass   string `json:"operationClass"`
	CreatedNodeCount int    `json:"createdNodeCount"`
}

// FleetRunnerTiming projects the immutable plan baseline onto one attempt. It
// is read-only advisory state and has no role in runner authorization, phase
// transitions, or heartbeat liveness.
type FleetRunnerTiming struct {
	SchemaVersion       string                      `json:"schemaVersion"`
	Scope               string                      `json:"scope"`
	AsOf                time.Time                   `json:"asOf"`
	Availability        string                      `json:"availability"`
	OperationClass      string                      `json:"operationClass"`
	ElapsedMs           int64                       `json:"elapsedMs"`
	EstimatedRemaining  *FleetTimingRange           `json:"estimatedRemaining,omitempty"`
	EstimatedTotal      *FleetTimingRange           `json:"estimatedTotal,omitempty"`
	EstimatedCompletion *FleetTimingCompletionRange `json:"estimatedCompletion,omitempty"`
	Confidence          string                      `json:"confidence"`
	Provenance          FleetTimingProvenance       `json:"provenance"`
	Phases              []FleetTimingPhase          `json:"phases"`
}

func FleetTimingExclusions() []string {
	return []string{
		"review_approval",
		"github_queue",
		"dns_propagation",
		"application_migrations",
	}
}

func NewFleetRunnerTiming(attempt *FleetRunnerAttempt, classification FleetTimingClassification, asOf time.Time) *FleetRunnerTiming {
	if attempt == nil {
		return nil
	}
	asOf = asOf.UTC()
	end := asOf
	if attempt.Status.Terminal() && attempt.FinishedAt != nil {
		end = attempt.FinishedAt.UTC()
	}
	elapsed := end.Sub(attempt.StartedAt).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	phaseStart := attempt.PhaseStartedAt
	if phaseStart.IsZero() {
		phaseStart = attempt.StartedAt
	}
	phaseElapsed := end.Sub(phaseStart).Milliseconds()
	if phaseElapsed < 0 {
		phaseElapsed = 0
	}
	phaseState := "active"
	if attempt.Status == FleetRunnerAttemptSucceeded {
		phaseState = "complete"
	} else if attempt.Status.Terminal() {
		phaseState = "terminal"
	}
	timing := &FleetRunnerTiming{
		SchemaVersion: FleetTimingSchemaVersion, Scope: FleetTimingAttemptScope, AsOf: asOf,
		Availability: FleetTimingUnavailable, OperationClass: FleetTimingUnclassified,
		ElapsedMs: elapsed, Confidence: FleetTimingNone,
		Provenance: FleetTimingProvenance{Method: FleetTimingNoEstimate, Exclusions: FleetTimingExclusions()},
		Phases:     []FleetTimingPhase{{Name: attempt.CurrentPhase, State: phaseState, ElapsedMs: phaseElapsed}},
	}
	if classification.OperationClass != "" {
		timing.OperationClass = classification.OperationClass
	}
	if classification.OperationClass != FleetTimingColdStart || classification.CreatedNodeCount != 5 {
		return timing
	}
	baseline := &FleetTimingRange{LowMs: (15 * time.Minute).Milliseconds(), HighMs: (30 * time.Minute).Milliseconds()}
	provenance := FleetTimingProvenance{Method: FleetTimingConfiguredRange, ConfiguredRange: baseline, Exclusions: FleetTimingExclusions()}
	timing.Availability, timing.Confidence, timing.Provenance = FleetTimingAvailable, FleetTimingLow, provenance
	timing.EstimatedTotal = baseline
	if attempt.Status.Terminal() {
		if attempt.Status == FleetRunnerAttemptSucceeded {
			zero := &FleetTimingRange{}
			timing.EstimatedRemaining = zero
			completion := &FleetTimingCompletionRange{EarliestAt: end, LatestAt: end}
			timing.EstimatedCompletion = completion
		}
		return timing
	}
	remaining := &FleetTimingRange{
		LowMs:  maxInt64(0, baseline.LowMs-elapsed),
		HighMs: maxInt64(0, baseline.HighMs-elapsed),
	}
	timing.EstimatedRemaining = remaining
	timing.EstimatedCompletion = &FleetTimingCompletionRange{EarliestAt: asOf.Add(time.Duration(remaining.LowMs) * time.Millisecond), LatestAt: asOf.Add(time.Duration(remaining.HighMs) * time.Millisecond)}
	return timing
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
