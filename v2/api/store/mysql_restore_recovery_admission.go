package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

const MySQLRestoreRecoveryOperationKind = "database.mysql-restore-recovery"

// MySQLRestoreRecoveryRequest is derived entirely from a verified completed
// restore and its held fence. It is signed before any resume effect is allowed.
type MySQLRestoreRecoveryRequest struct {
	RestoreOperationID        string                  `json:"restoreOperationId"`
	RestoreAcceptanceDigest   string                  `json:"restoreAcceptanceDigest"`
	CatalogRevision           int64                   `json:"catalogRevision"`
	RuntimeFenceEpoch         int64                   `json:"runtimeFenceEpoch"`
	RuntimeFenceOwner         string                  `json:"runtimeFenceOwner"`
	SourceArtifactOperationID string                  `json:"sourceArtifactOperationId"`
	SourceReceiptSHA256       string                  `json:"sourceReceiptSha256"`
	Target                    database.TargetIdentity `json:"target"`
}

type MySQLRestoreRecoveryAcceptanceInput struct {
	RestoreOperationID string
	Actor              OperationActor
	Key                string
	Audit              AcceptanceAuditContext
}

// AcceptPrivateMySQLRestoreRecovery signs the exact completed restore and
// held fence selected by an operator. No account unlock or fence release is
// performed here. An identity replay returns only the original exact request.
func (db *DB) AcceptPrivateMySQLRestoreRecovery(ctx context.Context, acceptance *PGOperationStore, input MySQLRestoreRecoveryAcceptanceInput) (AcceptedOperation, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || strings.TrimSpace(input.RestoreOperationID) == "" ||
		strings.TrimSpace(input.Key) == "" || strings.TrimSpace(input.Actor.Issuer) == "" || strings.TrimSpace(input.Actor.Subject) == "" {
		return AcceptedOperation{}, ErrMySQLRestoreFence
	}
	authority, err := acceptance.Authority(ctx)
	if err != nil {
		return AcceptedOperation{}, err
	}
	identity := OperationRequestIdentity{Authority: authority, Actor: input.Actor, Kind: MySQLRestoreRecoveryOperationKind,
		Resource: "mysql-restore/" + input.RestoreOperationID, Key: input.Key}
	if existing, err := acceptance.ResolveIdentity(ctx, identity); err == nil {
		var request MySQLRestoreRecoveryRequest
		if existing.Operation.Kind != MySQLRestoreRecoveryOperationKind || existing.Operation.MaxAttempts != 1 ||
			decodeMySQLRestoreRecoveryPayload(existing.Operation.Payload, &request) != nil || request.RestoreOperationID != input.RestoreOperationID {
			return AcceptedOperation{}, &AcceptanceConflictError{Identity: identity}
		}
		return existing, nil
	} else if !errors.Is(err, ErrAcceptanceNotFound) {
		return AcceptedOperation{}, err
	}
	ready, err := db.AssessCompletedMySQLRestoreRecovery(ctx, acceptance, input.RestoreOperationID)
	if err != nil {
		return AcceptedOperation{}, err
	}
	restore, err := acceptance.VerifyAcceptedOperation(ctx, input.RestoreOperationID)
	if err != nil {
		return AcceptedOperation{}, err
	}
	request := MySQLRestoreRecoveryRequest{
		RestoreOperationID: input.RestoreOperationID, RestoreAcceptanceDigest: restore.Intent.CanonicalDigest,
		CatalogRevision: ready.Request.CatalogRevision, RuntimeFenceEpoch: ready.Fence.Epoch,
		RuntimeFenceOwner: ready.Fence.Owner, SourceArtifactOperationID: ready.Request.SourceArtifact.OperationID,
		SourceReceiptSHA256: ready.Request.SourceArtifact.ReceiptSHA256, Target: ready.Request.Target,
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return AcceptedOperation{}, err
	}
	var payload map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return AcceptedOperation{}, err
	}
	entry := OperationAcceptance{Identity: identity, Audit: input.Audit,
		Operation: model.Operation{ID: uuid.NewString(), Kind: MySQLRestoreRecoveryOperationKind, Status: model.OperationQueued,
			Risk: "high", Source: "private-mysql-restore-recovery", MaxAttempts: 1, Payload: payload}}
	entry.Fingerprint, err = CanonicalOperationRequestFingerprint(entry)
	if err != nil {
		return AcceptedOperation{}, err
	}
	return acceptance.Accept(ctx, entry)
}

func decodeMySQLRestoreRecoveryPayload(payload map[string]interface{}, request *MySQLRestoreRecoveryRequest) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := decodeStrictAcceptanceJSON(encoded, request); err != nil {
		return err
	}
	if request.RestoreOperationID == "" || request.RestoreAcceptanceDigest == "" || request.CatalogRevision <= 0 ||
		request.RuntimeFenceEpoch <= 0 || request.RuntimeFenceOwner != "mysql-restore:"+request.RestoreOperationID ||
		request.SourceArtifactOperationID == "" || request.SourceReceiptSHA256 == "" || request.Target.Engine != database.EngineMySQL {
		return ErrMySQLRestoreFence
	}
	return nil
}
