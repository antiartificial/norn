package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

var ErrFleetRunnerAttemptConflict = fmt.Errorf("fleet runner attempt changed or is not transitionable")

func (db *DB) CreateFleetRunnerAttempt(ctx context.Context, attempt *model.FleetRunnerAttempt) error {
	if attempt == nil {
		return fmt.Errorf("attempt is required")
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('norn.fleet-runner:' || $1))`, attempt.PlanID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt), 0) + 1 FROM fleet_runner_attempts WHERE plan_id = $1`, attempt.PlanID).Scan(&attempt.Attempt); err != nil {
		return err
	}
	metadata, _ := json.Marshal(attempt.Metadata)
	_, err = tx.Exec(ctx, `
		INSERT INTO fleet_runner_attempts (
			id, plan_id, attempt, runner_attempt_id, status, current_phase,
			commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
			heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at,
			heartbeat_at, updated_at, finished_at, last_error, metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	`, attempt.ID, attempt.PlanID, attempt.Attempt, attempt.RunnerAttemptID, attempt.Status, attempt.CurrentPhase,
		attempt.CommitSHA, attempt.PlanSHA256, attempt.WorkflowURL, attempt.PrincipalSubject, attempt.RetryOf,
		attempt.HeartbeatSequence, attempt.HeartbeatTimeoutSeconds, attempt.Revision, attempt.StartedAt,
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
		SELECT id, plan_id, attempt, runner_attempt_id, status, current_phase,
		       commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
		       heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at,
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
		SELECT id, plan_id, attempt, runner_attempt_id, status, current_phase,
		       commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
		       heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at,
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
		    updated_at = now(), revision = revision + 1,
		    metadata = metadata || jsonb_build_object('heartbeatMessage', $2::text)
		WHERE id = $3 AND current_phase = $4 AND revision = $5
		  AND status IN ('queued', 'running') AND heartbeat_sequence < $1
		RETURNING id, plan_id, attempt, runner_attempt_id, status, current_phase,
		          commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
		          heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at,
		          heartbeat_at, updated_at, finished_at, last_error, metadata
	`, sequence, message, id, phase, expectedRevision)
	attempt, err := scanFleetRunnerAttempt(row)
	if err == pgx.ErrNoRows {
		return nil, ErrFleetRunnerAttemptConflict
	}
	return attempt, err
}

func (db *DB) AdvanceFleetRunnerAttempt(ctx context.Context, id, fromPhase, toPhase string, expectedRevision int64, terminal bool) (*model.FleetRunnerAttempt, error) {
	status := model.FleetRunnerAttemptRunning
	var finished interface{}
	if terminal {
		status = model.FleetRunnerAttemptSucceeded
		finished = time.Now().UTC()
	}
	row := db.Pool.QueryRow(ctx, `
		UPDATE fleet_runner_attempts a
		SET current_phase = $1, status = $2, updated_at = now(), heartbeat_at = now(),
		    revision = revision + 1, finished_at = $3
		WHERE a.id = $4 AND a.current_phase = $5 AND a.revision = $6
		  AND a.status IN ('queued', 'running')
		  AND EXISTS (
		    SELECT 1 FROM operations o
		    WHERE o.kind = 'fleet.reconciliation' AND o.ref = a.plan_id
		      AND o.status = 'succeeded' AND o.payload->>'phase' = $5
		      AND o.payload->>'attemptId' = a.id
		  )
		RETURNING id, plan_id, attempt, runner_attempt_id, status, current_phase,
		          commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
		          heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at,
		          heartbeat_at, updated_at, finished_at, last_error, metadata
	`, toPhase, status, finished, id, fromPhase, expectedRevision)
	attempt, err := scanFleetRunnerAttempt(row)
	if err == pgx.ErrNoRows {
		return nil, ErrFleetRunnerAttemptConflict
	}
	return attempt, err
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
	if err := tx.QueryRow(ctx, `
		SELECT plan_id, current_phase, status, commit_sha, plan_sha256
		FROM fleet_runner_attempts WHERE id=$1 FOR UPDATE
	`, attemptID).Scan(&planID, &phase, &status, &commitSHA, &planSHA); err != nil {
		return err
	}
	payloadPhase, _ := op.Payload["phase"].(string)
	payloadCommit, _ := op.Payload["commitSha"].(string)
	payloadPlan, _ := op.Payload["planSha256"].(string)
	if planID != op.Ref || phase != payloadPhase || (status != "queued" && status != "running") || commitSHA != payloadCommit || planSHA != payloadPlan {
		return ErrFleetRunnerAttemptConflict
	}
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
		RETURNING id, plan_id, attempt, runner_attempt_id, status, current_phase,
		          commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
		          heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at,
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
	if err := tx.QueryRow(ctx, `SELECT status, revision FROM fleet_runner_attempts WHERE id=$1 FOR UPDATE`, previous.ID).Scan(&status, &revision); err != nil {
		return err
	}
	if status != model.FleetRunnerAttemptFailed && status != model.FleetRunnerAttemptAbandoned && status != model.FleetRunnerAttemptCanceled {
		return ErrFleetRunnerAttemptConflict
	}
	if revision != previous.Revision {
		return ErrFleetRunnerAttemptConflict
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt), 0) + 1 FROM fleet_runner_attempts WHERE plan_id=$1`, previous.PlanID).Scan(&replacement.Attempt); err != nil {
		return err
	}
	metadata, _ := json.Marshal(replacement.Metadata)
	_, err = tx.Exec(ctx, `
		INSERT INTO fleet_runner_attempts (
			id, plan_id, attempt, runner_attempt_id, status, current_phase,
			commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
			heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at,
			heartbeat_at, updated_at, finished_at, last_error, metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	`, replacement.ID, replacement.PlanID, replacement.Attempt, replacement.RunnerAttemptID,
		replacement.Status, replacement.CurrentPhase, replacement.CommitSHA, replacement.PlanSHA256,
		replacement.WorkflowURL, replacement.PrincipalSubject, replacement.RetryOf,
		replacement.HeartbeatSequence, replacement.HeartbeatTimeoutSeconds, replacement.Revision,
		replacement.StartedAt, replacement.HeartbeatAt, replacement.UpdatedAt, replacement.FinishedAt,
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
	err := row.Scan(&attempt.ID, &attempt.PlanID, &attempt.Attempt, &attempt.RunnerAttemptID,
		&attempt.Status, &attempt.CurrentPhase, &attempt.CommitSHA, &attempt.PlanSHA256,
		&attempt.WorkflowURL, &attempt.PrincipalSubject, &attempt.RetryOf,
		&attempt.HeartbeatSequence, &attempt.HeartbeatTimeoutSeconds, &attempt.Revision,
		&attempt.StartedAt, &attempt.HeartbeatAt, &attempt.UpdatedAt, &attempt.FinishedAt,
		&attempt.LastError, &metadata)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(metadata, &attempt.Metadata)
	attempt.SetDerivedFields()
	return &attempt, nil
}
