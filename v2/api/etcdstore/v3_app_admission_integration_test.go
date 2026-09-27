package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
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

func TestV3AppAdmissionConcurrentAPIsEtcd(t *testing.T) {
	first, client, prefix := privateInvocationEtcdStore(t)
	secondClient, err := clientv3.New(clientv3.Config{Endpoints: client.Endpoints(), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondClient.Close() })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-private-invocation-signing-key")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewV3OperationStore(secondClient, prefix, first.authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	requests := []store.OperationAcceptance{
		appAdmissionRequest(t, first.authority, "concurrent-a", true),
		appAdmissionRequest(t, first.authority, "concurrent-b", true),
	}
	start := make(chan struct{})
	results := make([]error, 2)
	var workers sync.WaitGroup
	for i, adapter := range []*V3OperationStore{first, second} {
		workers.Add(1)
		go func(i int, adapter *V3OperationStore) {
			defer workers.Done()
			<-start
			_, results[i] = adapter.Accept(context.Background(), requests[i])
		}(i, adapter)
	}
	close(start)
	workers.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, store.ErrAcceptanceAdmission) && !errors.Is(err, store.ErrAcceptanceIndeterminate) {
			t.Fatalf("unexpected concurrent admission error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("exclusive admission winners=%d, errors=%v", winners, results)
	}
	active, err := client.Get(context.Background(), first.appAdmissionActivePrefix("demo"), clientv3.WithPrefix())
	if err != nil || len(active.Kvs) != 1 {
		t.Fatalf("active app index entries=%d err=%v", len(active.Kvs), err)
	}
}

func TestV3AppAdmissionRejectsPreIndexActiveOperationEtcd(t *testing.T) {
	adapter, client, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	legacyID := uuid.NewString()
	legacy, err := json.Marshal(v3Record{Operation: model.Operation{ID: legacyID, Kind: "app.preflight", App: "demo", Status: model.OperationQueued}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, adapter.opKey(legacyID), string(legacy)); err != nil {
		t.Fatal(err)
	}
	request := appAdmissionRequest(t, adapter.authority, "after-legacy", true)
	if _, err := adapter.Accept(ctx, request); !errors.Is(err, store.ErrAcceptanceAdmission) {
		t.Fatalf("pre-index queued work was missed: %v", err)
	}
	if _, err := adapter.loadAcceptance(ctx, adapter.acceptanceKey(request.Identity)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected acceptance was written: %v", err)
	}
	if _, err := client.Delete(ctx, adapter.opKey(legacyID)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, request); err != nil {
		t.Fatalf("clean app could not initialize index: %v", err)
	}
}
