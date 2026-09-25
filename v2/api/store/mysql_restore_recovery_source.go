package store

import (
	"context"
	"encoding/json"
	"strconv"

	"norn/v2/api/nomad"
)

type MySQLSourceStoppedObserver interface {
	ObserveStoppedMySQLSourceJob(context.Context, nomad.CASStopJobRequest) error
}

// AssessCompletedMySQLRestoreStoppedSource ties the signed source artifact
// back to the original stop and account-lock checkpoints, then reobserves
// Nomad's exact stopped revision. It has no external mutation authority.
func (db *DB) AssessCompletedMySQLRestoreStoppedSource(ctx context.Context, acceptance *PGOperationStore, operationID string, observer MySQLSourceStoppedObserver) (MySQLRestoreRecoveryReadiness, error) {
	if observer == nil {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	ready, err := db.AssessCompletedMySQLRestoreRecovery(ctx, acceptance, operationID)
	if err != nil {
		return MySQLRestoreRecoveryReadiness{}, err
	}
	sourceID := ready.Request.SourceArtifact.OperationID
	signedReceipt, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptance, sourceID)
	if err != nil || signedReceipt.SHA256 != ready.Request.SourceArtifact.ReceiptSHA256 ||
		signedReceipt.Receipt.Source != ready.Request.Artifact.Source || signedReceipt.Receipt.CatalogRevision != ready.Request.CatalogRevision {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, sourceID)
	if err != nil || accepted.Operation.Kind != MySQLSourceSnapshotOperationKind || accepted.Operation.MaxAttempts != 1 {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	encoded, err := json.Marshal(accepted.Operation.Payload)
	var source MySQLSourceSnapshotRequest
	if err != nil || decodeStrictAcceptanceJSON(encoded, &source) != nil || !validMySQLSourceSnapshotRequest(source) ||
		source.CatalogRevision != ready.Request.CatalogRevision || source.Source != ready.Request.Artifact.Source {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	var state, intentID, profileID, logicalID, fenceOwner string
	var revision, fenceEpoch int64
	var savedSource, savedMaintenance, savedJob []byte
	var stopped, locked bool
	err = db.Pool.QueryRow(ctx, `SELECT state,acceptance_intent_id,catalog_revision,profile_id,logical_id,
		source,maintenance,job_identity,runtime_fence_epoch,runtime_fence_owner,
		stop_proved_at IS NOT NULL,lock_proved_at IS NOT NULL
		FROM mysql_source_snapshot_intents WHERE operation_id=$1`, sourceID).Scan(
		&state, &intentID, &revision, &profileID, &logicalID, &savedSource, &savedMaintenance, &savedJob,
		&fenceEpoch, &fenceOwner, &stopped, &locked)
	sourceJSON, _ := json.Marshal(source.Source)
	maintenanceJSON, _ := json.Marshal(source.Maintenance)
	jobJSON, _ := json.Marshal(source.JobIdentity)
	if err != nil || (state != "stage-proved" && state != "retained-proved") || intentID != accepted.AcceptanceIntentID ||
		revision != source.CatalogRevision || profileID != source.ProfileID || logicalID != source.LogicalID ||
		!sameJSON(savedSource, sourceJSON) || !sameJSON(savedMaintenance, maintenanceJSON) || !sameJSON(savedJob, jobJSON) ||
		!stopped || !locked || fenceEpoch != ready.Fence.Epoch || fenceOwner != "mysql-source-snapshot:"+sourceID {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	version, versionErr := strconv.ParseUint(source.JobIdentity.JobVersion, 10, 64)
	index, indexErr := strconv.ParseUint(source.JobIdentity.JobModifyIndex, 10, 64)
	if versionErr != nil || indexErr != nil {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	job := source.JobIdentity
	if err := observer.ObserveStoppedMySQLSourceJob(ctx, nomad.CASStopJobRequest{
		JobID: job.JobID, Region: job.NomadRegion, JobVersion: version, JobModifyIndex: index,
		AllocationIDs: append([]string(nil), job.AllocationIDs...), DeploymentID: job.DeploymentID,
		SpecDigest: job.SpecDigest, DatabaseBindingSchema: job.DatabaseBindingSchema,
		DatabaseBindingSHA256: job.DatabaseBindingSHA256, DatabaseCatalogRevision: job.DatabaseCatalogRevision,
	}); err != nil {
		return MySQLRestoreRecoveryReadiness{}, err
	}
	latest, err := db.AssessCompletedMySQLRestoreRecovery(ctx, acceptance, operationID)
	if err != nil || latest.Fence != ready.Fence || latest.Request != ready.Request {
		return MySQLRestoreRecoveryReadiness{}, ErrMySQLRestoreFence
	}
	return latest, nil
}
