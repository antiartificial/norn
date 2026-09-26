package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

var ErrMySQLRestoreInspectionNotFound = errors.New("MySQL restore inspection not found")

// MySQLRestoreInspection is a read-only, secret-free projection of an
// ambiguous restore. The acceptance fields are returned only after the signed
// acceptance has been verified. ArtifactPath and catalog contents are never
// disclosed.
type MySQLRestoreInspection struct {
	OperationID        string                    `json:"operationId"`
	OperationKind      string                    `json:"operationKind"`
	OperationStatus    model.OperationStatus     `json:"operationStatus"`
	IntentState        string                    `json:"intentState"`
	ManualRecovery     bool                      `json:"manualRecoveryRequired"`
	AcceptanceIntentID string                    `json:"acceptanceIntentId"`
	AcceptanceSchema   string                    `json:"acceptanceSchema"`
	CanonicalDigest    string                    `json:"canonicalDigest"`
	Signature          AcceptanceSignature       `json:"signature"`
	CatalogRevision    int64                     `json:"catalogRevision"`
	CatalogDigest      string                    `json:"catalogDigest"`
	Target             database.TargetIdentity   `json:"target"`
	Artifact           database.MySQLSQLArtifact `json:"artifact"`
	StartedAt          time.Time                 `json:"startedAt"`
}

// InspectMySQLRestore verifies the signed operation before returning its
// durable catalog, target, and artifact identities. Only executing or
// needs-inspection rows are visible through this operator path.
func (s *PGOperationStore) InspectMySQLRestore(ctx context.Context, operationID string) (MySQLRestoreInspection, error) {
	accepted, err := s.VerifyAcceptedOperation(ctx, operationID)
	if err != nil {
		return MySQLRestoreInspection{}, err
	}
	if accepted.Operation.Kind != MySQLRestoreOperationKind || accepted.Operation.MaxAttempts != 1 {
		return MySQLRestoreInspection{}, ErrMySQLRestoreInspectionNotFound
	}
	var request MySQLRestoreRequest
	if err := decodeMySQLRestorePayload(accepted.Operation.Payload, &request); err != nil {
		return MySQLRestoreInspection{}, ErrMySQLRestoreFence
	}
	inspection := MySQLRestoreInspection{
		OperationID: operationID, OperationKind: accepted.Operation.Kind,
		OperationStatus: accepted.Operation.Status, AcceptanceIntentID: accepted.AcceptanceIntentID,
		AcceptanceSchema: accepted.Intent.Schema, CanonicalDigest: accepted.Intent.CanonicalDigest,
		Signature: accepted.Intent.Signature, CatalogRevision: request.CatalogRevision,
		Target: request.Target, Artifact: request.Artifact,
	}
	var savedTarget, savedArtifact []byte
	err = s.db.Pool.QueryRow(ctx, `SELECT o.status, i.state,
		COALESCE(o.metadata->>'manualRecoveryRequired'='true',false), c.catalog_digest,
		i.target, i.artifact, i.started_at
		FROM mysql_restore_intents i
		JOIN operations o ON o.id=i.operation_id
		JOIN database_catalog_revisions c ON c.revision=i.catalog_revision
		WHERE i.operation_id=$1 AND i.acceptance_intent_id=$2
		  AND i.catalog_revision=$3 AND i.state IN ('executing','needs-inspection')`,
		operationID, accepted.AcceptanceIntentID, request.CatalogRevision).Scan(
		&inspection.OperationStatus, &inspection.IntentState, &inspection.ManualRecovery, &inspection.CatalogDigest,
		&savedTarget, &savedArtifact, &inspection.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MySQLRestoreInspection{}, ErrMySQLRestoreInspectionNotFound
	}
	if err != nil {
		return MySQLRestoreInspection{}, err
	}
	var target database.TargetIdentity
	var artifact database.MySQLSQLArtifact
	if json.Unmarshal(savedTarget, &target) != nil || json.Unmarshal(savedArtifact, &artifact) != nil || target != request.Target || artifact != request.Artifact {
		return MySQLRestoreInspection{}, ErrMySQLRestoreFence
	}
	return inspection, nil
}
