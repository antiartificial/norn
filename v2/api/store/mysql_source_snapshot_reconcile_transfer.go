package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

const MySQLSourceReconciliationProofSchema = "norn.mysql-source-reconciliation-proof/v1"

// MySQLSourceReconciliationProof is a signed service attestation of fresh
// external reads. It records the exact accepted identities and observation
// time, not a provider-issued receipt or permission to release the fence.
type MySQLSourceReconciliationProof struct {
	Schema                 string `json:"schema"`
	PriorSourceOperationID string `json:"priorSourceOperationId"`
	PriorSourceDigest      string `json:"priorSourceDigest"`
	SuccessorOperationID   string `json:"successorOperationId"`
	SuccessorDigest        string `json:"successorDigest"`
	Checkpoint             string `json:"checkpoint"`
	RuntimeFenceEpoch      int64  `json:"runtimeFenceEpoch"`
	RuntimeFenceOwner      string `json:"runtimeFenceOwner"`
	NomadStoppedVerified   bool   `json:"nomadStoppedVerified"`
	RuntimeAccountLocked   bool   `json:"runtimeAccountLocked"`
	ObservedAt             string `json:"observedAt"`
}

type MySQLSourceAccountLockInspector interface {
	InspectLocked(context.Context, database.ResolvedBinding, database.MySQLMaintenanceCredentials, database.SecretSource) error
}

type mysqlSourceDatabaseLockInspector struct{}

func (mysqlSourceDatabaseLockInspector) InspectLocked(ctx context.Context, resolved database.ResolvedBinding,
	maintenance database.MySQLMaintenanceCredentials, secrets database.SecretSource) error {
	return database.InspectMySQLRuntimeAccountLockForRestore(ctx, resolved, maintenance, secrets)
}

// ReconcileClaimedMySQLSourceSnapshot observes the exact stopped job and,
// when needed, the locked/drained account before transferring the permanent
// reservation to a signed successor. It never repeats either external effect.
func (db *DB) ReconcileClaimedMySQLSourceSnapshot(ctx context.Context, acceptance *PGOperationStore,
	claim OperationClaim, observer MySQLSourceStoppedObserver, inspector MySQLSourceAccountLockInspector,
	secrets database.SecretSource) error {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || validateOperationClaim(claim) != nil ||
		observer == nil || inspector == nil || secrets == nil {
		return ErrMySQLSourceSnapshotFence
	}
	successor, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil || successor.Operation.Kind != MySQLSourceSnapshotOperationKind ||
		successor.Operation.Status != model.OperationRunning || successor.Operation.MaxAttempts != 1 ||
		successor.Operation.Source != "private-mysql-source-reconciliation" {
		return ErrMySQLSourceSnapshotFence
	}
	request, err := sourceSnapshotRequestFromAccepted(successor)
	if err != nil {
		return ErrMySQLSourceSnapshotFence
	}
	var link MySQLSourceReconciliationLink
	if decodeMySQLSourceReconciliationLink(successor.Operation.Metadata, &link) != nil {
		return ErrMySQLSourceSnapshotFence
	}
	epoch, _ := strconv.ParseInt(link.RuntimeFenceEpoch, 10, 64)
	prior, err := acceptance.VerifyAcceptedOperation(ctx, link.PriorSourceOperationID)
	if err != nil || prior.Operation.Kind != MySQLSourceSnapshotOperationKind || prior.Operation.Status != model.OperationFailed ||
		prior.Operation.MaxAttempts != 1 || prior.Operation.Metadata["manualRecoveryRequired"] != true ||
		prior.Intent.CanonicalDigest != link.PriorSourceDigest || !sameMySQLSourceSnapshotPayload(prior.Operation.Payload, request) {
		return ErrMySQLSourceSnapshotFence
	}
	priorInspection, err := db.InspectPrivateMySQLSourceSnapshot(ctx, acceptance, prior.Operation.ID)
	if err != nil {
		return ErrMySQLSourceSnapshotFence
	}
	if priorInspection.ReconciledByOperationID != "" {
		if priorInspection.ReconciledByOperationID != claim.OperationID() || !priorInspection.RuntimeFenceHeld {
			return ErrMySQLSourceSnapshotFence
		}
		heldBySuccessor, err := db.sourceRuntimeFenceHeld(ctx, claim)
		if err != nil || !heldBySuccessor {
			return ErrMySQLSourceSnapshotFence
		}
		var liveClaim bool
		if err := db.Pool.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND status='running'
			AND locked_by=$2 AND lock_generation=$3 AND locked_until>clock_timestamp()`,
			claim.OperationID(), claim.OwnerID(), claim.Generation()).Scan(&liveClaim); err != nil || !liveClaim {
			return ownershipLost(claim)
		}
		return nil
	}
	if priorInspection.IntentState != link.Checkpoint || !priorInspection.RuntimeFenceHeld {
		return ErrMySQLSourceSnapshotFence
	}
	active, err := db.ActiveDatabaseCatalog(ctx)
	if err != nil || active.Revision != request.CatalogRevision {
		return ErrMySQLSourceSnapshotFence
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return ErrMySQLSourceSnapshotFence
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID,
		Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Source})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != request.Maintenance {
		return ErrMySQLSourceSnapshotFence
	}
	job := request.JobIdentity
	version, _ := strconv.ParseUint(job.JobVersion, 10, 64)
	index, _ := strconv.ParseUint(job.JobModifyIndex, 10, 64)
	if err := observer.ObserveStoppedMySQLSourceJob(ctx, nomad.CASStopJobRequest{
		JobID: job.JobID, Region: job.NomadRegion, JobVersion: version, JobModifyIndex: index,
		AllocationIDs: append([]string(nil), job.AllocationIDs...), DeploymentID: job.DeploymentID,
		SpecDigest: job.SpecDigest, DatabaseBindingSchema: job.DatabaseBindingSchema,
		DatabaseBindingSHA256: job.DatabaseBindingSHA256, DatabaseCatalogRevision: job.DatabaseCatalogRevision,
	}); err != nil {
		return errors.Join(ErrMySQLSourceStopIndeterminate, err)
	}
	locked := link.Checkpoint == "lock-intended"
	if locked {
		if err := inspector.InspectLocked(ctx, resolved, request.Maintenance, secrets); err != nil {
			return errors.Join(ErrMySQLSourceAccountLockIndeterminate, err)
		}
	}
	proof := MySQLSourceReconciliationProof{Schema: MySQLSourceReconciliationProofSchema,
		PriorSourceOperationID: link.PriorSourceOperationID, PriorSourceDigest: link.PriorSourceDigest,
		SuccessorOperationID: claim.OperationID(), SuccessorDigest: successor.Intent.CanonicalDigest,
		Checkpoint: link.Checkpoint, RuntimeFenceEpoch: epoch, RuntimeFenceOwner: link.RuntimeFenceOwner,
		NomadStoppedVerified: true, RuntimeAccountLocked: locked, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	canonical, err := json.Marshal(proof)
	if err != nil {
		return err
	}
	signature, err := acceptance.signer.Sign(ctx, canonical)
	if err != nil || signature.Algorithm == "" || signature.KeyID == "" || signature.Value == "" ||
		acceptance.signer.Verify(ctx, signature, canonical) != nil {
		return ErrMySQLSourceSnapshotFence
	}
	return db.transferClaimedMySQLSourceReconciliation(ctx, acceptance, claim, successor, prior, request, proof, canonical, signature)
}

func (db *DB) transferClaimedMySQLSourceReconciliation(ctx context.Context, acceptance *PGOperationStore,
	claim OperationClaim, successor, prior AcceptedOperation, request MySQLSourceSnapshotRequest,
	proof MySQLSourceReconciliationProof, canonical []byte, signature AcceptanceSignature) error {
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
		AND source='private-mysql-source-reconciliation' AND locked_by=$3 AND lock_generation=$4
		AND locked_until>clock_timestamp() FOR UPDATE`, claim.OperationID(), MySQLSourceSnapshotOperationKind,
		claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return ownershipLost(claim)
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return ErrMySQLSourceSnapshotFence
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return ErrMySQLSourceSnapshotFence
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID,
		Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Source})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != request.Maintenance {
		return ErrMySQLSourceSnapshotFence
	}
	var priorStatus, checkpoint, intentID, sourceKey, fenceOwner, liveOwner string
	var manual, fenceActive, stopIntended, stopProved, lockIntended bool
	var fenceEpoch, liveEpoch, revision int64
	var sourceJSON, maintenanceJSON, jobJSON, priorIntent []byte
	err = tx.QueryRow(ctx, `SELECT p.status,COALESCE((p.metadata->>'manualRecoveryRequired')::boolean,false),
		i.state,i.acceptance_intent_id,i.catalog_revision,i.source_key,i.source,i.maintenance,i.job_identity,
		i.stop_intended_at IS NOT NULL,i.stop_proved_at IS NOT NULL,i.lock_intended_at IS NOT NULL,
		i.runtime_fence_epoch,i.runtime_fence_owner,f.active,f.epoch,f.owner,to_jsonb(i)
		FROM mysql_source_snapshot_intents i JOIN operations p ON p.id=i.operation_id
		CROSS JOIN runtime_mutation_fence f WHERE i.operation_id=$1 AND f.singleton=true
		FOR UPDATE OF p,i,f`, prior.Operation.ID).Scan(&priorStatus, &manual, &checkpoint, &intentID,
		&revision, &sourceKey, &sourceJSON, &maintenanceJSON, &jobJSON,
		&stopIntended, &stopProved, &lockIntended, &fenceEpoch, &fenceOwner, &fenceActive, &liveEpoch, &liveOwner, &priorIntent)
	wantSource, _ := json.Marshal(request.Source)
	wantMaintenance, _ := json.Marshal(request.Maintenance)
	wantJob, _ := json.Marshal(request.JobIdentity)
	if err != nil || priorStatus != "failed" || !manual || checkpoint != proof.Checkpoint ||
		intentID != prior.AcceptanceIntentID || revision != request.CatalogRevision ||
		sourceKey != mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Source) ||
		!sameJSON(sourceJSON, wantSource) || !sameJSON(maintenanceJSON, wantMaintenance) || !sameJSON(jobJSON, wantJob) ||
		!stopIntended || (checkpoint == "lock-intended" && (!stopProved || !lockIntended)) ||
		!fenceActive || fenceEpoch != proof.RuntimeFenceEpoch || liveEpoch != fenceEpoch ||
		fenceOwner != proof.RuntimeFenceOwner || liveOwner != fenceOwner {
		return ErrMySQLSourceSnapshotFence
	}
	if request.RuntimeLaunchReservationID != "" && checkpoint == "stop-intended" {
		if err := stopObservedMySQLSourceRuntimeLaunch(ctx, tx, request); err != nil {
			return err
		}
	}
	digest := sha256.Sum256(canonical)
	if updated, err := tx.Exec(ctx, `INSERT INTO mysql_source_snapshot_reconciliations
		(prior_operation_id,successor_operation_id,prior_intent,checkpoint,proof_canonical,proof_sha256,
		proof_signing_algorithm,proof_signing_key_id,proof_signature)
		VALUES ($1,$2,$3::jsonb,$4,$5,$6,$7,$8,$9)`, prior.Operation.ID, claim.OperationID(),
		string(priorIntent), checkpoint, canonical, hex.EncodeToString(digest[:]), signature.Algorithm,
		signature.KeyID, signature.Value); err != nil || updated.RowsAffected() != 1 {
		return ErrMySQLSourceSnapshotFence
	}
	successorOwner := "mysql-source-snapshot:" + claim.OperationID()
	state := "stop-proved"
	if checkpoint == "lock-intended" {
		state = "lock-proved"
	}
	if updated, err := tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET operation_id=$2,
		acceptance_intent_id=$3,state=$4,runtime_fence_owner=$5,
		stop_proved_at=COALESCE(stop_proved_at,clock_timestamp()),
		lock_proved_at=CASE WHEN $4='lock-proved' THEN clock_timestamp() ELSE lock_proved_at END
		WHERE operation_id=$1 AND state=$6 AND runtime_fence_epoch=$7 AND runtime_fence_owner=$8`,
		prior.Operation.ID, claim.OperationID(), successor.AcceptanceIntentID, state, successorOwner,
		checkpoint, fenceEpoch, fenceOwner); err != nil || updated.RowsAffected() != 1 {
		return ErrMySQLSourceSnapshotFence
	}
	if updated, err := tx.Exec(ctx, `UPDATE runtime_mutation_fence SET owner=$1,
		reason='reconciled MySQL source snapshot; source remains fenced'
		WHERE singleton=true AND active=true AND epoch=$2 AND owner=$3`, successorOwner, fenceEpoch, fenceOwner); err != nil || updated.RowsAffected() != 1 {
		return ErrMySQLSourceSnapshotFence
	}
	if updated, err := tx.Exec(ctx, `UPDATE operations SET metadata=metadata || jsonb_build_object('reconciledByOperationId',$2::text),
		updated_at=clock_timestamp() WHERE id=$1 AND status='failed'`, prior.Operation.ID, claim.OperationID()); err != nil || updated.RowsAffected() != 1 {
		return ErrMySQLSourceSnapshotFence
	}
	if updated, err := tx.Exec(ctx, `UPDATE operations SET metadata=metadata || jsonb_build_object('mysqlSourceReconciliationProofSha256',$2::text),
		updated_at=clock_timestamp() WHERE id=$1 AND status='running' AND locked_by=$3 AND lock_generation=$4`,
		claim.OperationID(), hex.EncodeToString(digest[:]), claim.OwnerID(), claim.Generation()); err != nil || updated.RowsAffected() != 1 {
		return ErrMySQLSourceSnapshotFence
	}
	return tx.Commit(ctx)
}
