package storetest

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"norn/v2/api/model"
	"norn/v2/api/store"
	"testing"
	"time"
)

// RunSignedExecutionConformance verifies the shared accepted-operation and
// generation-fencing invariant for a backend-neutral control store.
func RunSignedExecutionConformance(t *testing.T, authority string, accept func(context.Context, store.OperationAcceptance) (store.AcceptedOperation, error), execution store.ExecutionStore) {
	t.Helper()
	ctx := context.Background()
	a := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "conformance", Subject: "worker"}, Kind: "app.preflight", Resource: "app/conformance", Key: uuid.NewString()}, Operation: model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: "conformance", MaxAttempts: 2}, Audit: store.AcceptanceAuditContext{Source: "storetest"}}
	var err error
	a.Fingerprint, err = store.CanonicalOperationRequestFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = accept(ctx, a); err != nil {
		t.Fatal(err)
	}
	op, first, err := execution.ClaimNextOperation(ctx, "worker-a", time.Minute, []string{a.Operation.Kind})
	if err != nil || op == nil {
		t.Fatalf("first claim = %v, %v", op, err)
	}
	// A deliberate defer provides a safe requeue without asserting that an
	// unknown expired external effect can be replayed automatically.
	if err := execution.DeferClaimedOperation(ctx, first, "retry later", time.Now().Add(-time.Second), nil); err != nil {
		t.Fatal(err)
	}
	op, second, err := execution.ClaimNextOperation(ctx, "worker-b", time.Minute, []string{a.Operation.Kind})
	if err != nil || op == nil {
		t.Fatalf("replacement claim = %v, %v", op, err)
	}
	if second.Generation() <= first.Generation() {
		t.Fatalf("generation did not advance: %d then %d", first.Generation(), second.Generation())
	}
	if err := execution.FinishClaimedOperation(ctx, first, model.OperationSucceeded, "stale", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("stale finish error = %v, want ownership lost", err)
	}
	if err := execution.FinishClaimedOperation(ctx, second, model.OperationSucceeded, "done", nil); err != nil {
		t.Fatal(err)
	}
}
