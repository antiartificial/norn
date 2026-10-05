package store

// Executor-lane credential grants, stop attestations, provider evidence and
// the executor-lane release proof (plan-5-7 E5, ADR 0010).
//
// This file holds the backend-neutral facts the HTTP routes and both release
// paths decide from, the pure decisions over them, and the PostgreSQL readers.
// The etcd readers live in etcdstore/v3_fleet_executor_ops.go. Nothing here
// writes: the three new ledger kinds are recorded through the ordinary signed
// OperationStore.Accept, and a decision is re-run by the recovery admission
// (E2) or the release transaction against fresh facts.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

// FleetExecutorAttemptFact is one runner attempt as the E5 decisions need it.
// Executor is true only for an executor-lane attempt (metadata lane executor).
type FleetExecutorAttemptFact struct {
	ID                 string
	PlanID             string
	RunnerAttemptID    string
	Attempt            int
	Status             string
	CurrentPhase       string
	RetryOf            string
	PrincipalSubject   string
	Executor           bool
	Metadata           executor.AttemptMetadata
	StartedAt          time.Time
	HeartbeatExpiresAt time.Time
	FinishedAt         *time.Time
}

// Live reports a queued or running attempt whose heartbeat lease has not
// expired at now.
func (f FleetExecutorAttemptFact) Live(now time.Time) bool {
	return (f.Status == string(model.FleetRunnerAttemptQueued) || f.Status == string(model.FleetRunnerAttemptRunning)) && f.HeartbeatExpiresAt.After(now)
}

// FleetExecutorAttemptFactFromModel converts a PG attempt.
func FleetExecutorAttemptFactFromModel(a *model.FleetRunnerAttempt) FleetExecutorAttemptFact {
	fact := FleetExecutorAttemptFact{
		ID: a.ID, PlanID: a.PlanID, RunnerAttemptID: a.RunnerAttemptID, Attempt: a.Attempt, Status: string(a.Status),
		CurrentPhase: a.CurrentPhase, RetryOf: a.RetryOf, PrincipalSubject: a.PrincipalSubject, StartedAt: a.StartedAt,
		HeartbeatExpiresAt: a.HeartbeatAt.Add(time.Duration(a.HeartbeatTimeoutSeconds) * time.Second), FinishedAt: a.FinishedAt,
	}
	if !a.HeartbeatExpiresAt.IsZero() {
		fact.HeartbeatExpiresAt = a.HeartbeatExpiresAt
	}
	fact.Metadata, fact.Executor = FleetExecutorAttemptMetadata(a)
	return fact
}

// FleetExecutorLedgerRow is one succeeded ledger operation (payload, metadata
// and recording time), as the PG admission reads attestation rows.
type FleetExecutorLedgerRow = fleetLedgerRow

// FleetExecutorConvergePlanFact is what an executor refusal is checked
// against: the converge plan's resource and commit, and the hex part of its
// digest (the planSha256 an attempt start carries).
type FleetExecutorConvergePlanFact struct {
	Resource   string
	CommitSHA  string
	PlanSHA256 string
}

// FleetExecutorDeviceFact is the executor device's registry state.
type FleetExecutorDeviceFact struct {
	Exists   bool
	Revoked  bool
	Platform string
}

// FleetExecutorOpsReader is the read side of E5, implemented by *DB (PG) and
// etcdstore.V3OperationStore. Attempts are returned oldest first.
type FleetExecutorOpsReader interface {
	FleetExecutorAttempts(ctx context.Context, planID string) ([]FleetExecutorAttemptFact, error)
	// FleetExecutorAttemptByID returns (nil, nil) for an unknown attempt.
	FleetExecutorAttemptByID(ctx context.Context, attemptID string) (*FleetExecutorAttemptFact, error)
	FleetExecutorDevice(ctx context.Context, deviceID string) (FleetExecutorDeviceFact, error)
	FleetExecutorPlanIsConverge(ctx context.Context, planID string) (bool, error)
	FleetExecutorLedgerRows(ctx context.Context, kind, planID string) ([]FleetExecutorLedgerRow, error)
}

// FleetExecutorConvergePlanReader reads the facts an executor refusal is
// checked against, implemented by *DB (PG) and etcdstore.V3OperationStore.
// FleetExecutorConvergePlan returns (nil, nil) for an unknown plan or one that
// is not a converge plan.
type FleetExecutorConvergePlanReader interface {
	FleetExecutorConvergePlan(ctx context.Context, planID string) (*FleetExecutorConvergePlanFact, error)
}

// FleetExecutorDeviceID is the device named by an attempt's principal subject
// ("" when it is not an executor principal subject).
func FleetExecutorDeviceID(principalSubject string) string {
	deviceID, ok := strings.CutPrefix(principalSubject, executor.PrincipalSubject(""))
	if !ok {
		return ""
	}
	return deviceID
}

// FleetExecutorLane reports whether any attempt is on the executor lane. A
// holder with such an attempt is an executor-lane holder.
func FleetExecutorLane(attempts []FleetExecutorAttemptFact) bool {
	for _, attempt := range attempts {
		if attempt.Executor {
			return true
		}
	}
	return false
}

func latestFleetExecutorAttempt(attempts []FleetExecutorAttemptFact) *FleetExecutorAttemptFact {
	var latest *FleetExecutorAttemptFact
	for index := range attempts {
		if latest == nil || attempts[index].Attempt > latest.Attempt {
			latest = &attempts[index]
		}
	}
	return latest
}

// DecideFleetExecutorGrant applies the E5 grant admission for one executor
// device (principalSubject = executor.PrincipalSubject(deviceID)): the
// GrantTypes table for profile, then, for every phase except plan, a live
// attempt owned by the device whose currentPhase equals the requested phase.
// recover needs the latest attempt, live and owned, with retryOf set (a
// recovery attempt). plan needs a converge plan. It returns the normalized
// request and the attempt (nil for plan). Refusals are *executor.Error.
func DecideFleetExecutorGrant(request executor.CredentialGrantRequest, profile executor.GrantProfile, attempts []FleetExecutorAttemptFact, planIsConverge bool, principalSubject string, now time.Time) (executor.CredentialGrantRequest, *FleetExecutorAttemptFact, error) {
	request = request.Normalize()
	if err := request.Validate(profile); err != nil {
		return request, nil, err
	}
	if request.Phase == executor.GrantPhasePlan {
		if !planIsConverge {
			return request, nil, executorRefusal(executor.CodeFleetPlanNotConverge, "the plan is not a converge plan")
		}
		return request, nil, nil
	}
	var attempt *FleetExecutorAttemptFact
	for index := range attempts {
		if attempts[index].ID == request.AttemptID {
			attempt = &attempts[index]
		}
	}
	if attempt == nil || !attempt.Executor || principalSubject == "" || attempt.PrincipalSubject != principalSubject || !attempt.Live(now) {
		return request, nil, executorRefusal(executor.CodeFleetCredentialGrantAttemptRequired, "a grant needs a live attempt of this plan owned by this device")
	}
	if request.Phase == executor.GrantPhaseRecover {
		if latest := latestFleetExecutorAttempt(attempts); latest == nil || latest.ID != attempt.ID || attempt.RetryOf == "" {
			return request, nil, executorRefusal(executor.CodeFleetCredentialGrantAttemptRequired, "the recover grant needs the latest, live recovery attempt")
		}
		return request, attempt, nil
	}
	if request.Phase != attempt.CurrentPhase {
		return request, nil, executorRefusal(executor.CodeFleetCredentialGrantPhaseMismatch, "grant phase %q is not the attempt's current phase %q", request.Phase, attempt.CurrentPhase)
	}
	return request, attempt, nil
}

// DecideFleetExecutorStateLockRefusal admits a state_lock_foreign refusal: the
// source attempt must be the plan's latest attempt, on the executor lane, and
// not succeeded, which is exactly the source a recovery may resume
// (authorizeFleetExecutorRecoveryTx). Neither the heartbeat lease nor a
// queued/running status is required: the recovery that finds a foreign lock
// runs because the source loop died, so its lease has expired, and on PG any
// attempt read (ListFleetRunnerAttempts, GetFleetRunnerAttempt) then sweeps
// the attempt to abandoned.
func DecideFleetExecutorStateLockRefusal(sourceAttemptID string, attempts []FleetExecutorAttemptFact) error {
	latest := latestFleetExecutorAttempt(attempts)
	if latest == nil || !latest.Executor || latest.ID != sourceAttemptID || latest.Status == string(model.FleetRunnerAttemptSucceeded) {
		return executorRefusal(executor.CodeFleetConvergeRefusalRejected, "sourceAttemptId is not the plan's latest recoverable executor-lane attempt")
	}
	return nil
}

// DecideFleetExecutorStopAttestation validates an attestation by the device
// attesterSubject against the source attempt (executor.ValidStopAttestation).
// source nil (an unknown attempt) is refused.
func DecideFleetExecutorStopAttestation(request executor.StopAttestationRequest, source *FleetExecutorAttemptFact, attesterSubject string, now time.Time) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if source == nil || !source.Executor {
		return executorRefusal(executor.CodeFleetExecutorStopAttestationInvalid, "source attempt is not an executor-lane attempt")
	}
	return executor.ValidStopAttestation(request, source.Metadata, source.PrincipalSubject, attesterSubject, now)
}

// fleetExecutorStopAge is the H5 age gate for executor-lane evidence: the
// holder's latest attempt must have been silent for lifecycle.AbandonMinimumAge
// (heartbeat lease expiry, or the finish time when later). A live lease is
// negative age, so a partitioned old loop is never freed on a DB row alone.
func fleetExecutorStopAge(latest *FleetExecutorAttemptFact, now time.Time) error {
	last := latest.HeartbeatExpiresAt
	if latest.FinishedAt != nil && latest.FinishedAt.After(last) {
		last = *latest.FinishedAt
	}
	if now.Sub(last) < lifecycle.AbandonMinimumAge {
		return lifecycle.ErrFleetTargetAbandonTooSoon
	}
	return nil
}

// checkFleetExecutorProviderEvidence is the shared evidence check: shape and
// freshness, droplet equal to the latest attempt's metadata, and the attempt's
// device revoked, so the old loop can no longer heartbeat, advance or obtain a
// grant (CodeFleetExecutorDeviceActive otherwise).
func checkFleetExecutorProviderEvidence(evidence executor.ProviderEvidence, latest *FleetExecutorAttemptFact, device FleetExecutorDeviceFact, now time.Time) error {
	if err := evidence.Validate(now); err != nil {
		return err
	}
	if evidence.DropletID != latest.Metadata.DropletID {
		return executorRefusal(executor.CodeFleetProviderEvidenceInvalid, "dropletId differs from the holder's latest executor attempt")
	}
	if !device.Exists || !device.Revoked {
		return executorRefusal(executor.CodeFleetExecutorDeviceActive, "the attempt's executor device is still active; revoke it first")
	}
	return nil
}

// DecideFleetExecutorProviderEvidence is the non-releasing provider-evidence
// route's decision: the source attempt is the holder plan's latest executor
// attempt, then checkFleetExecutorProviderEvidence and the H5 age gate.
func DecideFleetExecutorProviderEvidence(evidence executor.ProviderEvidence, sourceAttemptID string, attempts []FleetExecutorAttemptFact, device FleetExecutorDeviceFact, now time.Time) error {
	latest := latestFleetExecutorAttempt(attempts)
	if latest == nil || !latest.Executor || latest.ID != sourceAttemptID {
		return &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetReleaseEvidenceMismatch, Reason: "sourceAttemptId is not the holder plan's latest executor attempt"}
	}
	if err := checkFleetExecutorProviderEvidence(evidence, latest, device, now); err != nil {
		return err
	}
	return fleetExecutorStopAge(latest, now)
}

// DecideFleetExecutorReleaseProof replaces the GitHub observers for an
// executor-lane holder (release and abandon-plan). It needs either the
// admission's providerEvidence (checked as above) or a stop attestation of the
// latest attempt that was valid when it was recorded, and the H5 age gate; it
// returns the synthetic terminal proof DecideRelease then re-checks (no live
// attempt, mode rules). The evidence itself is an assertion (ADR 0010): it is
// never written to FleetTargetReleaseEvidence.
func DecideFleetExecutorReleaseProof(attempts []FleetExecutorAttemptFact, attestations []FleetExecutorLedgerRow, evidence *executor.ProviderEvidence, device FleetExecutorDeviceFact, now time.Time) (*lifecycle.TerminalProof, error) {
	latest := latestFleetExecutorAttempt(attempts)
	if latest == nil || !latest.Executor {
		return nil, executorRefusal(executor.CodeFleetProviderEvidenceRequired, "the holder has no executor attempt")
	}
	basis := ""
	switch {
	case evidence != nil:
		if err := checkFleetExecutorProviderEvidence(*evidence, latest, device, now); err != nil {
			return nil, err
		}
		basis = evidence.Digest()
	default:
		for _, row := range attestations {
			var payload executor.StopAttestationPayload
			if !decodeLedgerValue(row.Payload, &payload) || payload.SourceAttemptID != latest.ID {
				continue
			}
			attester, _ := row.Metadata[FleetExecutorAttesterMetadataKey].(string)
			// Valid when recorded: the attestation's own freshness window
			// ends minutes after it, long before the H5 age gate opens.
			if executor.ValidStopAttestation(payload.StopAttestationRequest, latest.Metadata, latest.PrincipalSubject, attester, row.StartedAt) == nil {
				basis = "stop-attestation:" + row.ID
				break
			}
		}
		if basis == "" {
			return nil, executorRefusal(executor.CodeFleetProviderEvidenceRequired, "an executor-lane holder is released with a valid stop attestation or providerEvidence")
		}
	}
	if err := fleetExecutorStopAge(latest, now); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(basis))
	proof := &lifecycle.TerminalProof{BoundApplyRunCompleted: true, CompletedRunnerAttemptIDs: map[string]bool{}, ListingSnapshotSHA256: hex.EncodeToString(digest[:])}
	for _, attempt := range attempts {
		proof.CompletedRunnerAttemptIDs[attempt.RunnerAttemptID] = true
	}
	return proof, nil
}

// FleetExecutorReleaseProof is DecideFleetExecutorReleaseProof's entry for a
// backend that has read its facts: it returns the proof to use, and whether
// the holder is executor-lane. A non-executor plan (including one with no
// attempt, which nothing could have run for) keeps base and refuses a
// supplied providerEvidence, so the evidence is never recorded unused.
func FleetExecutorReleaseProof(attempts []FleetExecutorAttemptFact, attestations []FleetExecutorLedgerRow, evidence *executor.ProviderEvidence, device FleetExecutorDeviceFact, base *lifecycle.TerminalProof, now time.Time) (*lifecycle.TerminalProof, bool, error) {
	if !FleetExecutorLane(attempts) {
		if evidence != nil {
			return nil, false, &AcceptanceValidationError{Reason: "providerEvidence applies only to an executor-lane holder"}
		}
		return base, false, nil
	}
	proof, err := DecideFleetExecutorReleaseProof(attempts, attestations, evidence, device, now)
	return proof, true, err
}

// --- PostgreSQL readers ---

func (db *DB) FleetExecutorAttempts(ctx context.Context, planID string) ([]FleetExecutorAttemptFact, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return fleetExecutorAttemptFactsTx(ctx, tx, planID)
}

func fleetExecutorAttemptFactsTx(ctx context.Context, tx pgx.Tx, planID string) ([]FleetExecutorAttemptFact, error) {
	attempts, err := fleetExecutorAttemptsTx(ctx, tx, planID)
	if err != nil {
		return nil, err
	}
	facts := make([]FleetExecutorAttemptFact, 0, len(attempts))
	for index := range attempts {
		facts = append(facts, FleetExecutorAttemptFactFromModel(&attempts[index]))
	}
	return facts, nil
}

func (db *DB) FleetExecutorAttemptByID(ctx context.Context, attemptID string) (*FleetExecutorAttemptFact, error) {
	attempt, err := db.GetFleetRunnerAttempt(ctx, attemptID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fact := FleetExecutorAttemptFactFromModel(attempt)
	return &fact, nil
}

func (db *DB) FleetExecutorDevice(ctx context.Context, deviceID string) (FleetExecutorDeviceFact, error) {
	return fleetExecutorDeviceQuery(ctx, db.Pool, deviceID)
}

type fleetExecutorRowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
}

func fleetExecutorDeviceQuery(ctx context.Context, q fleetExecutorRowQuerier, deviceID string) (FleetExecutorDeviceFact, error) {
	if deviceID == "" {
		return FleetExecutorDeviceFact{}, nil
	}
	var fact FleetExecutorDeviceFact
	err := q.QueryRow(ctx, `SELECT COALESCE(platform,''), revoked_at IS NOT NULL FROM access_devices WHERE id=$1`, deviceID).Scan(&fact.Platform, &fact.Revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return FleetExecutorDeviceFact{}, nil
	}
	if err != nil {
		return FleetExecutorDeviceFact{}, err
	}
	fact.Exists = true
	return fact, nil
}

func (db *DB) FleetExecutorPlanIsConverge(ctx context.Context, planID string) (bool, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	converge, err := fleetConvergePlanTx(ctx, tx, planID)
	return converge != nil, err
}

func (db *DB) FleetExecutorConvergePlan(ctx context.Context, planID string) (*FleetExecutorConvergePlanFact, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, _, converge, digest, err := fleetExecutorPlanTx(ctx, tx, planID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && converge == nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &FleetExecutorConvergePlanFact{Resource: converge.Resource, CommitSHA: converge.CommitSHA, PlanSHA256: digest}, nil
}

func (db *DB) FleetExecutorLedgerRows(ctx context.Context, kind, planID string) ([]FleetExecutorLedgerRow, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return fleetLedgerRowsTx(ctx, tx, kind, planID)
}

var (
	_ FleetExecutorOpsReader          = (*DB)(nil)
	_ FleetExecutorConvergePlanReader = (*DB)(nil)
)

// fleetExecutorLaneProofTx is the PG release hook: inside the release or
// abandon transaction (after its plan lock) it returns the proof to decide
// with and whether planID is an executor-lane holder.
func fleetExecutorLaneProofTx(ctx context.Context, tx pgx.Tx, planID string, evidence *executor.ProviderEvidence, base *lifecycle.TerminalProof, now time.Time) (*lifecycle.TerminalProof, bool, error) {
	if planID == "" {
		return base, false, nil
	}
	attempts, err := fleetExecutorAttemptFactsTx(ctx, tx, planID)
	if err != nil {
		return nil, false, err
	}
	var rows []FleetExecutorLedgerRow
	var device FleetExecutorDeviceFact
	if FleetExecutorLane(attempts) {
		if rows, err = fleetLedgerRowsTx(ctx, tx, executor.KindStopAttestation, planID); err != nil {
			return nil, false, err
		}
		if latest := latestFleetExecutorAttempt(attempts); latest != nil {
			if device, err = fleetExecutorDeviceQuery(ctx, tx, FleetExecutorDeviceID(latest.PrincipalSubject)); err != nil {
				return nil, false, err
			}
		}
	}
	return FleetExecutorReleaseProof(attempts, rows, evidence, device, base, now)
}
