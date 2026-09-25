package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// MySQLSourceArtifactStager is the narrow SQL dump capability of the private
// source runner. The production implementation uses only the catalog-derived
// snapshot maintenance credential.
type MySQLSourceArtifactStager interface {
	Stage(context.Context, database.ResolvedBinding, database.TargetIdentity, database.SecretSource, string, string, string) (string, database.MySQLSQLArtifact, error)
}

type mysqlSourceDatabaseStager struct{}

func (mysqlSourceDatabaseStager) Stage(ctx context.Context, source database.ResolvedBinding, expected database.TargetIdentity, secrets database.SecretSource, tool, digest, directory string) (string, database.MySQLSQLArtifact, error) {
	return database.StageMySQLSQLSnapshotWithMaintenanceCredential(ctx, source, expected, secrets, tool, digest, directory)
}

const MySQLSourceArtifactReceiptSchema = "norn.mysql-source-artifact-receipt/v1"

// MySQLSourceArtifactReceipt is a service-signed attestation of the bytes
// staged under a previously signed source operation. It is not restore
// authorization or proof that the local file has been retained elsewhere.
type MySQLSourceArtifactReceipt struct {
	Schema                    string                    `json:"schema"`
	OperationID               string                    `json:"operationId"`
	AcceptanceIntentID        string                    `json:"acceptanceIntentId"`
	AcceptanceCanonicalDigest string                    `json:"acceptanceCanonicalDigest"`
	CatalogRevision           int64                     `json:"catalogRevision"`
	Source                    database.TargetIdentity   `json:"source"`
	DumpToolSHA256            string                    `json:"dumpToolSha256"`
	ArtifactPath              string                    `json:"artifactPath"`
	Artifact                  database.MySQLSQLArtifact `json:"artifact"`
}

type SignedMySQLSourceArtifactReceipt struct {
	Receipt        MySQLSourceArtifactReceipt
	CanonicalBytes []byte
	SHA256         string
	Signature      AcceptanceSignature
}

var ErrMySQLSourceArtifactIndeterminate = errors.New("MySQL source artifact staging may have executed; manual observation required")

// StageClaimedMySQLSourceArtifact writes a durable stage intent before running
// the dump tool. Any error after that point leaves the source fenced and the
// stage non-retryable. A successful result is signed and stored atomically.
func (db *DB) StageClaimedMySQLSourceArtifact(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLSourceSnapshotRequest, secrets database.SecretSource, dumpToolPath, privateDirectory string, stager MySQLSourceArtifactStager) (SignedMySQLSourceArtifactReceipt, error) {
	if secrets == nil || stager == nil || !filepath.IsAbs(privateDirectory) || !filepath.IsAbs(dumpToolPath) {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceSnapshotFence
	}
	resolved, accepted, err := db.intendClaimedMySQLSourceArtifact(ctx, acceptance, claim, request)
	if err != nil {
		return SignedMySQLSourceArtifactReceipt{}, err
	}
	path, artifact, err := stager.Stage(ctx, resolved, request.Source, secrets, dumpToolPath, request.DumpToolSHA256, privateDirectory)
	if err != nil {
		return SignedMySQLSourceArtifactReceipt{}, errors.Join(ErrMySQLSourceArtifactIndeterminate, err)
	}
	if filepath.Dir(path) != filepath.Clean(privateDirectory) || artifact.Source != request.Source || database.VerifyMySQLSQLArtifact(path, artifact) != nil {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceArtifactIndeterminate
	}
	receipt := MySQLSourceArtifactReceipt{Schema: MySQLSourceArtifactReceiptSchema, OperationID: claim.OperationID(), AcceptanceIntentID: accepted.AcceptanceIntentID,
		AcceptanceCanonicalDigest: accepted.Intent.CanonicalDigest, CatalogRevision: request.CatalogRevision, Source: request.Source,
		DumpToolSHA256: request.DumpToolSHA256, ArtifactPath: path, Artifact: artifact}
	canonical, err := json.Marshal(receipt)
	if err != nil {
		return SignedMySQLSourceArtifactReceipt{}, errors.Join(ErrMySQLSourceArtifactIndeterminate, err)
	}
	signature, err := acceptance.signer.Sign(ctx, canonical)
	if err != nil || signature.Algorithm == "" || signature.KeyID == "" || signature.Value == "" {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceArtifactIndeterminate
	}
	if err := acceptance.signer.Verify(ctx, signature, canonical); err != nil {
		return SignedMySQLSourceArtifactReceipt{}, errors.Join(ErrMySQLSourceArtifactIndeterminate, err)
	}
	hash := sha256.Sum256(canonical)
	signed := SignedMySQLSourceArtifactReceipt{Receipt: receipt, CanonicalBytes: canonical, SHA256: hex.EncodeToString(hash[:]), Signature: signature}
	if err := db.proveClaimedMySQLSourceArtifact(ctx, acceptance, claim, request, signed); err != nil {
		return SignedMySQLSourceArtifactReceipt{}, errors.Join(ErrMySQLSourceArtifactIndeterminate, err)
	}
	return signed, nil
}

func (db *DB) StageClaimedMySQLSourceArtifactWithDatabase(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLSourceSnapshotRequest, secrets database.SecretSource, dumpToolPath, privateDirectory string) (SignedMySQLSourceArtifactReceipt, error) {
	return db.StageClaimedMySQLSourceArtifact(ctx, acceptance, claim, request, secrets, dumpToolPath, privateDirectory, mysqlSourceDatabaseStager{})
}

func (db *DB) intendClaimedMySQLSourceArtifact(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLSourceSnapshotRequest) (database.ResolvedBinding, AcceptedOperation, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || validateOperationClaim(claim) != nil || !validMySQLSourceSnapshotRequest(request) {
		return database.ResolvedBinding{}, AcceptedOperation{}, ErrMySQLSourceSnapshotFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil {
		return database.ResolvedBinding{}, AcceptedOperation{}, err
	}
	if accepted.Operation.Kind != MySQLSourceSnapshotOperationKind || accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 || !sameMySQLSourceSnapshotPayload(accepted.Operation.Payload, request) {
		return database.ResolvedBinding{}, AcceptedOperation{}, ErrMySQLSourceSnapshotFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return database.ResolvedBinding{}, AcceptedOperation{}, err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return database.ResolvedBinding{}, AcceptedOperation{}, err
	}
	if err := verifyClaimedMySQLSourceStage(ctx, tx, claim, accepted, request, "lock-proved"); err != nil {
		return database.ResolvedBinding{}, AcceptedOperation{}, err
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return database.ResolvedBinding{}, AcceptedOperation{}, ErrDatabaseCatalogRevisionConflict
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return database.ResolvedBinding{}, AcceptedOperation{}, err
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Source})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != request.Maintenance {
		return database.ResolvedBinding{}, AcceptedOperation{}, ErrMySQLSourceSnapshotFence
	}
	tag, err := tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET state='stage-intended',stage_intended_at=clock_timestamp() WHERE operation_id=$1 AND state='lock-proved'`, claim.OperationID())
	if err != nil || tag.RowsAffected() != 1 {
		return database.ResolvedBinding{}, AcceptedOperation{}, ErrMySQLSourceArtifactIndeterminate
	}
	if err := tx.Commit(ctx); err != nil {
		return database.ResolvedBinding{}, AcceptedOperation{}, err
	}
	return resolved, accepted, nil
}

func verifyClaimedMySQLSourceStage(ctx context.Context, tx pgx.Tx, claim OperationClaim, accepted AcceptedOperation, request MySQLSourceSnapshotRequest, state string) error {
	var held bool
	if err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND status='running' AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp() FOR UPDATE`, claim.OperationID(), MySQLSourceSnapshotOperationKind, claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return ownershipLost(claim)
	}
	var intentID, savedState, savedDigest, fenceOwner string
	var revision, fenceEpoch int64
	var source, maintenance, job []byte
	var stopProved, lockProved, active bool
	var liveEpoch int64
	var liveOwner string
	err := tx.QueryRow(ctx, `SELECT s.acceptance_intent_id,s.catalog_revision,s.source,s.maintenance,s.job_identity,s.dump_tool_sha256,s.state,
	 s.stop_proved_at IS NOT NULL,s.lock_proved_at IS NOT NULL,s.runtime_fence_epoch,s.runtime_fence_owner,
	 f.active,f.epoch,f.owner FROM mysql_source_snapshot_intents s CROSS JOIN runtime_mutation_fence f
	 WHERE s.operation_id=$1 AND f.singleton=true FOR UPDATE OF s,f`, claim.OperationID()).Scan(&intentID, &revision, &source, &maintenance, &job, &savedDigest, &savedState,
		&stopProved, &lockProved, &fenceEpoch, &fenceOwner, &active, &liveEpoch, &liveOwner)
	wantSource, _ := json.Marshal(request.Source)
	wantMaintenance, _ := json.Marshal(request.Maintenance)
	wantJob, _ := json.Marshal(request.JobIdentity)
	if err != nil || intentID != accepted.AcceptanceIntentID || revision != request.CatalogRevision || !sameJSON(source, wantSource) || !sameJSON(maintenance, wantMaintenance) || !sameJSON(job, wantJob) || savedDigest != request.DumpToolSHA256 || savedState != state || !stopProved || !lockProved || !active || fenceEpoch == 0 || fenceEpoch != liveEpoch || fenceOwner != liveOwner || fenceOwner != "mysql-source-snapshot:"+claim.OperationID() {
		return ErrMySQLSourceArtifactIndeterminate
	}
	return nil
}

func (db *DB) proveClaimedMySQLSourceArtifact(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLSourceSnapshotRequest, signed SignedMySQLSourceArtifactReceipt) error {
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil || accepted.AcceptanceIntentID != signed.Receipt.AcceptanceIntentID || accepted.Intent.CanonicalDigest != signed.Receipt.AcceptanceCanonicalDigest || !sameMySQLSourceSnapshotPayload(accepted.Operation.Payload, request) {
		return ErrMySQLSourceArtifactIndeterminate
	}
	if err := database.VerifyMySQLSQLArtifact(signed.Receipt.ArtifactPath, signed.Receipt.Artifact); err != nil {
		return err
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return err
	}
	if err := verifyClaimedMySQLSourceStage(ctx, tx, claim, accepted, request, "stage-intended"); err != nil {
		return err
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return ErrDatabaseCatalogRevisionConflict
	}
	artifact, err := json.Marshal(signed.Receipt.Artifact)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET state='stage-proved',stage_proved_at=clock_timestamp(),
	 artifact_path=$2,artifact=$3,artifact_receipt_canonical=$4,artifact_receipt_sha256=$5,
	 artifact_receipt_signing_algorithm=$6,artifact_receipt_signing_key_id=$7,artifact_receipt_signature=$8
	 WHERE operation_id=$1 AND state='stage-intended'`, claim.OperationID(), signed.Receipt.ArtifactPath, artifact,
		signed.CanonicalBytes, signed.SHA256, signed.Signature.Algorithm, signed.Signature.KeyID, signed.Signature.Value)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrMySQLSourceArtifactIndeterminate
	}
	return tx.Commit(ctx)
}

// LoadSignedMySQLSourceArtifactReceipt verifies the exact persisted receipt
// bytes and cross-checks their meaning against the immutable intent row.
func (db *DB) LoadSignedMySQLSourceArtifactReceipt(ctx context.Context, acceptance *PGOperationStore, operationID string) (SignedMySQLSourceArtifactReceipt, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || operationID == "" {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceSnapshotFence
	}
	var canonical, artifactJSON, sourceJSON []byte
	var digest, algorithm, keyID, signature, path, intentID, toolDigest, state string
	var revision int64
	err := db.Pool.QueryRow(ctx, `SELECT artifact_receipt_canonical,artifact,source,artifact_receipt_sha256,
	 artifact_receipt_signing_algorithm,artifact_receipt_signing_key_id,artifact_receipt_signature,
	 artifact_path,acceptance_intent_id,dump_tool_sha256,state,catalog_revision
	 FROM mysql_source_snapshot_intents WHERE operation_id=$1`, operationID).Scan(&canonical, &artifactJSON, &sourceJSON, &digest,
		&algorithm, &keyID, &signature, &path, &intentID, &toolDigest, &state, &revision)
	if err != nil || state != "stage-proved" {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceArtifactIndeterminate
	}
	hash := sha256.Sum256(canonical)
	if hex.EncodeToString(hash[:]) != digest {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceArtifactIndeterminate
	}
	sig := AcceptanceSignature{Algorithm: algorithm, KeyID: keyID, Value: signature}
	if err := acceptance.signer.Verify(ctx, sig, canonical); err != nil {
		return SignedMySQLSourceArtifactReceipt{}, errors.Join(ErrMySQLSourceArtifactIndeterminate, err)
	}
	var receipt MySQLSourceArtifactReceipt
	var artifact database.MySQLSQLArtifact
	var source database.TargetIdentity
	if err := json.Unmarshal(canonical, &receipt); err != nil {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceArtifactIndeterminate
	}
	if err := json.Unmarshal(artifactJSON, &artifact); err != nil {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceArtifactIndeterminate
	}
	if err := json.Unmarshal(sourceJSON, &source); err != nil {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceArtifactIndeterminate
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, operationID)
	if err != nil || accepted.Operation.Kind != MySQLSourceSnapshotOperationKind || accepted.Operation.MaxAttempts != 1 || receipt.Schema != MySQLSourceArtifactReceiptSchema || receipt.OperationID != operationID || receipt.AcceptanceIntentID != intentID || receipt.AcceptanceIntentID != accepted.AcceptanceIntentID || receipt.AcceptanceCanonicalDigest != accepted.Intent.CanonicalDigest || receipt.CatalogRevision != revision || receipt.Source != source || receipt.DumpToolSHA256 != toolDigest || receipt.ArtifactPath != path || receipt.Artifact != artifact {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceArtifactIndeterminate
	}
	var sourceRequest MySQLSourceSnapshotRequest
	encodedPayload, err := json.Marshal(accepted.Operation.Payload)
	if err != nil || json.Unmarshal(encodedPayload, &sourceRequest) != nil || !sameMySQLSourceSnapshotPayload(accepted.Operation.Payload, sourceRequest) ||
		sourceRequest.CatalogRevision != receipt.CatalogRevision || sourceRequest.Source != receipt.Source || sourceRequest.DumpToolSHA256 != receipt.DumpToolSHA256 {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceArtifactIndeterminate
	}
	return SignedMySQLSourceArtifactReceipt{Receipt: receipt, CanonicalBytes: canonical, SHA256: digest, Signature: sig}, nil
}
