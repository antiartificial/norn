package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

type OperationFilter struct {
	App    string
	Kind   string
	Status string
	Active bool
	Limit  int
}

type OperationMetric struct {
	Kind            string
	Status          model.OperationStatus
	Count           int64
	DurationSeconds float64
	LastStartedUnix float64
}

func (db *DB) InsertOperation(ctx context.Context, op *model.Operation) error {
	if op.Metadata == nil {
		op.Metadata = map[string]interface{}{}
	}
	if op.Payload == nil {
		op.Payload = map[string]interface{}{}
	}
	if op.Status == "" {
		op.Status = model.OperationQueued
	}
	if op.StartedAt.IsZero() {
		op.StartedAt = time.Now()
	}
	if op.MaxAttempts <= 0 {
		op.MaxAttempts = 1
	}
	if op.NextAttemptAt.IsZero() {
		op.NextAttemptAt = op.StartedAt
	}
	payload, _ := json.Marshal(op.Payload)
	metadata, _ := json.Marshal(op.Metadata)
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO operations (id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now())
	`, op.ID, op.Kind, op.App, op.SagaID, op.Ref, op.Status, op.Risk, op.Source, op.Message, payload, metadata, op.Attempts, op.MaxAttempts, op.NextAttemptAt, op.StartedAt)
	return err
}

func (db *DB) FinishOperation(ctx context.Context, id string, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	_, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = $1, message = $2, metadata = metadata || $3::jsonb, locked_by = '', locked_until = NULL, updated_at = now(), finished_at = now()
		WHERE id = $4
	`, status, message, data, id)
	return err
}

func (db *DB) FinishOperationBySaga(ctx context.Context, sagaID string, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	_, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = $1, message = $2, metadata = metadata || $3::jsonb, locked_by = '', locked_until = NULL, updated_at = now(), finished_at = now()
		WHERE saga_id = $4 AND status IN ('queued', 'running')
	`, status, message, data, sagaID)
	return err
}

func (db *DB) RetryOperation(ctx context.Context, id, message, lastError string, nextAttemptAt time.Time, metadata map[string]interface{}) error {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	_, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = 'queued',
		    message = $1,
		    last_error = $2,
		    next_attempt_at = $3,
		    metadata = metadata || $4::jsonb,
		    locked_by = '',
		    locked_until = NULL,
		    updated_at = now()
		WHERE id = $5
	`, message, lastError, nextAttemptAt, data, id)
	return err
}

func (db *DB) ClaimNextOperation(ctx context.Context, workerID string, lease time.Duration, kinds []string) (*model.Operation, error) {
	args := []interface{}{workerID, time.Now().Add(lease)}
	kindClause := ""
	if len(kinds) > 0 {
		holders := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			args = append(args, kind)
			holders = append(holders, fmt.Sprintf("$%d", len(args)))
		}
		kindClause = "AND kind IN (" + strings.Join(holders, ", ") + ")"
	}
	query := fmt.Sprintf(`
		WITH candidate AS (
			SELECT id
			FROM operations
			WHERE status = 'queued'
			  AND next_attempt_at <= now()
			  AND attempts < max_attempts
			  AND (locked_until IS NULL OR locked_until < now())
			  %s
			ORDER BY started_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE operations o
		SET status = 'running',
		    attempts = attempts + 1,
		    locked_by = $1,
		    locked_until = $2,
		    updated_at = now()
		FROM candidate
		WHERE o.id = candidate.id
		RETURNING o.id, o.kind, o.app, o.saga_id, o.ref, o.status, o.risk, o.source, o.message, o.payload, o.metadata,
		          o.attempts, o.max_attempts, o.locked_by, o.locked_until, o.next_attempt_at, o.last_error,
		          o.started_at, o.updated_at, o.finished_at
	`, kindClause)

	var op model.Operation
	var payload, metadata []byte
	err := db.Pool.QueryRow(ctx, query, args...).Scan(
		&op.ID, &op.Kind, &op.App, &op.SagaID, &op.Ref, &op.Status, &op.Risk, &op.Source, &op.Message, &payload, &metadata,
		&op.Attempts, &op.MaxAttempts, &op.LockedBy, &op.LockedUntil, &op.NextAttemptAt, &op.LastError,
		&op.StartedAt, &op.UpdatedAt, &op.FinishedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(payload, &op.Payload)
	_ = json.Unmarshal(metadata, &op.Metadata)
	return &op, nil
}

func (db *DB) ListOperations(ctx context.Context, filter OperationFilter) ([]model.Operation, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	clauses := []string{}
	args := []interface{}{}
	add := func(clause string, value interface{}) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if filter.App != "" {
		add("app = $%d", filter.App)
	}
	if filter.Kind != "" {
		add("kind = $%d", filter.Kind)
	}
	if filter.Status != "" {
		add("status = $%d", filter.Status)
	}
	if filter.Active {
		clauses = append(clauses, "status IN ('queued', 'running')")
	}

	query := `SELECT id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, locked_by, locked_until, next_attempt_at, last_error, started_at, updated_at, finished_at FROM operations`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY started_at DESC LIMIT $%d", len(args))

	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Operation
	for rows.Next() {
		var op model.Operation
		var payload, metadata []byte
		if err := rows.Scan(&op.ID, &op.Kind, &op.App, &op.SagaID, &op.Ref, &op.Status, &op.Risk, &op.Source, &op.Message, &payload, &metadata, &op.Attempts, &op.MaxAttempts, &op.LockedBy, &op.LockedUntil, &op.NextAttemptAt, &op.LastError, &op.StartedAt, &op.UpdatedAt, &op.FinishedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(payload, &op.Payload)
		_ = json.Unmarshal(metadata, &op.Metadata)
		out = append(out, op)
	}
	return out, rows.Err()
}

func (db *DB) RecoverInFlightOperations(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = 'queued',
		    message = CASE WHEN message = '' THEN 'operation recovered after API restart' ELSE message END,
		    locked_by = '',
		    locked_until = NULL,
		    next_attempt_at = now(),
		    updated_at = now()
		WHERE status = 'running'
		  AND attempts < max_attempts
		  AND (
		    kind != 'app.deploy'
		    OR NOT EXISTS (
		      SELECT 1
		      FROM deployment_steps ds
		      WHERE ds.deployment_id = operations.payload->>'deploymentId'
		        AND ds.step IN ('snapshot', 'migrate', 'submit', 'healthy', 'forge', 'cleanup')
		        AND ds.status IN ('running', 'complete')
		    )
		  );

		UPDATE operations
		SET status = 'failed',
		    message = CASE
		      WHEN kind = 'app.deploy' THEN 'deploy interrupted after mutable stage; manual review required before retry'
		      WHEN message = '' THEN 'operation interrupted by API restart'
		      ELSE message
		    END,
		    locked_by = '',
		    locked_until = NULL,
		    updated_at = now(),
		    finished_at = now()
		WHERE status = 'running'
	`)
	return err
}

func (db *DB) OperationMetrics(ctx context.Context) ([]OperationMetric, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT
			kind,
			status,
			COUNT(*)::bigint,
			COALESCE(SUM(EXTRACT(EPOCH FROM (finished_at - started_at))) FILTER (WHERE finished_at IS NOT NULL), 0)::float8,
			COALESCE(EXTRACT(EPOCH FROM MAX(started_at)), 0)::float8
		FROM operations
		GROUP BY kind, status
		ORDER BY kind, status
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OperationMetric
	for rows.Next() {
		var metric OperationMetric
		if err := rows.Scan(&metric.Kind, &metric.Status, &metric.Count, &metric.DurationSeconds, &metric.LastStartedUnix); err != nil {
			return nil, err
		}
		out = append(out, metric)
	}
	return out, rows.Err()
}
