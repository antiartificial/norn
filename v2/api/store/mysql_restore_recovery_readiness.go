package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// MySQLRestoreRecoveryReadiness is a control-plane prerequisite for an
// operator-observed resume. It does not prove the MySQL account state or the
// target's current contents and grants no authority to unlock either account.
type MySQLRestoreRecoveryReadiness struct {
	OperationID string
	Fence       RuntimeMutationFence
	Request     MySQLRestoreRequest
}

// AssessCompletedMySQLRestoreRecovery verifies the signed acceptance and
// durable completed lineage. An ambiguous, stale, or replaced restore cannot
// be presented as a completed recovery candidate.
func (db *DB) AssessCompletedMySQLRestoreRecovery(ctx context.Context, acceptance *PGOperationStore, operationID string) (MySQLRestoreRecoveryReadiness, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || operationID == "" {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, operationID)
	if err != nil {
		return MySQLRestoreRecoveryReadiness{}, err
	}
	var request MySQLRestoreRequest
	if accepted.Operation.Kind != MySQLRestoreOperationKind || accepted.Operation.Status != model.OperationSucceeded || accepted.Operation.MaxAttempts != 1 ||
		decodeMySQLRestorePayload(accepted.Operation.Payload, &request) != nil || !validMySQLRestoreSourceArtifact(request.SourceArtifact) {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return MySQLRestoreRecoveryReadiness{}, err
	}
	defer tx.Rollback(context.Background())
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Target})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != request.Maintenance {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	var state, intentID, profileID, logicalID, artifactPath, sourceID, receiptSHA, lockState string
	var revision, fenceEpoch, claimGeneration, lockGeneration int64
	var targetJSON, artifactJSON, lockTargetJSON []byte
	var claimOwner, lockOwner string
	var fence RuntimeMutationFence
	var fenceActive bool
	err = tx.QueryRow(ctx, `SELECT i.state,i.acceptance_intent_id,i.catalog_revision,i.profile_id,i.logical_id,i.target,i.artifact,i.artifact_path,
  m.source_artifact_operation_id,m.source_artifact_receipt_sha256,m.runtime_fence_epoch,m.runtime_fence_claim_owner,m.runtime_fence_claim_generation,
  l.state,l.target,l.claim_owner,l.claim_generation,f.active,f.epoch,f.owner,f.reason,f.held_at
  FROM mysql_restore_intents i
  JOIN mysql_restore_maintenance_fences m ON m.operation_id=i.operation_id
  JOIN mysql_restore_runtime_locks l ON l.operation_id=i.operation_id
  JOIN runtime_mutation_fence f ON f.singleton=true AND f.epoch=m.runtime_fence_epoch
  WHERE i.operation_id=$1 AND i.completed_at IS NOT NULL AND m.runtime_fence_transferred_at IS NOT NULL AND l.verified_at IS NOT NULL`, operationID).Scan(
		&state, &intentID, &revision, &profileID, &logicalID, &targetJSON, &artifactJSON, &artifactPath,
		&sourceID, &receiptSHA, &fenceEpoch, &claimOwner, &claimGeneration,
		&lockState, &lockTargetJSON, &lockOwner, &lockGeneration, &fenceActive, &fence.Epoch, &fence.Owner, &fence.Reason, &fence.HeldAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	if err != nil {
		return MySQLRestoreRecoveryReadiness{}, err
	}
	var target, lockTarget database.TargetIdentity
	var artifact database.MySQLSQLArtifact
	if state != "completed" || lockState != "verified-lock" || intentID != accepted.AcceptanceIntentID || revision != request.CatalogRevision ||
		profileID != request.ProfileID || logicalID != request.LogicalID || artifactPath != request.ArtifactPath ||
		sourceID != request.SourceArtifact.OperationID || receiptSHA != request.SourceArtifact.ReceiptSHA256 ||
		fenceEpoch <= 0 || fenceEpoch != fence.Epoch || !fenceActive || fence.Owner != "mysql-restore:"+operationID ||
		claimOwner == "" || claimGeneration <= 0 || lockOwner != claimOwner || lockGeneration != claimGeneration ||
		json.Unmarshal(targetJSON, &target) != nil || json.Unmarshal(lockTargetJSON, &lockTarget) != nil ||
		json.Unmarshal(artifactJSON, &artifact) != nil || target != request.Target || lockTarget != request.Target || artifact != request.Artifact {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	if err := tx.Commit(ctx); err != nil {
		return MySQLRestoreRecoveryReadiness{}, err
	}
	return MySQLRestoreRecoveryReadiness{OperationID: operationID, Fence: fence, Request: request}, nil
}
