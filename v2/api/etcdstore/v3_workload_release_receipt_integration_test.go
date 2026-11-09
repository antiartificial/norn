package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestV3WorkloadReleaseReceiptCASFencesAndRecoversEtcd(t *testing.T) {
	adapter, client, _ := functionInvocationEffectAttemptStore(t)
	ctx := context.Background()
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: "widgets", Status: model.OperationQueued, MaxAttempts: 3}
	acceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "workload-release-test", Subject: "operator"}, Kind: operation.Kind, Resource: operation.App, Key: uuid.NewString()},
		Operation: operation, Audit: store.AcceptanceAuditContext{Source: "workload-release-test"},
		Semantics: map[string]interface{}{"intentId": "intent-1"},
	}
	fingerprint, err := store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	acceptance.Fingerprint = fingerprint
	if _, err := adapter.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := adapter.ClaimNextOperation(ctx, "workload-release-worker", time.Minute, []string{operation.Kind})
	if err != nil || claimed == nil || claimed.ID != operation.ID {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	first := json.RawMessage(`{"intentId":"intent-1","phase":"planned"}`)
	record, err := adapter.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, "intent-1", "", first)
	if err != nil || record.Revision != 1 || record.OperationID != operation.ID || record.ClaimGeneration != claim.Generation() {
		t.Fatalf("initial receipt=%+v err=%v", record, err)
	}
	loaded, err := adapter.LoadWorkloadReleaseReceipt(ctx, "intent-1")
	if err != nil || loaded == nil || loaded.ReceiptDigest != record.ReceiptDigest || string(loaded.Receipt) != string(first) {
		t.Fatalf("loaded receipt=%+v err=%v", loaded, err)
	}
	second := json.RawMessage(`{"intentId":"intent-1","phase":"warming"}`)
	advanced, err := adapter.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, "intent-1", record.ReceiptDigest, second)
	if err != nil || advanced.Revision != 2 || advanced.ReceiptDigest == record.ReceiptDigest {
		t.Fatalf("advanced receipt=%+v err=%v", advanced, err)
	}
	if _, err := adapter.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, "intent-1", record.ReceiptDigest, first); !errors.Is(err, store.ErrWorkloadReleaseReceiptConflict) {
		t.Fatalf("stale CAS error=%v", err)
	}
	if _, err := adapter.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, "intent-other", advanced.ReceiptDigest, second); err == nil {
		t.Fatal("receipt identity substituted under another intent ID")
	}

	owner, err := client.Get(ctx, adapter.ownerKey(operation.ID))
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 {
		t.Fatalf("owner lease=%+v err=%v", owner.Kvs, err)
	}
	if _, err := client.Revoke(ctx, clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, "intent-1", advanced.ReceiptDigest, first); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("expired claim CAS error=%v", err)
	}
	if err := adapter.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	_, successor, err := adapter.ClaimNextOperation(ctx, "workload-release-recovery", time.Minute, []string{operation.Kind})
	if err != nil || successor.OperationID() != claim.OperationID() || successor.Generation() <= claim.Generation() {
		t.Fatalf("successor claim=%+v err=%v", successor, err)
	}
	third := json.RawMessage(`{"intentId":"intent-1","phase":"routing"}`)
	recovered, err := adapter.CompareAndSwapWorkloadReleaseReceipt(ctx, successor, "intent-1", advanced.ReceiptDigest, third)
	if err != nil || recovered.Revision != 3 || recovered.ClaimGeneration != successor.Generation() {
		t.Fatalf("same-intent successor receipt=%+v err=%v", recovered, err)
	}

	if _, err := client.Put(ctx, adapter.workloadReleaseReceiptKey("intent-1"), `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.LoadWorkloadReleaseReceipt(ctx, "intent-1"); err == nil {
		t.Fatal("corrupt receipt was returned")
	}
}
