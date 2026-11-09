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
)

var ErrWorkloadReleaseReceiptConflict = errors.New("workload release receipt compare-and-swap conflict")

type WorkloadReleaseReceiptRecord struct {
	IntentID        string
	OperationID     string
	Receipt         json.RawMessage
	ReceiptDigest   string
	ClaimGeneration int64
	Revision        int64
	UpdatedAt       time.Time
}

// LoadWorkloadReleaseReceipt reads and integrity-checks the current immutable
// receipt bytes for an intent. It returns nil when the intent has no receipt.
func (db *DB) LoadWorkloadReleaseReceipt(ctx context.Context, intentID string) (*WorkloadReleaseReceiptRecord, error) {
	if db == nil || db.Pool == nil || strings.TrimSpace(intentID) == "" {
		return nil, fmt.Errorf("workload release receipt store is unavailable")
	}
	var record WorkloadReleaseReceiptRecord
	var raw []byte
	err := db.Pool.QueryRow(ctx, `SELECT intent_id,operation_id,receipt,receipt_digest,claim_generation,revision,updated_at FROM workload_release_receipts WHERE intent_id=$1`, intentID).
		Scan(&record.IntentID, &record.OperationID, &raw, &record.ReceiptDigest, &record.ClaimGeneration, &record.Revision, &record.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid(raw) || workloadReceiptDigest(raw) != record.ReceiptDigest {
		return nil, fmt.Errorf("workload release receipt %q failed integrity verification", intentID)
	}
	record.Receipt = bytes.Clone(raw)
	return &record, nil
}

// CompareAndSwapWorkloadReleaseReceipt durably advances one intent receipt.
// expectedDigest is empty only for the first revision. Each mutation is fenced
// by the current operation claim and exact stored digest.
func (db *DB) CompareAndSwapWorkloadReleaseReceipt(ctx context.Context, claim OperationClaim, intentID, expectedDigest string, receipt json.RawMessage) (WorkloadReleaseReceiptRecord, error) {
	if db == nil || db.Pool == nil || validateOperationClaim(claim) != nil || strings.TrimSpace(intentID) == "" ||
		len(receipt) < 2 || len(receipt) > 262144 || !json.Valid(receipt) || (expectedDigest != "" && !validWorkloadReceiptDigest(expectedDigest)) {
		return WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release receipt compare-and-swap input is invalid")
	}
	var identity struct {
		IntentID string `json:"intentId"`
	}
	if err := json.Unmarshal(receipt, &identity); err != nil || identity.IntentID != intentID {
		return WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release receipt intent identity is invalid")
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WorkloadReleaseReceiptRecord{}, err
	}
	defer tx.Rollback(ctx)
	var status, owner string
	var generation int64
	var lockedUntil *time.Time
	var now time.Time
	err = tx.QueryRow(ctx, `SELECT status,locked_by,lock_generation,locked_until FROM operations WHERE id=$1 FOR UPDATE`, claim.OperationID()).
		Scan(&status, &owner, &generation, &lockedUntil)
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	}
	if err != nil || status != "running" || owner != claim.OwnerID() || generation != claim.Generation() || lockedUntil == nil || !lockedUntil.After(now) {
		return WorkloadReleaseReceiptRecord{}, ownershipLost(claim)
	}
	digest := workloadReceiptDigest(receipt)
	var raw []byte
	var record WorkloadReleaseReceiptRecord
	if expectedDigest == "" {
		err = tx.QueryRow(ctx, `INSERT INTO workload_release_receipts(intent_id,operation_id,receipt,receipt_digest,claim_generation,revision)
			VALUES ($1,$2,$3,$4,$5,1) ON CONFLICT (intent_id) DO NOTHING
			RETURNING intent_id,operation_id,receipt,receipt_digest,claim_generation,revision,updated_at`, intentID, claim.OperationID(), []byte(receipt), digest, claim.Generation()).
			Scan(&record.IntentID, &record.OperationID, &raw, &record.ReceiptDigest, &record.ClaimGeneration, &record.Revision, &record.UpdatedAt)
	} else {
		err = tx.QueryRow(ctx, `UPDATE workload_release_receipts SET receipt=$3,receipt_digest=$4,claim_generation=$5,revision=revision+1,updated_at=clock_timestamp()
			WHERE intent_id=$1 AND receipt_digest=$2 AND operation_id=$6
			RETURNING intent_id,operation_id,receipt,receipt_digest,claim_generation,revision,updated_at`, intentID, expectedDigest, []byte(receipt), digest, claim.Generation(), claim.OperationID()).
			Scan(&record.IntentID, &record.OperationID, &raw, &record.ReceiptDigest, &record.ClaimGeneration, &record.Revision, &record.UpdatedAt)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkloadReleaseReceiptRecord{}, ErrWorkloadReleaseReceiptConflict
	}
	if err != nil {
		return WorkloadReleaseReceiptRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkloadReleaseReceiptRecord{}, err
	}
	if !json.Valid(raw) || workloadReceiptDigest(raw) != record.ReceiptDigest {
		return WorkloadReleaseReceiptRecord{}, fmt.Errorf("stored workload release receipt failed integrity verification")
	}
	record.Receipt = bytes.Clone(raw)
	return record, nil
}

func workloadReceiptDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validWorkloadReceiptDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
