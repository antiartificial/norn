package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
	CatalogRevision  int64                                `json:"catalogRevision"`
	ProfileID        string                               `json:"profileId"`
	LogicalID        string                               `json:"logicalId"`
	Target           database.TargetIdentity              `json:"target"`
	Maintenance      database.MySQLMaintenanceCredentials `json:"maintenance"`
	Artifact         database.MySQLSQLArtifact            `json:"artifact"`
	ArtifactPath     string                               `json:"artifactPath"`
	SourceQuiescence MySQLRestoreSourceQuiescence         `json:"sourceQuiescence"`
}

// MySQLRestoreSourceQuiescence is the operator evidence that the source was
// quiesced before its dump was accepted for restore. The complete record is
// signed as part of MySQLRestoreRequest and copied into the durable maintenance
// fence. It deliberately does not purport to fence an application's writes:
// that requires the application's runtime write path to consult the fence.
type MySQLRestoreSourceQuiescence struct {
	Source         database.TargetIdentity `json:"source"`
	ObservedAt     time.Time               `json:"observedAt"`
	Method         string                  `json:"method"`
	EvidenceSHA256 string                  `json:"evidenceSha256"`
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
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || secrets == nil || request.CatalogRevision <= 0 || !validMySQLRestoreSourceQuiescence(request.SourceQuiescence, request.Artifact.Source) {
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
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Target})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != request.Maintenance {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if _, err := database.PrepareMySQLRestore(ctx, resolver, request.ProfileID, request.LogicalID, request.Target, secrets, request.ArtifactPath, request.Artifact); err != nil {
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
	quiescence, _ := json.Marshal(request.SourceQuiescence)
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
	if existing.AcceptanceIntentID != accepted.AcceptanceIntentID || existing.Request.CatalogRevision != request.CatalogRevision || existing.Request.ProfileID != request.ProfileID || existing.Request.LogicalID != request.LogicalID || existing.Request.Target != request.Target || existing.Request.Artifact != request.Artifact || existing.Request.ArtifactPath != request.ArtifactPath || existing.State != "prepared" {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	// Insert only after confirming that this operation owns the durable intent.
	// A target-key collision leaves no row for this operation, so inserting the
	// fence first would turn the expected rejection into an FK error.
	if _, err := tx.Exec(ctx, `INSERT INTO mysql_restore_maintenance_fences
		(operation_id, catalog_revision, source_quiescence)
		VALUES ($1,$2,$3) ON CONFLICT (operation_id) DO NOTHING`, claim.OperationID(), request.CatalogRevision, quiescence); err != nil {
		return MySQLRestoreIntent{}, err
	}
	var savedQuiescence []byte
	if err := tx.QueryRow(ctx, `SELECT source_quiescence FROM mysql_restore_maintenance_fences WHERE operation_id=$1`, claim.OperationID()).Scan(&savedQuiescence); err != nil || json.Unmarshal(savedQuiescence, &existing.Request.SourceQuiescence) != nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if !sameMySQLRestoreRequest(existing.Request, request) {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	var fenceMatches bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mysql_restore_maintenance_fences WHERE operation_id=$1 AND source_quiescence=$2::jsonb)`, claim.OperationID(), quiescence).Scan(&fenceMatches); err != nil || !fenceMatches {
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
	if err := decodeMySQLRestorePayload(accepted.Operation.Payload, &request); err != nil || !validMySQLRestoreSourceQuiescence(request.SourceQuiescence, request.Artifact.Source) {
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
	quiescence, _ := json.Marshal(request.SourceQuiescence)
	var fenceMatches bool
	if err := tx.QueryRow(ctx, `SELECT source_quiescence=$2::jsonb FROM mysql_restore_maintenance_fences WHERE operation_id=$1 FOR UPDATE`, claim.OperationID(), quiescence).Scan(&fenceMatches); err != nil || !fenceMatches {
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
	if _, err := database.PrepareMySQLRestore(ctx, resolver, request.ProfileID, request.LogicalID, request.Target, secrets, request.ArtifactPath, request.Artifact); err != nil {
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
	if succeeded {
		result, err := tx.Exec(ctx, `DELETE FROM mysql_restore_maintenance_fences WHERE operation_id=$1`, claim.OperationID())
		if err != nil || result.RowsAffected() != 1 {
			return ErrMySQLRestoreFence
		}
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

func validMySQLRestoreSourceQuiescence(evidence MySQLRestoreSourceQuiescence, source database.TargetIdentity) bool {
	if evidence.Source != source || evidence.ObservedAt.IsZero() || strings.TrimSpace(evidence.Method) == "" || len(evidence.Method) > 200 || len(evidence.EvidenceSHA256) != 64 {
		return false
	}
	if _, err := hex.DecodeString(evidence.EvidenceSHA256); err != nil || strings.ToLower(evidence.EvidenceSHA256) != evidence.EvidenceSHA256 {
		return false
	}
	return true
}
