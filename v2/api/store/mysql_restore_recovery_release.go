package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// ReleaseClaimedMySQLRestoreRuntimeFence records the recovery and opens the
// exact global fence in one transaction. The source job and account remain
// stopped and locked; this operation only releases global mutation admission.
func (db *DB) ReleaseClaimedMySQLRestoreRuntimeFence(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, observer MySQLSourceStoppedObserver, secrets database.SecretSource) error {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || observer == nil || secrets == nil || validateOperationClaim(claim) != nil {
		return ErrMySQLRestoreFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	var signed MySQLRestoreRecoveryRequest
	if err != nil || accepted.Operation.Kind != MySQLRestoreRecoveryOperationKind || accepted.Operation.Status != model.OperationRunning ||
		accepted.Operation.MaxAttempts != 1 || decodeMySQLRestoreRecoveryPayload(accepted.Operation.Payload, &signed) != nil {
		return ErrMySQLRestoreFence
	}
	restore, err := acceptance.VerifyAcceptedOperation(ctx, signed.RestoreOperationID)
	if err != nil || restore.Intent.CanonicalDigest != signed.RestoreAcceptanceDigest {
		return ErrMySQLRestoreFence
	}
	ready, err := db.AssessCompletedMySQLRestoreLiveSource(ctx, acceptance, signed.RestoreOperationID, observer, secrets)
	if err != nil || ready.Fence.Epoch != signed.RuntimeFenceEpoch || ready.Fence.Owner != signed.RuntimeFenceOwner ||
		ready.Request.CatalogRevision != signed.CatalogRevision || ready.Request.Target != signed.Target ||
		ready.Request.SourceArtifact.OperationID != signed.SourceArtifactOperationID ||
		ready.Request.SourceArtifact.ReceiptSHA256 != signed.SourceReceiptSHA256 {
		return ErrMySQLRestoreFence
	}
	resolved, err := db.resolvedMySQLRecoveryTarget(ctx, ready)
	if err != nil {
		return err
	}
	if err := database.InspectMySQLRuntimeAccountUnlockedForRecovery(ctx, resolved, ready.Request.Maintenance, secrets); err != nil {
		return err
	}
	binding, err := database.MySQLRestoreBinding(resolved)
	if err != nil {
		return err
	}
	if err := database.VerifyMySQLRestoreTarget(ctx, binding, secrets, ready.Request.Artifact.Expectation); err != nil {
		return err
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
	if err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND status='running'
		AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp() FOR UPDATE`,
		claim.OperationID(), MySQLRestoreRecoveryOperationKind, claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return ownershipLost(claim)
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != signed.CatalogRevision {
		return ErrDatabaseCatalogRevisionConflict
	}
	var state, intentID, restoreID, claimOwner, fenceOwner, liveOwner, sourceID, receiptSHA string
	var revision, epoch, generation, liveEpoch int64
	var liveActive bool
	err = tx.QueryRow(ctx, `SELECT r.state,r.acceptance_intent_id,r.restore_operation_id,r.catalog_revision,
		r.runtime_fence_epoch,r.runtime_fence_owner,r.claim_owner,r.claim_generation,
		f.active,f.epoch,f.owner,m.source_artifact_operation_id,m.source_artifact_receipt_sha256
		FROM mysql_restore_recovery_intents r
		JOIN mysql_restore_maintenance_fences m ON m.operation_id=r.restore_operation_id
		CROSS JOIN runtime_mutation_fence f
		WHERE r.operation_id=$1 AND f.singleton=true AND r.target_unlock_proved_at IS NOT NULL
		AND m.runtime_fence_transferred_at IS NOT NULL AND m.recovery_released_at IS NULL
		FOR UPDATE OF r,m,f`, claim.OperationID()).Scan(
		&state, &intentID, &restoreID, &revision, &epoch, &fenceOwner, &claimOwner, &generation,
		&liveActive, &liveEpoch, &liveOwner, &sourceID, &receiptSHA)
	if err != nil || state != "target-unlock-proved" || intentID != accepted.AcceptanceIntentID ||
		restoreID != signed.RestoreOperationID || revision != signed.CatalogRevision || epoch != signed.RuntimeFenceEpoch ||
		fenceOwner != signed.RuntimeFenceOwner || claimOwner != claim.OwnerID() || generation != claim.Generation() ||
		!liveActive || liveEpoch != epoch || liveOwner != fenceOwner ||
		sourceID != signed.SourceArtifactOperationID || receiptSHA != signed.SourceReceiptSHA256 {
		return ErrMySQLRestoreFence
	}
	if result, err := tx.Exec(ctx, `UPDATE mysql_restore_maintenance_fences
		SET recovery_released_at=clock_timestamp(),recovery_operation_id=$1
		WHERE operation_id=$2 AND recovery_released_at IS NULL AND runtime_fence_epoch=$3`,
		claim.OperationID(), restoreID, epoch); err != nil || result.RowsAffected() != 1 {
		return ErrMySQLRestoreFence
	}
	if result, err := tx.Exec(ctx, `UPDATE mysql_restore_recovery_intents
		SET state='runtime-released',runtime_released_at=clock_timestamp()
		WHERE operation_id=$1 AND state='target-unlock-proved' AND runtime_released_at IS NULL`, claim.OperationID()); err != nil || result.RowsAffected() != 1 {
		return ErrMySQLRestoreFence
	}
	if result, err := tx.Exec(ctx, `UPDATE runtime_mutation_fence
		SET active=false,owner='',reason='',released_at=clock_timestamp()
		WHERE singleton=true AND active=true AND epoch=$1 AND owner=$2`, epoch, fenceOwner); err != nil || result.RowsAffected() != 1 {
		return ErrMySQLRestoreFence
	}
	metadata, err := json.Marshal(map[string]interface{}{"mysqlRestoreRecoveryState": "runtime-released", "restoreOperationId": restoreID, "acceptanceIntentId": intentID})
	if err != nil {
		return err
	}
	if result, err := tx.Exec(ctx, `UPDATE operations SET status='succeeded',message='MySQL restore runtime fence released',
		metadata=metadata || $1::jsonb,locked_by='',locked_until=NULL,updated_at=clock_timestamp(),finished_at=clock_timestamp()
		WHERE id=$2 AND status='running' AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp()`,
		metadata, claim.OperationID(), claim.OwnerID(), claim.Generation()); err != nil || result.RowsAffected() != 1 {
		return ownershipLost(claim)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO evidence_archive_intents (id,subject_kind,subject_id,app,operation_id,sequence,state)
		SELECT 'ei-' || gen_random_uuid()::text,'saga',saga_id,app,id,1,'pending' FROM operations WHERE id=$1 AND saga_id<>''
		ON CONFLICT (subject_kind,subject_id,sequence) DO NOTHING`, claim.OperationID()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
