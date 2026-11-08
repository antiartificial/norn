package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"norn/v2/api/store"
)

// WorkloadReleaseReceiptRepository validates protocol transitions before
// delegating receipt persistence to the claim-fenced PostgreSQL CAS store.
type WorkloadReleaseReceiptRepository struct {
	store WorkloadReleaseReceiptCAS
}

func (r *WorkloadReleaseReceiptRepository) Load(ctx context.Context, intent WorkloadReleaseIntent) (*store.WorkloadReleaseReceiptRecord, WorkloadReleaseReceipt, error) {
	if r == nil || r.store == nil || intent.Validate() != nil {
		return nil, WorkloadReleaseReceipt{}, fmt.Errorf("workload release receipt repository is unavailable or intent is invalid")
	}
	record, err := r.store.LoadWorkloadReleaseReceipt(ctx, intent.ID)
	if err != nil || record == nil {
		return record, WorkloadReleaseReceipt{}, err
	}
	if record.IntentID != intent.ID || workloadReceiptDigest(record.Receipt) != record.ReceiptDigest {
		return nil, WorkloadReleaseReceipt{}, fmt.Errorf("workload release receipt identity or digest is invalid")
	}
	var receipt WorkloadReleaseReceipt
	if !json.Valid(record.Receipt) || json.Unmarshal(record.Receipt, &receipt) != nil {
		return nil, WorkloadReleaseReceipt{}, fmt.Errorf("workload release receipt bytes are invalid")
	}
	if err := receipt.ValidateAgainst(intent); err != nil {
		return nil, WorkloadReleaseReceipt{}, fmt.Errorf("stored workload release receipt is invalid: %w", err)
	}
	return record, receipt, nil
}

func (r *WorkloadReleaseReceiptRepository) Advance(ctx context.Context, claim store.OperationClaim, intent WorkloadReleaseIntent, expectedDigest string, next WorkloadReleaseReceipt) (store.WorkloadReleaseReceiptRecord, error) {
	if r == nil || r.store == nil || strings.TrimSpace(claim.OperationID()) == "" || intent.Validate() != nil {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release receipt advance is unavailable or unclaimed")
	}
	if expectedDigest == "" {
		if next.Phase != WorkloadReleasePlanned {
			return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("first workload release receipt must be planned")
		}
		if err := next.ValidateAgainst(intent); err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
	} else {
		_, current, err := r.Load(ctx, intent)
		if err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
		if current.IntentID == "" {
			return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("current workload release receipt is missing")
		}
		if err := next.ValidateSuccessor(intent, current); err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	if expectedDigest != "" && !strings.HasPrefix(expectedDigest, "sha256:") {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release receipt expected digest is malformed")
	}
	if expectedDigest != "" && len(expectedDigest) != len("sha256:")+64 {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release receipt expected digest is malformed")
	}
	if expectedDigest != "" {
		if _, err := hex.DecodeString(strings.TrimPrefix(expectedDigest, "sha256:")); err != nil {
			return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release receipt expected digest is malformed")
		}
	}
	record, err := r.store.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, intent.ID, expectedDigest, encoded)
	if err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	if record.IntentID != intent.ID || record.ReceiptDigest != workloadReceiptDigest(encoded) || !bytes.Equal(record.Receipt, encoded) {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("receipt store returned different workload release bytes")
	}
	return record, nil
}

func workloadReceiptDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
