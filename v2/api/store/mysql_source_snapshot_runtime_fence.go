package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrMySQLSourceRuntimeFenceIndeterminate = errors.New("MySQL source runtime mutation fence may have been acquired; manual observation required")

// EnsureClaimedMySQLSourceRuntimeFence acquires the global runtime mutation
// fence before the first Nomad stop. The epoch is bound durably to this source
// operation. It is never released automatically by a failed snapshot.
func (db *DB) EnsureClaimedMySQLSourceRuntimeFence(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLSourceSnapshotRequest) error {
	intent, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptance, claim, request)
	if err != nil {
		return err
	}
	if intent.State != "quiesce-intended" {
		return ErrMySQLSourceRuntimeFenceIndeterminate
	}
	active, err := db.sourceRuntimeFenceHeld(ctx, claim)
	if err != nil {
		return err
	}
	if active {
		return nil
	}
	owner := "mysql-source-snapshot:" + claim.OperationID()
	fence, err := db.AcquireRuntimeMutationFence(ctx, owner, "quiesce MySQL source for signed snapshot")
	if err != nil {
		return err
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return errors.Join(ErrMySQLSourceRuntimeFenceIndeterminate, err)
	}
	defer tx.Rollback(context.Background())
	var held bool
	if err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND status='running' AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp() FOR UPDATE`, claim.OperationID(), MySQLSourceSnapshotOperationKind, claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return errors.Join(ErrMySQLSourceRuntimeFenceIndeterminate, ownershipLost(claim))
	}
	tag, err := tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET runtime_fence_epoch=$2,runtime_fence_owner=$3 WHERE operation_id=$1 AND state='quiesce-intended' AND runtime_fence_epoch IS NULL AND runtime_fence_owner IS NULL`, claim.OperationID(), fence.Epoch, owner)
	if err != nil {
		return errors.Join(ErrMySQLSourceRuntimeFenceIndeterminate, err)
	}
	if tag.RowsAffected() != 1 {
		return ErrMySQLSourceRuntimeFenceIndeterminate
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Join(ErrMySQLSourceRuntimeFenceIndeterminate, err)
	}
	return nil
}

func (db *DB) sourceRuntimeFenceHeld(ctx context.Context, claim OperationClaim) (bool, error) {
	if db == nil || db.Pool == nil || validateOperationClaim(claim) != nil {
		return false, ErrMySQLSourceSnapshotFence
	}
	var epoch *int64
	var owner *string
	var active bool
	var liveEpoch int64
	var liveOwner string
	err := db.Pool.QueryRow(ctx, `SELECT s.runtime_fence_epoch,s.runtime_fence_owner,f.active,f.epoch,f.owner FROM mysql_source_snapshot_intents s CROSS JOIN runtime_mutation_fence f WHERE s.operation_id=$1 AND f.singleton=true`, claim.OperationID()).Scan(&epoch, &owner, &active, &liveEpoch, &liveOwner)
	if err != nil {
		return false, err
	}
	if epoch == nil && owner == nil {
		return false, nil
	}
	if epoch == nil || owner == nil || !active || *epoch != liveEpoch || *owner != liveOwner || *owner != "mysql-source-snapshot:"+claim.OperationID() {
		return false, ErrMySQLSourceRuntimeFenceIndeterminate
	}
	return true, nil
}
