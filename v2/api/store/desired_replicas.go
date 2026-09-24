package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// DesiredReplicaCounts returns explicit operator scale intent. Absence is
// intentional: callers must fall back to the InfraSpec declared minimum so
// databases created before this feature retain their historic behavior.
func (db *DB) DesiredReplicaCounts(ctx context.Context, app string) (map[string]int, error) {
	rows, err := db.Pool.Query(ctx, `SELECT process, desired_count FROM app_desired_replicas WHERE app=$1`, app)
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
func (db *DB) FinishScaleClaimedOperation(ctx context.Context, claim OperationClaim, app, process string, count int, message string, metadata map[string]interface{}) error {
	if err := validateOperationClaim(claim); err != nil {
		return err
	}
	if app == "" || process == "" || count < 0 {
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
	var updated int
	err = tx.QueryRow(ctx, `
		WITH owned AS (
			SELECT id FROM operations WHERE id=$1 AND status='running' AND locked_by=$2 AND lock_generation=$3 AND locked_until > now() FOR UPDATE
		), intent AS (
			INSERT INTO app_desired_replicas (app, process, desired_count, revision, operation_id)
			SELECT $4,$5,$6,1,$1 FROM owned
			ON CONFLICT (app, process) DO UPDATE SET desired_count=EXCLUDED.desired_count, revision=app_desired_replicas.revision+1, operation_id=EXCLUDED.operation_id, updated_at=now()
			RETURNING app
		), finished AS (
			UPDATE operations SET status='succeeded', message=$7, metadata=metadata || $8::jsonb, locked_by='', locked_until=NULL, updated_at=now(), finished_at=now()
			WHERE id=$1 AND EXISTS (SELECT 1 FROM intent) RETURNING id, saga_id, app
		), outbox AS (
			INSERT INTO evidence_archive_intents (id, subject_kind, subject_id, app, operation_id, sequence, state)
			SELECT 'ei-' || gen_random_uuid()::text, 'saga', saga_id, app, id, 1, 'pending' FROM finished WHERE saga_id <> ''
			ON CONFLICT (subject_kind, subject_id, sequence) DO NOTHING
		) SELECT count(*) FROM finished`, claim.OperationID(), claim.OwnerID(), claim.Generation(), app, process, count, message, data).Scan(&updated)
	if err != nil {
		return err
	}
	if updated != 1 {
		return ownershipLost(claim)
	}
	return tx.Commit(ctx)
}
