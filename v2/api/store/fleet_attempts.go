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

func (db *DB) ListFleetRunnerAttempts(ctx context.Context, planID string) ([]fleet.RunnerAttempt, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("fleet runner attempt store is unavailable")
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE fleet_runner_attempts SET status='abandoned', message='heartbeat lease expired', revision=revision+1, updated_at=now(), finished_at=now() WHERE plan_id=$1 AND status IN ('queued','running') AND heartbeat_expires_at < now()`, planID); err != nil {
		return nil, err
	}
	rows, err := db.Pool.Query(ctx, `SELECT `+fleetRunnerAttemptColumns+` FROM fleet_runner_attempts WHERE plan_id=$1 ORDER BY attempt DESC`, planID)
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
	if _, err := db.Pool.Exec(ctx, `UPDATE fleet_runner_attempts SET status='abandoned', message='heartbeat lease expired', revision=revision+1, updated_at=now(), finished_at=now() WHERE id=$1 AND plan_id=$2 AND status IN ('queued','running') AND heartbeat_expires_at < now()`, attemptID, planID); err != nil {
		return nil, err
	}
	return scanFleetRunnerAttempt(db.Pool.QueryRow(ctx, `SELECT `+fleetRunnerAttemptColumns+` FROM fleet_runner_attempts WHERE id=$1 AND plan_id=$2`, attemptID, planID))
}

func (db *DB) CreateFleetRunnerAttempt(ctx context.Context, item fleet.RunnerAttempt) (*fleet.RunnerAttempt, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("fleet runner attempt store is unavailable")
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "norn:fleet-attempt:"+item.PlanID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE fleet_runner_attempts SET status='abandoned', message='heartbeat lease expired', revision=revision+1, updated_at=now(), finished_at=now() WHERE plan_id=$1 AND status IN ('queued','running') AND heartbeat_expires_at < now()`, item.PlanID); err != nil {
		return nil, err
	}
	var attempt int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt), 0)+1 FROM fleet_runner_attempts WHERE plan_id=$1`, item.PlanID).Scan(&attempt); err != nil {
		return nil, err
	}
	item.Attempt = attempt
	if attempt == 1 {
		item.RootAttemptID, err = serverOwnedRootAttemptID(attempt, item.ID, "")
	} else {
		var persistedRoot string
		err = tx.QueryRow(ctx, `SELECT root_attempt_id FROM fleet_runner_attempts WHERE plan_id=$1 ORDER BY attempt ASC LIMIT 1`, item.PlanID).Scan(&persistedRoot)
		if err == nil {
			item.RootAttemptID, err = serverOwnedRootAttemptID(attempt, item.ID, persistedRoot)
		}
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	item.StartedAt, item.UpdatedAt, item.HeartbeatAt = now, now, now
	item.HeartbeatExpiresAt = now.Add(time.Duration(item.HeartbeatTimeoutSeconds) * time.Second)
	item.Status = "queued"
	if item.CurrentPhase == "" {
		item.CurrentPhase = "provider_applying"
	}
	item.Revision = 1
	if _, err := tx.Exec(ctx, `INSERT INTO fleet_runner_attempts (`+fleetRunnerAttemptColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, item.ID, item.PlanID, item.Attempt, item.RunnerAttemptID, item.CommitSHA, item.PlanSHA256, item.WorkflowURL, item.Status, item.CurrentPhase, item.RootAttemptID, item.RetryOf, item.HeartbeatSequence, item.HeartbeatTimeoutSeconds, item.Revision, item.HeartbeatAt, item.HeartbeatExpiresAt, item.LastError, item.StartedAt, item.UpdatedAt, item.FinishedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &item, nil
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
	var query string
	args := []interface{}{planID, id, revision}
	switch action {
	case "heartbeat":
		query = `UPDATE fleet_runner_attempts SET status='running', heartbeat_sequence=$4, heartbeat_at=now(), heartbeat_expires_at=now() + make_interval(secs => heartbeat_timeout_seconds), message=$5, revision=revision+1, updated_at=now() WHERE plan_id=$1 AND id=$2 AND revision=$3 AND status IN ('queued','running') AND heartbeat_expires_at >= now() RETURNING ` + fleetRunnerAttemptColumns
		args = append(args, values...)
	case "advance":
		query = `UPDATE fleet_runner_attempts SET current_phase=$4, status=CASE WHEN $4='complete' THEN 'succeeded' ELSE 'running' END, revision=revision+1, updated_at=now(), finished_at=CASE WHEN $4='complete' THEN now() ELSE NULL END WHERE plan_id=$1 AND id=$2 AND revision=$3 AND status IN ('queued','running') AND heartbeat_expires_at >= now() RETURNING ` + fleetRunnerAttemptColumns
		args = append(args, values...)
	case "cancel":
		query = `UPDATE fleet_runner_attempts SET status='canceled', message=$4, revision=revision+1, updated_at=now(), finished_at=now() WHERE plan_id=$1 AND id=$2 AND revision=$3 AND status IN ('queued','running') RETURNING ` + fleetRunnerAttemptColumns
		args = append(args, values...)
	default:
		return nil, fmt.Errorf("unsupported runner attempt update")
	}
	return scanFleetRunnerAttempt(db.Pool.QueryRow(ctx, query, args...))
}
