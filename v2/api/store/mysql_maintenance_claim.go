package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

var ErrMySQLMaintenanceClaimUnavailable = errors.New("signed MySQL maintenance operation is not claimable")

// ClaimPrivateMySQLOperation claims only the named, accepted, one-attempt
// maintenance operation. The private runner still verifies its signature and
// exact payload before any external effect. This method never selects another
// queued operation if the requested one is absent, busy, or already consumed.
func (db *DB) ClaimPrivateMySQLOperation(ctx context.Context, operationID, workerID, kind string, lease time.Duration) (*model.Operation, OperationClaim, error) {
	if db == nil || db.Pool == nil || strings.TrimSpace(workerID) == "" || lease <= 0 {
		return nil, OperationClaim{}, ErrMySQLMaintenanceClaimUnavailable
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return nil, OperationClaim{}, ErrMySQLMaintenanceClaimUnavailable
	}
	switch kind {
	case MySQLSourceSnapshotOperationKind, MySQLRestoreOperationKind, MySQLRestoreRecoveryOperationKind:
	default:
		return nil, OperationClaim{}, ErrMySQLMaintenanceClaimUnavailable
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, OperationClaim{}, err
	}
	defer tx.Rollback(context.Background())
	// Serialize with runtime-fence acquisition just like ClaimNextOperation.
	var singleton bool
	if err := tx.QueryRow(ctx, `SELECT singleton FROM runtime_mutation_fence WHERE singleton=true FOR SHARE`).Scan(&singleton); err != nil {
		return nil, OperationClaim{}, err
	}
	if !singleton {
		return nil, OperationClaim{}, ErrRuntimeMutationFenceHeld
	}
	var op model.Operation
	var payload, metadata []byte
	err = tx.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM operations
			WHERE id=$3 AND kind=$4 AND status='queued'
			  AND next_attempt_at <= now() AND attempts=0 AND max_attempts=1
			  AND (locked_until IS NULL OR locked_until < now())
			  AND acceptance_required
			  AND EXISTS (SELECT 1 FROM operation_acceptance_intents ai WHERE ai.operation_id=operations.id)
			FOR UPDATE SKIP LOCKED
		)
		UPDATE operations o
		SET status='running',attempts=1,lock_generation=lock_generation+1,
		    locked_by=$1,locked_until=now()+($2::bigint * interval '1 microsecond'),updated_at=now()
		FROM candidate WHERE o.id=candidate.id
		RETURNING o.id,o.kind,o.app,o.saga_id,o.ref,o.status,o.risk,o.source,o.message,o.payload,o.metadata,
		          o.attempts,o.max_attempts,o.locked_by,o.lock_generation,o.locked_until,o.next_attempt_at,o.last_error,
		          o.started_at,o.updated_at,o.finished_at`, workerID, lease.Microseconds(), operationID, kind).Scan(
		&op.ID, &op.Kind, &op.App, &op.SagaID, &op.Ref, &op.Status, &op.Risk, &op.Source, &op.Message, &payload, &metadata,
		&op.Attempts, &op.MaxAttempts, &op.LockedBy, &op.LockGeneration, &op.LockedUntil, &op.NextAttemptAt, &op.LastError,
		&op.StartedAt, &op.UpdatedAt, &op.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, OperationClaim{}, ErrMySQLMaintenanceClaimUnavailable
	}
	if err != nil {
		return nil, OperationClaim{}, fmt.Errorf("claim signed MySQL maintenance operation: %w", err)
	}
	if err := decodeOperationFields(&op, payload, metadata); err != nil {
		return nil, OperationClaim{}, err
	}
	claim, err := NewOperationClaim(op.ID, op.LockedBy, op.LockGeneration)
	if err != nil {
		return nil, OperationClaim{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, OperationClaim{}, err
	}
	return &op, claim, nil
}
