package etcdstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

type ambiguousAcceptanceKV struct {
	clientv3.KV
	clientv3.Lease
	grantCount  int
	revokeCount int
}

func (*ambiguousAcceptanceKV) Get(context.Context, string, ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	return &clientv3.GetResponse{}, nil
}

func (*ambiguousAcceptanceKV) Txn(context.Context) clientv3.Txn {
	return failedCommitTxn{err: context.DeadlineExceeded}
}

func (kv *ambiguousAcceptanceKV) Grant(context.Context, int64) (*clientv3.LeaseGrantResponse, error) {
	kv.grantCount++
	return &clientv3.LeaseGrantResponse{ID: 42, TTL: 60}, nil
}

func (kv *ambiguousAcceptanceKV) Revoke(context.Context, clientv3.LeaseID) (*clientv3.LeaseRevokeResponse, error) {
	kv.revokeCount++
	return &clientv3.LeaseRevokeResponse{}, nil
}

func TestAmbiguousAcceptanceKeepsReplayLease(t *testing.T) {
	kv := &ambiguousAcceptanceKV{}
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-ambiguous-acceptance-test-key")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	s, err := NewV3OperationStoreWithPolicy(kv, "/test", authority, signer, store.AcceptancePolicy{ReplayTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: "app.preflight", Resource: "app/example", Key: uuid.NewString()},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: "example", Status: model.OperationSucceeded, Source: "test", Risk: "read-only", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}},
		Audit:     store.AcceptanceAuditContext{Source: "test"},
	}
	a.Fingerprint, err = store.CanonicalOperationRequestFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Accept(context.Background(), a)
	if !errors.Is(err, store.ErrAcceptanceIndeterminate) {
		t.Fatalf("accept err=%v, want indeterminate", err)
	}
	if kv.grantCount != 1 || kv.revokeCount != 0 {
		t.Fatalf("grants=%d revokes=%d, want one grant and no revoke after ambiguous commit", kv.grantCount, kv.revokeCount)
	}
}
