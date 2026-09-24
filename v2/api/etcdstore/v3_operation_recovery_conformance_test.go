package etcdstore_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestV3OperationStoreExpiredRecoveryFencesAndClassifiesAmbiguityEtcd(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx := context.Background()
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-conf/v3-recovery/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-recovery-conformance-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	newStore := func() *etcdstore.V3OperationStore {
		adapter, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
		if err != nil {
			t.Fatal(err)
		}
		return adapter
	}
	newAcceptance := func(key string) store.OperationAcceptance {
		acceptance := store.OperationAcceptance{
			Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "etcd-recovery", Subject: "worker"}, Kind: "app.preflight", Resource: "app/recovery", Key: key},
			Operation: model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: "recovery", MaxAttempts: 2, Payload: map[string]interface{}{}},
			Audit:     store.AcceptanceAuditContext{Source: "etcd-recovery"},
		}
		var err error
		acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
		if err != nil {
			t.Fatal(err)
		}
		return acceptance
	}

	t.Run("expired owner is terminalized and fenced", func(t *testing.T) {
		adapter := newStore()
		acceptance := newAcceptance("expired-owner")
		if _, err := adapter.Accept(ctx, acceptance); err != nil {
			t.Fatal(err)
		}
		_, claim, err := adapter.ClaimNextOperation(ctx, "expired-worker", 30*time.Millisecond, nil)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Millisecond)
		if err := adapter.RecoverExpiredOperations(ctx); err != nil {
			t.Fatal(err)
		}
		resolved, err := adapter.Resolve(ctx, acceptance.Identity, acceptance.Fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Operation.Status != model.OperationFailed || resolved.Operation.FinishedAt == nil || resolved.Operation.Metadata["manualRecoveryRequired"] != true || resolved.Operation.Metadata["externalEffectRecoveryPending"] != true {
			t.Fatalf("recovered operation=%+v", resolved.Operation)
		}
		if err := adapter.FinishClaimedOperation(ctx, claim, model.OperationSucceeded, "stale", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
			t.Fatalf("stale finish error=%v", err)
		}
		if got, replacement, err := adapter.ClaimNextOperation(ctx, "replacement", time.Minute, nil); err != nil || got != nil || replacement.Generation() != 0 {
			t.Fatalf("terminal recovery requeued operation=%+v claim=%+v err=%v", got, replacement, err)
		}
	})

	t.Run("live owner survives recovery", func(t *testing.T) {
		adapter := newStore()
		acceptance := newAcceptance("live-owner")
		if _, err := adapter.Accept(ctx, acceptance); err != nil {
			t.Fatal(err)
		}
		if _, _, err := adapter.ClaimNextOperation(ctx, "live-worker", time.Minute, nil); err != nil {
			t.Fatal(err)
		}
		if err := adapter.RecoverExpiredOperations(ctx); err != nil {
			t.Fatal(err)
		}
		resolved, err := adapter.Resolve(ctx, acceptance.Identity, acceptance.Fingerprint)
		if err != nil || resolved.Operation.Status != model.OperationRunning || resolved.Operation.Metadata["manualRecoveryRequired"] != nil {
			t.Fatalf("live operation=%+v err=%v", resolved.Operation, err)
		}
	})

	t.Run("competing recovery passes do not overwrite a newer state", func(t *testing.T) {
		first, second := newStore(), newStore()
		acceptance := newAcceptance("competing-recovery")
		if _, err := first.Accept(ctx, acceptance); err != nil {
			t.Fatal(err)
		}
		if _, _, err := first.ClaimNextOperation(ctx, "expired-worker", 30*time.Millisecond, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Millisecond)
		var group sync.WaitGroup
		errs := make(chan error, 2)
		for _, adapter := range []*etcdstore.V3OperationStore{first, second} {
			group.Add(1)
			go func(adapter *etcdstore.V3OperationStore) {
				defer group.Done()
				errs <- adapter.RecoverExpiredOperations(ctx)
			}(adapter)
		}
		group.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		resolved, err := first.Resolve(ctx, acceptance.Identity, acceptance.Fingerprint)
		if err != nil || resolved.Operation.Status != model.OperationFailed || resolved.Operation.Metadata["recoveredAfterLeaseExpiry"] != true {
			t.Fatalf("competing recovery result=%+v err=%v", resolved.Operation, err)
		}
	})
}
