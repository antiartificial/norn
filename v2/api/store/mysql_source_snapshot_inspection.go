package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

var ErrMySQLSourceSnapshotInspection = errors.New("MySQL source snapshot inspection cannot establish signed state")

// MySQLSourceSnapshotInspection is a read-only projection of the durable
// source boundary. It intentionally omits credentials, SQL paths and artifact
// locations. External Nomad, MySQL and object-store state require separate
// observation before any operator reconciliation.
type MySQLSourceSnapshotInspection struct {
	OperationID              string                `json:"operationId"`
	OperationStatus          model.OperationStatus `json:"operationStatus"`
	IntentState              string                `json:"intentState"`
	ClaimLeaseCurrent        bool                  `json:"claimLeaseCurrent"`
	RuntimeFenceHeld         bool                  `json:"runtimeFenceHeld"`
	StageReceiptVerified     bool                  `json:"stageReceiptVerified"`
	RetentionReceiptVerified bool                  `json:"retentionReceiptVerified"`
	ReconciledByOperationID  string                `json:"reconciledByOperationId,omitempty"`
}

func (db *DB) InspectPrivateMySQLSourceSnapshot(ctx context.Context, acceptance *PGOperationStore, operationID string) (MySQLSourceSnapshotInspection, error) {
	return db.inspectPrivateMySQLSourceSnapshot(ctx, acceptance, operationID, 0)
}

func (db *DB) inspectPrivateMySQLSourceSnapshot(ctx context.Context, acceptance *PGOperationStore,
	operationID string, depth int) (MySQLSourceSnapshotInspection, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || operationID == "" {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	if depth > 32 {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, operationID)
	if err != nil {
		return MySQLSourceSnapshotInspection{}, err
	}
	if accepted.Operation.Kind != MySQLSourceSnapshotOperationKind || accepted.Operation.MaxAttempts != 1 {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	request, err := sourceSnapshotRequestFromAccepted(accepted)
	if err != nil {
		return MySQLSourceSnapshotInspection{}, errors.Join(ErrMySQLSourceSnapshotInspection, err)
	}
	result := MySQLSourceSnapshotInspection{OperationID: operationID}
	var revision int64
	var profileID, logicalID, dumpDigest string
	var source, maintenance, job []byte
	err = db.Pool.QueryRow(ctx, `SELECT o.status,i.state,i.catalog_revision,i.profile_id,i.logical_id,
		i.source,i.maintenance,i.job_identity,i.dump_tool_sha256,
		(o.status='running' AND o.locked_by<>'' AND o.locked_until>clock_timestamp()),
		coalesce(f.active AND f.owner=('mysql-source-snapshot:' || i.operation_id)
		 AND i.runtime_fence_epoch=f.epoch AND i.runtime_fence_owner=f.owner,false)
		FROM mysql_source_snapshot_intents i
		JOIN operations o ON o.id=i.operation_id
		CROSS JOIN runtime_mutation_fence f
		WHERE i.operation_id=$1 AND i.acceptance_intent_id=$2 AND f.singleton=true`,
		operationID, accepted.AcceptanceIntentID).Scan(&result.OperationStatus, &result.IntentState,
		&revision, &profileID, &logicalID, &source, &maintenance, &job, &dumpDigest,
		&result.ClaimLeaseCurrent, &result.RuntimeFenceHeld)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.inspectReconciledMySQLSourceSnapshot(ctx, acceptance, accepted, request, depth)
	}
	if err != nil {
		return MySQLSourceSnapshotInspection{}, err
	}
	wantSource, _ := json.Marshal(request.Source)
	wantMaintenance, _ := json.Marshal(request.Maintenance)
	wantJob, _ := json.Marshal(request.JobIdentity)
	if revision != request.CatalogRevision || profileID != request.ProfileID || logicalID != request.LogicalID ||
		!sameJSON(source, wantSource) || !sameJSON(maintenance, wantMaintenance) || !sameJSON(job, wantJob) ||
		dumpDigest != request.DumpToolSHA256 {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	if result.OperationStatus == model.OperationSucceeded && result.IntentState != "retained-proved" {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	if result.IntentState == "stage-proved" || result.IntentState == "publish-intended" || result.IntentState == "retained-proved" {
		if _, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptance, operationID); err != nil {
			return MySQLSourceSnapshotInspection{}, errors.Join(ErrMySQLSourceSnapshotInspection, err)
		}
		result.StageReceiptVerified = true
	}
	if result.IntentState == "retained-proved" {
		if _, err := db.LoadSignedMySQLSourceArtifactRetentionReceipt(ctx, acceptance, operationID); err != nil {
			return MySQLSourceSnapshotInspection{}, errors.Join(ErrMySQLSourceSnapshotInspection, err)
		}
		result.RetentionReceiptVerified = true
	}
	return result, nil
}

func (db *DB) inspectReconciledMySQLSourceSnapshot(ctx context.Context, acceptance *PGOperationStore,
	prior AcceptedOperation, request MySQLSourceSnapshotRequest, depth int) (MySQLSourceSnapshotInspection, error) {
	var successorID, checkpoint, digest, algorithm, keyID, signature string
	var archived, canonical []byte
	if err := db.Pool.QueryRow(ctx, `SELECT successor_operation_id,prior_intent,checkpoint,proof_canonical,
		proof_sha256,proof_signing_algorithm,proof_signing_key_id,proof_signature
		FROM mysql_source_snapshot_reconciliations WHERE prior_operation_id=$1`, prior.Operation.ID).Scan(
		&successorID, &archived, &checkpoint, &canonical, &digest, &algorithm, &keyID, &signature); err != nil {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	hash := sha256.Sum256(canonical)
	if hex.EncodeToString(hash[:]) != digest || acceptance.signer.Verify(ctx,
		AcceptanceSignature{Algorithm: algorithm, KeyID: keyID, Value: signature}, canonical) != nil {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	var proof MySQLSourceReconciliationProof
	if json.Unmarshal(canonical, &proof) != nil || proof.Schema != MySQLSourceReconciliationProofSchema ||
		proof.PriorSourceOperationID != prior.Operation.ID || proof.PriorSourceDigest != prior.Intent.CanonicalDigest ||
		proof.SuccessorOperationID != successorID || proof.Checkpoint != checkpoint || !proof.NomadStoppedVerified ||
		(mysqlSourceLockCheckpoint(checkpoint) && !proof.RuntimeAccountLocked) ||
		!mysqlSourceReconciliationCheckpoint(checkpoint) ||
		prior.Operation.Status != model.OperationFailed {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	successor, err := acceptance.VerifyAcceptedOperation(ctx, successorID)
	if err != nil || successor.Operation.Kind != MySQLSourceSnapshotOperationKind ||
		successor.Operation.Source != "private-mysql-source-reconciliation" ||
		successor.Intent.CanonicalDigest != proof.SuccessorDigest ||
		!sameMySQLSourceSnapshotPayload(successor.Operation.Payload, request) {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	var row map[string]json.RawMessage
	if json.Unmarshal(archived, &row) != nil {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	var archivedID, intentID, state string
	var archivedOwner, sourceKey string
	var archivedEpoch int64
	if json.Unmarshal(row["operation_id"], &archivedID) != nil || archivedID != prior.Operation.ID ||
		json.Unmarshal(row["acceptance_intent_id"], &intentID) != nil || intentID != prior.AcceptanceIntentID ||
		json.Unmarshal(row["state"], &state) != nil || state != checkpoint ||
		json.Unmarshal(row["source_key"], &sourceKey) != nil || sourceKey == "" ||
		json.Unmarshal(row["runtime_fence_epoch"], &archivedEpoch) != nil || archivedEpoch != proof.RuntimeFenceEpoch ||
		json.Unmarshal(row["runtime_fence_owner"], &archivedOwner) != nil || archivedOwner != proof.RuntimeFenceOwner ||
		len(row["source"]) == 0 || len(row["maintenance"]) == 0 || len(row["job_identity"]) == 0 {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	wantSource, _ := json.Marshal(request.Source)
	wantMaintenance, _ := json.Marshal(request.Maintenance)
	wantJob, _ := json.Marshal(request.JobIdentity)
	if !sameJSON(row["source"], wantSource) || !sameJSON(row["maintenance"], wantMaintenance) ||
		!sameJSON(row["job_identity"], wantJob) {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	var active bool
	var epoch int64
	var activeID string
	if err := db.Pool.QueryRow(ctx, `SELECT i.operation_id,f.active,f.epoch FROM mysql_source_snapshot_intents i
		CROSS JOIN runtime_mutation_fence f WHERE i.source_key=$1 AND f.singleton=true`, sourceKey).Scan(
		&activeID, &active, &epoch); err != nil || !active || epoch != proof.RuntimeFenceEpoch {
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
	}
	if activeID != successorID {
		later, err := db.inspectPrivateMySQLSourceSnapshot(ctx, acceptance, successorID, depth+1)
		if err != nil || later.ReconciledByOperationID == "" || !later.RuntimeFenceHeld {
			return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
		}
	}
	return MySQLSourceSnapshotInspection{OperationID: prior.Operation.ID, OperationStatus: prior.Operation.Status,
		IntentState: checkpoint, ReconciledByOperationID: successorID,
		RuntimeFenceHeld: true}, nil
}
