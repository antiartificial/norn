package etcdstore_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type lostCommitClient struct {
	*clientv3.Client
	loseNext bool
}

func (client *lostCommitClient) Txn(ctx context.Context) clientv3.Txn {
	return &lostCommitTxn{Txn: client.Client.Txn(ctx), client: client}
}

type lostCommitTxn struct {
	clientv3.Txn
	client *lostCommitClient
}

func (txn *lostCommitTxn) If(cmps ...clientv3.Cmp) clientv3.Txn {
	txn.Txn = txn.Txn.If(cmps...)
	return txn
}

func (txn *lostCommitTxn) Then(ops ...clientv3.Op) clientv3.Txn {
	txn.Txn = txn.Txn.Then(ops...)
	return txn
}

func (txn *lostCommitTxn) Commit() (*clientv3.TxnResponse, error) {
	response, err := txn.Txn.Commit()
	if err == nil && txn.client.loseNext {
		txn.client.loseNext = false
		return nil, context.DeadlineExceeded
	}
	return response, err
}

func TestV3OperationAcceptanceResolvesLostCommitResponseEtcd(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoint == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoint, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-conf/v3-lost-commit/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-lost-commit-integration-key")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	adapter, err := etcdstore.NewV3OperationStoreWithPolicy(&lostCommitClient{Client: client, loseNext: true}, prefix, authority, signer, store.AcceptancePolicy{ReplayTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: "app.preflight", Resource: "app/example", Key: uuid.NewString()},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: "example", Status: model.OperationSucceeded, Source: "test", Risk: "read-only", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}},
		Audit:     store.AcceptanceAuditContext{Source: "test"},
	}
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := adapter.Accept(context.Background(), request)
	if err != nil {
		t.Fatalf("committed acceptance was not resolved: %v", err)
	}
	if !accepted.Replayed || accepted.Operation.ID != request.Operation.ID {
		t.Fatalf("resolved operation id=%q replayed=%v", accepted.Operation.ID, accepted.Replayed)
	}
	replayed, err := adapter.Accept(context.Background(), request)
	if err != nil || !replayed.Replayed || replayed.Operation.ID != accepted.Operation.ID {
		t.Fatalf("same-identity replay=%+v err=%v", replayed, err)
	}
	records, err := client.Get(context.Background(), prefix+"/v3/acceptance/", clientv3.WithPrefix())
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, entry := range records.Kvs {
		if strings.HasSuffix(string(entry.Key), "/replay-live") {
			live++
			if string(entry.Value) != store.OperationReplayContractVersion || entry.Lease == 0 {
				t.Fatalf("replay marker at %q is not lease-backed", entry.Key)
			}
		}
	}
	if live != 1 {
		t.Fatalf("replay live markers=%d, want one retained marker", live)
	}
}
