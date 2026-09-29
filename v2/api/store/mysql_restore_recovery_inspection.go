package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// MySQLRestoreRecoveryInspection is advisory evidence for an interrupted
// private recovery. None of its observations grants authority to repeat an
// account mutation or release the runtime fence.
type MySQLRestoreRecoveryInspection struct {
	RecoveryOperationID string                `json:"recoveryOperationId"`
	RestoreOperationID  string                `json:"restoreOperationId"`
	OperationStatus     model.OperationStatus `json:"operationStatus"`
	IntentState         string                `json:"intentState"`
	FenceEpoch          int64                 `json:"fenceEpoch"`
	FenceOwner          string                `json:"fenceOwner"`
	SourceVerified      bool                  `json:"sourceVerified"`
	TargetDataVerified  bool                  `json:"targetDataVerified"`
	TargetAccountState  string                `json:"targetAccountState"`
}

// InspectPrivateMySQLRestoreRecovery verifies signed recovery lineage and
// then observes the stopped source, target data, and target runtime account.
// An unavailable or conflicting live observation is reported as unverified;
// it is never inferred from a durable intent alone.
func (db *DB) InspectPrivateMySQLRestoreRecovery(ctx context.Context, acceptance *PGOperationStore, operationID string, observer MySQLSourceStoppedObserver, secrets database.SecretSource) (MySQLRestoreRecoveryInspection, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || observer == nil || secrets == nil || operationID == "" {
		return MySQLRestoreRecoveryInspection{}, ErrMySQLRestoreFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, operationID)
	var signed MySQLRestoreRecoveryRequest
	if err != nil || accepted.Operation.Kind != MySQLRestoreRecoveryOperationKind ||
		decodeMySQLRestoreRecoveryPayload(accepted.Operation.Payload, &signed) != nil {
		return MySQLRestoreRecoveryInspection{}, ErrMySQLRestoreFence
	}
	restore, err := acceptance.VerifyAcceptedOperation(ctx, signed.RestoreOperationID)
	if err != nil || restore.Intent.CanonicalDigest != signed.RestoreAcceptanceDigest {
		return MySQLRestoreRecoveryInspection{}, ErrMySQLRestoreFence
	}
	ready, err := db.AssessCompletedMySQLRestoreRecovery(ctx, acceptance, signed.RestoreOperationID)
	if err != nil || ready.Fence.Epoch != signed.RuntimeFenceEpoch || ready.Fence.Owner != signed.RuntimeFenceOwner ||
		ready.Request.CatalogRevision != signed.CatalogRevision || ready.Request.Target != signed.Target ||
		ready.Request.SourceArtifact.OperationID != signed.SourceArtifactOperationID ||
		ready.Request.SourceArtifact.ReceiptSHA256 != signed.SourceReceiptSHA256 {
		return MySQLRestoreRecoveryInspection{}, ErrMySQLRestoreFence
	}
	inspection := MySQLRestoreRecoveryInspection{RecoveryOperationID: operationID,
		RestoreOperationID: signed.RestoreOperationID, OperationStatus: accepted.Operation.Status,
		IntentState: "not-started", FenceEpoch: ready.Fence.Epoch, FenceOwner: ready.Fence.Owner,
		TargetAccountState: "indeterminate"}
	var intentID, restoreID, owner, state string
	var revision, epoch int64
	err = db.Pool.QueryRow(ctx, `SELECT acceptance_intent_id,restore_operation_id,catalog_revision,
		runtime_fence_epoch,runtime_fence_owner,state FROM mysql_restore_recovery_intents WHERE operation_id=$1`, operationID).Scan(
		&intentID, &restoreID, &revision, &epoch, &owner, &state)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return MySQLRestoreRecoveryInspection{}, err
	}
	if err == nil {
		if intentID != accepted.AcceptanceIntentID || restoreID != signed.RestoreOperationID ||
			revision != signed.CatalogRevision || epoch != signed.RuntimeFenceEpoch || owner != signed.RuntimeFenceOwner {
			return MySQLRestoreRecoveryInspection{}, ErrMySQLRestoreFence
		}
		inspection.IntentState = state
	}
	if _, err := db.AssessCompletedMySQLRestoreLiveSource(ctx, acceptance, signed.RestoreOperationID, observer, secrets); err == nil {
		inspection.SourceVerified = true
	}
	resolved, err := db.resolvedMySQLRecoveryTarget(ctx, ready)
	if err != nil {
		return MySQLRestoreRecoveryInspection{}, err
	}
	binding, err := database.MySQLRestoreBinding(resolved)
	if err != nil {
		return MySQLRestoreRecoveryInspection{}, err
	}
	if err := database.VerifyMySQLRestoreTarget(ctx, binding, secrets, ready.Request.Artifact.Expectation); err == nil {
		inspection.TargetDataVerified = true
	}
	locked := database.InspectMySQLRuntimeAccountLockForRestore(ctx, resolved, ready.Request.Maintenance, secrets) == nil
	unlocked := database.InspectMySQLRuntimeAccountUnlockedForRecovery(ctx, resolved, ready.Request.Maintenance, secrets) == nil
	if locked && !unlocked {
		inspection.TargetAccountState = "locked"
	} else if unlocked && !locked {
		inspection.TargetAccountState = "unlocked"
	}
	return inspection, nil
}
