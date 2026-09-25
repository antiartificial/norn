package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

type MySQLRestoreRecoveryIntent struct {
	OperationID        string
	RestoreOperationID string
	State              string
	Replayed           bool
}

// PrepareClaimedMySQLRestoreRecovery commits the signed, claim-bound recovery
// checkpoint before any external unlock. It rechecks the live fence and
// completed restore while holding the catalog gate and fence row. An exact
// same-claim retry is idempotent; another operation cannot take this restore.
func (db *DB) PrepareClaimedMySQLRestoreRecovery(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim) (MySQLRestoreRecoveryIntent, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || validateOperationClaim(claim) != nil {
		return MySQLRestoreRecoveryIntent{}, ErrMySQLRestoreFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil {
		return MySQLRestoreRecoveryIntent{}, err
	}
	var request MySQLRestoreRecoveryRequest
	if accepted.Operation.Kind != MySQLRestoreRecoveryOperationKind || accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 ||
		decodeMySQLRestoreRecoveryPayload(accepted.Operation.Payload, &request) != nil {
		return MySQLRestoreRecoveryIntent{}, ErrMySQLRestoreFence
	}
	ready, err := db.AssessCompletedMySQLRestoreRecovery(ctx, acceptance, request.RestoreOperationID)
	if err != nil {
		return MySQLRestoreRecoveryIntent{}, err
	}
	restore, err := acceptance.VerifyAcceptedOperation(ctx, request.RestoreOperationID)
	if err != nil || restore.Intent.CanonicalDigest != request.RestoreAcceptanceDigest ||
		ready.Request.CatalogRevision != request.CatalogRevision || ready.Fence.Epoch != request.RuntimeFenceEpoch ||
		ready.Fence.Owner != request.RuntimeFenceOwner || ready.Request.Target != request.Target ||
		ready.Request.SourceArtifact.OperationID != request.SourceArtifactOperationID ||
		ready.Request.SourceArtifact.ReceiptSHA256 != request.SourceReceiptSHA256 {
		return MySQLRestoreRecoveryIntent{}, ErrMySQLRestoreFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MySQLRestoreRecoveryIntent{}, err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return MySQLRestoreRecoveryIntent{}, err
	}
	var held bool
	if err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND status='running'
		AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp() FOR UPDATE`,
		claim.OperationID(), MySQLRestoreRecoveryOperationKind, claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return MySQLRestoreRecoveryIntent{}, ownershipLost(claim)
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return MySQLRestoreRecoveryIntent{}, ErrDatabaseCatalogRevisionConflict
	}
	var fenceActive bool
	var epoch int64
	var owner, restoreState, lockState, sourceID, receiptSHA string
	var revision int64
	err = tx.QueryRow(ctx, `SELECT f.active,f.epoch,f.owner,i.state,l.state,m.catalog_revision,
		m.source_artifact_operation_id,m.source_artifact_receipt_sha256
		FROM runtime_mutation_fence f
		JOIN mysql_restore_intents i ON i.operation_id=$1
		JOIN mysql_restore_runtime_locks l ON l.operation_id=i.operation_id
		JOIN mysql_restore_maintenance_fences m ON m.operation_id=i.operation_id AND m.runtime_fence_epoch=f.epoch
		WHERE f.singleton=true AND i.completed_at IS NOT NULL AND l.verified_at IS NOT NULL
		  AND m.runtime_fence_transferred_at IS NOT NULL
		FOR UPDATE OF f,i,l,m`, request.RestoreOperationID).Scan(&fenceActive, &epoch, &owner, &restoreState, &lockState, &revision, &sourceID, &receiptSHA)
	if errors.Is(err, pgx.ErrNoRows) {
		return MySQLRestoreRecoveryIntent{}, ErrMySQLRestoreFence
	}
	if err != nil {
		return MySQLRestoreRecoveryIntent{}, err
	}
	if !fenceActive || epoch != request.RuntimeFenceEpoch || owner != request.RuntimeFenceOwner ||
		restoreState != "completed" || lockState != "verified-lock" || revision != request.CatalogRevision ||
		sourceID != request.SourceArtifactOperationID || receiptSHA != request.SourceReceiptSHA256 {
		return MySQLRestoreRecoveryIntent{}, ErrMySQLRestoreFence
	}
	inserted, err := tx.Exec(ctx, `INSERT INTO mysql_restore_recovery_intents
		(operation_id,restore_operation_id,acceptance_intent_id,catalog_revision,runtime_fence_epoch,runtime_fence_owner,
		 claim_owner,claim_generation,state) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'prepared') ON CONFLICT DO NOTHING`,
		claim.OperationID(), request.RestoreOperationID, accepted.AcceptanceIntentID, request.CatalogRevision,
		request.RuntimeFenceEpoch, request.RuntimeFenceOwner, claim.OwnerID(), claim.Generation())
	if err != nil {
		return MySQLRestoreRecoveryIntent{}, err
	}
	var saved MySQLRestoreRecoveryIntent
	var intentID, fenceOwner, claimOwner string
	var savedRevision, savedEpoch, generation int64
	err = tx.QueryRow(ctx, `SELECT restore_operation_id,acceptance_intent_id,catalog_revision,runtime_fence_epoch,
		runtime_fence_owner,claim_owner,claim_generation,state FROM mysql_restore_recovery_intents WHERE operation_id=$1 FOR UPDATE`,
		claim.OperationID()).Scan(&saved.RestoreOperationID, &intentID, &savedRevision, &savedEpoch, &fenceOwner, &claimOwner, &generation, &saved.State)
	if err != nil || saved.RestoreOperationID != request.RestoreOperationID || intentID != accepted.AcceptanceIntentID ||
		savedRevision != request.CatalogRevision || savedEpoch != request.RuntimeFenceEpoch || fenceOwner != request.RuntimeFenceOwner ||
		claimOwner != claim.OwnerID() || generation != claim.Generation() || saved.State != "prepared" {
		return MySQLRestoreRecoveryIntent{}, ErrMySQLRestoreFence
	}
	if err := tx.Commit(ctx); err != nil {
		return MySQLRestoreRecoveryIntent{}, err
	}
	saved.OperationID = claim.OperationID()
	saved.Replayed = inserted.RowsAffected() == 0
	return saved, nil
}
