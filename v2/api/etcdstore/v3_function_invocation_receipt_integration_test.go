package etcdstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func functionInvocationReceiptProjection(operationID, app, process string, exitCode int, duration int64) store.FuncExecution {
	return store.FuncExecution{ID: operationID, App: app, Process: process, Status: "complete", ExitCode: &exitCode, DurationMs: &duration, StartedAt: time.Now().UTC().Add(-time.Second)}
}

func TestV3FinishClaimedFunctionInvocationAtomicallyPublishesReceiptEtcd(t *testing.T) {
	adapter, client, _ := functionInvocationEffectAttemptStore(t)
	op, claim := claimFunctionInvocationEffectAttempt(t, adapter, "function-worker", time.Minute)
	execution := functionInvocationReceiptProjection(op.ID, op.App, "resize", 0, 42)
	if err := adapter.FinishClaimedFunctionInvocation(context.Background(), claim, execution, model.OperationSucceeded, "function completed", map[string]interface{}{"allocationId": "alloc-1", "exitCode": 0, "durationMs": int64(42)}); err != nil {
		t.Fatal(err)
	}
	finished, err := adapter.GetOperation(context.Background(), op.ID)
	if err != nil || finished.Status != model.OperationSucceeded || finished.Message != "function completed" || finished.Metadata["allocationId"] != "alloc-1" || finished.FinishedAt == nil || finished.LockedBy != "" {
		t.Fatalf("receipt=%+v err=%v", finished, err)
	}
	value, err := client.Get(context.Background(), adapter.functionInvocationReceiptKey(op.ID))
	if err != nil || len(value.Kvs) != 1 {
		t.Fatalf("projection records=%d err=%v", len(value.Kvs), err)
	}
	var receipt v3FunctionInvocationReceipt
	if err := decodeV3Record(value.Kvs[0].Value, &receipt); err != nil || receipt.ID != op.ID || receipt.App != op.App || receipt.Process != "resize" || receipt.Status != "complete" || receipt.ExitCode != 0 || receipt.DurationMs != 42 || receipt.ClaimGeneration != claim.Generation() || receipt.FinishedAt.IsZero() {
		t.Fatalf("projection=%+v err=%v", receipt, err)
	}
	owner, err := client.Get(context.Background(), adapter.ownerKey(op.ID))
	if err != nil || len(owner.Kvs) != 0 {
		t.Fatalf("owner records=%d err=%v", len(owner.Kvs), err)
	}
}

func TestV3FinishClaimedFunctionInvocationRejectsConflictAndPrivateMetadataEtcd(t *testing.T) {
	adapter, client, _ := functionInvocationEffectAttemptStore(t)
	op, claim := claimFunctionInvocationEffectAttempt(t, adapter, "function-worker", time.Minute)
	if _, err := client.Put(context.Background(), adapter.functionInvocationReceiptKey(op.ID), `{}`); err != nil {
		t.Fatal(err)
	}
	execution := functionInvocationReceiptProjection(op.ID, op.App, "resize", 0, 2)
	if err := adapter.FinishClaimedFunctionInvocation(context.Background(), claim, execution, model.OperationSucceeded, "completed", nil); !errors.Is(err, store.ErrFunctionInvocationExecutionConflict) {
		t.Fatalf("projection conflict=%v", err)
	}
	running, err := adapter.GetOperation(context.Background(), op.ID)
	if err != nil || running.Status != model.OperationRunning || running.LockedBy != claim.OwnerID() {
		t.Fatalf("operation after conflict=%+v err=%v", running, err)
	}

	adapter, client, _ = functionInvocationEffectAttemptStore(t)
	op, claim = claimFunctionInvocationEffectAttempt(t, adapter, "function-worker", time.Minute)
	execution = functionInvocationReceiptProjection(op.ID, op.App, "resize", 0, 2)
	if err := adapter.FinishClaimedFunctionInvocation(context.Background(), claim, execution, model.OperationSucceeded, "completed", map[string]interface{}{"requestBody": "private"}); err == nil {
		t.Fatal("private metadata was accepted")
	}
	value, err := client.Get(context.Background(), adapter.functionInvocationReceiptKey(op.ID))
	if err != nil || len(value.Kvs) != 0 {
		t.Fatalf("private receipt records=%d err=%v", len(value.Kvs), err)
	}
}

func TestV3FinishClaimedFunctionInvocationFencesAppLockEtcd(t *testing.T) {
	adapter, client, _ := functionInvocationEffectAttemptStore(t)
	op, claim := claimFunctionInvocationEffectAttempt(t, adapter, "function-worker", time.Minute)
	lock, acquired, err := adapter.AcquireAppOperationLock(context.Background(), op.App)
	if err != nil || !acquired {
		t.Fatalf("lock acquired=%v err=%v", acquired, err)
	}
	lock.Release()
	execution := functionInvocationReceiptProjection(op.ID, op.App, "resize", 0, 2)
	if err := adapter.FinishClaimedFunctionInvocationWithAppLock(context.Background(), claim, lock, execution, model.OperationSucceeded, "completed", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("lost lock finish=%v", err)
	}
	value, err := client.Get(context.Background(), adapter.functionInvocationReceiptKey(op.ID))
	if err != nil || len(value.Kvs) != 0 {
		t.Fatalf("lost lock receipt records=%d err=%v", len(value.Kvs), err)
	}
}
