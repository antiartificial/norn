package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// RunClaimedReconciliation proves an already-unlocked target and releases the
// held fence under a new signed one-attempt operation. It never issues ALTER
// USER and leaves the failed predecessor's one-way intent unchanged.
func (r MySQLRestoreRecoveryRunner) RunClaimedReconciliation(ctx context.Context, claim OperationClaim) (runErr error) {
	if r.Control == nil || r.Acceptance == nil || r.Observer == nil || r.Secrets == nil {
		return ErrMySQLRestoreFence
	}
	lease := r.ClaimLease
	if lease == 0 {
		lease = 2 * time.Minute
	}
	supervisor, err := newMySQLRestoreClaimSupervisor(ctx, lease, func(renewCtx context.Context, duration time.Duration) error {
		return r.Control.RenewOperationClaim(renewCtx, claim, duration)
	})
	if err != nil {
		return err
	}
	if err := supervisor.Start(); err != nil {
		return err
	}
	terminal := false
	defer func() {
		if err := supervisor.Stop(); err != nil && !terminal && runErr == nil {
			runErr = err
		}
	}()
	if err := r.Control.ReconcileClaimedMySQLRestoreRecovery(supervisor.Context(), r.Acceptance, claim, r.Observer, r.Secrets); err != nil {
		return err
	}
	terminal = true
	return nil
}

// ReconcileClaimedMySQLRestoreRecovery requires an independently signed
// successor to a failed recovery whose unlock was intended or proved. Fresh
// Nomad and MySQL observations precede an atomic fence release and terminal
// receipt; the predecessor remains failed and inspectable.
func (db *DB) ReconcileClaimedMySQLRestoreRecovery(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, observer MySQLSourceStoppedObserver, secrets database.SecretSource) error {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || observer == nil || secrets == nil || validateOperationClaim(claim) != nil {
		return ErrMySQLRestoreFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	var signed MySQLRestoreRecoveryRequest
	if err != nil || accepted.Operation.Kind != MySQLRestoreRecoveryOperationKind || accepted.Operation.Status != model.OperationRunning ||
		accepted.Operation.MaxAttempts != 1 || accepted.Operation.Source != "private-mysql-restore-reconciliation" ||
		decodeMySQLRestoreRecoveryPayload(accepted.Operation.Payload, &signed) != nil || signed.PriorRecoveryOperationID == "" {
		return ErrMySQLRestoreFence
	}
	prior, err := acceptance.VerifyAcceptedOperation(ctx, signed.PriorRecoveryOperationID)
	var priorRequest MySQLRestoreRecoveryRequest
	if err != nil || prior.Operation.Kind != MySQLRestoreRecoveryOperationKind || prior.Operation.Status != model.OperationFailed ||
		prior.Intent.CanonicalDigest != signed.PriorRecoveryDigest || decodeMySQLRestoreRecoveryPayload(prior.Operation.Payload, &priorRequest) != nil ||
		priorRequest.PriorRecoveryOperationID != "" || priorRequest.RestoreOperationID != signed.RestoreOperationID ||
		priorRequest.RestoreAcceptanceDigest != signed.RestoreAcceptanceDigest || priorRequest.CatalogRevision != signed.CatalogRevision ||
		priorRequest.RuntimeFenceEpoch != signed.RuntimeFenceEpoch || priorRequest.RuntimeFenceOwner != signed.RuntimeFenceOwner ||
		priorRequest.SourceArtifactOperationID != signed.SourceArtifactOperationID || priorRequest.SourceReceiptSHA256 != signed.SourceReceiptSHA256 ||
		priorRequest.Target != signed.Target {
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
	if err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND source='private-mysql-restore-reconciliation'
		AND status='running' AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp() FOR UPDATE`,
		claim.OperationID(), MySQLRestoreRecoveryOperationKind, claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return ownershipLost(claim)
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != signed.CatalogRevision {
		return ErrDatabaseCatalogRevisionConflict
	}
	var priorStatus, priorState, priorIntentID, restoreID, fenceOwner, liveOwner, sourceID, receiptSHA string
	var revision, epoch, liveEpoch int64
	var manual, liveActive bool
	err = tx.QueryRow(ctx, `SELECT p.status,COALESCE((p.metadata->>'manualRecoveryRequired')::boolean,false),
		r.state,r.acceptance_intent_id,r.restore_operation_id,r.catalog_revision,r.runtime_fence_epoch,r.runtime_fence_owner,
		f.active,f.epoch,f.owner,m.source_artifact_operation_id,m.source_artifact_receipt_sha256
		FROM mysql_restore_recovery_intents r
		JOIN operations p ON p.id=r.operation_id
		JOIN mysql_restore_maintenance_fences m ON m.operation_id=r.restore_operation_id
		CROSS JOIN runtime_mutation_fence f
		WHERE r.operation_id=$1 AND f.singleton=true AND m.runtime_fence_transferred_at IS NOT NULL
		  AND m.recovery_released_at IS NULL AND r.target_unlock_intended_at IS NOT NULL
		  AND r.runtime_released_at IS NULL FOR UPDATE OF r,p,m,f`, signed.PriorRecoveryOperationID).Scan(
		&priorStatus, &manual, &priorState, &priorIntentID, &restoreID, &revision, &epoch, &fenceOwner,
		&liveActive, &liveEpoch, &liveOwner, &sourceID, &receiptSHA)
	if err != nil || priorStatus != "failed" || !manual ||
		(priorState != "target-unlock-intended" && priorState != "target-unlock-proved") ||
		priorIntentID != prior.AcceptanceIntentID || restoreID != signed.RestoreOperationID || revision != signed.CatalogRevision ||
		epoch != signed.RuntimeFenceEpoch || fenceOwner != signed.RuntimeFenceOwner || !liveActive || liveEpoch != epoch ||
		liveOwner != fenceOwner || sourceID != signed.SourceArtifactOperationID || receiptSHA != signed.SourceReceiptSHA256 {
		return ErrMySQLRestoreFence
	}
	if result, err := tx.Exec(ctx, `UPDATE mysql_restore_maintenance_fences
		SET recovery_released_at=clock_timestamp(),recovery_operation_id=$1
		WHERE operation_id=$2 AND recovery_released_at IS NULL AND runtime_fence_epoch=$3`,
		claim.OperationID(), restoreID, epoch); err != nil || result.RowsAffected() != 1 {
		return ErrMySQLRestoreFence
	}
	if result, err := tx.Exec(ctx, `UPDATE runtime_mutation_fence SET active=false,owner='',reason='',released_at=clock_timestamp()
		WHERE singleton=true AND active=true AND epoch=$1 AND owner=$2`, epoch, fenceOwner); err != nil || result.RowsAffected() != 1 {
		return ErrMySQLRestoreFence
	}
	metadata, err := json.Marshal(map[string]interface{}{"mysqlRestoreRecoveryState": "reconciled-runtime-released",
		"restoreOperationId": restoreID, "priorRecoveryOperationId": signed.PriorRecoveryOperationID,
		"priorRecoveryDigest": signed.PriorRecoveryDigest})
	if err != nil {
		return err
	}
	if result, err := tx.Exec(ctx, `UPDATE operations SET status='succeeded',message='MySQL target unlock observed and runtime fence released',
		metadata=metadata || $1::jsonb,locked_by='',locked_until=NULL,updated_at=clock_timestamp(),finished_at=clock_timestamp()
		WHERE id=$2 AND status='running' AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp()`,
		metadata, claim.OperationID(), claim.OwnerID(), claim.Generation()); err != nil || result.RowsAffected() != 1 {
		return ownershipLost(claim)
	}
	if result, err := tx.Exec(ctx, `UPDATE operations SET metadata=metadata || jsonb_build_object('reconciledByOperationId',$2::text),updated_at=clock_timestamp()
		WHERE id=$1 AND status='failed'`, signed.PriorRecoveryOperationID, claim.OperationID()); err != nil || result.RowsAffected() != 1 {
		return ErrMySQLRestoreFence
	}
	if _, err := tx.Exec(ctx, `INSERT INTO evidence_archive_intents (id,subject_kind,subject_id,app,operation_id,sequence,state)
		SELECT 'ei-' || gen_random_uuid()::text,'saga',saga_id,app,id,1,'pending' FROM operations WHERE id=$1 AND saga_id<>''
		ON CONFLICT (subject_kind,subject_id,sequence) DO NOTHING`, claim.OperationID()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
