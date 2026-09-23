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
