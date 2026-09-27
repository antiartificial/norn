package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/nomad"
)

func (db *DB) sourceRuntimeLaunchFromObservation(ctx context.Context, catalog database.Catalog, binding MySQLDeployedSourceBinding, observed nomad.MySQLSourceJobObservation) (string, error) {
	var reservationID, state, runtimeID string
	var encoded []byte
	err := db.Pool.QueryRow(ctx, `SELECT reservation_id,state,runtime_instance_id,target FROM mysql_runtime_launch_reservations
		WHERE target_key=$1 AND state IN ('reserved','launched','needs-inspection')`, mysqlRuntimePhysicalKeyForCatalog(catalog, binding.Source)).
		Scan(&reservationID, &state, &runtimeID, &encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil || state != "launched" || reservationID != WordPressColdStartReservationID(binding.OperationID, binding.DeploymentID, binding.SpecDigest) ||
		!containsMySQLSourceAllocation(observed.AllocationIDs, runtimeID) {
		return "", ErrMySQLSourceSnapshotFence
	}
	var target database.TargetIdentity
	if json.Unmarshal(encoded, &target) != nil || target != binding.Source {
		return "", ErrMySQLSourceSnapshotFence
	}
	return reservationID, nil
}

func verifyMySQLSourceRuntimeLaunch(ctx context.Context, tx pgx.Tx, catalog database.Catalog, request MySQLSourceSnapshotRequest) error {
	var reservationID, state, runtimeID string
	var encoded []byte
	err := tx.QueryRow(ctx, `SELECT reservation_id,state,runtime_instance_id,target FROM mysql_runtime_launch_reservations
		WHERE target_key=$1 AND state IN ('reserved','launched','needs-inspection') FOR UPDATE`, mysqlRuntimePhysicalKeyForCatalog(catalog, request.Source)).
		Scan(&reservationID, &state, &runtimeID, &encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		if request.RuntimeLaunchReservationID == "" {
			return nil
		}
		return ErrMySQLSourceSnapshotFence
	}
	if err != nil || request.RuntimeLaunchReservationID == "" || reservationID != request.RuntimeLaunchReservationID || state != "launched" ||
		!containsMySQLSourceAllocation(request.JobIdentity.AllocationIDs, runtimeID) {
		return ErrMySQLSourceSnapshotFence
	}
	var target database.TargetIdentity
	if json.Unmarshal(encoded, &target) != nil || target != request.Source {
		return ErrMySQLSourceSnapshotFence
	}
	return nil
}

func containsMySQLSourceAllocation(ids []string, wanted string) bool {
	if wanted == "" {
		return false
	}
	for _, id := range ids {
		if id == wanted {
			return true
		}
	}
	return false
}
