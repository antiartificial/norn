package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// DesiredReplicaCounts returns explicit operator scale intent. Absence is
// intentional: callers must fall back to the InfraSpec declared minimum so
// databases created before this feature retain their historic behavior.
func (db *DB) DesiredReplicaCounts(ctx context.Context, app, region string) (map[string]int, error) {
	rows, err := db.Pool.Query(ctx, `SELECT process, desired_count FROM app_desired_replicas WHERE app=$1 AND region=$2`, app, region)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var process string
		var count int
		if err := rows.Scan(&process, &count); err != nil {
			return nil, err
		}
		counts[process] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

// FinishScaleClaimedOperation atomically persists the acknowledged desired
// count and the operation terminal receipt. A stale claimant cannot leave a
// desired-count override behind after losing its operation lease.
func (db *DB) FinishScaleClaimedOperation(ctx context.Context, claim OperationClaim, app, process, region string, count int, message string, metadata map[string]interface{}) error {
	if err := validateOperationClaim(claim); err != nil {
		return err
	}
	if app == "" || process == "" || region == "" || count < 0 {
		return fmt.Errorf("desired replica intent is invalid")
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status, owner string
	var generation int64
	var lockedUntil *time.Time
	if err = tx.QueryRow(ctx, `SELECT status, locked_by, lock_generation, locked_until FROM operations WHERE id=$1 FOR UPDATE`, claim.OperationID()).Scan(&status, &owner, &generation, &lockedUntil); err != nil {
		return err
	}
	var databaseNow time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return err
	}
	if status != "running" || owner != claim.OwnerID() || generation != claim.Generation() || lockedUntil == nil || !lockedUntil.After(databaseNow) {
		return ownershipLost(claim)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO app_desired_replicas (app, process, region, desired_count, revision, operation_id) VALUES ($1,$2,$3,$4,1,$5) ON CONFLICT (app, process, region) DO UPDATE SET desired_count=EXCLUDED.desired_count, revision=app_desired_replicas.revision+1, operation_id=EXCLUDED.operation_id, updated_at=now()`, app, process, region, count, claim.OperationID()); err != nil {
		return err
	}
	var sagaID, operationApp string
	if err = tx.QueryRow(ctx, `UPDATE operations SET status='succeeded', message=$1, metadata=metadata || $2::jsonb, locked_by='', locked_until=NULL, updated_at=now(), finished_at=now() WHERE id=$3 RETURNING saga_id, app`, message, data, claim.OperationID()).Scan(&sagaID, &operationApp); err != nil {
		return err
	}
	if sagaID != "" {
		if _, err = tx.Exec(ctx, `INSERT INTO evidence_archive_intents (id, subject_kind, subject_id, app, operation_id, sequence, state) VALUES ('ei-' || gen_random_uuid()::text, 'saga', $1, $2, $3, 1, 'pending') ON CONFLICT (subject_kind, subject_id, sequence) DO NOTHING`, sagaID, operationApp, claim.OperationID()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
