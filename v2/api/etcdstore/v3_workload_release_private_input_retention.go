package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

const workloadReleasePrivateInputDeletionSchema = "norn.workload-release-private-input-deletion/v1"

type workloadReleasePrivateInputDeletion struct {
	SchemaVersion string    `json:"schemaVersion"`
	OperationID   string    `json:"operationId"`
	IntentID      string    `json:"intentId"`
	InputDigest   string    `json:"inputDigest"`
	KeyID         string    `json:"keyId"`
	ReceiptDigest string    `json:"receiptDigest"`
	DeletedAt     time.Time `json:"deletedAt"`
}

func (s *V3OperationStore) workloadReleasePrivateInputDeletionKey(operationID string) string {
	return s.prefix + "/v3/workload-release-private-input-deletions/" + operationID
}

// DeleteCompletedWorkloadReleasePrivateInput removes the encrypted source and
// SOPS snapshot only after the durable release receipt proves full completion.
// Pending and held releases retain their inputs for same-intent recovery. The
// deletion tombstone makes cleanup auditable and safe to repeat after a crash.
func (s *V3OperationStore) DeleteCompletedWorkloadReleasePrivateInput(ctx context.Context, claim store.OperationClaim,
	lock store.AppOperationLock, intent pipeline.WorkloadReleaseIntent, completed store.WorkloadReleaseReceiptRecord) error {
	if s == nil || s.kv == nil || lock == nil || lock.Context().Err() != nil || intent.Validate() != nil ||
		completed.IntentID != intent.ID || completed.OperationID != claim.OperationID() || completed.ReceiptDigest == "" {
		return fmt.Errorf("completed workload release private-input cleanup is unavailable")
	}

	for attempt := 0; attempt < 4; attempt++ {
		verified, err := s.verifyClaimedWorkloadReleasePlan(ctx, claim, lock)
		if err != nil {
			return err
		}
		if verified.Accepted.Operation.ID != claim.OperationID() {
			return store.ErrOperationOwnershipLost
		}
		bindingResponse, err := s.kv.Get(ctx, s.workloadReleaseStagedTargetKey(claim.OperationID()))
		if err != nil || len(bindingResponse.Kvs) != 1 {
			return fmt.Errorf("completed workload release target binding is unavailable: %v", err)
		}
		binding, err := decodeWorkloadReleaseStagedTarget(bindingResponse.Kvs[0].Value, claim.OperationID(), verified.Plan)
		if err != nil {
			return err
		}
		resolved, err := verified.Plan.Resolve(binding.Binding, binding.Binding.EffectID, claim.Generation())
		if err != nil {
			return err
		}
		wantDigest, err := resolved.CanonicalDigest()
		if err != nil {
			return err
		}
		gotDigest, err := intent.CanonicalDigest()
		if err != nil || gotDigest != wantDigest {
			return fmt.Errorf("private-input cleanup intent differs from the signed and bound release")
		}

		receiptKey := s.workloadReleaseReceiptKey(intent.ID)
		receiptResponse, err := s.kv.Get(ctx, receiptKey)
		if err != nil || len(receiptResponse.Kvs) != 1 {
			return fmt.Errorf("completed workload release receipt is unavailable: %v", err)
		}
		var receiptRecord v3WorkloadReleaseReceipt
		if decodeV3Record(receiptResponse.Kvs[0].Value, &receiptRecord) != nil ||
			receiptRecord.IntentID != intent.ID || receiptRecord.OperationID != claim.OperationID() ||
			receiptRecord.ReceiptDigest != completed.ReceiptDigest || checkpointDigest(receiptRecord.Receipt) != receiptRecord.ReceiptDigest {
			return fmt.Errorf("private-input cleanup receipt identity or digest is invalid")
		}
		var receipt pipeline.WorkloadReleaseReceipt
		if json.Unmarshal(receiptRecord.Receipt, &receipt) != nil || receipt.ValidateAgainst(resolved) != nil ||
			receipt.Phase != pipeline.WorkloadReleaseComplete {
			return fmt.Errorf("private-input cleanup requires a validated complete release receipt")
		}

		payload := verified.Accepted.Operation.Payload
		inputID, _ := payload["workloadReleasePrivateRecordId"].(string)
		inputDigest, _ := payload["workloadReleasePrivateInputDigest"].(string)
		keyID, _ := payload["workloadReleasePrivateKeyId"].(string)
		if inputID != claim.OperationID() || inputDigest == "" || keyID == "" {
			return fmt.Errorf("accepted workload release private-input binding is incomplete")
		}

		privateKey := s.workloadReleasePrivateInputKey(claim.OperationID())
		deletionKey := s.workloadReleasePrivateInputDeletionKey(claim.OperationID())
		privateResponse, err := s.kv.Get(ctx, privateKey)
		if err != nil {
			return err
		}
		deletionResponse, err := s.kv.Get(ctx, deletionKey)
		if err != nil {
			return err
		}
		if len(deletionResponse.Kvs) == 1 {
			var prior workloadReleasePrivateInputDeletion
			if decodeV3Record(deletionResponse.Kvs[0].Value, &prior) != nil || prior.SchemaVersion != workloadReleasePrivateInputDeletionSchema ||
				prior.OperationID != claim.OperationID() || prior.IntentID != intent.ID || prior.InputDigest != inputDigest ||
				prior.KeyID != keyID || prior.ReceiptDigest != receiptRecord.ReceiptDigest || len(privateResponse.Kvs) != 0 {
				return fmt.Errorf("workload release private-input deletion receipt conflicts with current state")
			}
			return nil
		}
		if len(privateResponse.Kvs) != 1 {
			return fmt.Errorf("workload release private input is missing without a deletion receipt")
		}
		var privateRecord v3WorkloadReleasePrivateInputRecord
		if decodeV3Record(privateResponse.Kvs[0].Value, &privateRecord) != nil || privateRecord.SchemaVersion != workloadReleasePrivateInputRecordSchema ||
			privateRecord.Digest != inputDigest || privateRecord.KeyID != keyID || privateRecord.Envelope.KeyID != keyID {
			return fmt.Errorf("workload release private-input record differs from its accepted binding")
		}
		sealedDigest, err := store.WorkloadReleaseInputDigest(privateRecord.Envelope)
		if err != nil || sealedDigest != inputDigest {
			return fmt.Errorf("workload release private-input envelope failed integrity verification")
		}
		deletion := workloadReleasePrivateInputDeletion{SchemaVersion: workloadReleasePrivateInputDeletionSchema,
			OperationID: claim.OperationID(), IntentID: intent.ID, InputDigest: inputDigest, KeyID: keyID,
			ReceiptDigest: receiptRecord.ReceiptDigest, DeletedAt: time.Now().UTC().Truncate(time.Microsecond)}
		deletionBytes, err := json.Marshal(deletion)
		if err != nil {
			return err
		}
		if lock.Context().Err() != nil {
			return store.ErrOperationOwnershipLost
		}
		ownerKey := s.ownerKey(claim.OperationID())
		txn, err := s.kv.Txn(ctx).If(
			clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", verified.OperationRevision),
			clientv3.Compare(clientv3.ModRevision(ownerKey), "=", verified.OwnerRevision),
			clientv3.Compare(clientv3.Value(ownerKey), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
			clientv3.Compare(clientv3.Value(s.appLockKey(verified.App)), "=", lock.Fence()),
			clientv3.Compare(clientv3.ModRevision(s.fleetAppTargetKey(verified.Target.App, verified.Target.ControlEnvironment)), "=", verified.TargetRevision),
			clientv3.Compare(clientv3.ModRevision(receiptKey), "=", receiptResponse.Kvs[0].ModRevision),
			clientv3.Compare(clientv3.ModRevision(privateKey), "=", privateResponse.Kvs[0].ModRevision),
			clientv3.Compare(clientv3.CreateRevision(deletionKey), "=", 0),
		).Then(clientv3.OpDelete(privateKey), clientv3.OpPut(deletionKey, string(deletionBytes))).Commit()
		if err != nil {
			return err
		}
		if txn.Succeeded {
			return nil
		}
		if lock.Context().Err() != nil {
			return store.ErrOperationOwnershipLost
		}
	}
	return fmt.Errorf("workload release private-input cleanup did not converge")
}
