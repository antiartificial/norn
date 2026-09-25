package store

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"norn/v2/api/nomad"
)

// MySQLSourceJobStopper is the narrow Nomad capability used by the private
// source-snapshot runner. StopJobCAS itself reobserves the exact allocation set.
type MySQLSourceJobStopper interface {
	StopJobCAS(context.Context, nomad.CASStopJobRequest) error
}

var ErrMySQLSourceStopIndeterminate = errors.New("MySQL source job stop may have executed; manual observation required")

// StopClaimedMySQLSourceJob persists the exact signed stop attempt before
// touching Nomad. An ambiguous outcome remains permanently fenced. It does
// not assert that MySQL is locked or the source artifact is safe to restore.
func (db *DB) StopClaimedMySQLSourceJob(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLSourceSnapshotRequest, stopper MySQLSourceJobStopper) error {
	if stopper == nil {
		return ErrMySQLSourceSnapshotFence
	}
	intent, err := db.PrepareClaimedMySQLSourceSnapshot(ctx, acceptance, claim, request)
	if err != nil {
		return err
	}
	if intent.State != "quiesce-intended" {
		return ErrMySQLSourceStopIndeterminate
	}
	if err := db.setClaimedMySQLSourceStopState(ctx, claim, "quiesce-intended", "stop-intended"); err != nil {
		return err
	}
	index, _ := strconv.ParseUint(request.JobIdentity.JobModifyIndex, 10, 64)
	if err := stopper.StopJobCAS(ctx, nomad.CASStopJobRequest{JobID: request.JobIdentity.JobID, Region: request.JobIdentity.NomadRegion, JobModifyIndex: index, AllocationIDs: append([]string(nil), request.JobIdentity.AllocationIDs...)}); err != nil {
		return errors.Join(ErrMySQLSourceStopIndeterminate, err)
	}
	if err := db.setClaimedMySQLSourceStopState(ctx, claim, "stop-intended", "stop-proved"); err != nil {
		return errors.Join(ErrMySQLSourceStopIndeterminate, err)
	}
	return nil
}

func (db *DB) setClaimedMySQLSourceStopState(ctx context.Context, claim OperationClaim, from, to string) error {
	if db == nil || db.Pool == nil || validateOperationClaim(claim) != nil {
		return ErrMySQLSourceSnapshotFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := checkOperationClaimLocked(ctx, tx, claim); err != nil {
		return err
	}
	var tag pgconn.CommandTag
	if to == "stop-intended" {
		tag, err = tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET state='stop-intended',stop_intended_at=clock_timestamp() WHERE operation_id=$1 AND state='quiesce-intended'`, claim.OperationID())
	} else if to == "stop-proved" {
		tag, err = tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET state='stop-proved',stop_proved_at=clock_timestamp() WHERE operation_id=$1 AND state='stop-intended' AND stop_intended_at IS NOT NULL`, claim.OperationID())
	} else {
		return ErrMySQLSourceSnapshotFence
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrMySQLSourceStopIndeterminate
	}
	return tx.Commit(ctx)
}
