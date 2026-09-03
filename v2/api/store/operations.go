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

// AcquireAppOperationLock serializes mutable app operations across API
// replicas. PostgreSQL advisory locks are session-scoped, so the returned
// release function must always be called to return the pinned connection.
func (db *DB) AcquireAppOperationLock(ctx context.Context, app string) (func(), bool, error) {
	if db == nil || db.Pool == nil || strings.TrimSpace(app) == "" {
		return func() {}, false, fmt.Errorf("app operation lock is unavailable")
	}
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return func() {}, false, err
	}
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, "norn:app:"+app).Scan(&locked); err != nil {
		conn.Release()
		return func() {}, false, err
	}
	if !locked {
		conn.Release()
		return func() {}, false, nil
	}
	return func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, "norn:app:"+app)
		conn.Release()
	}, true, nil
}

type OperationFilter struct {
	App                    string
	Kind                   string
	Ref                    string
	Status                 string
	ExcludeID              string
	Active                 bool
	UnexpiredQualification bool
	Limit                  int
}

type OperationMetric struct {
	Kind            string
	Status          model.OperationStatus
	Count           int64
	DurationSeconds float64
	LastStartedUnix float64
}

func (db *DB) InsertOperation(ctx context.Context, op *model.Operation) error {
	if db == nil || db.Pool == nil {
		return fmt.Errorf("operation store is unavailable")
	}
	payload, metadata, err := prepareOperation(op)
	if err != nil {
		return err
	}
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO operations (id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now())
	`, op.ID, op.Kind, op.App, op.SagaID, op.Ref, op.Status, op.Risk, op.Source, op.Message, payload, metadata, op.Attempts, op.MaxAttempts, op.NextAttemptAt, op.StartedAt)
	return err
}

func prepareOperation(op *model.Operation) ([]byte, []byte, error) {
	if op == nil {
		return nil, nil, fmt.Errorf("operation is required")
	}
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
	payload, err := json.Marshal(op.Payload)
	if err != nil {
		return nil, nil, fmt.Errorf("encode operation payload: %w", err)
	}
	metadata, err := json.Marshal(op.Metadata)
	if err != nil {
		return nil, nil, fmt.Errorf("encode operation metadata: %w", err)
	}
	return payload, metadata, nil
}

// InsertRollbackOperation commits the rollback deployment, its regional
// checkpoints, and the durable operation receipt as one unit. This prevents an
// API interruption or idempotency race from leaving an orphaned deployment.
func (db *DB) InsertRollbackOperation(ctx context.Context, deployment *model.Deployment, regions []model.ResolvedRegion, op *model.Operation) error {
	if db == nil || db.Pool == nil || deployment == nil || op == nil {
		return fmt.Errorf("rollback operation store is unavailable")
	}
	payload, metadata, err := prepareOperation(op)
	if err != nil {
		return err
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	changes, err := json.Marshal(deployment.SourceChanges)
	if err != nil {
		return fmt.Errorf("encode rollback source changes: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deployments
		(id, app, commit_sha, image_tag, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		deployment.ID, deployment.App, deployment.CommitSHA, deployment.ImageTag, deployment.Environment, deployment.SagaID, deployment.Status,
		deployment.SourceKind, deployment.SourceRef, deployment.SourceDirty, changes, deployment.StartedAt); err != nil {
		return err
	}
	for _, region := range regions {
		if _, err = tx.Exec(ctx, `INSERT INTO deployment_regions
			(deployment_id, region, nomad_region, status, desired_weight, active_weight)
			VALUES ($1, $2, $3, $4, $5, 0)`, deployment.ID, region.Name, region.NomadRegion, model.StatusQueued, region.TrafficWeight); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operations
		(id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now())`,
		op.ID, op.Kind, op.App, op.SagaID, op.Ref, op.Status, op.Risk, op.Source, op.Message, payload, metadata,
		op.Attempts, op.MaxAttempts, op.NextAttemptAt, op.StartedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InsertCompletedOperation persists a terminal, planning-only operation in a
// single statement. Callers must not use InsertOperation followed by
// FinishOperation for receipts that are already complete: a failure between
// those statements leaves an ambiguous terminal record without finished_at.
func (db *DB) InsertCompletedOperation(ctx context.Context, op *model.Operation) error {
	if db == nil || db.Pool == nil {
		return fmt.Errorf("operation store is unavailable")
	}
	if op == nil {
		return fmt.Errorf("operation is required")
	}
	if !op.Status.Terminal() {
		return fmt.Errorf("completed operation must have a terminal status")
	}
	if op.StartedAt.IsZero() {
		op.StartedAt = time.Now().UTC()
	}
	if op.FinishedAt == nil {
		finished := op.StartedAt
		op.FinishedAt = &finished
	}
	payload, metadata, err := prepareOperation(op)
	if err != nil {
		return err
	}
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO operations (id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now(), $16)
	`, op.ID, op.Kind, op.App, op.SagaID, op.Ref, op.Status, op.Risk, op.Source, op.Message, payload, metadata, op.Attempts, op.MaxAttempts, op.NextAttemptAt, op.StartedAt, op.FinishedAt)
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

// DeferClaimedOperation returns an operation to the queue without consuming an
// execution attempt. It is used when another replica holds the per-app lock;
// no application work has started in that case.
func (db *DB) DeferClaimedOperation(ctx context.Context, id, message string, nextAttemptAt time.Time, metadata map[string]interface{}) error {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	_, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = 'queued', message = $1, next_attempt_at = $2,
		    metadata = metadata || $3::jsonb, attempts = GREATEST(attempts - 1, 0),
		    locked_by = '', locked_until = NULL, updated_at = now()
		WHERE id = $4 AND status = 'running'
	`, message, nextAttemptAt, data, id)
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
	if err := decodeOperationFields(&op, payload, metadata); err != nil {
		return nil, err
	}
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
	if filter.Ref != "" {
		add("ref = $%d", filter.Ref)
	}
	if filter.Status != "" {
		add("status = $%d", filter.Status)
	}
	if filter.ExcludeID != "" {
		add("id != $%d", filter.ExcludeID)
	}
	if filter.Active {
		clauses = append(clauses, "status IN ('queued', 'running')")
	}
	if filter.UnexpiredQualification {
		clauses = append(clauses, "kind = 'release.qualification' AND payload ? 'expiresAt' AND (payload->>'expiresAt')::timestamptz > now()")
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
		if err := decodeOperationFields(&op, payload, metadata); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// ListOperationSummaries deliberately avoids selecting or decoding payload and
// metadata. Release operations can embed multi-megabyte signed evidence, while
// the overview and Fleet UIs poll these lists frequently. Only the two small,
// non-secret Fleet GitHub fields required to reconnect a plan to its review URL
// are projected from JSON.
func (db *DB) ListOperationSummaries(ctx context.Context, filter OperationFilter) ([]model.Operation, error) {
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
	if filter.Ref != "" {
		add("ref = $%d", filter.Ref)
	}
	if filter.Status != "" {
		add("status = $%d", filter.Status)
	}
	if filter.ExcludeID != "" {
		add("id != $%d", filter.ExcludeID)
	}
	if filter.Active {
		clauses = append(clauses, "status IN ('queued', 'running')")
	}
	if filter.UnexpiredQualification {
		clauses = append(clauses, "kind = 'release.qualification' AND payload ? 'expiresAt' AND (payload->>'expiresAt')::timestamptz > now()")
	}

	query := `SELECT id, kind, app, saga_id, ref, status, risk, source, message,
		attempts, max_attempts, locked_by, locked_until, next_attempt_at, last_error,
		started_at, updated_at, finished_at,
		CASE WHEN kind LIKE 'fleet.github.%' THEN COALESCE(payload->>'planId', '') ELSE '' END,
		CASE WHEN kind LIKE 'fleet.github.%' THEN COALESCE(payload->>'url', '') ELSE '' END
		FROM operations`
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
		var planID, workflowURL string
		if err := rows.Scan(&op.ID, &op.Kind, &op.App, &op.SagaID, &op.Ref, &op.Status, &op.Risk, &op.Source, &op.Message, &op.Attempts, &op.MaxAttempts, &op.LockedBy, &op.LockedUntil, &op.NextAttemptAt, &op.LastError, &op.StartedAt, &op.UpdatedAt, &op.FinishedAt, &planID, &workflowURL); err != nil {
			return nil, err
		}
		if strings.HasPrefix(op.Kind, "fleet.github.") {
			op.Payload = map[string]interface{}{"planId": planID, "url": workflowURL}
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// ListPromotionQualifications projects only the signed staging receipt from
// successful production promotion operations. Staging and production use
// independent databases, so production cannot list staging's
// release.qualification rows directly.
func (db *DB) ListPromotionQualifications(ctx context.Context, app string, limit int) ([]model.ReleaseQualification, error) {
	if db == nil || db.Pool == nil || strings.TrimSpace(app) == "" {
		return nil, fmt.Errorf("operation store is unavailable")
	}
	if limit <= 0 {
		limit = 3
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT metadata->'promotionQualification'
		FROM operations
		WHERE app=$1 AND kind='app.deploy' AND status='succeeded'
		  AND metadata->>'environment'='production'
		  AND metadata ? 'promotionQualification'
		  AND metadata->'promotionQualification' ? 'expiresAt'
		  AND (metadata->'promotionQualification'->>'expiresAt')::timestamptz > now()
		ORDER BY started_at DESC LIMIT $2`, app, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var receipts []model.ReleaseQualification
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var receipt model.ReleaseQualification
		if json.Unmarshal(raw, &receipt) != nil {
			continue
		}
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}

func (db *DB) GetOperation(ctx context.Context, id string) (*model.Operation, error) {
	var op model.Operation
	var payload, metadata []byte
	err := db.Pool.QueryRow(ctx, `
		SELECT id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata,
		       attempts, max_attempts, locked_by, locked_until, next_attempt_at, last_error,
		       started_at, updated_at, finished_at
		FROM operations WHERE id = $1
	`, id).Scan(
		&op.ID, &op.Kind, &op.App, &op.SagaID, &op.Ref, &op.Status, &op.Risk, &op.Source, &op.Message,
		&payload, &metadata, &op.Attempts, &op.MaxAttempts, &op.LockedBy, &op.LockedUntil,
		&op.NextAttemptAt, &op.LastError, &op.StartedAt, &op.UpdatedAt, &op.FinishedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := decodeOperationFields(&op, payload, metadata); err != nil {
		return nil, err
	}
	return &op, nil
}

func decodeOperationFields(op *model.Operation, payload, metadata []byte) error {
	if err := json.Unmarshal(payload, &op.Payload); err != nil {
		return fmt.Errorf("decode operation %s payload: %w", op.ID, err)
	}
	if err := json.Unmarshal(metadata, &op.Metadata); err != nil {
		return fmt.Errorf("decode operation %s metadata: %w", op.ID, err)
	}
	return nil
}

func (db *DB) GetOperationByIdempotencyKey(ctx context.Context, key string) (*model.Operation, error) {
	var id string
	err := db.Pool.QueryRow(ctx, `SELECT id FROM operations WHERE metadata->>'idempotencyKey' = $1`, key).Scan(&id)
	if err != nil {
		return nil, err
	}
	return db.GetOperation(ctx, id)
}

func (db *DB) CancelQueuedOperation(ctx context.Context, id, requestedBy string) (*model.Operation, bool, error) {
	result, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = 'canceled', message = 'operation canceled before execution',
		    metadata = metadata || jsonb_build_object('canceledBy', $1),
		    locked_by = '', locked_until = NULL, updated_at = now(), finished_at = now()
		WHERE id = $2 AND status = 'queued'
	`, requestedBy, id)
	if err != nil {
		return nil, false, err
	}
	op, getErr := db.GetOperation(ctx, id)
	return op, result.RowsAffected() == 1, getErr
}

func (db *DB) RenewOperationLease(ctx context.Context, id, workerID string, until time.Time) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE operations SET locked_until = $1, updated_at = now()
		WHERE id = $2 AND locked_by = $3 AND status = 'running'
	`, until, id, workerID)
	return err
}

func (db *DB) RecoverMaintenanceOperations(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = 'failed',
		    message = 'maintenance executor stopped before recording completion; inspect host state before retrying',
		    last_error = 'maintenance executor lease expired',
		    locked_by = '',
		    locked_until = NULL,
		    updated_at = now(),
		    finished_at = now()
		WHERE status = 'running'
		  AND (kind LIKE 'platform.%' OR kind LIKE 'host.%')
		  AND (locked_until IS NULL OR locked_until < now())
	`)
	return err
}

func (db *DB) RecoverInFlightOperations(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE operations
		SET status = 'queued',
		    message = 'operation recovered after API restart and queued for a safe retry',
		    metadata = metadata || '{"recoveredAfterRestart":true}'::jsonb,
		    locked_by = '',
		    locked_until = NULL,
		    next_attempt_at = now(),
		    updated_at = now()
		WHERE status = 'running'
		  AND kind LIKE 'app.%'
		  AND (locked_until IS NULL OR locked_until < now())
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
		      WHEN kind = 'app.snapshot-prune' THEN 'snapshot pruning was interrupted; inspect retained files before retrying'
		      WHEN kind = 'app.snapshot-restore' THEN 'snapshot restore was interrupted; verify database integrity before retrying'
		      WHEN kind = 'app.migrate' THEN 'schema migration was interrupted; inspect migration and database state before retrying'
		      WHEN kind = 'app.rollback' THEN 'rollback was interrupted; inspect regional deployment state before retrying'
		      ELSE 'operation interrupted after a non-retryable stage; manual review required'
		    END,
		    last_error = 'operation executor lease expired',
		    metadata = metadata || '{"manualRecoveryRequired":true}'::jsonb,
		    locked_by = '',
		    locked_until = NULL,
		    updated_at = now(),
		    finished_at = now()
		WHERE status = 'running'
		  AND kind LIKE 'app.%'
		  AND (locked_until IS NULL OR locked_until < now())
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
