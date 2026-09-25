package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

const MySQLRestoreOperationKind = "database.mysql-restore"

// MySQLRestoreRequest is the complete signed operation payload. Neither the
// destination nor the artifact can be supplied anew by an executor after
// acceptance. The catalog revision is an additional fence around the stable
// service and binding generations in Target.
type MySQLRestoreRequest struct {
	CatalogRevision int64                     `json:"catalogRevision"`
	ProfileID       string                    `json:"profileId"`
	LogicalID       string                    `json:"logicalId"`
	Target          database.TargetIdentity   `json:"target"`
	Artifact        database.MySQLSQLArtifact `json:"artifact"`
	ArtifactPath    string                    `json:"artifactPath"`
}

type MySQLRestoreIntent struct {
	OperationID        string
	AcceptanceIntentID string
	Request            MySQLRestoreRequest
	State              string
	Replayed           bool
}

var ErrMySQLRestoreFence = errors.New("MySQL restore durable fence rejected the request")

// PrepareClaimedMySQLRestore persists the exact signed request while the
// operation claim and active catalog revision are locked. It repeats the
// target and artifact preflight under the catalog activation advisory lock.
// This creates no SQL write against the application target. An identical
// retry is idempotent; a different operation cannot consume the same target
// generation, including after an ambiguous or completed restore.
func (db *DB) PrepareClaimedMySQLRestore(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLRestoreRequest, secrets database.SecretSource) (MySQLRestoreIntent, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || secrets == nil || request.CatalogRevision <= 0 {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if err := validateOperationClaim(claim); err != nil {
		return MySQLRestoreIntent{}, err
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	if accepted.Operation.Kind != MySQLRestoreOperationKind || accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if !sameMySQLRestorePayload(accepted.Operation.Payload, request) {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('norn:database-catalog', 0))`); err != nil {
		return MySQLRestoreIntent{}, err
	}
	if err := verifyMySQLRestoreClaim(ctx, tx, claim); err != nil {
		return MySQLRestoreIntent{}, err
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return MySQLRestoreIntent{}, ErrDatabaseCatalogRevisionConflict
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	if _, err := database.PrepareMySQLRestore(ctx, resolver, request.ProfileID, request.LogicalID, request.Target, secrets, request.ArtifactPath, request.Artifact); err != nil {
		return MySQLRestoreIntent{}, err
	}
	target, _ := json.Marshal(request.Target)
	artifact, _ := json.Marshal(request.Artifact)
	key := mysqlRestoreTargetKey(request.Target)
	inserted, err := tx.Exec(ctx, `INSERT INTO mysql_restore_intents
		(operation_id, acceptance_intent_id, catalog_revision, profile_id, logical_id, target_key, target, artifact, artifact_path, state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'prepared') ON CONFLICT DO NOTHING`,
		claim.OperationID(), accepted.AcceptanceIntentID, request.CatalogRevision, request.ProfileID, request.LogicalID, key, target, artifact, request.ArtifactPath)
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	var existing MySQLRestoreIntent
	var savedTarget, savedArtifact []byte
	err = tx.QueryRow(ctx, `SELECT acceptance_intent_id, catalog_revision, profile_id, logical_id, target, artifact, artifact_path, state
		FROM mysql_restore_intents WHERE operation_id=$1`, claim.OperationID()).Scan(
		&existing.AcceptanceIntentID, &existing.Request.CatalogRevision, &existing.Request.ProfileID, &existing.Request.LogicalID,
		&savedTarget, &savedArtifact, &existing.Request.ArtifactPath, &existing.State)
	if err != nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if err := json.Unmarshal(savedTarget, &existing.Request.Target); err != nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if err := json.Unmarshal(savedArtifact, &existing.Request.Artifact); err != nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if existing.AcceptanceIntentID != accepted.AcceptanceIntentID || existing.Request != request || existing.State != "prepared" {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	existing.OperationID = claim.OperationID()
	existing.Replayed = inserted.RowsAffected() == 0
	if err := tx.Commit(ctx); err != nil {
		return MySQLRestoreIntent{}, err
	}
	return existing, nil
}

// BeginClaimedMySQLRestore is a one-way ambiguity boundary. The caller must
// commit this state before starting the external mysql process. Reentry after
// this point fails closed, even if the worker crashed before issuing SQL: it
// is impossible to distinguish that case from a partially applied dump.
func (db *DB) BeginClaimedMySQLRestore(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, secrets database.SecretSource) (MySQLRestoreIntent, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || secrets == nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if err := validateOperationClaim(claim); err != nil {
		return MySQLRestoreIntent{}, err
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	if accepted.Operation.Kind != MySQLRestoreOperationKind || accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	var request MySQLRestoreRequest
	if err := decodeMySQLRestorePayload(accepted.Operation.Payload, &request); err != nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('norn:database-catalog', 0))`); err != nil {
		return MySQLRestoreIntent{}, err
	}
	if err := verifyMySQLRestoreClaim(ctx, tx, claim); err != nil {
		return MySQLRestoreIntent{}, err
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return MySQLRestoreIntent{}, ErrDatabaseCatalogRevisionConflict
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	if _, err := database.PrepareMySQLRestore(ctx, resolver, request.ProfileID, request.LogicalID, request.Target, secrets, request.ArtifactPath, request.Artifact); err != nil {
		return MySQLRestoreIntent{}, err
	}
	var state, intentID, key, profileID, logicalID, artifactPath string
	var revision int64
	var targetBytes, artifactBytes []byte
	err = tx.QueryRow(ctx, `SELECT state, acceptance_intent_id, target_key, catalog_revision, profile_id, logical_id, target, artifact, artifact_path
		FROM mysql_restore_intents WHERE operation_id=$1 FOR UPDATE`, claim.OperationID()).Scan(
		&state, &intentID, &key, &revision, &profileID, &logicalID, &targetBytes, &artifactBytes, &artifactPath)
	if err != nil || state != "prepared" || intentID != accepted.AcceptanceIntentID || key != mysqlRestoreTargetKey(request.Target) ||
		revision != request.CatalogRevision || profileID != request.ProfileID || logicalID != request.LogicalID || artifactPath != request.ArtifactPath {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	var target database.TargetIdentity
	var artifact database.MySQLSQLArtifact
	if json.Unmarshal(targetBytes, &target) != nil || json.Unmarshal(artifactBytes, &artifact) != nil || target != request.Target || artifact != request.Artifact {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	result, err := tx.Exec(ctx, `UPDATE mysql_restore_intents SET state='executing', started_at=clock_timestamp() WHERE operation_id=$1 AND state='prepared'`, claim.OperationID())
	if err != nil || result.RowsAffected() != 1 {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if err := tx.Commit(ctx); err != nil {
		return MySQLRestoreIntent{}, err
	}
	return MySQLRestoreIntent{OperationID: claim.OperationID(), AcceptanceIntentID: intentID, Request: request, State: "executing"}, nil
}

func verifyMySQLRestoreClaim(ctx context.Context, tx pgx.Tx, claim OperationClaim) error {
	var held bool
	err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND status='running'
		AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp() FOR UPDATE`,
		claim.OperationID(), MySQLRestoreOperationKind, claim.OwnerID(), claim.Generation()).Scan(&held)
	if err != nil || !held {
		return ownershipLost(claim)
	}
	return nil
}

func mysqlRestoreTargetKey(target database.TargetIdentity) string {
	encoded, _ := json.Marshal(target)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func sameMySQLRestorePayload(payload map[string]interface{}, request MySQLRestoreRequest) bool {
	actual, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	wantStruct, err := json.Marshal(request)
	if err != nil {
		return false
	}
	var wantMap map[string]interface{}
	if err := json.Unmarshal(wantStruct, &wantMap); err != nil {
		return false
	}
	want, err := json.Marshal(wantMap)
	return err == nil && bytes.Equal(actual, want)
}

func decodeMySQLRestorePayload(payload map[string]interface{}, request *MySQLRestoreRequest) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, request); err != nil {
		return err
	}
	if !sameMySQLRestorePayload(payload, *request) {
		return fmt.Errorf("MySQL restore signed payload is not exact")
	}
	return nil
}
