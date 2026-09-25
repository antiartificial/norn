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

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// v3FunctionInvocationReceipt is the public terminal projection of an
// invocation. Private request material remains solely in the separately
// encrypted private-invocation record.
type v3FunctionInvocationReceipt struct {
	ID              string    `json:"id"`
	App             string    `json:"app"`
	Process         string    `json:"process"`
	Status          string    `json:"status"`
	ExitCode        int       `json:"exitCode"`
	StartedAt       time.Time `json:"startedAt"`
	FinishedAt      time.Time `json:"finishedAt"`
	DurationMs      int64     `json:"durationMs"`
	ClaimGeneration int64     `json:"claimGeneration"`
}

func (s *V3OperationStore) functionInvocationReceiptKey(operationID string) string {
	sum := sha256.Sum256([]byte(operationID))
	return s.prefix + "/v3/function-invocation-receipts/" + hex.EncodeToString(sum[:])
}

// FinishClaimedFunctionInvocation atomically stores the immutable public
// execution projection and terminal operation receipt under the operation
// claim fence.
func (s *V3OperationStore) FinishClaimedFunctionInvocation(ctx context.Context, claim store.OperationClaim, execution store.FuncExecution, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	return s.finishClaimedFunctionInvocation(ctx, claim, nil, execution, status, message, metadata)
}

// FinishClaimedFunctionInvocationWithAppLock additionally compares the live
// app-lock fence in the terminal transaction. Function workers on the etcd
// adapter must use this method after a mutable invocation result is observed.
func (s *V3OperationStore) FinishClaimedFunctionInvocationWithAppLock(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, execution store.FuncExecution, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	if lock == nil || lock.Fence() == "" {
		return store.ErrOperationOwnershipLost
	}
	return s.finishClaimedFunctionInvocation(ctx, claim, lock, execution, status, message, metadata)
}

func (s *V3OperationStore) finishClaimedFunctionInvocation(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, execution store.FuncExecution, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	if s == nil || s.kv == nil || !validV3FunctionInvocationClaim(claim) {
		return fmt.Errorf("function invocation receipt is invalid")
	}
	if execution.ID != claim.OperationID() {
		return fmt.Errorf("function invocation execution projection does not match claimed operation")
	}
	if err := validateV3FunctionInvocationReceipt(execution, status, metadata); err != nil {
		return err
	}

	for attempt := 0; attempt < 4; attempt++ {
		record, operationRevision, err := s.load(ctx, claim.OperationID())
		if err != nil {
			return store.ErrOperationOwnershipLost
		}
		owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
		if err != nil {
			return err
		}
		if len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || record.Operation.Kind != store.PrivateInvocationOperationKind || record.Operation.Status != model.OperationRunning || record.Operation.LockedBy != claim.OwnerID() || record.Generation != claim.Generation() || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
			return store.ErrOperationOwnershipLost
		}
		process, ok := record.Operation.Payload["process"].(string)
		if !ok || record.Operation.App != execution.App || process != execution.Process {
			return fmt.Errorf("function invocation execution projection does not match accepted operation")
		}

		receiptKey := s.functionInvocationReceiptKey(claim.OperationID())
		existing, err := s.kv.Get(ctx, receiptKey)
		if err != nil {
			return err
		}
		if len(existing.Kvs) != 0 {
			return store.ErrFunctionInvocationExecutionConflict
		}

		now := time.Now().UTC().Truncate(time.Microsecond)
		receipt := v3FunctionInvocationReceipt{ID: execution.ID, App: execution.App, Process: execution.Process, Status: execution.Status, ExitCode: *execution.ExitCode, StartedAt: execution.StartedAt.UTC().Truncate(time.Microsecond), FinishedAt: now, DurationMs: *execution.DurationMs, ClaimGeneration: claim.Generation()}
		encodedReceipt, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		if record.Operation.Metadata == nil {
			record.Operation.Metadata = map[string]interface{}{}
		}
		for key, value := range metadata {
			record.Operation.Metadata[key] = value
		}
		record.Operation.Status = status
		record.Operation.Message = message
		record.Operation.LockedBy = ""
		record.Operation.LockedUntil = nil
		record.Operation.FinishedAt = &now
		encodedOperation, err := json.Marshal(record)
		if err != nil {
			return err
		}

		comparisons := []clientv3.Cmp{
			clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
			clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
			clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
			clientv3.Compare(clientv3.CreateRevision(receiptKey), "=", 0),
		}
		if lock != nil {
			comparisons = append(comparisons, clientv3.Compare(clientv3.Value(s.appLockKey(record.Operation.App)), "=", lock.Fence()))
		}
		txn, err := s.kv.Txn(ctx).If(comparisons...).Then(
			clientv3.OpPut(s.opKey(claim.OperationID()), string(encodedOperation)),
			clientv3.OpPut(receiptKey, string(encodedReceipt)),
			clientv3.OpDelete(s.ownerKey(claim.OperationID())),
			clientv3.OpDelete(s.runningKey(claim.OperationID())),
		).Commit()
		if err != nil {
			return err
		}
		if txn.Succeeded {
			return nil
		}
	}
	return store.ErrOperationOwnershipLost
}

func validateV3FunctionInvocationReceipt(execution store.FuncExecution, status model.OperationStatus, metadata map[string]interface{}) error {
	if status != model.OperationSucceeded && status != model.OperationFailed {
		return fmt.Errorf("function invocation completion status %q is invalid", status)
	}
	if strings.TrimSpace(execution.ID) == "" || strings.TrimSpace(execution.App) == "" || strings.TrimSpace(execution.Process) == "" ||
		(execution.Status != "complete" && execution.Status != "failed") || execution.ExitCode == nil || execution.DurationMs == nil || *execution.DurationMs < 0 || execution.StartedAt.IsZero() {
		return fmt.Errorf("function invocation execution projection is invalid")
	}
	if (status == model.OperationSucceeded) != (execution.Status == "complete") {
		return fmt.Errorf("function invocation execution status does not match operation receipt")
	}
	for key, value := range metadata {
		switch key {
		case "jobId", "allocationId":
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" || len(text) > 512 || strings.ContainsAny(text, "\r\n\x00") {
				return fmt.Errorf("function invocation receipt metadata %q is invalid", key)
			}
		case "exitCode":
			exitCode, ok := value.(int)
			if !ok || exitCode != *execution.ExitCode {
				return fmt.Errorf("function invocation receipt exit code differs from execution projection")
			}
		case "durationMs":
			duration, ok := value.(int64)
			if !ok || duration != *execution.DurationMs {
				return fmt.Errorf("function invocation receipt duration differs from execution projection")
			}
		default:
			return fmt.Errorf("function invocation receipt metadata %q is not public", key)
		}
	}
	return nil
}

var _ store.FunctionInvocationReceiptStore = (*V3OperationStore)(nil)
var _ store.FunctionInvocationReceiptWithAppLockStore = (*V3OperationStore)(nil)
