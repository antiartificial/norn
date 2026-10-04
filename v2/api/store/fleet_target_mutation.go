package store

// Signed Fleet target mutations (plan.md WP9a): register, fence release
// (modes terminal and abandon) and abandon-unregistered-plan (H7). Each is an
// ordinary signed operation in the existing ledger: it is replayable and
// idempotent through the normal request identity, and is attributed to the
// operations row acceptance already writes (never a second ledger).
//
// Proof is server-gathered, never client-supplied (B2). The admission
// (FleetTargetMutationAdmission) is the caller's intent only: kind, target or
// plan, mode, expected generation and reason. It is the only part of the
// mutation in the request fingerprint and the signed canonical request.
// The GitHub proof and listing-snapshot digest travel separately in
// OperationAcceptance.FleetTargetReleaseEvidence, which has json:"-" (no
// request decode can fill it), is excluded from the fingerprint (a retry
// with the same key replays even if GitHub observations moved on), and is
// set only by the server route after its own WP5 observer calls. Every
// backend re-validates that evidence with lifecycle.DecideRelease against
// fresh DB facts inside the acceptance transaction (PG) or Txn (etcd), and
// checks ExpectedGeneration against the live fence there too. The evidence
// is recorded, unfingerprinted, in the operation's metadata under
// FleetTargetReleaseEvidenceMetadataKey.
//
// T8: no fence or epoch fact lives in FleetReconciliationAdmission or
// FleetRunnerAttemptAdmission; this admission is its own optional field, so
// existing request fingerprints are byte-identical.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

// Signed mutation kinds (FleetTargetMutationAdmission.Kind).
const (
	FleetTargetMutationRegister    = "register"
	FleetTargetMutationRelease     = "release"
	FleetTargetMutationAbandonPlan = "abandon-plan"
)

// Operation kinds of the signed ledger entries.
const (
	FleetTargetRegisterOperationKind     = "fleet.target.register"
	FleetTargetFenceReleaseOperationKind = "fleet.target.fence-release"
	FleetTargetAbandonPlanOperationKind  = "fleet.target.abandon-plan"
)

// CodeFleetTargetExpectedGenerationMismatch is returned (as a
// *lifecycle.FenceError) when a release's ExpectedGeneration no longer equals
// the fence generation, or the fence holder changed while it was being
// decided. It lives in the store layer, not lifecycle (WP9a).
const CodeFleetTargetExpectedGenerationMismatch = "fleet_target_expected_generation_mismatch"

const (
	maxFleetTargetAliases     = 64
	maxFleetTargetProofRuns   = 256
	maxFleetTargetReasonBytes = 1024
)

// FleetTargetReleaseAdmission is the caller's release or abandon intent.
// Mode is "terminal" or "abandon". ExpectedGeneration is the fence
// generation the caller observed (always 0 for abandon-plan, which is keyed
// by plan). It deliberately carries no proof: see FleetTargetReleaseEvidence.
type FleetTargetReleaseAdmission struct {
	Mode               string `json:"mode"`
	ExpectedGeneration int64  `json:"expectedGeneration,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

// FleetTargetReleaseEvidence is the server-gathered GitHub evidence for a
// release or abandon (WP5 observers). Only the server route builds it, after
// signature/scope checks and its own observer calls; it is never decoded
// from a request body (OperationAcceptance.FleetTargetReleaseEvidence is
// json:"-") and is not part of the request fingerprint. The store treats it
// as a claim and re-validates it with lifecycle.DecideRelease against DB
// facts (every holder attempt's RunnerAttemptID must be listed as completed).
type FleetTargetReleaseEvidence struct {
	BoundApplyRunCompleted    bool     `json:"boundApplyRunCompleted"`
	CompletedRunnerAttemptIDs []string `json:"completedRunnerAttemptIds,omitempty"`
	ListingSnapshotSHA256     string   `json:"listingSnapshotSha256,omitempty"`
}

// FleetTargetReleaseEvidenceMetadataKey is the operation-metadata key under
// which the store records FleetTargetReleaseEvidence. Any caller-supplied
// value under this key is discarded and replaced by the server evidence.
const FleetTargetReleaseEvidenceMetadataKey = "fleetTargetReleaseEvidence"

// FleetTargetMutationAdmission is the typed admission for the three kinds.
// register uses TargetID (derived, canonical), Provider, ProviderAccount,
// StateBackend and Aliases; release uses TargetID and Release; abandon-plan
// uses PlanID and Release.
type FleetTargetMutationAdmission struct {
	Kind            string                       `json:"kind"`
	TargetID        string                       `json:"targetId,omitempty"`
	Provider        string                       `json:"provider,omitempty"`
	ProviderAccount string                       `json:"providerAccount,omitempty"`
	StateBackend    string                       `json:"stateBackend,omitempty"`
	Aliases         []string                     `json:"aliases,omitempty"`
	PlanID          string                       `json:"planId,omitempty"`
	Release         *FleetTargetReleaseAdmission `json:"release,omitempty"`
}

// FleetTargetMutationOperationKind maps an admission kind to its operation
// kind ("" if unknown).
func FleetTargetMutationOperationKind(kind string) string {
	switch kind {
	case FleetTargetMutationRegister:
		return FleetTargetRegisterOperationKind
	case FleetTargetMutationRelease:
		return FleetTargetFenceReleaseOperationKind
	case FleetTargetMutationAbandonPlan:
		return FleetTargetAbandonPlanOperationKind
	}
	return ""
}

// IsFleetTargetMutationKind reports whether kind is one of the signed target
// mutation operation kinds, which always require typed admission.
func IsFleetTargetMutationKind(kind string) bool {
	return kind == FleetTargetRegisterOperationKind || kind == FleetTargetFenceReleaseOperationKind || kind == FleetTargetAbandonPlanOperationKind
}

// Subject returns the identity resource of the mutation: the target ID, or
// the plan ID for abandon-plan.
func (a *FleetTargetMutationAdmission) Subject() string {
	if a.Kind == FleetTargetMutationAbandonPlan {
		return a.PlanID
	}
	return a.TargetID
}

// TerminalProof converts the server evidence into the proof DecideRelease
// re-validates. A nil evidence yields an empty proof, which DecideRelease
// refuses for both terminal and abandon modes.
func (e *FleetTargetReleaseEvidence) TerminalProof() *lifecycle.TerminalProof {
	proof := &lifecycle.TerminalProof{}
	if e == nil {
		return proof
	}
	proof.BoundApplyRunCompleted, proof.ListingSnapshotSHA256 = e.BoundApplyRunCompleted, e.ListingSnapshotSHA256
	proof.CompletedRunnerAttemptIDs = make(map[string]bool, len(e.CompletedRunnerAttemptIDs))
	for _, id := range e.CompletedRunnerAttemptIDs {
		proof.CompletedRunnerAttemptIDs[id] = true
	}
	return proof
}

// ReleaseMode returns the lifecycle release mode.
func (r *FleetTargetReleaseAdmission) ReleaseMode() lifecycle.ReleaseMode {
	return lifecycle.ReleaseMode(r.Mode)
}

// NewFleetTargetMutationAcceptance builds the complete signed acceptance for
// admission: normalized admission, identity, completed-receipt operation and
// request fingerprint. authority is the control authority UUID. evidence is
// the server-gathered release evidence (nil for register); it is attached
// outside the fingerprint and must never come from the request body.
func NewFleetTargetMutationAcceptance(authority string, actor OperationActor, key string, audit AcceptanceAuditContext, admission FleetTargetMutationAdmission, evidence *FleetTargetReleaseEvidence) (OperationAcceptance, error) {
	kind := FleetTargetMutationOperationKind(admission.Kind)
	if kind == "" {
		return OperationAcceptance{}, &AcceptanceValidationError{Reason: "fleet target mutation kind is invalid"}
	}
	acceptance := OperationAcceptance{
		Identity:                   OperationRequestIdentity{Authority: authority, Actor: actor, Kind: kind, Key: key},
		Audit:                      audit,
		FleetTargetMutation:        &admission,
		FleetTargetReleaseEvidence: evidence,
	}
	if err := normalizeFleetTargetMutationFields(acceptance.FleetTargetMutation); err != nil {
		return OperationAcceptance{}, err
	}
	subject := admission.Subject()
	acceptance.Identity.Resource = subject
	acceptance.Operation = model.Operation{
		ID: uuid.NewString(), Kind: kind, Ref: subject, Status: model.OperationSucceeded, MaxAttempts: 1,
		Payload: map[string]interface{}{"mutation": admission.Kind}, Metadata: map[string]interface{}{},
	}
	if err := attachFleetTargetReleaseEvidence(&acceptance); err != nil {
		return OperationAcceptance{}, err
	}
	fingerprint, err := CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		return OperationAcceptance{}, err
	}
	acceptance.Fingerprint = fingerprint
	return acceptance, nil
}

func normalizeFleetTargetMutationAcceptance(acceptance *OperationAcceptance) error {
	admission := acceptance.FleetTargetMutation
	if admission == nil {
		if IsFleetTargetMutationKind(acceptance.Operation.Kind) || IsFleetTargetMutationKind(acceptance.Identity.Kind) {
			return &AcceptanceValidationError{Reason: "fleet target mutation operations require typed admission"}
		}
		if acceptance.FleetTargetReleaseEvidence != nil {
			return &AcceptanceValidationError{Reason: "fleet target release evidence requires a fleet target mutation"}
		}
		return nil
	}
	if acceptance.FleetReconciliation != nil || acceptance.FleetRunnerAttempt != nil || acceptance.Deployment != nil {
		return &AcceptanceValidationError{Reason: "fleet target mutation cannot combine with another typed admission"}
	}
	if err := normalizeFleetTargetMutationFields(admission); err != nil {
		return err
	}
	kind := FleetTargetMutationOperationKind(admission.Kind)
	subject := admission.Subject()
	if acceptance.Operation.Kind != kind || acceptance.Identity.Kind != kind || acceptance.Identity.Resource != subject || acceptance.Operation.Ref != subject {
		return &AcceptanceValidationError{Reason: "fleet target mutation admission must match operation identity"}
	}
	if acceptance.Operation.Status != model.OperationSucceeded {
		return &AcceptanceValidationError{Reason: "fleet target mutation acceptance operation must be a completed receipt"}
	}
	return attachFleetTargetReleaseEvidence(acceptance)
}

// attachFleetTargetReleaseEvidence validates and canonicalizes the
// server-only evidence and records it in a copy of the operation metadata
// under FleetTargetReleaseEvidenceMetadataKey, replacing anything a caller
// put there. That key is excluded from the request fingerprint for target
// mutations (semanticOperationMap), so the recorded evidence never changes
// the replay identity.
func attachFleetTargetReleaseEvidence(acceptance *OperationAcceptance) error {
	bad := func(reason string) error {
		return &AcceptanceValidationError{Reason: "fleet target release evidence " + reason}
	}
	metadata := make(map[string]interface{}, len(acceptance.Operation.Metadata)+1)
	for key, value := range acceptance.Operation.Metadata {
		metadata[key] = value
	}
	delete(metadata, FleetTargetReleaseEvidenceMetadataKey)
	acceptance.Operation.Metadata = metadata
	e := acceptance.FleetTargetReleaseEvidence
	if e == nil {
		return nil
	}
	switch acceptance.FleetTargetMutation.Kind {
	case FleetTargetMutationRelease:
	case FleetTargetMutationAbandonPlan:
		if e.BoundApplyRunCompleted || len(e.CompletedRunnerAttemptIDs) != 0 {
			return bad("for abandon-plan carries only a listing snapshot")
		}
	default:
		return bad("is only valid for release and abandon-plan")
	}
	canonical := FleetTargetReleaseEvidence{BoundApplyRunCompleted: e.BoundApplyRunCompleted, ListingSnapshotSHA256: strings.TrimSpace(e.ListingSnapshotSHA256)}
	if canonical.ListingSnapshotSHA256 != "" && !lowerHex(canonical.ListingSnapshotSHA256, 64) {
		return bad("listing snapshot digest is invalid")
	}
	ids := make([]string, 0, len(e.CompletedRunnerAttemptIDs))
	for _, id := range e.CompletedRunnerAttemptIDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if n := len(canonical.CompletedRunnerAttemptIDs); n == 0 || canonical.CompletedRunnerAttemptIDs[n-1] != id {
			canonical.CompletedRunnerAttemptIDs = append(canonical.CompletedRunnerAttemptIDs, id)
		}
	}
	if len(canonical.CompletedRunnerAttemptIDs) > maxFleetTargetProofRuns {
		return bad("lists too many runs")
	}
	acceptance.FleetTargetReleaseEvidence = &canonical
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return err
	}
	var recorded map[string]interface{}
	if err := json.Unmarshal(encoded, &recorded); err != nil {
		return err
	}
	metadata[FleetTargetReleaseEvidenceMetadataKey] = recorded
	return nil
}

// NormalizeFleetTargetMutationAcceptance is the exported form backends other
// than PostgreSQL call before sealing the acceptance.
func NormalizeFleetTargetMutationAcceptance(acceptance *OperationAcceptance) error {
	return normalizeFleetTargetMutationAcceptance(acceptance)
}

// normalizeFleetTargetMutationFields canonicalizes the admission in place so
// the request fingerprint is stable (trimmed, canonical target, sorted unique
// aliases and proof IDs).
func normalizeFleetTargetMutationFields(a *FleetTargetMutationAdmission) error {
	bad := func(reason string) error {
		return &AcceptanceValidationError{Reason: "fleet target mutation " + reason}
	}
	a.TargetID, a.PlanID = strings.TrimSpace(a.TargetID), strings.TrimSpace(a.PlanID)
	switch a.Kind {
	case FleetTargetMutationRegister:
		if a.Release != nil || a.PlanID != "" {
			return bad("register carries release or plan fields")
		}
		canon, targetID, err := lifecycle.CanonicalTarget(lifecycle.TargetIdentity{Provider: a.Provider, ProviderAccount: a.ProviderAccount, StateBackend: a.StateBackend})
		if err != nil {
			return bad("target is invalid: " + err.Error())
		}
		if a.TargetID != "" && a.TargetID != targetID {
			return bad("target ID does not match its canonical identity")
		}
		a.TargetID, a.Provider, a.ProviderAccount, a.StateBackend = targetID, canon.Provider, canon.ProviderAccount, canon.StateBackend
		unique := make([]string, 0, len(a.Aliases))
		seen := map[string]bool{}
		for _, alias := range a.Aliases {
			alias = strings.TrimSpace(alias)
			if _, _, ok := lifecycle.ParseAlias(alias); !ok {
				return bad(fmt.Sprintf("alias %q is not a recognized kind", alias))
			}
			if !seen[alias] {
				seen[alias] = true
				unique = append(unique, alias)
			}
		}
		if len(unique) == 0 || len(unique) > maxFleetTargetAliases {
			return bad("requires between one and 64 aliases")
		}
		sort.Strings(unique)
		a.Aliases = unique
		return nil
	case FleetTargetMutationRelease, FleetTargetMutationAbandonPlan:
		if a.Provider != "" || a.ProviderAccount != "" || a.StateBackend != "" || len(a.Aliases) != 0 || a.Release == nil {
			return bad("release requires only release fields")
		}
		r := a.Release
		r.Reason = strings.TrimSpace(r.Reason)
		if len(r.Reason) > maxFleetTargetReasonBytes {
			return bad("reason is too long")
		}
		if r.Mode != string(lifecycle.ReleaseModeTerminal) && r.Mode != string(lifecycle.ReleaseModeAbandon) {
			return bad("release mode must be terminal or abandon")
		}
		if a.Kind == FleetTargetMutationRelease {
			if a.TargetID == "" || a.PlanID != "" || r.ExpectedGeneration < 0 {
				return bad("release requires a target ID and a non-negative expected generation")
			}
			return nil
		}
		if _, err := uuid.Parse(a.PlanID); err != nil || a.TargetID != "" || r.ExpectedGeneration != 0 || r.Mode != string(lifecycle.ReleaseModeAbandon) {
			return bad("abandon-plan requires a plan UUID and mode abandon")
		}
		return nil
	}
	return bad("kind is invalid")
}

func fleetTargetExpectedGenerationError(reason string) error {
	return &lifecycle.FenceError{Code: CodeFleetTargetExpectedGenerationMismatch, Reason: reason}
}

// enforceFleetTargetMutation is the acceptWithGuard hook: it applies the
// mutation inside the acceptance transaction, so a refusal rolls back the
// request identity too and leaves no ledger entry behind.
func enforceFleetTargetMutation(ctx context.Context, tx pgx.Tx, acceptance OperationAcceptance) error {
	admission := acceptance.FleetTargetMutation
	if admission == nil {
		return nil
	}
	// T2: release and abandon age checks use database time, read inside the
	// same transaction, never this process's clock.
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	now = now.UTC().Truncate(time.Microsecond)
	operationID := acceptance.Operation.ID
	switch admission.Kind {
	case FleetTargetMutationRegister:
		_, err := registerFleetTargetTx(ctx, tx, lifecycle.TargetIdentity{Provider: admission.Provider, ProviderAccount: admission.ProviderAccount, StateBackend: admission.StateBackend}, admission.Aliases, operationID, now)
		return err
	case FleetTargetMutationRelease:
		return releaseFleetTargetFenceTx(ctx, tx, admission.TargetID, admission.Release, acceptance.FleetTargetReleaseEvidence.TerminalProof(), operationID, now)
	case FleetTargetMutationAbandonPlan:
		return abandonFleetPlanTx(ctx, tx, admission.PlanID, acceptance.FleetTargetReleaseEvidence.TerminalProof(), operationID, now)
	}
	return &AcceptanceValidationError{Reason: "fleet target mutation kind is invalid"}
}

// releaseFleetTargetFenceTx is the internal PG release (modes terminal and
// abandon). Lock order (M7): the holder plan's advisory lock (1) is taken
// from an unlocked read of the holder, then the fence FOR UPDATE (4), then
// dispatch and attempts are read (5, 6). The fence is re-checked after the
// lock, so a holder that changed in between refuses instead of releasing
// someone else's fence. A terminal or abandon release also permanently
// abandons the holder plan (m16).
func releaseFleetTargetFenceTx(ctx context.Context, tx pgx.Tx, targetID string, release *FleetTargetReleaseAdmission, proof *lifecycle.TerminalProof, operationID string, now time.Time) error {
	var preHolder string
	if err := tx.QueryRow(ctx, `SELECT holder_plan_id FROM fleet_target_fences WHERE target_id=$1`, targetID).Scan(&preHolder); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetUnregistered, Reason: "fleet target is not registered"}
		}
		return err
	}
	if preHolder != "" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+preHolder); err != nil {
			return err
		}
	}
	fence, err := GetFleetTargetFence(ctx, tx, targetID, true)
	if err != nil {
		return err
	}
	if fence.HolderPlanID != preHolder {
		return fleetTargetExpectedGenerationError("fleet target fence holder changed concurrently")
	}
	if fence.Generation != release.ExpectedGeneration {
		return fleetTargetExpectedGenerationError("expected generation does not match the current fence generation")
	}
	holder, err := FleetTargetHolderFacts(ctx, tx, fence.HolderPlanID)
	if err != nil {
		return err
	}
	next, err := lifecycle.DecideRelease(fence, holder, release.ReleaseMode(), proof, now)
	if err != nil {
		return err
	}
	if err := PutFleetTargetFence(ctx, tx, next); err != nil {
		return err
	}
	return AbandonFleetPlan(ctx, tx, fence.HolderPlanID, fence.HolderNonceSHA256, targetID, operationID, now)
}

// abandonFleetPlanTx is the internal PG pre-registration abandon (H7), keyed
// by plan: it works whether or not the plan's cluster is registered. It
// applies DecideRelease's abandon rules (listing snapshot, minimum age) to
// the real fence when planID holds one, or to a synthetic held-by-plan fence
// otherwise, and records the permanent abandon row either way.
func abandonFleetPlanTx(ctx context.Context, tx pgx.Tx, planID string, proof *lifecycle.TerminalProof, operationID string, now time.Time) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+planID); err != nil {
		return err
	}
	if abandoned, err := IsFleetPlanAbandoned(ctx, tx, planID); err != nil {
		return err
	} else if abandoned {
		return &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetHolderAbandoned, Reason: "fleet target holder plan is permanently abandoned"}
	}
	targetID, fence, _, err := LockHeldFleetTargetFenceForPlan(ctx, tx, planID, true)
	if err != nil {
		return err
	}
	if targetID != "" && (!fence.Held || fence.HolderPlanID != planID) {
		targetID = ""
	}
	_, _, nonce, err := fleetTargetBindInputs(ctx, tx, planID)
	if err != nil {
		return err
	}
	decide := lifecycle.FenceFacts{Held: true, HolderPlanID: planID, HolderNonceSHA256: nonce}
	if targetID != "" {
		decide, nonce = fence, fence.HolderNonceSHA256
	}
	if nonce == "" {
		return lifecycle.ErrFleetTargetReleaseEvidenceMismatch
	}
	holder, err := FleetTargetHolderFacts(ctx, tx, planID)
	if err != nil {
		return err
	}
	next, err := lifecycle.DecideRelease(decide, holder, lifecycle.ReleaseModeAbandon, proof, now)
	if err != nil {
		return err
	}
	if targetID != "" {
		if err := PutFleetTargetFence(ctx, tx, next); err != nil {
			return err
		}
	}
	return AbandonFleetPlan(ctx, tx, planID, nonce, targetID, operationID, now)
}
