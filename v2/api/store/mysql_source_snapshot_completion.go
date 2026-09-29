package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

// FinishClaimedMySQLSourceRetention terminalizes only a live exact source
// claim with a verified signed retention receipt. It deliberately leaves the
// source runtime fence and locked account in place for a separately accepted
// restore. A proof or ownership failure leaves the operation running and the
// source fenced for inspection.
func (db *DB) FinishClaimedMySQLSourceRetention(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim) error {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || validateOperationClaim(claim) != nil {
		return ErrMySQLSourceSnapshotFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil || accepted.Operation.Kind != MySQLSourceSnapshotOperationKind ||
		accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 {
		return ErrMySQLSourceSnapshotFence
	}
	request, err := sourceSnapshotRequestFromAccepted(accepted)
	if err != nil {
		return errors.Join(ErrMySQLSourceSnapshotFence, err)
	}
	retained, err := db.LoadSignedMySQLSourceArtifactRetentionReceipt(ctx, acceptance, claim.OperationID())
	if err != nil {
		return errors.Join(ErrMySQLSourceSnapshotFence, err)
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return err
	}
	if err := verifyClaimedMySQLSourceStage(ctx, tx, claim, accepted, request, "retained-proved"); err != nil {
		return err
	}
	var digest string
	if err := tx.QueryRow(ctx, `SELECT retention_receipt_sha256 FROM mysql_source_snapshot_intents WHERE operation_id=$1`, claim.OperationID()).Scan(&digest); err != nil || digest != retained.SHA256 {
		return ErrMySQLSourceSnapshotFence
	}
	var finished int
	err = tx.QueryRow(ctx, `
		WITH terminal AS (
			UPDATE operations SET status='succeeded',
				message='MySQL source artifact retained and verified',
				metadata=metadata || jsonb_build_object('mysqlSourceRetentionReceiptSha256',$4::text),
				locked_by='',locked_until=NULL,updated_at=now(),finished_at=now()
			WHERE id=$1 AND kind='database.mysql-source-snapshot' AND status='running'
			  AND locked_by=$2 AND lock_generation=$3 AND locked_until>now()
			RETURNING id,saga_id,app
		), archive_intents AS (
			INSERT INTO evidence_archive_intents (id,subject_kind,subject_id,app,operation_id,sequence,state)
			SELECT 'ei-' || gen_random_uuid()::text,'saga',saga_id,app,id,1,'pending'
			FROM terminal WHERE saga_id<>''
			ON CONFLICT (subject_kind,subject_id,sequence) DO NOTHING
		)
		SELECT count(*) FROM terminal`, claim.OperationID(), claim.OwnerID(), claim.Generation(), digest).Scan(&finished)
	if err != nil {
		return err
	}
	if finished != 1 {
		return ownershipLost(claim)
	}
	return tx.Commit(ctx)
}
