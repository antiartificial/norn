package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/artifactstore"
	"norn/v2/api/database"
	"norn/v2/api/model"
)

const MySQLSourceArtifactRetentionReceiptSchema = "norn.mysql-source-artifact-retention-receipt/v2"

// MySQLSourceArtifactRetentionReceipt is a second service signature. It binds
// an exact immutable object descriptor to the already-signed v1 staging
// receipt; it does not make a local path or the evidence archive a restore
// source.
type MySQLSourceArtifactRetentionReceipt struct {
	Schema               string                   `json:"schema"`
	OperationID          string                   `json:"operationId"`
	StagingReceiptSHA256 string                   `json:"stagingReceiptSha256"`
	Artifact             artifactstore.Descriptor `json:"artifact"`
}

type SignedMySQLSourceArtifactRetentionReceipt struct {
	Receipt        MySQLSourceArtifactRetentionReceipt
	CanonicalBytes []byte
	SHA256         string
	Signature      AcceptanceSignature
}

var ErrMySQLSourceArtifactRetentionIndeterminate = errors.New("MySQL source artifact retention may have executed; manual observation required")

// RetainClaimedMySQLSourceArtifact publishes a previously stage-proved dump
// under its digest-derived key. It records publish-intended before opening the
// store. Any publication ambiguity is resolved only by Verify of the exact
// descriptor; the dump is never staged again here.
func (db *DB) RetainClaimedMySQLSourceArtifact(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, objects artifactstore.Store) (SignedMySQLSourceArtifactRetentionReceipt, error) {
	return db.retainClaimedMySQLSourceArtifactSupervised(ctx, acceptance, claim, objects, 2*time.Minute)
}

func (db *DB) retainClaimedMySQLSourceArtifactSupervised(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, objects artifactstore.Store, lease time.Duration) (SignedMySQLSourceArtifactRetentionReceipt, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || objects == nil || validateOperationClaim(claim) != nil || lease <= 0 {
		return SignedMySQLSourceArtifactRetentionReceipt{}, ErrMySQLSourceSnapshotFence
	}
	return runClaimedMySQLSourceArtifactRetention(ctx, lease,
		func(renewCtx context.Context, duration time.Duration) error {
			return db.RenewOperationClaim(renewCtx, claim, duration)
		},
		func(runCtx context.Context, ready func() error) (SignedMySQLSourceArtifactRetentionReceipt, error) {
			return db.retainClaimedMySQLSourceArtifact(runCtx, acceptance, claim, objects, ready)
		})
}

func (db *DB) retainClaimedMySQLSourceArtifact(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, objects artifactstore.Store, ready func() error) (SignedMySQLSourceArtifactRetentionReceipt, error) {
	stage, descriptor, publish, retained, err := db.intendClaimedMySQLSourceArtifactRetention(ctx, acceptance, claim)
	if err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, err
	}
	if retained != nil {
		return *retained, nil
	}
	if err := ready(); err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, err
	}
	if publish {
		file, err := os.Open(stage.Receipt.ArtifactPath)
		if err != nil {
			return SignedMySQLSourceArtifactRetentionReceipt{}, errors.Join(ErrMySQLSourceArtifactRetentionIndeterminate, err)
		}
		_, publishErr := objects.Publish(ctx, descriptor, file)
		closeErr := file.Close()
		if publishErr == nil {
			publishErr = closeErr
		}
		if publishErr != nil {
			if readyErr := ready(); readyErr != nil {
				return SignedMySQLSourceArtifactRetentionReceipt{}, readyErr
			}
			if verifyErr := objects.Verify(ctx, descriptor); verifyErr != nil {
				return SignedMySQLSourceArtifactRetentionReceipt{}, errors.Join(ErrMySQLSourceArtifactRetentionIndeterminate, publishErr, verifyErr)
			}
		}
	}
	if err := ready(); err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, err
	}
	if err := objects.Verify(ctx, descriptor); err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, errors.Join(ErrMySQLSourceArtifactRetentionIndeterminate, err)
	}
	if err := ready(); err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, err
	}
	receipt := MySQLSourceArtifactRetentionReceipt{Schema: MySQLSourceArtifactRetentionReceiptSchema, OperationID: claim.OperationID(), StagingReceiptSHA256: stage.SHA256, Artifact: descriptor}
	canonical, err := json.Marshal(receipt)
	if err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, errors.Join(ErrMySQLSourceArtifactRetentionIndeterminate, err)
	}
	signature, err := acceptance.signer.Sign(ctx, canonical)
	if err != nil || signature.Algorithm == "" || signature.KeyID == "" || signature.Value == "" {
		return SignedMySQLSourceArtifactRetentionReceipt{}, ErrMySQLSourceArtifactRetentionIndeterminate
	}
	if err := acceptance.signer.Verify(ctx, signature, canonical); err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, errors.Join(ErrMySQLSourceArtifactRetentionIndeterminate, err)
	}
	hash := sha256.Sum256(canonical)
	signed := SignedMySQLSourceArtifactRetentionReceipt{Receipt: receipt, CanonicalBytes: canonical, SHA256: hex.EncodeToString(hash[:]), Signature: signature}
	if err := db.proveClaimedMySQLSourceArtifactRetention(ctx, acceptance, claim, descriptor, signed); err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, errors.Join(ErrMySQLSourceArtifactRetentionIndeterminate, err)
	}
	return signed, nil
}

func descriptorForStagedMySQLArtifact(stage SignedMySQLSourceArtifactReceipt) (artifactstore.Descriptor, error) {
	artifact := stage.Receipt.Artifact
	descriptor := artifactstore.Descriptor{Key: artifactstore.KeyForSHA256(artifact.SHA256), SHA256: artifact.SHA256, Size: artifact.Bytes}
	if artifact.Format != database.MySQLSQLArtifactV2 || descriptor.Validate() != nil {
		return artifactstore.Descriptor{}, ErrMySQLSourceArtifactRetentionIndeterminate
	}
	return descriptor, nil
}

func (db *DB) intendClaimedMySQLSourceArtifactRetention(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim) (SignedMySQLSourceArtifactReceipt, artifactstore.Descriptor, bool, *SignedMySQLSourceArtifactRetentionReceipt, error) {
	stage, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptance, claim.OperationID())
	if err != nil {
		return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
	}
	descriptor, err := descriptorForStagedMySQLArtifact(stage)
	if err != nil {
		return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil || accepted.Operation.Kind != MySQLSourceSnapshotOperationKind || accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 {
		return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, ErrMySQLSourceSnapshotFence
	}
	request, err := sourceSnapshotRequestFromAccepted(accepted)
	if err != nil {
		return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
	}
	var state string
	var retainedJSON []byte
	if err := tx.QueryRow(ctx, `SELECT state,retained_artifact FROM mysql_source_snapshot_intents WHERE operation_id=$1 FOR UPDATE`, claim.OperationID()).Scan(&state, &retainedJSON); err != nil {
		return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, ErrMySQLSourceArtifactRetentionIndeterminate
	}
	switch state {
	case "retained-proved":
		if err := verifyClaimedMySQLSourceStage(ctx, tx, claim, accepted, request, "retained-proved"); err != nil {
			return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
		}
		retained, err := db.LoadSignedMySQLSourceArtifactRetentionReceipt(ctx, acceptance, claim.OperationID())
		if err != nil {
			return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
		}
		return stage, descriptor, false, &retained, nil
	case "publish-intended":
		var saved artifactstore.Descriptor
		if json.Unmarshal(retainedJSON, &saved) != nil || saved != descriptor {
			return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, ErrMySQLSourceArtifactRetentionIndeterminate
		}
		if err := verifyClaimedMySQLSourceStage(ctx, tx, claim, accepted, request, "publish-intended"); err != nil {
			return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
		}
		return stage, descriptor, false, nil, nil
	case "stage-proved":
		if err := verifyClaimedMySQLSourceStage(ctx, tx, claim, accepted, request, "stage-proved"); err != nil {
			return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
		}
		if err := verifyStagedArtifactPath(stage.Receipt.ArtifactPath, stage.Receipt.Artifact, descriptor); err != nil {
			return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
		}
		encoded, err := json.Marshal(descriptor)
		if err != nil {
			return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
		}
		tag, err := tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET state='publish-intended',artifact_publish_intended_at=clock_timestamp(),retained_artifact=$2::jsonb WHERE operation_id=$1 AND state='stage-proved'`, claim.OperationID(), string(encoded))
		if err != nil || tag.RowsAffected() != 1 {
			return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, ErrMySQLSourceArtifactRetentionIndeterminate
		}
		if err := tx.Commit(ctx); err != nil {
			return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, err
		}
		return stage, descriptor, true, nil, nil
	default:
		return SignedMySQLSourceArtifactReceipt{}, artifactstore.Descriptor{}, false, nil, ErrMySQLSourceArtifactRetentionIndeterminate
	}
}

func sourceSnapshotRequestFromAccepted(accepted AcceptedOperation) (MySQLSourceSnapshotRequest, error) {
	encoded, err := json.Marshal(accepted.Operation.Payload)
	if err != nil {
		return MySQLSourceSnapshotRequest{}, ErrMySQLSourceArtifactRetentionIndeterminate
	}
	var request MySQLSourceSnapshotRequest
	if err := decodeStrictAcceptanceJSON(encoded, &request); err != nil || !validMySQLSourceSnapshotRequest(request) || !sameMySQLSourceSnapshotPayload(accepted.Operation.Payload, request) {
		return MySQLSourceSnapshotRequest{}, ErrMySQLSourceArtifactRetentionIndeterminate
	}
	return request, nil
}

func verifyStagedArtifactPath(filePath string, artifact database.MySQLSQLArtifact, descriptor artifactstore.Descriptor) error {
	if !filepath.IsAbs(filePath) || artifact.Bytes != descriptor.Size || artifact.SHA256 != descriptor.SHA256 || database.VerifyMySQLSQLArtifact(filePath, artifact) != nil {
		return ErrMySQLSourceArtifactRetentionIndeterminate
	}
	return nil
}

func (db *DB) proveClaimedMySQLSourceArtifactRetention(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, descriptor artifactstore.Descriptor, signed SignedMySQLSourceArtifactRetentionReceipt) error {
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil || signed.Receipt.OperationID != claim.OperationID() || signed.Receipt.Schema != MySQLSourceArtifactRetentionReceiptSchema || signed.Receipt.Artifact != descriptor {
		return ErrMySQLSourceArtifactRetentionIndeterminate
	}
	stage, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptance, claim.OperationID())
	if err != nil || signed.Receipt.StagingReceiptSHA256 != stage.SHA256 {
		return ErrMySQLSourceArtifactRetentionIndeterminate
	}
	request, err := sourceSnapshotRequestFromAccepted(accepted)
	if err != nil {
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
	if err := verifyClaimedMySQLSourceStage(ctx, tx, claim, accepted, request, "publish-intended"); err != nil {
		return err
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE mysql_source_snapshot_intents SET state='retained-proved',artifact_retained_proved_at=clock_timestamp(),
		retention_receipt_canonical=$2,retention_receipt_sha256=$3,retention_receipt_signing_algorithm=$4,
		retention_receipt_signing_key_id=$5,retention_receipt_signature=$6
		WHERE operation_id=$1 AND state='publish-intended' AND retained_artifact=$7::jsonb`, claim.OperationID(), signed.CanonicalBytes, signed.SHA256,
		signed.Signature.Algorithm, signed.Signature.KeyID, signed.Signature.Value, string(encoded))
	if err != nil || tag.RowsAffected() != 1 {
		return ErrMySQLSourceArtifactRetentionIndeterminate
	}
	return tx.Commit(ctx)
}

// LoadSignedMySQLSourceArtifactRetentionReceipt verifies the persisted v2
// bytes and binds them back to the exact v1 staging receipt and descriptor.
func (db *DB) LoadSignedMySQLSourceArtifactRetentionReceipt(ctx context.Context, acceptance *PGOperationStore, operationID string) (SignedMySQLSourceArtifactRetentionReceipt, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || operationID == "" {
		return SignedMySQLSourceArtifactRetentionReceipt{}, ErrMySQLSourceSnapshotFence
	}
	var canonical, descriptorJSON []byte
	var digest, algorithm, keyID, signature, state string
	err := db.Pool.QueryRow(ctx, `SELECT retention_receipt_canonical,retained_artifact,retention_receipt_sha256,
		retention_receipt_signing_algorithm,retention_receipt_signing_key_id,retention_receipt_signature,state
		FROM mysql_source_snapshot_intents WHERE operation_id=$1`, operationID).Scan(&canonical, &descriptorJSON, &digest, &algorithm, &keyID, &signature, &state)
	if err != nil || state != "retained-proved" {
		return SignedMySQLSourceArtifactRetentionReceipt{}, ErrMySQLSourceArtifactRetentionIndeterminate
	}
	hash := sha256.Sum256(canonical)
	if hex.EncodeToString(hash[:]) != digest {
		return SignedMySQLSourceArtifactRetentionReceipt{}, ErrMySQLSourceArtifactRetentionIndeterminate
	}
	sig := AcceptanceSignature{Algorithm: algorithm, KeyID: keyID, Value: signature}
	if err := acceptance.signer.Verify(ctx, sig, canonical); err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, errors.Join(ErrMySQLSourceArtifactRetentionIndeterminate, err)
	}
	var receipt MySQLSourceArtifactRetentionReceipt
	var descriptor artifactstore.Descriptor
	if json.Unmarshal(canonical, &receipt) != nil || json.Unmarshal(descriptorJSON, &descriptor) != nil || descriptor.Validate() != nil || receipt.Schema != MySQLSourceArtifactRetentionReceiptSchema || receipt.OperationID != operationID || receipt.Artifact != descriptor {
		return SignedMySQLSourceArtifactRetentionReceipt{}, ErrMySQLSourceArtifactRetentionIndeterminate
	}
	stage, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptance, operationID)
	if err != nil || receipt.StagingReceiptSHA256 != stage.SHA256 || descriptor.Size != stage.Receipt.Artifact.Bytes || descriptor.SHA256 != stage.Receipt.Artifact.SHA256 {
		return SignedMySQLSourceArtifactRetentionReceipt{}, ErrMySQLSourceArtifactRetentionIndeterminate
	}
	return SignedMySQLSourceArtifactRetentionReceipt{Receipt: receipt, CanonicalBytes: canonical, SHA256: digest, Signature: sig}, nil
}
