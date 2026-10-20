package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
)

// ValidateFleetRunnerFenceBinding checks the exact attempt and current fence
// under one PG snapshot. It is a narrow runner read; callers must separately
// authenticate the attempt's workload identity before invoking it.
func (db *DB) ValidateFleetRunnerFenceBinding(ctx context.Context, planID, attemptID string) (*fleet.RunnerFenceBinding, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	targetID, fence, epoch, nonce, err := lockFleetTargetFenceForPlanID(ctx, tx, planID, false, true)
	if err != nil {
		return nil, err
	}
	if targetID == "" {
		return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetUnregistered, Reason: "attempt has no registered target"}
	}
	var raw []byte
	var status string
	var expires time.Time
	if err := tx.QueryRow(ctx, `SELECT COALESCE(fence_binding,metadata->'fenceBinding'),status,heartbeat_expires_at FROM fleet_runner_attempts WHERE plan_id=$1 AND id=$2 FOR SHARE`, planID, attemptID).Scan(&raw, &status, &expires); err != nil {
		return nil, err
	}
	if len(raw) == 0 || status != "queued" && status != "running" || !expires.After(time.Now().UTC()) {
		return nil, &lifecycle.FenceError{Code: "fleet_runner_attempt_stale", Reason: "runner attempt has no live fence binding"}
	}
	var binding fleet.RunnerFenceBinding
	if err := json.Unmarshal(raw, &binding); err != nil || binding.TargetID == "" || binding.Generation <= 0 || binding.AuthorityEpoch <= 0 {
		return nil, &lifecycle.FenceError{Code: "fleet_runner_attempt_stale", Reason: "runner attempt fence binding is invalid"}
	}
	if binding.TargetID != targetID || !fence.Held || fence.HolderPlanID != planID || fence.HolderNonceSHA256 != nonce || fence.Generation != binding.Generation {
		return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetExecutionOccupied, Reason: "runner attempt no longer holds its exact target fence"}
	}
	if epoch != binding.AuthorityEpoch || fence.AuthorityEpoch != binding.AuthorityEpoch {
		return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetAuthoritySuperseded, Reason: "runner attempt authority epoch was superseded"}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &binding, nil
}
