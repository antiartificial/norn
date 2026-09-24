package etcdstore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestV3OperationStoreSignedExecutionConformanceEtcd(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-conf/v3-ops/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-conformance-key-000000000000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	adapter, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	storetest.RunSignedExecutionConformance(t, authority, adapter.Accept, adapter)
}

func TestV3OperationStoreCanonicalAcceptanceReplayAndTamperEtcd(t *testing.T) {
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
	prefix := "/norn-conf/v3-acceptance/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-canonical-acceptance-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	adapter, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	newAcceptance := func(key string) store.OperationAcceptance {
		a := store.OperationAcceptance{
			Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "etcd-test", Subject: "operator"}, Kind: "app.preflight", Resource: "app/canonical", Key: key},
			Operation: model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: "canonical", Ref: "main", Risk: "low", Source: "etcd-test", MaxAttempts: 1, Payload: map[string]interface{}{"target": "canonical"}},
			Audit:     store.AcceptanceAuditContext{Source: "etcd-test", RequestID: uuid.NewString(), Scopes: []string{"apps:write"}},
		}
		var err error
		a.Fingerprint, err = store.CanonicalOperationRequestFingerprint(a)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	t.Run("replay verifies canonical envelope", func(t *testing.T) {
		a := newAcceptance("canonical-replay")
		first, err := adapter.Accept(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(first.Intent.CanonicalBytes, []byte(`"intentId"`)) || bytes.Contains(first.Intent.CanonicalBytes, []byte(`"operation"`)) {
			t.Fatalf("signed evidence is not the canonical acceptance envelope: %s", first.Intent.CanonicalBytes)
		}
		replayed, err := adapter.Accept(ctx, a)
		if err != nil || !replayed.Replayed || replayed.Operation.ID != first.Operation.ID {
			t.Fatalf("replay=%+v err=%v", replayed, err)
		}
	})

	t.Run("persisted operation tamper is rejected", func(t *testing.T) {
		a := newAcceptance("canonical-operation-tamper")
		accepted, err := adapter.Accept(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		key := prefix + "/v3/operations/" + accepted.Operation.ID
		response, err := client.Get(ctx, key)
		if err != nil || len(response.Kvs) != 1 {
			t.Fatalf("load operation err=%v records=%d", err, len(response.Kvs))
		}
		var value map[string]interface{}
		if err := json.Unmarshal(response.Kvs[0].Value, &value); err != nil {
			t.Fatal(err)
		}
		value["operation"].(map[string]interface{})["app"] = "tampered"
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Put(ctx, key, string(encoded)); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Resolve(ctx, a.Identity, a.Fingerprint); !errors.Is(err, store.ErrAcceptanceSignature) {
			t.Fatalf("tampered persisted operation resolved: %v", err)
		}
	})

	t.Run("persisted large JSON integer replays exactly", func(t *testing.T) {
		a := newAcceptance("canonical-large-integer")
		a.Operation.Payload["generation"] = json.Number("9007199254740993")
		var err error
		a.Fingerprint, err = store.CanonicalOperationRequestFingerprint(a)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Accept(ctx, a); err != nil {
			t.Fatal(err)
		}
		replayed, err := adapter.Resolve(ctx, a.Identity, a.Fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		value, ok := replayed.Operation.Payload["generation"].(json.Number)
		if !ok || value.String() != "9007199254740993" {
			t.Fatalf("replayed large integer=%#v", replayed.Operation.Payload["generation"])
		}
	})

	t.Run("stored identity tamper is rejected", func(t *testing.T) {
		a := newAcceptance("canonical-identity-tamper")
		if _, err := adapter.Accept(ctx, a); err != nil {
			t.Fatal(err)
		}
		identityBytes, err := json.Marshal(a.Identity)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(identityBytes)
		key := prefix + "/v3/acceptance/" + hex.EncodeToString(digest[:])
		response, err := client.Get(ctx, key)
		if err != nil || len(response.Kvs) != 1 {
			t.Fatalf("load acceptance err=%v records=%d", err, len(response.Kvs))
		}
		var value map[string]interface{}
		if err := json.Unmarshal(response.Kvs[0].Value, &value); err != nil {
			t.Fatal(err)
		}
		value["identity"].(map[string]interface{})["resource"] = "app/tampered"
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Put(ctx, key, string(encoded)); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Resolve(ctx, a.Identity, a.Fingerprint); !errors.Is(err, store.ErrAcceptanceSignature) {
			t.Fatalf("tampered stored identity resolved: %v", err)
		}
	})
}

// TestV3OperationStoreAppOperationLockEtcd proves the lease-backed part of the
// execution contract against etcd itself. In particular, a delayed release
// from an expired holder must not erase a newer holder, and a client that loses
// its lease is told through the lock context before it can terminalize work.
func TestV3OperationStoreAppOperationLockEtcd(t *testing.T) {
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
	prefix := "/norn-conf/v3-app-lock/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-app-lock-conformance-key")
	if err != nil {
		t.Fatal(err)
	}
	firstAuthority := uuid.NewString()
	first, err := etcdstore.NewV3OperationStore(client, prefix, firstAuthority, signer)
	if err != nil {
		t.Fatal(err)
	}
	second, err := etcdstore.NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	acceptance := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: firstAuthority, Actor: store.OperationActor{Issuer: "etcd-test", Subject: "worker"}, Kind: "app.deploy", Resource: "app/orders", Key: uuid.NewString()},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: "orders", Ref: "main", Risk: "low", Source: "etcd-test", MaxAttempts: 1},
		Audit:     store.AcceptanceAuditContext{Source: "etcd-test", RequestID: uuid.NewString(), Scopes: []string{"apps:write"}},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	_, claim, err := first.ClaimNextOperation(ctx, "lock-test-worker", time.Minute, nil)
	if err != nil || claim.OperationID() != acceptance.Operation.ID {
		t.Fatalf("claim err=%v claim=%+v", err, claim)
	}

	lock, acquired, err := first.AcquireAppOperationLock(ctx, "orders")
	if err != nil || !acquired || lock == nil {
		t.Fatalf("first acquire lock=%v acquired=%v err=%v", lock, acquired, err)
	}
	t.Cleanup(lock.Release)
	competing, acquired, err := second.AcquireAppOperationLock(ctx, "orders")
	if err != nil || acquired || competing != nil {
		if competing != nil {
			competing.Release()
		}
		t.Fatalf("competing acquire lock=%v acquired=%v err=%v", competing, acquired, err)
	}

	// appLockKey hashes the app to make arbitrary app names unambiguous in the
	// etcd hierarchy. Compute the same key here without depending on internals.
	digest := sha256.Sum256([]byte("orders"))
	key := prefix + "/v3/app-locks/" + hex.EncodeToString(digest[:])
	record, err := client.Get(ctx, key)
	if err != nil || len(record.Kvs) != 1 {
		t.Fatalf("leased lock record err=%v records=%d", err, len(record.Kvs))
	}
	if record.Kvs[0].Lease == 0 {
		t.Fatal("app lock record is not attached to an etcd lease")
	}
	if _, err := client.Revoke(ctx, clientv3.LeaseID(record.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lock.Context().Done():
		if !errors.Is(context.Cause(lock.Context()), store.ErrAppOperationLockLost) {
			t.Fatalf("lock cancellation cause=%v", context.Cause(lock.Context()))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expired lease did not cancel lock context")
	}

	replacement, acquired, err := second.AcquireAppOperationLock(ctx, "orders")
	if err != nil || !acquired || replacement == nil {
		t.Fatalf("replacement acquire lock=%v acquired=%v err=%v", replacement, acquired, err)
	}
	defer replacement.Release()
	lock.Release()
	third, acquired, err := first.AcquireAppOperationLock(ctx, "orders")
	if err != nil || acquired || third != nil {
		if third != nil {
			third.Release()
		}
		t.Fatalf("stale release erased replacement lock=%v acquired=%v err=%v", third, acquired, err)
	}
	if err := first.FinishClaimedOperationWithAppLock(ctx, claim, lock, model.OperationSucceeded, "must not commit", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("stale app-lock fence terminalized claimed operation: %v", err)
	}
}
