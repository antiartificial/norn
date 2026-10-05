package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

// fleetTargetEpochRefusalSQL is the epoch-check predicate M7 requires:
// instead of taking a new lock, legacy heartbeat and advance add this
// "AND NOT EXISTS" clause to their existing single UPDATE. It resolves
// plan's target purely through the (already-immutable) cluster alias, so an
// unregistered cluster or an empty registry makes it vacuously true — byte-
// identical to the pre-fence UPDATE (Q1). planIDExpr names the column or
// table-qualified expression holding the attempt's plan_id in that UPDATE's
// own scope (bare "plan_id" when the table has no alias, "a.plan_id" when
// it is aliased "a").
func fleetTargetEpochRefusalSQL(planIDExpr string) string {
	return `
	  AND NOT EXISTS (
	    SELECT 1 FROM operations p
	    JOIN fleet_target_aliases ca ON ca.alias = 'cluster:' || (p.payload->>'cluster')
	    JOIN fleet_target_fences f ON f.target_id = ca.target_id
	    CROSS JOIN fleet_authority_epoch e
	    WHERE p.id = ` + planIDExpr + ` AND f.authority_epoch < e.epoch
	  )`
}

// bindFleetTargetFenceTx is the fence Bind transition (plan.md §2.2) shared
// by CreateFleetRunnerAttempt (first attempt) and RecoverFleetRunnerAttempt
// (recovery; M13 means both re-bind identically on an epoch advance). It
// must run inside tx immediately after the existing "norn.fleet-runner:"
// plan advisory lock and before any fleet_runner_attempts row is locked or
// inserted, so the fence (lock-order position 4) is always locked before
// attempts (position 6). An abandoned plan refuses permanently (B1/H7); an
// empty registry is byte-identical to no fence check at all (Q1).
func bindFleetTargetFenceTx(ctx context.Context, tx pgx.Tx, planID string) error {
	abandoned, err := IsFleetPlanAbandoned(ctx, tx, planID)
	if err != nil {
		return err
	}
	if abandoned {
		return &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetHolderAbandoned}
	}
	// Bind resolves the plan's own cluster alias exactly as acquire did
	// (strict, plan.md §2.2 and the etcd backend): an empty registry is
	// byte-identical to no fence check (Q1); a non-empty registry with an
	// unregistered cluster is refused (fleet_target_unregistered); and a
	// registered target whose fence this plan and nonce do not hold is
	// refused by DecideBind, never treated as unfenced.
	targetID, fence, epoch, nonceHash, err := lockFleetTargetFenceForPlanID(ctx, tx, planID, true, true)
	if err != nil || targetID == "" {
		return err
	}
	next, decErr := lifecycle.DecideBind(fence, planID, nonceHash, epoch)
	if decErr != nil {
		return decErr
	}
	return PutFleetTargetFence(ctx, tx, next)
}

var ErrFleetRunnerAttemptConflict = fmt.Errorf("fleet runner attempt changed or is not transitionable")

func (db *DB) CreateFleetRunnerAttempt(ctx context.Context, attempt *model.FleetRunnerAttempt) error {
	if attempt == nil {
		return fmt.Errorf("attempt is required")
	}
	if attempt.PhaseStartedAt.IsZero() {
		attempt.PhaseStartedAt = attempt.StartedAt
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('norn.fleet-runner:' || $1))`, attempt.PlanID); err != nil {
		return err
	}
	if err := bindFleetTargetFenceTx(ctx, tx, attempt.PlanID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt), 0) + 1 FROM fleet_runner_attempts WHERE plan_id = $1`, attempt.PlanID).Scan(&attempt.Attempt); err != nil {
		return err
	}
	if attempt.Attempt == 1 {
		attempt.RootAttemptID = attempt.ID
	} else if err := tx.QueryRow(ctx, `SELECT root_attempt_id FROM fleet_runner_attempts WHERE plan_id=$1 ORDER BY attempt ASC LIMIT 1`, attempt.PlanID).Scan(&attempt.RootAttemptID); err != nil {
		return err
	}
	metadata, _ := json.Marshal(attempt.Metadata)
	_, err = tx.Exec(ctx, `
		INSERT INTO fleet_runner_attempts (
			id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
			commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
			heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
			heartbeat_at, updated_at, finished_at, last_error, metadata, heartbeat_expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$21::timestamptz + ($17::int * interval '1 second'))
	`, attempt.ID, attempt.PlanID, attempt.Attempt, attempt.RootAttemptID, attempt.SourceDispatchRunID, attempt.PilotRunID, attempt.Recovery, attempt.RunnerAttemptID, attempt.Status, attempt.CurrentPhase,
		attempt.CommitSHA, attempt.PlanSHA256, attempt.WorkflowURL, attempt.PrincipalSubject, attempt.RetryOf,
		attempt.HeartbeatSequence, attempt.HeartbeatTimeoutSeconds, attempt.Revision, attempt.StartedAt, attempt.PhaseStartedAt,
		attempt.HeartbeatAt, attempt.UpdatedAt, attempt.FinishedAt, attempt.LastError, metadata)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	attempt.SetDerivedFields()
	return nil
}

func (db *DB) ListFleetRunnerAttempts(ctx context.Context, planID string, limit int) ([]model.FleetRunnerAttempt, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if err := db.AbandonStaleFleetRunnerAttempts(ctx, planID, time.Now().UTC()); err != nil {
		return nil, err
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
		       commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
		       heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
		       heartbeat_at, updated_at, finished_at, last_error, metadata
		FROM fleet_runner_attempts WHERE plan_id = $1
		ORDER BY attempt DESC LIMIT $2
	`, planID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.FleetRunnerAttempt{}
	for rows.Next() {
		attempt, err := scanFleetRunnerAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *attempt)
	}
	return out, rows.Err()
}

func (db *DB) GetFleetRunnerAttempt(ctx context.Context, id string) (*model.FleetRunnerAttempt, error) {
	if err := db.abandonStaleFleetRunnerAttempt(ctx, id, time.Now().UTC()); err != nil {
		return nil, err
	}
	return scanFleetRunnerAttempt(db.Pool.QueryRow(ctx, `
		SELECT id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
		       commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
		       heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
		       heartbeat_at, updated_at, finished_at, last_error, metadata
		FROM fleet_runner_attempts WHERE id = $1
	`, id))
}

func (db *DB) abandonStaleFleetRunnerAttempt(ctx context.Context, id string, now time.Time) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE fleet_runner_attempts
		SET status='abandoned', finished_at=$1, updated_at=$1, revision=revision+1,
		    last_error=CASE WHEN last_error='' THEN 'runner heartbeat expired' ELSE last_error END
		WHERE id=$2 AND status IN ('queued','running')
		  AND heartbeat_at + (heartbeat_timeout_seconds * interval '1 second') < $1
	`, now, id)
	return err
}

func (db *DB) HeartbeatFleetRunnerAttempt(ctx context.Context, id, phase string, sequence, expectedRevision int64, message string) (*model.FleetRunnerAttempt, error) {
	row := db.Pool.QueryRow(ctx, `
		UPDATE fleet_runner_attempts
		SET status = 'running', heartbeat_sequence = $1, heartbeat_at = now(),
		    heartbeat_expires_at = now() + (heartbeat_timeout_seconds * interval '1 second'),
		    updated_at = now(), revision = revision + 1,
		    metadata = metadata || jsonb_build_object('heartbeatMessage', $2::text)
		WHERE id = $3 AND current_phase = $4 AND revision = $5
		  AND status IN ('queued', 'running') AND heartbeat_sequence < $1
		  `+fleetTargetEpochRefusalSQL("plan_id")+`
		RETURNING id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
		          commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
		          heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
		          heartbeat_at, updated_at, finished_at, last_error, metadata
	`, sequence, message, id, phase, expectedRevision)
	attempt, err := scanFleetRunnerAttempt(row)
	if err == pgx.ErrNoRows {
		return nil, ErrFleetRunnerAttemptConflict
	}
	return attempt, err
}

// fleetRunnerAdvanceSQL is shared by the non-terminal (lock-free) and
// terminal (fenced) branches of AdvanceFleetRunnerAttempt below: it is the
// one statement that transitions the attempt row, identical either way.
var fleetRunnerAdvanceSQL = `
	UPDATE fleet_runner_attempts a
	SET current_phase = $1, status = $2, updated_at = now(), heartbeat_at = now(),
	    heartbeat_expires_at = now() + (heartbeat_timeout_seconds * interval '1 second'),
	    phase_started_at = CASE WHEN $2 = 'succeeded' THEN a.phase_started_at ELSE now() END,
	    revision = revision + 1, finished_at = $3
	WHERE a.id = $4 AND a.current_phase = $5 AND a.revision = $6
	  AND a.status IN ('queued', 'running')
	  AND EXISTS (
	    SELECT 1 FROM operations o
	    WHERE o.kind = 'fleet.reconciliation' AND o.ref = a.plan_id
	      AND o.status = 'succeeded' AND o.payload->>'phase' = $5
	      AND o.payload->>'attemptId' = a.id
	  )
	  ` + fleetTargetEpochRefusalSQL("a.plan_id") + `
	RETURNING id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
	          commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
	          heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
	          heartbeat_at, updated_at, finished_at, last_error, metadata`

func (db *DB) AdvanceFleetRunnerAttempt(ctx context.Context, id, fromPhase, toPhase string, expectedRevision int64, terminal bool) (*model.FleetRunnerAttempt, error) {
	status := model.FleetRunnerAttemptRunning
	var finished interface{}
	if terminal {
		status = model.FleetRunnerAttemptSucceeded
		finished = time.Now().UTC()
	}
	if !terminal {
		attempt, err := scanFleetRunnerAttempt(db.Pool.QueryRow(ctx, fleetRunnerAdvanceSQL, toPhase, status, finished, id, fromPhase, expectedRevision))
		if err == pgx.ErrNoRows {
			return nil, ErrFleetRunnerAttemptConflict
		}
		return attempt, err
	}
	// Terminal: Release: succeeded (plan.md §2.2) must commit atomically with
	// the same UPDATE that marks the attempt succeeded, never as a separate
	// best-effort call after the fact (a crash between the two would leave a
	// succeeded attempt with its fence still held, or silently drop the
	// release). The fence this plan holds (if any) is locked FOR UPDATE
	// (lock-order position 4) before the attempt row (position 6) below.
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var planID string
	if err := tx.QueryRow(ctx, `SELECT plan_id FROM fleet_runner_attempts WHERE id=$1`, id).Scan(&planID); err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrFleetRunnerAttemptConflict
		}
		return nil, err
	}
	// The target is resolved through the plan's cluster alias (non-strict,
	// mirroring the etcd backend), the same resolution the epoch predicate
	// in fleetRunnerAdvanceSQL uses, and its fence is locked FOR UPDATE
	// before the attempt row.
	targetID, fence, _, _, err := lockFleetTargetFenceForPlanID(ctx, tx, planID, true, false)
	if err != nil {
		return nil, err
	}
	if targetID != "" && fence.Held && fence.HolderPlanID != planID {
		// Never release a fence some other plan holds: DecideRelease checks
		// only Held, not the holder's identity (mirrors the etcd side's
		// fleet_target_execution_occupied). A fence this plan should hold
		// but that is free is refused by DecideRelease below.
		return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetExecutionOccupied}
	}
	attempt, err := scanFleetRunnerAttempt(tx.QueryRow(ctx, fleetRunnerAdvanceSQL, toPhase, status, finished, id, fromPhase, expectedRevision))
	if err == pgx.ErrNoRows {
		return nil, ErrFleetRunnerAttemptConflict
	}
	if err != nil {
		return nil, err
	}
	if targetID != "" {
		holder, err := FleetTargetHolderFacts(ctx, tx, planID)
		if err != nil {
			return nil, err
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return nil, err
		}
		next, relErr := lifecycle.DecideRelease(fence, holder, lifecycle.ReleaseModeSucceeded, nil, now)
		if relErr != nil {
			return nil, relErr
		}
		if err := PutFleetTargetFence(ctx, tx, next); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return attempt, nil
}

func (db *DB) FailFleetRunnerAttempt(ctx context.Context, id, phase, message string) error {
	result, err := db.Pool.Exec(ctx, `
		UPDATE fleet_runner_attempts
		SET status='failed', last_error=$1, finished_at=now(), updated_at=now(), revision=revision+1
		WHERE id=$2 AND current_phase=$3 AND status IN ('queued','running')
	`, message, id, phase)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrFleetRunnerAttemptConflict
	}
	return nil
}

// InsertFleetReconciliation stores append-only phase evidence and, for a failed
// checkpoint, terminates the bound runner attempt in the same transaction.
// This prevents a failed runner from remaining live when its proof operation
// committed, or from becoming failed without the corresponding audit evidence.
func (db *DB) InsertFleetReconciliation(ctx context.Context, op *model.Operation, attemptID string) error {
	if attemptID == "" {
		return db.InsertCompletedOperation(ctx, op)
	}
	payload, metadata, err := prepareOperation(op)
	if err != nil {
		return err
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var planID, phase, status, commitSHA, planSHA string
	var phaseStartedAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT plan_id, current_phase, status, commit_sha, plan_sha256, phase_started_at
		FROM fleet_runner_attempts WHERE id=$1 FOR UPDATE
	`, attemptID).Scan(&planID, &phase, &status, &commitSHA, &planSHA, &phaseStartedAt); err != nil {
		return err
	}
	payloadPhase, _ := op.Payload["phase"].(string)
	payloadCommit, _ := op.Payload["commitSha"].(string)
	payloadPlan, _ := op.Payload["planSha256"].(string)
	if planID != op.Ref || phase != payloadPhase || (status != "queued" && status != "running") || commitSHA != payloadCommit || planSHA != payloadPlan {
		return ErrFleetRunnerAttemptConflict
	}
	// The server, not the runner, owns phase start. Persist it on the immutable
	// checkpoint so later timing displays have truthful phase bounds.
	op.StartedAt = phaseStartedAt
	if _, err := tx.Exec(ctx, `
		INSERT INTO operations (id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,now(),$16)
	`, op.ID, op.Kind, op.App, op.SagaID, op.Ref, op.Status, op.Risk, op.Source, op.Message,
		payload, metadata, op.Attempts, op.MaxAttempts, op.NextAttemptAt, op.StartedAt, op.FinishedAt); err != nil {
		return err
	}
	if op.Status == model.OperationFailed {
		if _, err := tx.Exec(ctx, `
			UPDATE fleet_runner_attempts
			SET status='failed', last_error=$1, finished_at=now(), updated_at=now(), revision=revision+1
			WHERE id=$2
		`, op.Message, attemptID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (db *DB) CancelFleetRunnerAttempt(ctx context.Context, id string, expectedRevision int64, reason string) (*model.FleetRunnerAttempt, error) {
	row := db.Pool.QueryRow(ctx, `
		UPDATE fleet_runner_attempts
		SET status='canceled', last_error=$1, finished_at=now(), updated_at=now(), revision=revision+1
		WHERE id=$2 AND revision=$3 AND status IN ('queued','running')
		RETURNING id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
		          commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
		          heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
		          heartbeat_at, updated_at, finished_at, last_error, metadata
	`, reason, id, expectedRevision)
	attempt, err := scanFleetRunnerAttempt(row)
	if err == pgx.ErrNoRows {
		return nil, ErrFleetRunnerAttemptConflict
	}
	return attempt, err
}

func (db *DB) RetryFleetRunnerAttempt(ctx context.Context, previous *model.FleetRunnerAttempt, replacement *model.FleetRunnerAttempt) error {
	if previous == nil || replacement == nil {
		return fmt.Errorf("previous and replacement attempts are required")
	}
	if replacement.PhaseStartedAt.IsZero() {
		replacement.PhaseStartedAt = replacement.StartedAt
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('norn.fleet-runner:' || $1))`, previous.PlanID); err != nil {
		return err
	}
	var status model.FleetRunnerAttemptStatus
	var revision int64
	var sourceRunID int64
	var pilotRunID string
	if err := tx.QueryRow(ctx, `SELECT status, revision, source_dispatch_run_id, pilot_run_id FROM fleet_runner_attempts WHERE id=$1 FOR UPDATE`, previous.ID).Scan(&status, &revision, &sourceRunID, &pilotRunID); err != nil {
		return err
	}
	if status != model.FleetRunnerAttemptFailed && status != model.FleetRunnerAttemptAbandoned && status != model.FleetRunnerAttemptCanceled {
		return ErrFleetRunnerAttemptConflict
	}
	if revision != previous.Revision || sourceRunID != previous.SourceDispatchRunID || sourceRunID != replacement.SourceDispatchRunID || pilotRunID != previous.PilotRunID || pilotRunID != replacement.PilotRunID {
		return ErrFleetRunnerAttemptConflict
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt), 0) + 1 FROM fleet_runner_attempts WHERE plan_id=$1`, previous.PlanID).Scan(&replacement.Attempt); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT root_attempt_id FROM fleet_runner_attempts WHERE id=$1`, previous.ID).Scan(&replacement.RootAttemptID); err != nil {
		return err
	}
	metadata, _ := json.Marshal(replacement.Metadata)
	_, err = tx.Exec(ctx, `
		INSERT INTO fleet_runner_attempts (
			id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
			commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
			heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
			heartbeat_at, updated_at, finished_at, last_error, metadata, heartbeat_expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$21::timestamptz + ($17::int * interval '1 second'))
	`, replacement.ID, replacement.PlanID, replacement.Attempt, replacement.RootAttemptID, replacement.SourceDispatchRunID, replacement.PilotRunID, replacement.Recovery, replacement.RunnerAttemptID,
		replacement.Status, replacement.CurrentPhase, replacement.CommitSHA, replacement.PlanSHA256,
		replacement.WorkflowURL, replacement.PrincipalSubject, replacement.RetryOf,
		replacement.HeartbeatSequence, replacement.HeartbeatTimeoutSeconds, replacement.Revision,
		replacement.StartedAt, replacement.PhaseStartedAt, replacement.HeartbeatAt, replacement.UpdatedAt, replacement.FinishedAt,
		replacement.LastError, metadata)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	replacement.SetDerivedFields()
	return nil
}

// RecoverFleetRunnerAttempt creates a new, auditable recovery retry. It never
// mutates the identity or lineage of the source attempt. If the source is
// still live, it is first terminally canceled in the same transaction, under
// its observed revision, before the recovery attempt is inserted.
func (db *DB) RecoverFleetRunnerAttempt(ctx context.Context, previous *model.FleetRunnerAttempt, recovery *model.FleetRunnerAttempt) error {
	if previous == nil || recovery == nil || recovery.PlanID != previous.PlanID || recovery.RetryOf != previous.ID || !recovery.Recovery {
		return fmt.Errorf("source and recovery attempts with an explicit retry lineage are required")
	}
	if recovery.PhaseStartedAt.IsZero() {
		recovery.PhaseStartedAt = recovery.StartedAt
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('norn.fleet-runner:' || $1))`, previous.PlanID); err != nil {
		return err
	}
	// Bind (fence lock-order position 4) must be locked and written before
	// any fleet_runner_attempts row (position 6) is locked below, so this
	// path's order can never invert against one that locks fences first.
	if err := bindFleetTargetFenceTx(ctx, tx, previous.PlanID); err != nil {
		return err
	}
	var status model.FleetRunnerAttemptStatus
	var revision int64
	var sourceRunID int64
	var pilotRunID, commitSHA, planSHA, rootAttemptID, currentPhase string
	if err := tx.QueryRow(ctx, `SELECT status, revision, source_dispatch_run_id, pilot_run_id, commit_sha, plan_sha256, root_attempt_id, current_phase FROM fleet_runner_attempts WHERE id=$1 FOR UPDATE`, previous.ID).Scan(&status, &revision, &sourceRunID, &pilotRunID, &commitSHA, &planSHA, &rootAttemptID, &currentPhase); err != nil {
		return err
	}
	if revision != previous.Revision || sourceRunID != previous.SourceDispatchRunID || sourceRunID != recovery.SourceDispatchRunID || pilotRunID != previous.PilotRunID || pilotRunID != recovery.PilotRunID || commitSHA != recovery.CommitSHA || planSHA != recovery.PlanSHA256 || currentPhase != recovery.CurrentPhase {
		return ErrFleetRunnerAttemptConflict
	}
	switch status {
	case model.FleetRunnerAttemptQueued, model.FleetRunnerAttemptRunning:
		result, updateErr := tx.Exec(ctx, `UPDATE fleet_runner_attempts
			SET status='canceled', last_error='superseded by verified recovery workflow', finished_at=now(), updated_at=now(), revision=revision+1
			WHERE id=$1 AND revision=$2 AND status IN ('queued','running')`, previous.ID, previous.Revision)
		if updateErr != nil {
			return updateErr
		}
		if result.RowsAffected() != 1 {
			return ErrFleetRunnerAttemptConflict
		}
	case model.FleetRunnerAttemptFailed, model.FleetRunnerAttemptAbandoned, model.FleetRunnerAttemptCanceled:
		// A terminal source preserves its immutable final state and becomes the
		// explicit parent of the recovery attempt.
	default:
		return ErrFleetRunnerAttemptConflict
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt), 0) + 1 FROM fleet_runner_attempts WHERE plan_id=$1`, previous.PlanID).Scan(&recovery.Attempt); err != nil {
		return err
	}
	recovery.RootAttemptID = rootAttemptID
	metadata, _ := json.Marshal(recovery.Metadata)
	_, err = tx.Exec(ctx, `
		INSERT INTO fleet_runner_attempts (
			id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
			commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
			heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
			heartbeat_at, updated_at, finished_at, last_error, metadata, heartbeat_expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$21::timestamptz + ($17::int * interval '1 second'))
	`, recovery.ID, recovery.PlanID, recovery.Attempt, recovery.RootAttemptID, recovery.SourceDispatchRunID, recovery.PilotRunID, recovery.Recovery, recovery.RunnerAttemptID,
		recovery.Status, recovery.CurrentPhase, recovery.CommitSHA, recovery.PlanSHA256,
		recovery.WorkflowURL, recovery.PrincipalSubject, recovery.RetryOf,
		recovery.HeartbeatSequence, recovery.HeartbeatTimeoutSeconds, recovery.Revision,
		recovery.StartedAt, recovery.PhaseStartedAt, recovery.HeartbeatAt, recovery.UpdatedAt, recovery.FinishedAt,
		recovery.LastError, metadata)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	recovery.SetDerivedFields()
	return nil
}

func (db *DB) AbandonStaleFleetRunnerAttempts(ctx context.Context, planID string, now time.Time) error {
	query := `
		UPDATE fleet_runner_attempts
		SET status='abandoned', finished_at=$1, updated_at=$1, revision=revision+1,
		    last_error=CASE WHEN last_error='' THEN 'runner heartbeat expired' ELSE last_error END
		WHERE status IN ('queued','running')
		  AND heartbeat_at + (heartbeat_timeout_seconds * interval '1 second') < $1`
	args := []interface{}{now}
	if planID != "" {
		query += " AND plan_id=$2"
		args = append(args, planID)
	}
	_, err := db.Pool.Exec(ctx, query, args...)
	return err
}

type fleetRunnerScanner interface {
	Scan(...interface{}) error
}

func scanFleetRunnerAttempt(row fleetRunnerScanner) (*model.FleetRunnerAttempt, error) {
	var attempt model.FleetRunnerAttempt
	var metadata []byte
	err := row.Scan(&attempt.ID, &attempt.PlanID, &attempt.Attempt, &attempt.RootAttemptID, &attempt.SourceDispatchRunID, &attempt.PilotRunID, &attempt.Recovery, &attempt.RunnerAttemptID,
		&attempt.Status, &attempt.CurrentPhase, &attempt.CommitSHA, &attempt.PlanSHA256,
		&attempt.WorkflowURL, &attempt.PrincipalSubject, &attempt.RetryOf,
		&attempt.HeartbeatSequence, &attempt.HeartbeatTimeoutSeconds, &attempt.Revision,
		&attempt.StartedAt, &attempt.PhaseStartedAt, &attempt.HeartbeatAt, &attempt.UpdatedAt, &attempt.FinishedAt,
		&attempt.LastError, &metadata)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(metadata, &attempt.Metadata)
	attempt.SetDerivedFields()
	return &attempt, nil
}
