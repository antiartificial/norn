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
	CatalogRevision int64                                `json:"catalogRevision"`
	ProfileID       string                               `json:"profileId"`
	LogicalID       string                               `json:"logicalId"`
	Target          database.TargetIdentity              `json:"target"`
	Maintenance     database.MySQLMaintenanceCredentials `json:"maintenance"`
	Artifact        database.MySQLSQLArtifact            `json:"artifact"`
	ArtifactPath    string                               `json:"artifactPath"`
	SourceArtifact  MySQLRestoreSourceArtifact           `json:"sourceArtifact"`
}

// MySQLRestoreSourceArtifact identifies the exact service-signed source
// artifact receipt accepted by a separate source snapshot operation.
type MySQLRestoreSourceArtifact struct {
	OperationID   string `json:"operationId"`
	ReceiptSHA256 string `json:"receiptSha256"`
}

type MySQLRestoreIntent struct {
	OperationID        string
	AcceptanceIntentID string
	Request            MySQLRestoreRequest
	State              string
	Replayed           bool
}

var (
	ErrMySQLRestoreFence            = errors.New("MySQL restore durable fence rejected the request")
	ErrMySQLRestoreMaintenanceFence = errors.New("MySQL restore maintenance fence is active")
)

// PrepareClaimedMySQLRestore persists the exact signed request while the
// operation claim and active catalog revision are locked. It repeats the
// target and artifact preflight under the catalog activation advisory lock.
// This creates no SQL write against the application target. An identical
// retry is idempotent; a different operation cannot consume the same target
// generation, including after an ambiguous or completed restore.
func (db *DB) PrepareClaimedMySQLRestore(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLRestoreRequest, secrets database.SecretSource) (MySQLRestoreIntent, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || secrets == nil || request.CatalogRevision <= 0 || !validMySQLRestoreSourceArtifact(request.SourceArtifact) {
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
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return MySQLRestoreIntent{}, err
	}
	if err := verifyMySQLRestoreClaim(ctx, tx, claim); err != nil {
		return MySQLRestoreIntent{}, err
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return MySQLRestoreIntent{}, ErrDatabaseCatalogRevisionConflict
	}
	if mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Artifact.Source) == mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Target) {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Target})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != request.Maintenance {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if err := db.verifyMySQLRestoreSourceArtifact(ctx, tx, acceptance, request); err != nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	restore, err := database.MySQLRestoreBinding(resolved)
	if err != nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if _, err := database.PrepareMySQLRestoreWithResolvedCredential(ctx, resolved, restore, request.Target, secrets, request.ArtifactPath, request.Artifact); err != nil {
		return MySQLRestoreIntent{}, err
	}
	// The exact signed artifact source can be writable at the instant of the
	// restore. A launch reservation must therefore protect both it and the
	// destination, not only the target named by the restore command.
	if err := rejectMySQLRuntimeLaunchReservations(ctx, tx, []database.TargetIdentity{request.Artifact.Source, request.Target}); err != nil {
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
	// Maintenance identity is not duplicated in the intent row. The signed
	// operation and immutable catalog revision are its durable sources; reload
	// it from the revision resolved under the catalog gate before exact replay
	// comparison.
	existing.Request.Maintenance = *resolved.MySQLMaintenance
	if existing.AcceptanceIntentID != accepted.AcceptanceIntentID || existing.Request.CatalogRevision != request.CatalogRevision || existing.Request.ProfileID != request.ProfileID || existing.Request.LogicalID != request.LogicalID || existing.Request.Target != request.Target || existing.Request.Artifact != request.Artifact || existing.Request.ArtifactPath != request.ArtifactPath || existing.State != "prepared" {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	// Insert only after confirming that this operation owns the durable intent.
	// A target-key collision leaves no row for this operation, so inserting the
	// fence first would turn the expected rejection into an FK error.
	if _, err := tx.Exec(ctx, `INSERT INTO mysql_restore_maintenance_fences
		(operation_id, catalog_revision, source_artifact_operation_id, source_artifact_receipt_sha256)
		VALUES ($1,$2,$3,$4) ON CONFLICT (operation_id) DO NOTHING`, claim.OperationID(), request.CatalogRevision, request.SourceArtifact.OperationID, request.SourceArtifact.ReceiptSHA256); err != nil {
		return MySQLRestoreIntent{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT source_artifact_operation_id,source_artifact_receipt_sha256 FROM mysql_restore_maintenance_fences WHERE operation_id=$1`, claim.OperationID()).Scan(&existing.Request.SourceArtifact.OperationID, &existing.Request.SourceArtifact.ReceiptSHA256); err != nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if !sameMySQLRestoreRequest(existing.Request, request) {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	var fenceMatches bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mysql_restore_maintenance_fences WHERE operation_id=$1 AND source_artifact_operation_id=$2 AND source_artifact_receipt_sha256=$3)`, claim.OperationID(), request.SourceArtifact.OperationID, request.SourceArtifact.ReceiptSHA256).Scan(&fenceMatches); err != nil || !fenceMatches {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	existing.OperationID = claim.OperationID()
	existing.Replayed = inserted.RowsAffected() == 0
	if err := tx.Commit(ctx); err != nil {
		return MySQLRestoreIntent{}, err
	}
	return existing, nil
}

// IntendClaimedMySQLRestoreRuntimeLock records the exact operation, catalog,
// target, and live claim that is about to lock the destination runtime account.
// It commits before any external ALTER USER may be issued. A retry is allowed
// only for the same claim binding; ambiguity is retained for inspection.
func (db *DB) IntendClaimedMySQLRestoreRuntimeLock(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, secrets database.SecretSource) (MySQLRestoreIntent, error) {
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
	var request MySQLRestoreRequest
	if accepted.Operation.Kind != MySQLRestoreOperationKind || accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 ||
		decodeMySQLRestorePayload(accepted.Operation.Payload, &request) != nil || !validMySQLRestoreSourceArtifact(request.SourceArtifact) {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return MySQLRestoreIntent{}, err
	}
	if err := verifyMySQLRestoreClaim(ctx, tx, claim); err != nil {
		return MySQLRestoreIntent{}, err
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return MySQLRestoreIntent{}, ErrDatabaseCatalogRevisionConflict
	}
	var state, intentID string
	var revision int64
	var targetBytes []byte
	if err := tx.QueryRow(ctx, `SELECT state, acceptance_intent_id, catalog_revision, target FROM mysql_restore_intents WHERE operation_id=$1 FOR UPDATE`, claim.OperationID()).Scan(&state, &intentID, &revision, &targetBytes); err != nil || state != "prepared" || intentID != accepted.AcceptanceIntentID || revision != request.CatalogRevision {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	var target database.TargetIdentity
	if json.Unmarshal(targetBytes, &target) != nil || target != request.Target {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Target})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != request.Maintenance {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if err := db.verifyMySQLRestoreSourceArtifact(ctx, tx, acceptance, request); err != nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if err := verifyClaimedMySQLRestoreTransferredFence(ctx, tx, claim, request); err != nil {
		return MySQLRestoreIntent{}, err
	}
	result, err := tx.Exec(ctx, `INSERT INTO mysql_restore_runtime_locks
		(operation_id, acceptance_intent_id, catalog_revision, target, claim_owner, claim_generation, state)
		VALUES ($1,$2,$3,$4,$5,$6,'lock-intended') ON CONFLICT DO NOTHING`, claim.OperationID(), accepted.AcceptanceIntentID, request.CatalogRevision, targetBytes, claim.OwnerID(), claim.Generation())
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	var savedIntent, savedOwner, savedState string
	var savedRevision, savedGeneration int64
	var savedTarget []byte
	if err := tx.QueryRow(ctx, `SELECT acceptance_intent_id,catalog_revision,target,claim_owner,claim_generation,state FROM mysql_restore_runtime_locks WHERE operation_id=$1 FOR UPDATE`, claim.OperationID()).Scan(&savedIntent, &savedRevision, &savedTarget, &savedOwner, &savedGeneration, &savedState); err != nil || savedIntent != accepted.AcceptanceIntentID || savedRevision != request.CatalogRevision || !bytes.Equal(savedTarget, targetBytes) || savedOwner != claim.OwnerID() || savedGeneration != claim.Generation() || savedState != "lock-intended" {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if result.RowsAffected() > 1 {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if err := tx.Commit(ctx); err != nil {
		return MySQLRestoreIntent{}, err
	}
	return MySQLRestoreIntent{OperationID: claim.OperationID(), AcceptanceIntentID: accepted.AcceptanceIntentID, Request: request, State: "lock-intended"}, nil
}

// VerifyClaimedMySQLRestoreRuntimeLock persists the post-ALTER verification
// checkpoint. The primitive itself verifies account lock and session drain;
// this method binds that proof to the same accepted request and claim.
func (db *DB) VerifyClaimedMySQLRestoreRuntimeLock(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim) error {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || validateOperationClaim(claim) != nil {
		return ErrMySQLRestoreFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil || accepted.Operation.Kind != MySQLRestoreOperationKind || accepted.Operation.Status != model.OperationRunning {
		return ErrMySQLRestoreFence
	}
	var request MySQLRestoreRequest
	if decodeMySQLRestorePayload(accepted.Operation.Payload, &request) != nil {
		return ErrMySQLRestoreFence
	}
	target, _ := json.Marshal(request.Target)
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := verifyMySQLRestoreClaim(ctx, tx, claim); err != nil {
		return err
	}
	if err := verifyClaimedMySQLRestoreTransferredFence(ctx, tx, claim, request); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE mysql_restore_runtime_locks SET state='verified-lock', verified_at=clock_timestamp()
		WHERE operation_id=$1 AND acceptance_intent_id=$2 AND catalog_revision=$3 AND target=$4::jsonb
		  AND claim_owner=$5 AND claim_generation=$6 AND state='lock-intended'`, claim.OperationID(), accepted.AcceptanceIntentID, request.CatalogRevision, target, claim.OwnerID(), claim.Generation())
	if err != nil || result.RowsAffected() != 1 {
		return ErrMySQLRestoreFence
	}
	return tx.Commit(ctx)
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
	if err := decodeMySQLRestorePayload(accepted.Operation.Payload, &request); err != nil || !validMySQLRestoreSourceArtifact(request.SourceArtifact) {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return MySQLRestoreIntent{}, err
	}
	if err := verifyMySQLRestoreClaim(ctx, tx, claim); err != nil {
		return MySQLRestoreIntent{}, err
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return MySQLRestoreIntent{}, ErrDatabaseCatalogRevisionConflict
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
	var fenceMatches bool
	if err := tx.QueryRow(ctx, `SELECT source_artifact_operation_id=$2 AND source_artifact_receipt_sha256=$3 FROM mysql_restore_maintenance_fences WHERE operation_id=$1 FOR UPDATE`, claim.OperationID(), request.SourceArtifact.OperationID, request.SourceArtifact.ReceiptSHA256).Scan(&fenceMatches); err != nil || !fenceMatches {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if err := db.verifyMySQLRestoreSourceArtifact(ctx, tx, acceptance, request); err != nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if err := verifyClaimedMySQLRestoreTransferredFence(ctx, tx, claim, request); err != nil {
		return MySQLRestoreIntent{}, err
	}
	var lockVerified bool
	if err := tx.QueryRow(ctx, `SELECT state='verified-lock' FROM mysql_restore_runtime_locks
		WHERE operation_id=$1 AND acceptance_intent_id=$2 AND catalog_revision=$3 AND target=$4::jsonb
		  AND claim_owner=$5 AND claim_generation=$6 FOR UPDATE`, claim.OperationID(), accepted.AcceptanceIntentID, request.CatalogRevision, targetBytes, claim.OwnerID(), claim.Generation()).Scan(&lockVerified); err != nil || !lockVerified {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	// Reject a consumed or ambiguous intent before touching the target again.
	// A successor must never run even a read-only restore preflight as a
	// substitute for operator inspection after external SQL may have started.
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Target})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != request.Maintenance {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	restore, err := database.MySQLRestoreBinding(resolved)
	if err != nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if _, err := database.PrepareMySQLRestoreWithResolvedCredential(ctx, resolved, restore, request.Target, secrets, request.ArtifactPath, request.Artifact); err != nil {
		return MySQLRestoreIntent{}, err
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

// FinishClaimedMySQLRestore atomically records the external result and the
// operation receipt. A failed or interrupted client is deliberately retained
// as needs-inspection: a local process exit cannot prove whether MySQL applied
// a prefix of the SQL stream.
func (db *DB) FinishClaimedMySQLRestore(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, succeeded bool, message string) error {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || validateOperationClaim(claim) != nil {
		return ErrMySQLRestoreFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil || accepted.Operation.Kind != MySQLRestoreOperationKind || accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 {
		return ErrMySQLRestoreFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := verifyMySQLRestoreClaim(ctx, tx, claim); err != nil {
		return err
	}
	state := "needs-inspection"
	status := model.OperationFailed
	if succeeded {
		state, status = "completed", model.OperationSucceeded
	}
	result, err := tx.Exec(ctx, `UPDATE mysql_restore_intents SET state=$1, completed_at=CASE WHEN $1='completed' THEN clock_timestamp() ELSE NULL END
		WHERE operation_id=$2 AND acceptance_intent_id=$3 AND state='executing'`, state, claim.OperationID(), accepted.AcceptanceIntentID)
	if err != nil || result.RowsAffected() != 1 {
		return ErrMySQLRestoreFence
	}
	metadata, err := json.Marshal(map[string]interface{}{"mysqlRestoreState": state, "acceptanceIntentId": accepted.AcceptanceIntentID})
	if err != nil {
		return err
	}
	result, err = tx.Exec(ctx, `UPDATE operations SET status=$1, message=$2, metadata=metadata || $3::jsonb,
		locked_by='', locked_until=NULL, updated_at=clock_timestamp(), finished_at=clock_timestamp()
		WHERE id=$4 AND status='running' AND locked_by=$5 AND lock_generation=$6 AND locked_until>clock_timestamp()`,
		status, message, metadata, claim.OperationID(), claim.OwnerID(), claim.Generation())
	if err != nil || result.RowsAffected() != 1 {
		return ownershipLost(claim)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO evidence_archive_intents (id, subject_kind, subject_id, app, operation_id, sequence, state)
		SELECT 'ei-' || gen_random_uuid()::text, 'saga', saga_id, app, id, 1, 'pending' FROM operations WHERE id=$1 AND saga_id<>''
		ON CONFLICT (subject_kind, subject_id, sequence) DO NOTHING`, claim.OperationID()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ContainMySQLRestoreForInspection records an ambiguous private SQL client
// outcome after its operation claim has been lost. It intentionally does not
// create an operation receipt or alter the successor's claim: an executing
// restore can have applied an unknown SQL prefix, so containment must remain
// possible even when the original worker can no longer terminalize it.
//
// This is an idempotent, one-way transition. It is kept behind the private
// runner; MySQL restore has no public capability or HTTP route.
func (db *DB) ContainMySQLRestoreForInspection(ctx context.Context, operationID string) error {
	if db == nil || db.Pool == nil || operationID == "" {
		return ErrMySQLRestoreFence
	}
	result, err := db.Pool.Exec(ctx, `UPDATE mysql_restore_intents
		SET state='needs-inspection'
		WHERE operation_id=$1 AND state='executing'`, operationID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	var state string
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM mysql_restore_intents WHERE operation_id=$1`, operationID).Scan(&state); err != nil || state != "needs-inspection" {
		return ErrMySQLRestoreFence
	}
	return nil
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

func sameMySQLRestoreRequest(left, right MySQLRestoreRequest) bool {
	leftEncoded, leftErr := json.Marshal(left)
	rightEncoded, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftEncoded, rightEncoded)
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

func validMySQLRestoreSourceArtifact(source MySQLRestoreSourceArtifact) bool {
	if source.OperationID == "" || len(source.ReceiptSHA256) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(source.ReceiptSHA256)
	return err == nil && hex.EncodeToString(decoded) == source.ReceiptSHA256
}

// verifyMySQLRestoreSourceArtifact binds the restore's separately signed
// payload to the retained, service-signed source receipt and its current file.
// The source row remains reserved; this check does not unlock or transfer its
// runtime mutation fence.
func (db *DB) verifyMySQLRestoreSourceArtifact(ctx context.Context, tx pgx.Tx, acceptance *PGOperationStore, request MySQLRestoreRequest) error {
	if !validMySQLRestoreSourceArtifact(request.SourceArtifact) {
		return ErrMySQLRestoreFence
	}
	var state, digest, sourceKey string
	if err := tx.QueryRow(ctx, `SELECT state, artifact_receipt_sha256, source_key FROM mysql_source_snapshot_intents
		WHERE operation_id=$1 FOR SHARE`, request.SourceArtifact.OperationID).Scan(&state, &digest, &sourceKey); err != nil || state != "stage-proved" || digest != request.SourceArtifact.ReceiptSHA256 {
		return ErrMySQLRestoreFence
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision || sourceKey != mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Artifact.Source) {
		return ErrMySQLRestoreFence
	}
	receipt, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptance, request.SourceArtifact.OperationID)
	if err != nil || receipt.SHA256 != request.SourceArtifact.ReceiptSHA256 || receipt.Receipt.CatalogRevision != request.CatalogRevision ||
		receipt.Receipt.Source != request.Artifact.Source || receipt.Receipt.Artifact != request.Artifact || receipt.Receipt.ArtifactPath != request.ArtifactPath {
		return ErrMySQLRestoreFence
	}
	if err := database.VerifyMySQLSQLArtifact(receipt.Receipt.ArtifactPath, receipt.Receipt.Artifact); err != nil {
		return errors.Join(ErrMySQLRestoreFence, err)
	}
	return nil
}
