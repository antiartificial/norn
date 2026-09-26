package etcdstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func privateInvocationEtcdStore(t *testing.T) (*V3OperationStore, *clientv3.Client, string) {
	t.Helper()
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-conf/v3-private-invocation/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-private-invocation-signing-key")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, client, prefix
}

func privateInvocationAcceptance(t *testing.T, authority, key string) store.OperationAcceptance {
	t.Helper()
	request := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: store.PrivateInvocationOperationKind, Resource: "app/demo/process/resize", Key: key},
		Operation: model.Operation{ID: uuid.NewString(), Kind: store.PrivateInvocationOperationKind, App: "demo", Ref: "main", Source: "test", Risk: "write", Payload: map[string]interface{}{"process": "resize", "imageTag": "image@sha256:abcdef"}},
		Audit:     store.AcceptanceAuditContext{Source: "test", Scopes: []string{"functions:invoke"}},
	}
	var err error
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestV3PrivateInvocationAtomicAcceptanceAndReplayEtcd(t *testing.T) {
	adapter, client, prefix := privateInvocationEtcdStore(t)
	ctx := context.Background()
	keys, err := store.NewPrivateInvocationKeyRing("invocation-1", map[string][]byte{"invocation-1": bytes.Repeat([]byte{0x77}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	request := privateInvocationAcceptance(t, adapter.authority, "invoke-once")
	private := store.PrivateInvocationInput{Body: "secret-body-canary", Method: "POST", Path: "/private-path-canary"}
	first, err := adapter.AcceptPrivateInvocation(ctx, request, private, keys)
	if err != nil || first.Replayed {
		t.Fatalf("first accepted=%+v err=%v", first, err)
	}
	if first.Operation.Payload["privateRecordId"] != first.Operation.ID || first.Operation.Payload["privateKeyId"] != "invocation-1" {
		t.Fatalf("private descriptor=%+v", first.Operation.Payload)
	}
	all, err := client.Get(ctx, prefix+"/v3/", clientv3.WithPrefix())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range all.Kvs {
		if bytes.Contains(value.Value, []byte(private.Body)) || bytes.Contains(value.Value, []byte(private.Path)) {
			t.Fatalf("plaintext private request appeared in etcd key %q", value.Key)
		}
	}
	needed, err := adapter.RequiredPrivateInvocationKeys(ctx)
	if err != nil || len(needed) != 1 || needed[0] != "invocation-1" {
		t.Fatalf("required keys=%v err=%v", needed, err)
	}
	opened, err := adapter.OpenPrivateInvocation(ctx, first.Operation, keys)
	if err != nil || opened != private {
		t.Fatalf("opened=%+v err=%v", opened, err)
	}
	replay := request
	replay.Operation.ID, replay.Operation.SagaID = uuid.NewString(), uuid.NewString()
	second, err := adapter.AcceptPrivateInvocation(ctx, replay, private, keys)
	if err != nil || !second.Replayed || second.Operation.ID != first.Operation.ID {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
	if _, err := adapter.AcceptPrivateInvocation(ctx, replay, store.PrivateInvocationInput{Body: "different"}, keys); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("changed private request err=%v, want conflict", err)
	}
	replay.Operation.Payload = map[string]interface{}{"process": "different", "imageTag": "image@sha256:abcdef"}
	if _, err := adapter.AcceptPrivateInvocation(ctx, replay, private, keys); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("changed public request err=%v, want conflict", err)
	}
	if _, err := adapter.Accept(ctx, request); !errors.Is(err, store.ErrAcceptanceInvalid) {
		t.Fatalf("generic acceptance accepted private invocation: %v", err)
	}
}

func TestV3PrivateInvocationOperationConflictLeavesNoPrivateRecordEtcd(t *testing.T) {
	adapter, client, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	keys, _ := store.NewPrivateInvocationKeyRing("invocation-1", map[string][]byte{"invocation-1": bytes.Repeat([]byte{0x77}, 32)})
	request := privateInvocationAcceptance(t, adapter.authority, "invoke-operation-conflict")
	if _, err := client.Put(ctx, adapter.opKey(request.Operation.ID), `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.AcceptPrivateInvocation(ctx, request, store.PrivateInvocationInput{Body: "private"}, keys); !errors.Is(err, store.ErrAcceptanceIndeterminate) {
		t.Fatalf("conflicting operation err=%v, want indeterminate", err)
	}
	for _, key := range []string{adapter.acceptanceKey(request.Identity), adapter.privateInvocationKey(request.Operation.ID), adapter.privateInvocationAcceptanceIndexKey(request.Operation.ID)} {
		response, err := client.Get(ctx, key)
		if err != nil || len(response.Kvs) != 0 {
			t.Fatalf("atomic rejection left key=%q records=%d err=%v", key, len(response.Kvs), err)
		}
	}
}
