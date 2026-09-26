package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"norn/v2/api/database"
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
	if err := db.EnsureClaimedMySQLSourceRuntimeFence(ctx, acceptance, claim, request); err != nil {
		return err
	}
	if err := db.setClaimedMySQLSourceStopState(ctx, claim, request, "quiesce-intended", "stop-intended"); err != nil {
		return err
	}
	index, _ := strconv.ParseUint(request.JobIdentity.JobModifyIndex, 10, 64)
	version, _ := strconv.ParseUint(request.JobIdentity.JobVersion, 10, 64)
	if err := stopper.StopJobCAS(ctx, nomad.CASStopJobRequest{
		JobID: request.JobIdentity.JobID, Region: request.JobIdentity.NomadRegion, JobVersion: version, JobModifyIndex: index,
		AllocationIDs: append([]string(nil), request.JobIdentity.AllocationIDs...), DeploymentID: request.JobIdentity.DeploymentID,
		SpecDigest: request.JobIdentity.SpecDigest, DatabaseBindingSchema: request.JobIdentity.DatabaseBindingSchema,
		DatabaseBindingSHA256: request.JobIdentity.DatabaseBindingSHA256, DatabaseCatalogRevision: request.JobIdentity.DatabaseCatalogRevision,
	}); err != nil {
		return errors.Join(ErrMySQLSourceStopIndeterminate, err)
	}
	if err := db.setClaimedMySQLSourceStopState(ctx, claim, request, "stop-intended", "stop-proved"); err != nil {
		return errors.Join(ErrMySQLSourceStopIndeterminate, err)
	}
	return nil
}

func (db *DB) setClaimedMySQLSourceStopState(ctx context.Context, claim OperationClaim, request MySQLSourceSnapshotRequest, from, to string) error {
	if db == nil || db.Pool == nil || validateOperationClaim(claim) != nil {
		return ErrMySQLSourceSnapshotFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return err
	}
	var held bool
	if err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND status='running' AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp() FOR UPDATE`, claim.OperationID(), MySQLSourceSnapshotOperationKind, claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return ownershipLost(claim)
	}
	var tag pgconn.CommandTag
	if to == "stop-intended" {
		tag, err = tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET state='stop-intended',stop_intended_at=clock_timestamp() WHERE operation_id=$1 AND state='quiesce-intended' AND EXISTS (SELECT 1 FROM runtime_mutation_fence f WHERE f.singleton=true AND f.active=true AND f.epoch=mysql_source_snapshot_intents.runtime_fence_epoch AND f.owner=mysql_source_snapshot_intents.runtime_fence_owner)`, claim.OperationID())
	} else if to == "stop-proved" {
		if request.RuntimeLaunchReservationID != "" {
			if err := stopClaimedMySQLSourceRuntimeLaunch(ctx, tx, request); err != nil {
				return err
			}
		}
		tag, err = tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET state='stop-proved',stop_proved_at=clock_timestamp() WHERE operation_id=$1 AND state='stop-intended' AND stop_intended_at IS NOT NULL AND EXISTS (SELECT 1 FROM runtime_mutation_fence f WHERE f.singleton=true AND f.active=true AND f.epoch=mysql_source_snapshot_intents.runtime_fence_epoch AND f.owner=mysql_source_snapshot_intents.runtime_fence_owner)`, claim.OperationID())
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

func stopClaimedMySQLSourceRuntimeLaunch(ctx context.Context, tx pgx.Tx, request MySQLSourceSnapshotRequest) error {
	return stopMySQLSourceRuntimeLaunchWithProof(ctx, tx, request, "nomad-cas-stop-proved\x00", "exact signed Nomad CAS stop")
}

func stopObservedMySQLSourceRuntimeLaunch(ctx context.Context, tx pgx.Tx, request MySQLSourceSnapshotRequest) error {
	return stopMySQLSourceRuntimeLaunchWithProof(ctx, tx, request, "nomad-stopped-observed\x00", "exact signed Nomad stopped observation")
}

func stopMySQLSourceRuntimeLaunchWithProof(ctx context.Context, tx pgx.Tx, request MySQLSourceSnapshotRequest, domain, method string) error {
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return ErrMySQLSourceSnapshotFence
	}
	key := mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Source)
	var runtimeID string
	var encoded []byte
	if err := tx.QueryRow(ctx, `SELECT runtime_instance_id,target FROM mysql_runtime_launch_reservations
		WHERE reservation_id=$1 AND target_key=$2 AND state='launched' FOR UPDATE`, request.RuntimeLaunchReservationID, key).Scan(&runtimeID, &encoded); err != nil || !containsMySQLSourceAllocation(request.JobIdentity.AllocationIDs, runtimeID) {
		return ErrMySQLSourceSnapshotFence
	}
	var target database.TargetIdentity
	if json.Unmarshal(encoded, &target) != nil || target != request.Source {
		return ErrMySQLSourceSnapshotFence
	}
	job, _ := json.Marshal(request.JobIdentity)
	digest := sha256.Sum256(append([]byte(domain), job...))
	proof, _ := json.Marshal(MySQLRuntimeLaunchStopProof{RuntimeInstanceID: runtimeID, ObservedAt: time.Now().UTC(),
		Method: method, EvidenceSHA256: hex.EncodeToString(digest[:])})
	result, err := tx.Exec(ctx, `UPDATE mysql_runtime_launch_reservations SET state='stopped',stop_proof=$3,updated_at=clock_timestamp()
		WHERE reservation_id=$1 AND target_key=$2 AND state='launched' AND runtime_instance_id=$4`, request.RuntimeLaunchReservationID, key, proof, runtimeID)
	if err != nil || result.RowsAffected() != 1 {
		return ErrMySQLSourceSnapshotFence
	}
	return nil
}
