package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/effect"
)

type PGEffectStore struct {
	db *DB
}

func NewPGEffectStore(db *DB) (*PGEffectStore, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("external effect store is unavailable")
	}
	return &PGEffectStore{db: db}, nil
}

func (s *PGEffectStore) Reserve(ctx context.Context, reservation effect.Reservation) (effect.ReservationResult, error) {
	if err := validateEffectReservation(reservation); err != nil {
		return effect.ReservationResult{}, err
	}
	tx, err := s.db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return effect.ReservationResult{}, err
	}
	defer tx.Rollback(ctx)
	app := appEffectResource(reservation.Resource)
	if app != "" {
		// Every unresolved mutable app effect shares one durable transaction
		// gate. Workers release their advisory app lock while deferred, so this
		// is the boundary that prevents restart and canary effects overlapping.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "norn:effect-app:"+app); err != nil {
			return effect.ReservationResult{}, err
		}
	}

	var authority string
	if err := tx.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity WHERE singleton FOR SHARE`).Scan(&authority); err != nil {
		return effect.ReservationResult{}, fmt.Errorf("load control authority: %w", err)
	}
	if reservation.Authority != authority {
		return effect.ReservationResult{}, fmt.Errorf("external effect authority %q does not match control authority", reservation.Authority)
	}

	var status, owner string
	var generation int64
	var lockedUntil *time.Time
	err = tx.QueryRow(ctx, `
		SELECT status, locked_by, lock_generation, locked_until
		FROM operations
		WHERE id=$1
		FOR UPDATE
	`, reservation.OperationClaim.OperationID).Scan(&status, &owner, &generation, &lockedUntil)
	var databaseNow time.Time
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow)
	}
	leaseCurrent := lockedUntil != nil && lockedUntil.After(databaseNow)
	claimOwned := err == nil && status == "running" && owner == reservation.OperationClaim.OwnerID &&
		generation == reservation.OperationClaim.Generation && leaseCurrent
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return effect.ReservationResult{}, err
	}
	if !claimOwned {
		claim, claimErr := NewOperationClaim(reservation.OperationClaim.OperationID, reservation.OperationClaim.OwnerID, reservation.OperationClaim.Generation)
		if claimErr != nil {
			return effect.ReservationResult{}, claimErr
		}
		return effect.ReservationResult{}, ownershipLost(claim)
	}

	existing, err := queryEffectRecord(ctx, tx, `
		SELECT `+effectColumns+`
		FROM operation_effects
		WHERE authority=$1::uuid AND operation_id=$2 AND stage=$3 AND input_digest=$4
		  AND lifecycle <> 'resolved'
		ORDER BY created_at DESC
		LIMIT 1
	`, reservation.Authority, reservation.OperationClaim.OperationID, reservation.Stage, reservation.InputDigest)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return effect.ReservationResult{}, err
		}
		return effect.ReservationResult{Record: existing}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return effect.ReservationResult{}, err
	}
	if app != "" {
		var blockingID string
		err = tx.QueryRow(ctx, `SELECT id FROM operation_effects WHERE authority=$1::uuid AND split_part(resource,'/',2)=$2 AND lifecycle IN ('reserved','launched') LIMIT 1`, reservation.Authority, app).Scan(&blockingID)
		if err == nil {
			return effect.ReservationResult{}, &effect.ResourceBlockedError{Resource: reservation.Resource, BlockingEffectID: blockingID}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return effect.ReservationResult{}, err
		}
	}

	token := effect.Token{EffectID: uuid.NewString(), Generation: reservation.OperationClaim.Generation}
	result, err := tx.Exec(ctx, `
		INSERT INTO operation_effects
			(id, generation, authority, resource, operation_id, claim_owner, claim_generation,
			 stage, input_digest, launch_payload, supervisor, supervisor_execution_id, lifecycle)
		VALUES ($1,$2,$3::uuid,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12,'reserved')
		ON CONFLICT DO NOTHING
	`, token.EffectID, token.Generation, reservation.Authority, reservation.Resource,
		reservation.OperationClaim.OperationID, reservation.OperationClaim.OwnerID, reservation.OperationClaim.Generation,
		reservation.Stage, reservation.InputDigest, []byte(reservation.LaunchPayload), reservation.Supervisor, reservation.SupervisorExecutionID)
	if err != nil {
		return effect.ReservationResult{}, err
	}
	if result.RowsAffected() == 1 {
		record := effect.Record{Token: token, Reservation: reservation, Lifecycle: effect.LifecycleReserved}
		if err := tx.Commit(ctx); err != nil {
			return effect.ReservationResult{}, err
		}
		return effect.ReservationResult{Record: record, Created: true}, nil
	}

	existing, err = queryEffectRecord(ctx, tx, `
		SELECT `+effectColumns+`
		FROM operation_effects
		WHERE authority=$1::uuid AND operation_id=$2 AND stage=$3 AND input_digest=$4
		  AND lifecycle <> 'resolved'
		ORDER BY created_at DESC
		LIMIT 1
	`, reservation.Authority, reservation.OperationClaim.OperationID, reservation.Stage, reservation.InputDigest)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return effect.ReservationResult{}, err
		}
		return effect.ReservationResult{Record: existing}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return effect.ReservationResult{}, err
	}

	var blockingID string
	query, args := `SELECT id FROM operation_effects WHERE authority=$1::uuid AND resource=$2 AND lifecycle IN ('reserved','launched') LIMIT 1`, []any{reservation.Authority, reservation.Resource}
	if app != "" {
		query, args = `SELECT id FROM operation_effects WHERE authority=$1::uuid AND split_part(resource,'/',2)=$2 AND lifecycle IN ('reserved','launched') LIMIT 1`, []any{reservation.Authority, app}
	}
	err = tx.QueryRow(ctx, query, args...).Scan(&blockingID)
	if err == nil {
		return effect.ReservationResult{}, &effect.ResourceBlockedError{Resource: reservation.Resource, BlockingEffectID: blockingID}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return effect.ReservationResult{}, err
	}
	return effect.ReservationResult{}, fmt.Errorf("supervisor execution identity is already reserved")
}

func appEffectResource(resource string) string {
	parts := strings.Split(resource, "/")
	if len(parts) >= 3 && parts[0] == "app" && strings.TrimSpace(parts[1]) != "" {
		return parts[1]
	}
	return ""
}

// Authority returns the control-plane authority that scopes effect rows.
func (s *PGEffectStore) Authority(ctx context.Context) (string, error) {
	var authority string
	if err := s.db.Pool.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity WHERE singleton`).Scan(&authority); err != nil {
		return "", fmt.Errorf("load control authority: %w", err)
	}
	return authority, nil
}

// UnresolvedForResource returns the reserved or launched effect holding the
// resource gate, regardless of which operation owns it.
func (s *PGEffectStore) UnresolvedForResource(ctx context.Context, authority, resource string) (effect.Record, bool, error) {
	record, err := queryEffectRecord(ctx, s.db.Pool, `
		SELECT `+effectColumns+`
		FROM operation_effects
		WHERE authority=$1::uuid AND resource=$2 AND lifecycle IN ('reserved','launched')
	`, authority, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return effect.Record{}, false, nil
	}
	if err != nil {
		return effect.Record{}, false, err
	}
	return record, true, nil
}

// LatestForOperation returns the original durable effect for an operation
// stage, including a completed record whose terminal operation write was lost.
// Recovery must use its stored descriptor instead of sampling a changed runtime.
func (s *PGEffectStore) LatestForOperation(ctx context.Context, operationID, stage string) (effect.Record, bool, error) {
	if s == nil || s.db == nil || s.db.Pool == nil || strings.TrimSpace(operationID) == "" || strings.TrimSpace(stage) == "" {
		return effect.Record{}, false, fmt.Errorf("operation effect lookup is unavailable")
	}
	record, err := queryEffectRecord(ctx, s.db.Pool, `
		SELECT `+effectColumns+`
		FROM operation_effects
		WHERE operation_id=$1 AND stage=$2
		ORDER BY created_at DESC
		LIMIT 1
	`, operationID, stage)
	if errors.Is(err, pgx.ErrNoRows) {
		return effect.Record{}, false, nil
	}
	if err != nil {
		return effect.Record{}, false, err
	}
	return record, true, nil
}

// CompletedSnapshotOperations returns only effects whose public operation
// success is already durable. Those are safe private-artifact cleanup targets.
func (s *PGEffectStore) CompletedSnapshotOperations(ctx context.Context) ([]effect.Record, error) {
	if s == nil || s.db == nil || s.db.Pool == nil {
		return nil, fmt.Errorf("operation effect lookup is unavailable")
	}
	rows, err := s.db.Pool.Query(ctx, `SELECT `+effectColumns+` FROM operation_effects e JOIN operations o ON o.id=e.operation_id WHERE e.stage='app.snapshot' AND e.lifecycle='completed' AND o.status='succeeded'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []effect.Record
	for rows.Next() {
		record, err := scanEffectRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *PGEffectStore) MarkLaunched(ctx context.Context, token effect.Token, identity effect.ExecutionIdentity) error {
	if err := validateEffectToken(token); err != nil {
		return err
	}
	if strings.TrimSpace(identity.Supervisor) == "" || strings.TrimSpace(identity.SupervisorExecutionID) == "" || strings.TrimSpace(identity.RuntimeInstanceID) == "" {
		return fmt.Errorf("external effect execution identity is incomplete")
	}
	result, err := s.db.Pool.Exec(ctx, `
		UPDATE operation_effects
		SET lifecycle='launched', runtime_instance_id=$1, launched_at=now(), updated_at=now()
		WHERE id=$2 AND generation=$3 AND lifecycle='reserved'
		  AND supervisor=$4 AND supervisor_execution_id=$5 AND runtime_instance_id=''
	`, identity.RuntimeInstanceID, token.EffectID, token.Generation, identity.Supervisor, identity.SupervisorExecutionID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return effect.ErrStaleToken
	}
	return nil
}

func (s *PGEffectStore) Complete(ctx context.Context, token effect.Token, completion effect.Completion) error {
	if err := validateEffectToken(token); err != nil {
		return err
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	record, err := queryEffectRecord(ctx, tx, `SELECT `+effectColumns+` FROM operation_effects WHERE id=$1 AND generation=$2 FOR UPDATE`, token.EffectID, token.Generation)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return effect.ErrStaleToken
		}
		return err
	}
	if record.Lifecycle != effect.LifecycleReserved && record.Lifecycle != effect.LifecycleLaunched {
		return effect.ErrStaleToken
	}
	if err := validateEffectCompletion(record, completion); err != nil {
		return err
	}
	verification := completion.Verification
	result, err := tx.Exec(ctx, `
		UPDATE operation_effects
		SET lifecycle='completed', outcome=$1, exit_code=$2, runtime_instance_id=$3,
		    result_digest=$4, result_reference=$5, evidence_source=$6, evidence_reference=$7,
		    evidence_observed_at=$8, completed_at=now(), updated_at=now()
		WHERE id=$9 AND generation=$10 AND lifecycle IN ('reserved','launched')
		  AND supervisor_execution_id=$11
	`, completion.Outcome, completion.ExitCode, verification.RuntimeInstanceID, verification.ResultDigest,
		verification.ResultReference, verification.EvidenceSource, verification.EvidenceReference,
		verification.ObservedAt, token.EffectID, token.Generation, verification.SupervisorExecutionID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return effect.ErrStaleToken
	}
	return tx.Commit(ctx)
}

func (s *PGEffectStore) Resolve(ctx context.Context, token effect.Token, resolution effect.Resolution) error {
	if err := validateEffectToken(token); err != nil {
		return err
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	record, err := queryEffectRecord(ctx, tx, `SELECT `+effectColumns+` FROM operation_effects WHERE id=$1 AND generation=$2 FOR UPDATE`, token.EffectID, token.Generation)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return effect.ErrStaleToken
		}
		return err
	}
	if record.Lifecycle != effect.LifecycleReserved && record.Lifecycle != effect.LifecycleLaunched {
		return effect.ErrStaleToken
	}
	if err := validateEffectResolution(record, resolution); err != nil {
		return err
	}
	verification := resolution.Verification
	result, err := tx.Exec(ctx, `
		UPDATE operation_effects
		SET lifecycle='resolved', runtime_instance_id=$1, resolution_decision=$2,
		    evidence_source=$3, evidence_reference=$4, evidence_observed_at=$5,
		    resolved_at=now(), updated_at=now()
		WHERE id=$6 AND generation=$7 AND lifecycle IN ('reserved','launched')
		  AND supervisor_execution_id=$8
	`, verification.RuntimeInstanceID, resolution.Decision, verification.EvidenceSource,
		verification.EvidenceReference, verification.ObservedAt, token.EffectID,
		token.Generation, verification.SupervisorExecutionID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return effect.ErrStaleToken
	}
	return tx.Commit(ctx)
}

const effectColumns = `
	id, generation, authority::text, resource, operation_id, claim_owner, claim_generation,
	stage, input_digest, launch_payload, supervisor, supervisor_execution_id, runtime_instance_id,
	lifecycle, outcome, exit_code, result_digest, result_reference, evidence_source,
	evidence_reference, evidence_observed_at, resolution_decision
`

func queryEffectRecord(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, query string, args ...any) (effect.Record, error) {
	return scanEffectRecord(queryer.QueryRow(ctx, query, args...))
}

func scanEffectRecord(row interface{ Scan(...any) error }) (effect.Record, error) {
	var record effect.Record
	var launchPayload []byte
	var outcome effect.Outcome
	var exitCode *int
	var resultDigest, resultReference, evidenceSource, evidenceReference string
	var evidenceObservedAt *time.Time
	var resolutionDecision effect.VerificationDecision
	err := row.Scan(
		&record.Token.EffectID, &record.Token.Generation, &record.Reservation.Authority,
		&record.Reservation.Resource, &record.Reservation.OperationClaim.OperationID,
		&record.Reservation.OperationClaim.OwnerID, &record.Reservation.OperationClaim.Generation,
		&record.Reservation.Stage, &record.Reservation.InputDigest, &launchPayload,
		&record.Reservation.Supervisor, &record.Reservation.SupervisorExecutionID,
		&record.Execution.RuntimeInstanceID, &record.Lifecycle, &outcome, &exitCode,
		&resultDigest, &resultReference, &evidenceSource, &evidenceReference,
		&evidenceObservedAt, &resolutionDecision,
	)
	if err != nil {
		return effect.Record{}, err
	}
	record.Reservation.LaunchPayload = json.RawMessage(append([]byte(nil), launchPayload...))
	record.Execution.Supervisor = record.Reservation.Supervisor
	record.Execution.SupervisorExecutionID = record.Reservation.SupervisorExecutionID
	if record.Lifecycle == effect.LifecycleCompleted {
		verification := effect.Verification{
			Decision:              completionDecision(outcome),
			InputDigest:           record.Reservation.InputDigest,
			ResultDigest:          resultDigest,
			ResultReference:       resultReference,
			SupervisorExecutionID: record.Reservation.SupervisorExecutionID,
			RuntimeInstanceID:     record.Execution.RuntimeInstanceID,
			EvidenceSource:        evidenceSource,
			EvidenceReference:     evidenceReference,
		}
		if evidenceObservedAt != nil {
			verification.ObservedAt = *evidenceObservedAt
		}
		record.Completion = &effect.Completion{Outcome: outcome, ExitCode: exitCode, Verification: verification}
	}
	return record, nil
}

func validateEffectReservation(reservation effect.Reservation) error {
	expected, err := effect.ComputeInputDigest(reservation)
	if err != nil {
		return err
	}
	if reservation.InputDigest != expected {
		return fmt.Errorf("external effect input digest does not match canonical launch input")
	}
	if strings.TrimSpace(reservation.Authority) == "" || strings.TrimSpace(reservation.Resource) == "" ||
		strings.TrimSpace(reservation.OperationClaim.OperationID) == "" || strings.TrimSpace(reservation.OperationClaim.OwnerID) == "" ||
		reservation.OperationClaim.Generation <= 0 || strings.TrimSpace(reservation.Stage) == "" ||
		strings.TrimSpace(reservation.Supervisor) == "" || strings.TrimSpace(reservation.SupervisorExecutionID) == "" {
		return fmt.Errorf("external effect reservation is incomplete")
	}
	return nil
}

func validateEffectToken(token effect.Token) error {
	if strings.TrimSpace(token.EffectID) == "" || token.Generation <= 0 {
		return fmt.Errorf("external effect token is incomplete")
	}
	return nil
}

func validateEffectCompletion(record effect.Record, completion effect.Completion) error {
	verification := completion.Verification
	failedDecision := verification.Decision == effect.VerificationFailed || verification.Decision == effect.VerificationFailedRepeatSafe
	if (completion.Outcome == effect.OutcomeSucceeded && verification.Decision != effect.VerificationSucceeded) ||
		(completion.Outcome == effect.OutcomeFailed && !failedDecision) ||
		(completion.Outcome != effect.OutcomeSucceeded && completion.Outcome != effect.OutcomeFailed) {
		return fmt.Errorf("external effect completion outcome and decision do not match")
	}
	if err := validateEffectVerification(record, verification); err != nil {
		return err
	}
	if strings.TrimSpace(verification.RuntimeInstanceID) == "" {
		return fmt.Errorf("external effect completion runtime identity is required")
	}
	if record.Execution.RuntimeInstanceID != "" && verification.RuntimeInstanceID != record.Execution.RuntimeInstanceID {
		return fmt.Errorf("external effect completion runtime identity does not match launch")
	}
	if completion.Outcome == effect.OutcomeSucceeded &&
		(strings.TrimSpace(verification.ResultDigest) == "" || strings.TrimSpace(verification.ResultReference) == "") {
		return fmt.Errorf("successful external effect completion result evidence is incomplete")
	}
	return nil
}

func validateEffectResolution(record effect.Record, resolution effect.Resolution) error {
	verification := resolution.Verification
	if resolution.Decision != verification.Decision ||
		(resolution.Decision != effect.VerificationNeverLaunched && resolution.Decision != effect.VerificationStoppedRepeatSafe) {
		return fmt.Errorf("external effect resolution decision is not repeat-safe")
	}
	if err := validateEffectVerification(record, verification); err != nil {
		return err
	}
	if resolution.Decision == effect.VerificationNeverLaunched {
		if verification.RuntimeInstanceID != "" {
			return fmt.Errorf("never-launched resolution cannot name a runtime instance")
		}
		if record.Lifecycle == effect.LifecycleLaunched || record.Execution.RuntimeInstanceID != "" {
			return fmt.Errorf("never-launched resolution cannot erase a recorded launch")
		}
	}
	if resolution.Decision == effect.VerificationStoppedRepeatSafe {
		if strings.TrimSpace(verification.RuntimeInstanceID) == "" {
			return fmt.Errorf("stopped resolution runtime identity is required")
		}
		if record.Execution.RuntimeInstanceID != "" && verification.RuntimeInstanceID != record.Execution.RuntimeInstanceID {
			return fmt.Errorf("stopped resolution runtime identity does not match launch")
		}
	}
	return nil
}

func validateEffectVerification(record effect.Record, verification effect.Verification) error {
	if verification.InputDigest != record.Reservation.InputDigest ||
		verification.SupervisorExecutionID != record.Reservation.SupervisorExecutionID ||
		strings.TrimSpace(verification.EvidenceSource) == "" ||
		strings.TrimSpace(verification.EvidenceReference) == "" ||
		verification.ObservedAt.IsZero() {
		return fmt.Errorf("external effect verification is not bound to the reserved execution")
	}
	return nil
}

func completionDecision(outcome effect.Outcome) effect.VerificationDecision {
	if outcome == effect.OutcomeSucceeded {
		return effect.VerificationSucceeded
	}
	if outcome == effect.OutcomeFailed {
		// The row records a final failure; it never asserts repeat safety.
		return effect.VerificationFailed
	}
	return ""
}

var (
	_ effect.Store         = (*PGEffectStore)(nil)
	_ effect.RecoveryStore = (*PGEffectStore)(nil)
)
