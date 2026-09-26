package worker

import (
	"context"
	"errors"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

type functionReceiptFake struct{ claimOnly int }

func (f *functionReceiptFake) FinishClaimedFunctionInvocation(context.Context, store.OperationClaim, store.FuncExecution, model.OperationStatus, string, map[string]interface{}) error {
	f.claimOnly++
	return nil
}

type fencedFunctionReceiptFake struct {
	store.AppLockFencedExecutionStore
	functionReceiptFake
	withLock int
}

func (f *fencedFunctionReceiptFake) FinishClaimedFunctionInvocationWithAppLock(_ context.Context, _ store.OperationClaim, lock store.AppOperationLock, _ store.FuncExecution, _ model.OperationStatus, _ string, _ map[string]interface{}) error {
	if lock.Fence() != "fence-1" {
		return errors.New("wrong fence")
	}
	f.withLock++
	return nil
}

type incompleteFencedFunctionReceiptFake struct {
	store.AppLockFencedExecutionStore
	functionReceiptFake
}

func TestFinishFunctionInvocationReceiptSelectsBackendFence(t *testing.T) {
	lock := store.NewFencedAppOperationLock(context.Background(), "fence-1", func() {})
	defer lock.Release()
	plain := &functionReceiptFake{}
	if err := FinishFunctionInvocationReceipt(context.Background(), plain, lock, store.OperationClaim{}, store.FuncExecution{}, model.OperationSucceeded, "done", nil); err != nil || plain.claimOnly != 1 {
		t.Fatalf("plain receipt: err=%v calls=%d", err, plain.claimOnly)
	}
	fenced := &fencedFunctionReceiptFake{}
	if err := FinishFunctionInvocationReceipt(context.Background(), fenced, lock, store.OperationClaim{}, store.FuncExecution{}, model.OperationSucceeded, "done", nil); err != nil || fenced.withLock != 1 || fenced.claimOnly != 0 {
		t.Fatalf("fenced receipt: err=%v fenced=%d claim-only=%d", err, fenced.withLock, fenced.claimOnly)
	}
	incomplete := &incompleteFencedFunctionReceiptFake{}
	if err := FinishFunctionInvocationReceipt(context.Background(), incomplete, lock, store.OperationClaim{}, store.FuncExecution{}, model.OperationSucceeded, "done", nil); err == nil || incomplete.claimOnly != 0 {
		t.Fatalf("missing fenced receipt fell back: err=%v claim-only=%d", err, incomplete.claimOnly)
	}
}

func TestFinishFunctionInvocationReceiptRejectsReleasedLock(t *testing.T) {
	lock := store.NewFencedAppOperationLock(context.Background(), "fence-1", func() {})
	lock.Release()
	plain := &functionReceiptFake{}
	if err := FinishFunctionInvocationReceipt(context.Background(), plain, lock, store.OperationClaim{}, store.FuncExecution{}, model.OperationSucceeded, "done", nil); !errors.Is(err, store.ErrOperationOwnershipLost) || plain.claimOnly != 0 {
		t.Fatalf("released lock accepted: err=%v calls=%d", err, plain.claimOnly)
	}
}
