package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	CheckpointSource = "source"
	CheckpointBuild  = "build"
)

var ErrCheckpointConflict = errors.New("operation checkpoint already records different outputs")

// OperationCheckpointStore preserves execution-affecting stage outputs across
// claims without exposing a storage backend to the pipeline.
type OperationCheckpointStore interface {
	RecordOperationCheckpoint(context.Context, OperationClaim, string, json.RawMessage) (OperationCheckpoint, error)
	LoadOperationCheckpoint(context.Context, string, string) (*OperationCheckpoint, error)
}

// OperationCheckpoint is a write-once record of one execution stage's outputs
// for an accepted operation. Outputs are stored as the exact bytes whose
// digest is recorded, so a read verifies integrity before use.
type OperationCheckpoint struct {
	OperationID     string
	Stage           string
	ClaimGeneration int64
	Outputs         json.RawMessage
	OutputsDigest   string
	CreatedAt       time.Time
}

func checkpointDigest(outputs []byte) string {
	digest := sha256.Sum256(outputs)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validCheckpointStage(stage string) bool {
	return stage == CheckpointSource || stage == CheckpointBuild
}

// RecordOperationCheckpoint stores outputs for (operation, stage) only while
// claim is the live owner. The first record wins: a repeat with identical
// outputs returns the stored record, and different outputs return the stored
// record with ErrCheckpointConflict.
func (db *DB) RecordOperationCheckpoint(ctx context.Context, claim OperationClaim, stage string, outputs json.RawMessage) (OperationCheckpoint, error) {
	if err := validateOperationClaim(claim); err != nil {
		return OperationCheckpoint{}, err
	}
	if !validCheckpointStage(stage) || len(outputs) < 2 || len(outputs) > 65536 || !json.Valid(outputs) {
		return OperationCheckpoint{}, fmt.Errorf("operation checkpoint is invalid")
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OperationCheckpoint{}, err
	}
	defer tx.Rollback(ctx)
	var status, owner string
	var generation int64
	var lockedUntil *time.Time
	var databaseNow time.Time
	err = tx.QueryRow(ctx, `SELECT status, locked_by, lock_generation, locked_until FROM operations WHERE id=$1 FOR UPDATE`, claim.OperationID()).
		Scan(&status, &owner, &generation, &lockedUntil)
	if err == nil {
		// Read the clock only after the row lock is held: a lease that expired
		// while this transaction waited for the lock must not be accepted.
		err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return OperationCheckpoint{}, err
	}
	if err != nil || status != "running" || owner != claim.OwnerID() || generation != claim.Generation() || lockedUntil == nil || !lockedUntil.After(databaseNow) {
		return OperationCheckpoint{}, ownershipLost(claim)
	}
	digest := checkpointDigest(outputs)
	if _, err := tx.Exec(ctx, `
		INSERT INTO operation_checkpoints (operation_id, stage, claim_generation, outputs, outputs_digest)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (operation_id, stage) DO NOTHING
	`, claim.OperationID(), stage, claim.Generation(), []byte(outputs), digest); err != nil {
		return OperationCheckpoint{}, err
	}
	stored, err := loadCheckpoint(ctx, tx, claim.OperationID(), stage)
	if err != nil {
		return OperationCheckpoint{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OperationCheckpoint{}, err
	}
	if stored.OutputsDigest != digest {
		return stored, ErrCheckpointConflict
	}
	return stored, nil
}

// LoadOperationCheckpoint returns the verified checkpoint, or nil when none
// has been recorded.
func (db *DB) LoadOperationCheckpoint(ctx context.Context, operationID, stage string) (*OperationCheckpoint, error) {
	if !validCheckpointStage(stage) {
		return nil, fmt.Errorf("operation checkpoint stage is invalid")
	}
	checkpoint, err := loadCheckpoint(ctx, db.Pool, operationID, stage)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

func loadCheckpoint(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, operationID, stage string) (OperationCheckpoint, error) {
	var checkpoint OperationCheckpoint
	var outputs []byte
	err := queryer.QueryRow(ctx, `SELECT operation_id, stage, claim_generation, outputs, outputs_digest, created_at FROM operation_checkpoints WHERE operation_id=$1 AND stage=$2`, operationID, stage).
		Scan(&checkpoint.OperationID, &checkpoint.Stage, &checkpoint.ClaimGeneration, &outputs, &checkpoint.OutputsDigest, &checkpoint.CreatedAt)
	if err != nil {
		return OperationCheckpoint{}, err
	}
	if checkpointDigest(outputs) != checkpoint.OutputsDigest || !json.Valid(outputs) {
		return OperationCheckpoint{}, fmt.Errorf("operation checkpoint %s/%s failed integrity verification", operationID, stage)
	}
	checkpoint.Outputs = json.RawMessage(bytes.Clone(outputs))
	return checkpoint, nil
}
