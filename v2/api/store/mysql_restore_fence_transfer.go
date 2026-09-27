package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// TransferClaimedMySQLRestoreRuntimeFence atomically changes ownership of the
// active source fence to the separately accepted restore. It never clears the
// global gate, increments its epoch, or unlocks either MySQL account. A retry
// requires the same live claim binding; a successor claim cannot silently
// resume an uncertain maintenance operation.
func (db *DB) TransferClaimedMySQLRestoreRuntimeFence(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim) (RuntimeMutationFence, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || validateOperationClaim(claim) != nil {
		return RuntimeMutationFence{}, ErrMySQLRestoreFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil {
		return RuntimeMutationFence{}, err
	}
	var request MySQLRestoreRequest
	if accepted.Operation.Kind != MySQLRestoreOperationKind || accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 ||
		decodeMySQLRestorePayload(accepted.Operation.Payload, &request) != nil || !validMySQLRestoreSourceArtifact(request.SourceArtifact) {
		return RuntimeMutationFence{}, ErrMySQLRestoreFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RuntimeMutationFence{}, err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return RuntimeMutationFence{}, err
	}
	if err := verifyMySQLRestoreClaim(ctx, tx, claim); err != nil {
		return RuntimeMutationFence{}, err
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision ||
		mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Artifact.Source) == mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Target) {
		return RuntimeMutationFence{}, ErrMySQLRestoreFence
	}
	var fence RuntimeMutationFence
	var fenceActive bool
	if err := tx.QueryRow(ctx, `SELECT active,epoch,owner,reason,held_at FROM runtime_mutation_fence WHERE singleton=true FOR UPDATE`).Scan(
		&fenceActive, &fence.Epoch, &fence.Owner, &fence.Reason, &fence.HeldAt); err != nil || !fenceActive || fence.Epoch <= 0 {
		return RuntimeMutationFence{}, ErrMySQLRestoreFence
	}
	var intentID, state, profileID, logicalID, artifactPath string
	var revision int64
	var targetJSON, artifactJSON []byte
	if err := tx.QueryRow(ctx, `SELECT acceptance_intent_id,state,catalog_revision,profile_id,logical_id,target,artifact,artifact_path
		FROM mysql_restore_intents WHERE operation_id=$1 FOR UPDATE`, claim.OperationID()).Scan(
		&intentID, &state, &revision, &profileID, &logicalID, &targetJSON, &artifactJSON, &artifactPath); err != nil ||
		intentID != accepted.AcceptanceIntentID || state != "prepared" || revision != request.CatalogRevision ||
		profileID != request.ProfileID || logicalID != request.LogicalID || artifactPath != request.ArtifactPath {
		return RuntimeMutationFence{}, ErrMySQLRestoreFence
	}
	var target database.TargetIdentity
	var artifact database.MySQLSQLArtifact
	if json.Unmarshal(targetJSON, &target) != nil || json.Unmarshal(artifactJSON, &artifact) != nil || target != request.Target || artifact != request.Artifact {
		return RuntimeMutationFence{}, ErrMySQLRestoreFence
	}
	var sourceOperation, receiptDigest string
	var transferredEpoch, transferredGeneration sql.NullInt64
	var transferredOwner sql.NullString
	var transferredAt sql.NullTime
	if err := tx.QueryRow(ctx, `SELECT source_artifact_operation_id,source_artifact_receipt_sha256,runtime_fence_epoch,
		runtime_fence_claim_owner,runtime_fence_claim_generation,runtime_fence_transferred_at
		FROM mysql_restore_maintenance_fences WHERE operation_id=$1 FOR UPDATE`, claim.OperationID()).Scan(
		&sourceOperation, &receiptDigest, &transferredEpoch, &transferredOwner, &transferredGeneration, &transferredAt); err != nil ||
		sourceOperation != request.SourceArtifact.OperationID || receiptDigest != request.SourceArtifact.ReceiptSHA256 {
		return RuntimeMutationFence{}, ErrMySQLRestoreFence
	}
	if err := db.verifyMySQLRestoreSourceArtifact(ctx, tx, acceptance, request); err != nil {
		return RuntimeMutationFence{}, errors.Join(ErrMySQLRestoreFence, err)
	}
	var sourceEpoch int64
	var sourceOwner, sourceState string
	var stopped, locked bool
	if err := tx.QueryRow(ctx, `SELECT runtime_fence_epoch,runtime_fence_owner,state,
		stop_proved_at IS NOT NULL,lock_proved_at IS NOT NULL FROM mysql_source_snapshot_intents
		WHERE operation_id=$1 FOR UPDATE`, sourceOperation).Scan(&sourceEpoch, &sourceOwner, &sourceState, &stopped, &locked); err != nil ||
		(sourceState != "stage-proved" && sourceState != "retained-proved") || !stopped || !locked || sourceEpoch != fence.Epoch || sourceOwner != "mysql-source-snapshot:"+sourceOperation {
		return RuntimeMutationFence{}, ErrMySQLRestoreFence
	}
	restoreOwner := "mysql-restore:" + claim.OperationID()
	if transferredAt.Valid {
		if !transferredEpoch.Valid || transferredEpoch.Int64 != fence.Epoch || !transferredOwner.Valid || transferredOwner.String != claim.OwnerID() ||
			!transferredGeneration.Valid || transferredGeneration.Int64 != claim.Generation() || fence.Owner != restoreOwner {
			return RuntimeMutationFence{}, ErrMySQLRestoreFence
		}
		if err := tx.Commit(ctx); err != nil {
			return RuntimeMutationFence{}, err
		}
		return fence, nil
	}
	if transferredEpoch.Valid || transferredOwner.Valid || transferredGeneration.Valid || fence.Owner != sourceOwner {
		return RuntimeMutationFence{}, ErrMySQLRestoreFence
	}
	const reason = "MySQL restore runtime account and SQL maintenance"
	updated, err := tx.Exec(ctx, `UPDATE runtime_mutation_fence SET owner=$1,reason=$2
		WHERE singleton=true AND active=true AND epoch=$3 AND owner=$4`, restoreOwner, reason, fence.Epoch, sourceOwner)
	if err != nil || updated.RowsAffected() != 1 {
		return RuntimeMutationFence{}, ErrMySQLRestoreFence
	}
	updated, err = tx.Exec(ctx, `UPDATE mysql_restore_maintenance_fences SET runtime_fence_epoch=$2,
		runtime_fence_claim_owner=$3,runtime_fence_claim_generation=$4,runtime_fence_transferred_at=clock_timestamp()
		WHERE operation_id=$1 AND runtime_fence_transferred_at IS NULL`, claim.OperationID(), fence.Epoch, claim.OwnerID(), claim.Generation())
	if err != nil || updated.RowsAffected() != 1 {
		return RuntimeMutationFence{}, ErrMySQLRestoreFence
	}
	if err := tx.Commit(ctx); err != nil {
		return RuntimeMutationFence{}, err
	}
	fence.Owner, fence.Reason = restoreOwner, reason
	return fence, nil
}

func verifyClaimedMySQLRestoreTransferredFence(ctx context.Context, tx pgx.Tx, claim OperationClaim, request MySQLRestoreRequest) error {
	var epoch int64
	var owner, claimOwner, sourceOperation, receiptDigest string
	var generation int64
	var active bool
	err := tx.QueryRow(ctx, `SELECT f.epoch,f.owner,f.active,m.runtime_fence_claim_owner,m.runtime_fence_claim_generation,
		m.source_artifact_operation_id,m.source_artifact_receipt_sha256
		FROM runtime_mutation_fence f JOIN mysql_restore_maintenance_fences m ON m.operation_id=$1
		WHERE f.singleton=true AND m.runtime_fence_epoch=f.epoch AND m.runtime_fence_transferred_at IS NOT NULL
		FOR UPDATE OF f,m`, claim.OperationID()).Scan(&epoch, &owner, &active, &claimOwner, &generation, &sourceOperation, &receiptDigest)
	if err != nil || epoch <= 0 || !active || owner != "mysql-restore:"+claim.OperationID() || claimOwner != claim.OwnerID() ||
		generation != claim.Generation() || sourceOperation != request.SourceArtifact.OperationID || receiptDigest != request.SourceArtifact.ReceiptSHA256 {
		return ErrMySQLRestoreFence
	}
	return nil
}
