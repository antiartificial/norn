package etcdstore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func appAdmissionRequest(t *testing.T, authority, key string, exclusive bool) store.OperationAcceptance {
	t.Helper()
	a := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: "app.preflight", Resource: "app/demo", Key: key},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: "demo", Status: model.OperationQueued, MaxAttempts: 1},
		Audit:     store.AcceptanceAuditContext{Source: "test"},
		Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: exclusive},
	}
	var err error
	a.Fingerprint, err = store.CanonicalOperationRequestFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestV3AppAdmissionExclusiveAndTerminalReleaseEtcd(t *testing.T) {
	adapter, _, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	first := appAdmissionRequest(t, adapter.authority, "first", true)
	accepted, err := adapter.Accept(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := adapter.Accept(ctx, first)
	if err != nil || !replayed.Replayed || replayed.Operation.ID != accepted.Operation.ID {
		t.Fatalf("same identity replay=%+v err=%v", replayed, err)
	}
	second := appAdmissionRequest(t, adapter.authority, "second", false)
	if _, err := adapter.Accept(ctx, second); !errors.Is(err, store.ErrAcceptanceAdmission) {
		t.Fatalf("ordinary app work bypassed exclusive operation: %v", err)
	}
	keys, err := store.NewPrivateInvocationKeyRing("invocation-1", map[string][]byte{"invocation-1": bytes.Repeat([]byte{0x77}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	private := privateInvocationAcceptance(t, adapter.authority, "private-blocked")
	if _, err := adapter.AcceptPrivateInvocation(ctx, private, store.PrivateInvocationInput{Body: "private"}, keys); !errors.Is(err, store.ErrAcceptanceAdmission) {
		t.Fatalf("private invocation bypassed exclusive operation: %v", err)
	}
	claimed, claim, err := adapter.ClaimNextOperation(ctx, "worker", time.Minute, []string{"app.preflight"})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if err := adapter.FinishClaimedOperation(ctx, claim, model.OperationSucceeded, "done", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, second); err != nil {
		t.Fatalf("terminal operation did not release app admission: %v", err)
	}
	third := appAdmissionRequest(t, adapter.authority, "third", true)
	if _, err := adapter.Accept(ctx, third); !errors.Is(err, store.ErrAcceptanceAdmission) {
		t.Fatalf("exclusive work bypassed queued ordinary operation: %v", err)
	}
}

func TestV3AppAdmissionRetainsManualRecoveryHoldEtcd(t *testing.T) {
	adapter, _, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	first := appAdmissionRequest(t, adapter.authority, "manual-first", true)
	if _, err := adapter.Accept(ctx, first); err != nil {
		t.Fatal(err)
	}
	_, claim, err := adapter.ClaimNextOperation(ctx, "worker", time.Minute, []string{"app.preflight"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.FinishClaimedOperation(ctx, claim, model.OperationFailed, "outcome unresolved", map[string]interface{}{"manualRecoveryRequired": true}); err != nil {
		t.Fatal(err)
	}
	second := appAdmissionRequest(t, adapter.authority, "manual-second", true)
	if _, err := adapter.Accept(ctx, second); !errors.Is(err, store.ErrAcceptanceAdmission) {
		t.Fatalf("manual recovery hold was released: %v", err)
	}
}
