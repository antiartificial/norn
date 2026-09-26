package store

import (
	"context"
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
}

func (db *DB) InspectPrivateMySQLSourceSnapshot(ctx context.Context, acceptance *PGOperationStore, operationID string) (MySQLSourceSnapshotInspection, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || operationID == "" {
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
		return MySQLSourceSnapshotInspection{}, ErrMySQLSourceSnapshotInspection
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
