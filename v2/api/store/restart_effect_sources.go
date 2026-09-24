package store

import (
	"context"
	"fmt"

	"norn/v2/api/nomad"
)

type RestartEffectSource struct {
	Allocation   nomad.RestartAllocation
	Attempted    bool
	Acknowledged bool
}

// EnsureRestartEffectSources writes the immutable restart source set while the
// operation claim is live. Each later stop transition is independently fenced.
func (db *DB) EnsureRestartEffectSources(ctx context.Context, claim OperationClaim, sources []nomad.RestartAllocation) error {
	if err := db.CheckOperationClaim(ctx, claim); err != nil {
		return err
	}
	for _, source := range sources {
		result, err := db.Pool.Exec(ctx, `INSERT INTO restart_effect_sources(operation_id,allocation_id,job_id,namespace,task_group,create_index) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT (operation_id,allocation_id) DO NOTHING`, claim.OperationID(), source.ID, source.JobID, source.Namespace, source.TaskGroup, source.CreateIndex)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			var createIndex uint64
			if err := db.Pool.QueryRow(ctx, `SELECT create_index FROM restart_effect_sources WHERE operation_id=$1 AND allocation_id=$2`, claim.OperationID(), source.ID).Scan(&createIndex); err != nil || createIndex != source.CreateIndex {
				return fmt.Errorf("restart source identity conflicts with durable record")
			}
		}
	}
	return nil
}

func (db *DB) RestartEffectSources(ctx context.Context, operationID string) ([]RestartEffectSource, error) {
	rows, err := db.Pool.Query(ctx, `SELECT allocation_id,job_id,namespace,task_group,create_index,attempted_at IS NOT NULL,acknowledged_at IS NOT NULL FROM restart_effect_sources WHERE operation_id=$1 ORDER BY allocation_id`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RestartEffectSource
	for rows.Next() {
		var v RestartEffectSource
		if err := rows.Scan(&v.Allocation.ID, &v.Allocation.JobID, &v.Allocation.Namespace, &v.Allocation.TaskGroup, &v.Allocation.CreateIndex, &v.Attempted, &v.Acknowledged); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (db *DB) MarkRestartSourceAttempted(ctx context.Context, claim OperationClaim, source nomad.RestartAllocation) error {
	if err := db.CheckOperationClaim(ctx, claim); err != nil {
		return err
	}
	result, err := db.Pool.Exec(ctx, `UPDATE restart_effect_sources SET attempted_at=COALESCE(attempted_at,now()), updated_at=now() WHERE operation_id=$1 AND allocation_id=$2 AND create_index=$3`, claim.OperationID(), source.ID, source.CreateIndex)
	if err != nil || result.RowsAffected() != 1 {
		if err != nil {
			return err
		}
		return fmt.Errorf("restart source attempt was not recorded")
	}
	return nil
}
func (db *DB) MarkRestartSourceAcknowledged(ctx context.Context, claim OperationClaim, source nomad.RestartAllocation) error {
	if err := db.CheckOperationClaim(ctx, claim); err != nil {
		return err
	}
	result, err := db.Pool.Exec(ctx, `UPDATE restart_effect_sources SET acknowledged_at=now(),updated_at=now() WHERE operation_id=$1 AND allocation_id=$2 AND create_index=$3 AND attempted_at IS NOT NULL`, claim.OperationID(), source.ID, source.CreateIndex)
	if err != nil || result.RowsAffected() != 1 {
		if err != nil {
			return err
		}
		return fmt.Errorf("restart source acknowledgement was not recorded")
	}
	return nil
}
