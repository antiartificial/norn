package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	if op.Status == "" {
		op.Status = model.OperationRunning
	}
	if op.StartedAt.IsZero() {
		op.StartedAt = time.Now()
	}
	metadata, _ := json.Marshal(op.Metadata)
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO operations (id, kind, app, saga_id, ref, status, risk, source, message, metadata, started_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now())
	`, op.ID, op.Kind, op.App, op.SagaID, op.Ref, op.Status, op.Risk, op.Source, op.Message, metadata, op.StartedAt)
	return err
}

func (db *DB) FinishOperation(ctx context.Context, id string, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	_, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = $1, message = $2, metadata = metadata || $3::jsonb, updated_at = now(), finished_at = now()
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
		SET status = $1, message = $2, metadata = metadata || $3::jsonb, updated_at = now(), finished_at = now()
		WHERE saga_id = $4 AND status IN ('queued', 'running')
	`, status, message, data, sagaID)
	return err
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

	query := `SELECT id, kind, app, saga_id, ref, status, risk, source, message, metadata, started_at, updated_at, finished_at FROM operations`
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
		var metadata []byte
		if err := rows.Scan(&op.ID, &op.Kind, &op.App, &op.SagaID, &op.Ref, &op.Status, &op.Risk, &op.Source, &op.Message, &metadata, &op.StartedAt, &op.UpdatedAt, &op.FinishedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metadata, &op.Metadata)
		out = append(out, op)
	}
	return out, rows.Err()
}

func (db *DB) RecoverInFlightOperations(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = 'failed',
		    message = CASE WHEN message = '' THEN 'operation interrupted by API restart' ELSE message END,
		    updated_at = now(),
		    finished_at = now()
		WHERE status IN ('queued', 'running')
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
