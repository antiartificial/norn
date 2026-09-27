package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// MySQLRestoreAcceptanceInput selects an already retained source and a named
// catalog destination. The artifact bytes, path, maintenance credentials and
// physical target identity are never accepted from the operator.
type MySQLRestoreAcceptanceInput struct {
	SourceOperationID string
	ProfileID         string
	LogicalID         string
	ExpectedDatabase  string
	Actor             OperationActor
	Key               string
	Audit             AcceptanceAuditContext
}

// AcceptPrivateMySQLRestore signs the exact retained source receipt and
// catalog-resolved destination while the source's mutation fence is held.
// It performs no MySQL write; the separate restore command must claim and
// reverify this operation before any import effect.
func (db *DB) AcceptPrivateMySQLRestore(ctx context.Context, acceptance *PGOperationStore, input MySQLRestoreAcceptanceInput) (AcceptedOperation, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db ||
		strings.TrimSpace(input.SourceOperationID) == "" || strings.TrimSpace(input.ProfileID) == "" ||
		strings.TrimSpace(input.LogicalID) == "" || strings.TrimSpace(input.ExpectedDatabase) == "" ||
		strings.TrimSpace(input.Actor.Issuer) == "" || strings.TrimSpace(input.Actor.Subject) == "" || strings.TrimSpace(input.Key) == "" {
		return AcceptedOperation{}, ErrMySQLRestoreFence
	}
	if _, err := uuid.Parse(input.SourceOperationID); err != nil {
		return AcceptedOperation{}, ErrMySQLRestoreFence
	}
	authority, err := acceptance.Authority(ctx)
	if err != nil {
		return AcceptedOperation{}, err
	}
	identity := OperationRequestIdentity{Authority: authority, Actor: input.Actor, Kind: MySQLRestoreOperationKind,
		Resource: "mysql-restore/" + input.ProfileID + "/" + input.LogicalID + "/" + input.ExpectedDatabase, Key: input.Key}
	if existing, err := acceptance.ResolveIdentity(ctx, identity); err == nil {
		var request MySQLRestoreRequest
		if existing.Operation.Kind != MySQLRestoreOperationKind || existing.Operation.MaxAttempts != 1 ||
			decodeMySQLRestorePayload(existing.Operation.Payload, &request) != nil ||
			request.SourceArtifact.OperationID != input.SourceOperationID || request.ProfileID != input.ProfileID ||
			request.LogicalID != input.LogicalID || request.Target.Database != input.ExpectedDatabase {
			return AcceptedOperation{}, &AcceptanceConflictError{Identity: identity}
		}
		return existing, nil
	} else if !errors.Is(err, ErrAcceptanceNotFound) {
		return AcceptedOperation{}, err
	}
	stage, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptance, input.SourceOperationID)
	if err != nil {
		return AcceptedOperation{}, err
	}
	retained, err := db.LoadSignedMySQLSourceArtifactRetentionReceipt(ctx, acceptance, input.SourceOperationID)
	if err != nil || retained.Receipt.StagingReceiptSHA256 != stage.SHA256 {
		return AcceptedOperation{}, ErrMySQLRestoreFence
	}
	active, err := db.ActiveDatabaseCatalog(ctx)
	if err != nil || active.Revision != stage.Receipt.CatalogRevision {
		return AcceptedOperation{}, ErrMySQLRestoreFence
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return AcceptedOperation{}, ErrMySQLRestoreFence
	}
	target, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: input.ProfileID,
		Purpose: database.PurposeApplication, LogicalResourceID: input.LogicalID})
	if err != nil || target.MySQLMaintenance == nil || target.Target.Database != input.ExpectedDatabase ||
		mysqlRuntimePhysicalKeyForCatalog(active.Catalog, stage.Receipt.Source) == mysqlRuntimePhysicalKeyForCatalog(active.Catalog, target.Target) {
		return AcceptedOperation{}, ErrMySQLRestoreFence
	}
	request := MySQLRestoreRequest{CatalogRevision: active.Revision, ProfileID: input.ProfileID,
		LogicalID: input.LogicalID, Target: target.Target, Maintenance: *target.MySQLMaintenance,
		Artifact: stage.Receipt.Artifact, ArtifactPath: stage.Receipt.ArtifactPath,
		SourceArtifact: MySQLRestoreSourceArtifact{OperationID: input.SourceOperationID, ReceiptSHA256: stage.SHA256}}
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
		Operation: model.Operation{ID: uuid.NewString(), Kind: MySQLRestoreOperationKind, App: input.ExpectedDatabase,
			Ref: "mysql/" + input.ExpectedDatabase, Status: model.OperationQueued, Risk: "high",
			Source: "private-mysql-restore", MaxAttempts: 1, Payload: payload}}
	entry.Fingerprint, err = CanonicalOperationRequestFingerprint(entry)
	if err != nil {
		return AcceptedOperation{}, err
	}
	return acceptance.acceptWithGuard(ctx, entry, nil, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockMySQLCatalogGate(ctx, tx); err != nil {
			return err
		}
		current, err := loadActiveDatabaseCatalog(ctx, tx)
		if err != nil || current.Revision != request.CatalogRevision {
			return ErrMySQLRestoreFence
		}
		currentResolver, err := database.NewResolver(current.Catalog)
		if err != nil {
			return ErrMySQLRestoreFence
		}
		currentTarget, err := currentResolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID,
			Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Target})
		if err != nil || currentTarget.MySQLMaintenance == nil || *currentTarget.MySQLMaintenance != request.Maintenance ||
			mysqlRuntimePhysicalKeyForCatalog(current.Catalog, request.Artifact.Source) == mysqlRuntimePhysicalKeyForCatalog(current.Catalog, request.Target) {
			return ErrMySQLRestoreFence
		}
		var fenceActive bool
		var fenceOwner string
		var fenceEpoch int64
		if err := tx.QueryRow(ctx, `SELECT active,epoch,owner FROM runtime_mutation_fence WHERE singleton=true FOR SHARE`).Scan(&fenceActive, &fenceEpoch, &fenceOwner); err != nil || !fenceActive || fenceEpoch <= 0 || fenceOwner != "mysql-source-snapshot:"+input.SourceOperationID {
			return ErrMySQLRestoreFence
		}
		var sourceState string
		var sourceEpoch int64
		var stopped, locked bool
		if err := tx.QueryRow(ctx, `SELECT state,runtime_fence_epoch,stop_proved_at IS NOT NULL,lock_proved_at IS NOT NULL
			FROM mysql_source_snapshot_intents WHERE operation_id=$1 FOR SHARE`, input.SourceOperationID).Scan(&sourceState, &sourceEpoch, &stopped, &locked); err != nil ||
			sourceState != "retained-proved" || sourceEpoch != fenceEpoch || !stopped || !locked {
			return ErrMySQLRestoreFence
		}
		return db.verifyMySQLRestoreSourceArtifact(ctx, tx, acceptance, request)
	})
}
