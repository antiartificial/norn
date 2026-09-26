package store

import (
	"context"
	"errors"
	"os"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/artifactstore"
	"norn/v2/api/database"
	"norn/v2/api/model"
)

// PrepareClaimedMySQLRestoreFromRetained preflights the target using a
// verified temporary copy of the retained object. The durable request still
// binds the original staging path as provenance; the runner materializes the
// object again under its supervised claim before crossing the SQL boundary.
func (db *DB) PrepareClaimedMySQLRestoreFromRetained(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLRestoreRequest, secrets database.SecretSource, objects artifactstore.Store, directory string) (MySQLRestoreIntent, error) {
	path, err := db.MaterializeClaimedMySQLRestoreArtifact(ctx, acceptance, claim, objects, directory)
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	defer os.Remove(path)
	return db.PrepareClaimedMySQLRestore(ctx, acceptance, claim, request, secrets, path)
}

// MaterializeClaimedMySQLRestoreArtifact resolves the separately signed restore
// request through the source's signed staging and retention receipts. The
// original staging path remains audit provenance, never the source of bytes.
// The caller must supervise the claim while this streams and remove the
// returned private file after the SQL process has finished.
func (db *DB) MaterializeClaimedMySQLRestoreArtifact(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, objects artifactstore.Store, directory string) (string, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || objects == nil || validateOperationClaim(claim) != nil {
		return "", ErrMySQLRestoreFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil || accepted.Operation.Kind != MySQLRestoreOperationKind || accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 {
		return "", ErrMySQLRestoreFence
	}
	var request MySQLRestoreRequest
	if decodeMySQLRestorePayload(accepted.Operation.Payload, &request) != nil || !validMySQLRestoreSourceArtifact(request.SourceArtifact) {
		return "", ErrMySQLRestoreFence
	}
	if err := db.verifyLiveMySQLRestoreClaim(ctx, claim); err != nil {
		return "", err
	}
	stage, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptance, request.SourceArtifact.OperationID)
	if err != nil || stage.SHA256 != request.SourceArtifact.ReceiptSHA256 || stage.Receipt.CatalogRevision != request.CatalogRevision ||
		stage.Receipt.Source != request.Artifact.Source || stage.Receipt.Artifact != request.Artifact || stage.Receipt.ArtifactPath != request.ArtifactPath {
		return "", ErrMySQLRestoreFence
	}
	retained, err := db.LoadSignedMySQLSourceArtifactRetentionReceipt(ctx, acceptance, request.SourceArtifact.OperationID)
	if err != nil || retained.Receipt.StagingReceiptSHA256 != stage.SHA256 || retained.Receipt.Artifact.SHA256 != request.Artifact.SHA256 ||
		retained.Receipt.Artifact.Size != request.Artifact.Bytes {
		return "", ErrMySQLRestoreFence
	}
	path, err := artifactstore.MaterializePrivate(ctx, objects, retained.Receipt.Artifact, directory)
	if err != nil {
		return "", errors.Join(ErrMySQLRestoreFence, err)
	}
	if err := db.verifyLiveMySQLRestoreClaim(ctx, claim); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func (db *DB) verifyLiveMySQLRestoreClaim(ctx context.Context, claim OperationClaim) error {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := verifyMySQLRestoreClaim(ctx, tx, claim); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
