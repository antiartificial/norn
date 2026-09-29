package worker

import (
	"context"
	"fmt"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// FinishFunctionInvocationReceipt selects the terminal boundary required by
// the control backend. A lease-fenced backend must prove its app lock in the
// same transaction as the claim and projection; there is no fallback to the
// claim-only method when that capability is absent.
func FinishFunctionInvocationReceipt(ctx context.Context, receipts store.FunctionInvocationReceiptStore, lock store.AppOperationLock, claim store.OperationClaim, execution store.FuncExecution, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	if receipts == nil || lock == nil || lock.Context().Err() != nil {
		return store.ErrOperationOwnershipLost
	}
	if _, fenced := receipts.(store.AppLockFencedExecutionStore); fenced {
		withLock, ok := receipts.(store.FunctionInvocationReceiptWithAppLockStore)
		if !ok {
			return fmt.Errorf("function invocation terminal app-lock fence is unavailable")
		}
		return withLock.FinishClaimedFunctionInvocationWithAppLock(ctx, claim, lock, execution, status, message, metadata)
	}
	return receipts.FinishClaimedFunctionInvocation(ctx, claim, execution, status, message, metadata)
}
