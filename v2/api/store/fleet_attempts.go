package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet"
)

func scanFleetRunnerAttempt(row pgx.Row) (*fleet.RunnerAttempt, error) {
	var item fleet.RunnerAttempt
	var retryOf string
	err := row.Scan(&item.ID, &item.PlanID, &item.Attempt, &item.RunnerAttemptID, &item.CommitSHA, &item.PlanSHA256, &item.WorkflowURL, &item.Status, &item.CurrentPhase, &item.RootAttemptID, &retryOf, &item.HeartbeatSequence, &item.HeartbeatTimeoutSeconds, &item.Revision, &item.HeartbeatAt, &item.HeartbeatExpiresAt, &item.LastError, &item.StartedAt, &item.UpdatedAt, &item.FinishedAt)
	if err != nil {
		return nil, err
	}
	item.SchemaVersion = fleet.RunnerAttemptSchemaVersion
	item.RetryOf = retryOf
	return &item, nil
}

const fleetRunnerAttemptColumns = `id, plan_id, attempt, runner_attempt_id, commit_sha, plan_sha256, workflow_url, status, current_phase, root_attempt_id, retry_of, heartbeat_sequence, heartbeat_timeout_seconds, revision, heartbeat_at, heartbeat_expires_at, message, started_at, updated_at, finished_at`

// Read paths project an expired live lease as abandoned without mutating it.
// Expiry reconciliation belongs to a plan-serialized mutation transaction.
const fleetRunnerAttemptReadColumns = `id, plan_id, attempt, runner_attempt_id, commit_sha, plan_sha256, workflow_url,
	CASE WHEN status IN ('queued','running') AND heartbeat_expires_at < statement_timestamp() THEN 'abandoned' ELSE status END,
	current_phase, root_attempt_id, retry_of, heartbeat_sequence, heartbeat_timeout_seconds, revision, heartbeat_at, heartbeat_expires_at,
	CASE WHEN status IN ('queued','running') AND heartbeat_expires_at < statement_timestamp() THEN 'heartbeat lease expired; external execution termination unproven' ELSE message END,
	started_at, updated_at,
	CASE WHEN status IN ('queued','running') AND heartbeat_expires_at < statement_timestamp() THEN COALESCE(finished_at,heartbeat_expires_at) ELSE finished_at END`

func (db *DB) ListFleetRunnerAttempts(ctx context.Context, planID string) ([]fleet.RunnerAttempt, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("fleet runner attempt store is unavailable")
	}
	rows, err := db.Pool.Query(ctx, `SELECT `+fleetRunnerAttemptReadColumns+` FROM fleet_runner_attempts WHERE plan_id=$1 ORDER BY attempt DESC`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []fleet.RunnerAttempt{}
	for rows.Next() {
		item, err := scanFleetRunnerAttempt(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (db *DB) GetFleetRunnerAttempt(ctx context.Context, planID, attemptID string) (*fleet.RunnerAttempt, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("fleet runner attempt store is unavailable")
	}
	return scanFleetRunnerAttempt(db.Pool.QueryRow(ctx, `SELECT `+fleetRunnerAttemptReadColumns+` FROM fleet_runner_attempts WHERE id=$1 AND plan_id=$2`, attemptID, planID))
}

func serverOwnedRootAttemptID(attempt int, id, persistedRoot string) (string, error) {
	if attempt == 1 && id != "" {
		return id, nil
	}
	if attempt > 1 && persistedRoot != "" {
		return persistedRoot, nil
	}
	return "", fmt.Errorf("fleet runner attempt root is missing")
}

func (db *DB) UpdateFleetRunnerAttempt(ctx context.Context, planID, id string, revision int64, action string, values ...interface{}) (*fleet.RunnerAttempt, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("fleet runner attempt store is unavailable")
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `/* fleet-runner-attempt-update-lock */ SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+planID); err != nil {
		return nil, err
	}
	var locked int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM fleet_runner_attempts WHERE plan_id=$1 AND id=$2 FOR UPDATE`, planID, id).Scan(&locked); err != nil {
		return nil, err
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return nil, err
	}
	var query string
	args := []interface{}{planID, id, revision}
	switch action {
	case "heartbeat":
		if len(values) != 2 {
			return nil, fmt.Errorf("heartbeat requires sequence and message")
		}
		query = `UPDATE fleet_runner_attempts SET status='running', heartbeat_sequence=$4, heartbeat_at=$6, heartbeat_expires_at=$6 + make_interval(secs => heartbeat_timeout_seconds), message=$5, revision=revision+1, updated_at=$6 WHERE plan_id=$1 AND id=$2 AND revision=$3 AND status IN ('queued','running') AND heartbeat_expires_at >= $6 RETURNING ` + fleetRunnerAttemptColumns
		args = append(args, values[0], values[1], databaseNow)
	case "advance":
		if len(values) != 1 {
			return nil, fmt.Errorf("advance requires phase")
		}
		query = `UPDATE fleet_runner_attempts SET current_phase=$4, status=CASE WHEN $4='complete' THEN 'succeeded' ELSE 'running' END, revision=revision+1, updated_at=$5, finished_at=CASE WHEN $4='complete' THEN $5 ELSE NULL END WHERE plan_id=$1 AND id=$2 AND revision=$3 AND status IN ('queued','running') AND heartbeat_expires_at >= $5 RETURNING ` + fleetRunnerAttemptColumns
		args = append(args, values[0], databaseNow)
	case "cancel":
		if len(values) != 1 {
			return nil, fmt.Errorf("cancel requires message")
		}
		query = `UPDATE fleet_runner_attempts SET status='canceled', message=$4, revision=revision+1, updated_at=$5, finished_at=$5 WHERE plan_id=$1 AND id=$2 AND revision=$3 AND status IN ('queued','running') RETURNING ` + fleetRunnerAttemptColumns
		args = append(args, values[0], databaseNow)
	default:
		return nil, fmt.Errorf("unsupported runner attempt update")
	}
	item, err := scanFleetRunnerAttempt(tx.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return item, nil
}
