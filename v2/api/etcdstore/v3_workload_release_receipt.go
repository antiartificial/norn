package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

type v3WorkloadReleaseReceipt struct {
	IntentID        string          `json:"intentId"`
	OperationID     string          `json:"operationId"`
	Receipt         json.RawMessage `json:"receipt"`
	ReceiptDigest   string          `json:"receiptDigest"`
	ClaimGeneration int64           `json:"claimGeneration"`
	Revision        int64           `json:"revision"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

func (s *V3OperationStore) workloadReleaseReceiptKey(intentID string) string {
	sum := sha256.Sum256([]byte(intentID))
	return s.prefix + "/v3/workload-release-receipts/" + hex.EncodeToString(sum[:])
}

func (s *V3OperationStore) LoadWorkloadReleaseReceipt(ctx context.Context, intentID string) (*store.WorkloadReleaseReceiptRecord, error) {
	if s == nil || s.kv == nil || strings.TrimSpace(intentID) == "" {
		return nil, fmt.Errorf("workload release receipt store is unavailable")
	}
	response, err := s.kv.Get(ctx, s.workloadReleaseReceiptKey(intentID))
	if err != nil {
		return nil, err
	}
	if len(response.Kvs) == 0 {
		return nil, nil
	}
	var receipt v3WorkloadReleaseReceipt
	if err := decodeV3Record(response.Kvs[0].Value, &receipt); err != nil {
		return nil, fmt.Errorf("decode etcd workload release receipt: %w", err)
	}
	if receipt.IntentID != intentID || receipt.OperationID == "" || receipt.ClaimGeneration <= 0 || receipt.Revision <= 0 ||
		len(receipt.Receipt) < 2 || !json.Valid(receipt.Receipt) || checkpointDigest(receipt.Receipt) != receipt.ReceiptDigest {
		return nil, fmt.Errorf("etcd workload release receipt failed identity or integrity verification")
	}
	return &store.WorkloadReleaseReceiptRecord{IntentID: receipt.IntentID, OperationID: receipt.OperationID,
		Receipt: append(json.RawMessage(nil), receipt.Receipt...), ReceiptDigest: receipt.ReceiptDigest,
		ClaimGeneration: receipt.ClaimGeneration, Revision: receipt.Revision, UpdatedAt: receipt.UpdatedAt}, nil
}

// CompareAndSwapWorkloadReleaseReceipt stores exact receipt bytes while the
// operation claim is live and the expected predecessor revision is current.
func (s *V3OperationStore) CompareAndSwapWorkloadReleaseReceipt(ctx context.Context, claim store.OperationClaim, intentID, expectedDigest string, raw json.RawMessage) (store.WorkloadReleaseReceiptRecord, error) {
	if s == nil || s.kv == nil || claim.OperationID() == "" || claim.OwnerID() == "" || claim.Generation() <= 0 ||
		strings.TrimSpace(intentID) == "" || len(raw) < 2 || len(raw) > 262144 || !json.Valid(raw) ||
		(expectedDigest != "" && !validV3ReceiptDigest(expectedDigest)) {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release receipt compare-and-swap input is invalid")
	}
	var identity struct {
		IntentID string `json:"intentId"`
	}
	if err := json.Unmarshal(raw, &identity); err != nil || identity.IntentID != intentID {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release receipt intent identity is invalid")
	}
	key := s.workloadReleaseReceiptKey(intentID)
	for attempt := 0; attempt < 4; attempt++ {
		op, operationRevision, err := s.load(ctx, claim.OperationID())
		if err != nil {
			return store.WorkloadReleaseReceiptRecord{}, store.ErrOperationOwnershipLost
		}
		owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
		if err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
		if len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || op.Operation.Status != "running" ||
			op.Operation.LockedBy != claim.OwnerID() || op.Generation != claim.Generation() ||
			string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
			return store.WorkloadReleaseReceiptRecord{}, store.ErrOperationOwnershipLost
		}
		prior, err := s.kv.Get(ctx, key)
		if err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
		record := v3WorkloadReleaseReceipt{IntentID: intentID, OperationID: claim.OperationID(),
			Receipt: append(json.RawMessage(nil), raw...), ReceiptDigest: checkpointDigest(raw),
			ClaimGeneration: claim.Generation(), Revision: 1, UpdatedAt: time.Now().UTC().Truncate(time.Microsecond)}
		comparisons := []clientv3.Cmp{
			clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
			clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
			clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		}
		if expectedDigest == "" {
			if len(prior.Kvs) != 0 {
				return store.WorkloadReleaseReceiptRecord{}, store.ErrWorkloadReleaseReceiptConflict
			}
			comparisons = append(comparisons, clientv3.Compare(clientv3.CreateRevision(key), "=", 0))
		} else {
			if len(prior.Kvs) != 1 {
				return store.WorkloadReleaseReceiptRecord{}, store.ErrWorkloadReleaseReceiptConflict
			}
			var previous v3WorkloadReleaseReceipt
			if err := decodeV3Record(prior.Kvs[0].Value, &previous); err != nil || previous.IntentID != intentID || previous.OperationID != claim.OperationID() ||
				previous.ReceiptDigest != expectedDigest || checkpointDigest(previous.Receipt) != previous.ReceiptDigest {
				return store.WorkloadReleaseReceiptRecord{}, store.ErrWorkloadReleaseReceiptConflict
			}
			record.Revision = previous.Revision + 1
			comparisons = append(comparisons, clientv3.Compare(clientv3.ModRevision(key), "=", prior.Kvs[0].ModRevision))
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
		txn, err := s.kv.Txn(ctx).If(comparisons...).Then(clientv3.OpPut(key, string(encoded))).Commit()
		if err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
		if txn.Succeeded {
			return store.WorkloadReleaseReceiptRecord{IntentID: record.IntentID, OperationID: record.OperationID,
				Receipt: append(json.RawMessage(nil), record.Receipt...), ReceiptDigest: record.ReceiptDigest,
				ClaimGeneration: record.ClaimGeneration, Revision: record.Revision, UpdatedAt: record.UpdatedAt}, nil
		}
	}
	return store.WorkloadReleaseReceiptRecord{}, store.ErrOperationOwnershipLost
}

func validV3ReceiptDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
