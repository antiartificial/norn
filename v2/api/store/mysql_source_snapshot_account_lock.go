package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// MySQLSourceAccountLocker is the narrow external capability used by the
// private runner. The production implementation locks the exact catalog
// runtime account, kills existing sessions, and verifies the drain.
type MySQLSourceAccountLocker interface {
	FenceMySQLRuntimeAccount(context.Context, database.ResolvedBinding, database.MySQLMaintenanceCredentials, database.SecretSource) error
}

type mysqlSourceDatabaseAccountLocker struct{}

func (mysqlSourceDatabaseAccountLocker) FenceMySQLRuntimeAccount(ctx context.Context, resolved database.ResolvedBinding, maintenance database.MySQLMaintenanceCredentials, secrets database.SecretSource) error {
	return database.FenceMySQLRuntimeAccountForRestore(ctx, resolved, maintenance, secrets)
}

var ErrMySQLSourceAccountLockIndeterminate = errors.New("MySQL source runtime account lock may have executed; manual observation required")

// LockClaimedMySQLSourceAccount records the effect intent before touching
// MySQL. The reserved source and intended state remain after any ambiguous
// outcome; a retry never silently repeats an account mutation.
func (db *DB) LockClaimedMySQLSourceAccount(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLSourceSnapshotRequest, secrets database.SecretSource, locker MySQLSourceAccountLocker) error {
	if locker == nil || secrets == nil {
		return ErrMySQLSourceSnapshotFence
	}
	resolved, err := db.intendClaimedMySQLSourceAccountLock(ctx, acceptance, claim, request)
	if err != nil {
		return err
	}
	if err := locker.FenceMySQLRuntimeAccount(ctx, resolved, request.Maintenance, secrets); err != nil {
		return errors.Join(ErrMySQLSourceAccountLockIndeterminate, err)
	}
	if err := db.proveClaimedMySQLSourceAccountLock(ctx, claim); err != nil {
		return errors.Join(ErrMySQLSourceAccountLockIndeterminate, err)
	}
	return nil
}

// LockClaimedMySQLSourceAccountWithDatabase uses the catalog-bound MySQL
// account fence. It is private and has no HTTP route or implicit unlock.
func (db *DB) LockClaimedMySQLSourceAccountWithDatabase(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLSourceSnapshotRequest, secrets database.SecretSource) error {
	return db.LockClaimedMySQLSourceAccount(ctx, acceptance, claim, request, secrets, mysqlSourceDatabaseAccountLocker{})
}

func (db *DB) intendClaimedMySQLSourceAccountLock(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLSourceSnapshotRequest) (database.ResolvedBinding, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || validateOperationClaim(claim) != nil || !validMySQLSourceSnapshotRequest(request) {
		return database.ResolvedBinding{}, ErrMySQLSourceSnapshotFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil {
		return database.ResolvedBinding{}, err
	}
	if accepted.Operation.Kind != MySQLSourceSnapshotOperationKind || accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 || !sameMySQLSourceSnapshotPayload(accepted.Operation.Payload, request) {
		return database.ResolvedBinding{}, ErrMySQLSourceSnapshotFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return database.ResolvedBinding{}, err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return database.ResolvedBinding{}, err
	}
	var activeFence bool
	var fenceEpoch int64
	var fenceOwner string
	if err := tx.QueryRow(ctx, `SELECT active,epoch,owner FROM runtime_mutation_fence WHERE singleton=true FOR UPDATE`).Scan(&activeFence, &fenceEpoch, &fenceOwner); err != nil || !activeFence {
		return database.ResolvedBinding{}, ErrMySQLSourceRuntimeFenceIndeterminate
	}
	var held bool
	if err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND status='running' AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp() FOR UPDATE`, claim.OperationID(), MySQLSourceSnapshotOperationKind, claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return database.ResolvedBinding{}, ownershipLost(claim)
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return database.ResolvedBinding{}, ErrDatabaseCatalogRevisionConflict
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return database.ResolvedBinding{}, err
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Source})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != request.Maintenance {
		return database.ResolvedBinding{}, ErrMySQLSourceSnapshotFence
	}
	source, _ := json.Marshal(request.Source)
	maintenance, _ := json.Marshal(request.Maintenance)
	job, _ := json.Marshal(request.JobIdentity)
	key := mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Source)
	var savedIntent, savedProfile, savedLogical, savedKey, savedDigest, state string
	var savedRevision int64
	var savedSource, savedMaintenance, savedJob []byte
	var stopped bool
	var savedFenceEpoch *int64
	var savedFenceOwner *string
	err = tx.QueryRow(ctx, `SELECT acceptance_intent_id,catalog_revision,profile_id,logical_id,source_key,source,maintenance,job_identity,dump_tool_sha256,state,stop_proved_at IS NOT NULL,runtime_fence_epoch,runtime_fence_owner FROM mysql_source_snapshot_intents WHERE operation_id=$1 FOR UPDATE`, claim.OperationID()).Scan(&savedIntent, &savedRevision, &savedProfile, &savedLogical, &savedKey, &savedSource, &savedMaintenance, &savedJob, &savedDigest, &state, &stopped, &savedFenceEpoch, &savedFenceOwner)
	if err != nil || savedIntent != accepted.AcceptanceIntentID || savedRevision != request.CatalogRevision || savedProfile != request.ProfileID || savedLogical != request.LogicalID || savedKey != key || !sameJSON(savedSource, source) || !sameJSON(savedMaintenance, maintenance) || !sameJSON(savedJob, job) || savedDigest != request.DumpToolSHA256 || state != "stop-proved" || !stopped || savedFenceEpoch == nil || savedFenceOwner == nil || *savedFenceEpoch != fenceEpoch || *savedFenceOwner != fenceOwner || fenceOwner != "mysql-source-snapshot:"+claim.OperationID() {
		return database.ResolvedBinding{}, ErrMySQLSourceAccountLockIndeterminate
	}
	tag, err := tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET state='lock-intended',lock_intended_at=clock_timestamp() WHERE operation_id=$1 AND state='stop-proved' AND stop_proved_at IS NOT NULL`, claim.OperationID())
	if err != nil {
		return database.ResolvedBinding{}, err
	}
	if tag.RowsAffected() != 1 {
		return database.ResolvedBinding{}, ErrMySQLSourceAccountLockIndeterminate
	}
	if err := tx.Commit(ctx); err != nil {
		return database.ResolvedBinding{}, err
	}
	return resolved, nil
}

func (db *DB) proveClaimedMySQLSourceAccountLock(ctx context.Context, claim OperationClaim) error {
	if db == nil || db.Pool == nil || validateOperationClaim(claim) != nil {
		return ErrMySQLSourceSnapshotFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var held bool
	if err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND status='running' AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp() FOR UPDATE`, claim.OperationID(), MySQLSourceSnapshotOperationKind, claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return ownershipLost(claim)
	}
	tag, err := tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET state='lock-proved',lock_proved_at=clock_timestamp() WHERE operation_id=$1 AND state='lock-intended' AND lock_intended_at IS NOT NULL AND stop_proved_at IS NOT NULL`, claim.OperationID())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrMySQLSourceAccountLockIndeterminate
	}
	return tx.Commit(ctx)
}
