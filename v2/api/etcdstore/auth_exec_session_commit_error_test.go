package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

type failedCommitKV struct {
	clientv3.KV
	value []byte
	err   error
}

func (kv failedCommitKV) Get(context.Context, string, ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	return &clientv3.GetResponse{Kvs: []*mvccpb.KeyValue{{Value: kv.value, ModRevision: 1}}}, nil
}

func (kv failedCommitKV) Txn(context.Context) clientv3.Txn {
	return failedCommitTxn{err: kv.err}
}

type failedCommitTxn struct {
	clientv3.Txn
	err error
}

func (txn failedCommitTxn) If(...clientv3.Cmp) clientv3.Txn  { return txn }
func (txn failedCommitTxn) Then(...clientv3.Op) clientv3.Txn { return txn }
func (txn failedCommitTxn) Commit() (*clientv3.TxnResponse, error) {
	return nil, txn.err
}

func TestOwnedExecSessionCommitErrorDoesNotPanic(t *testing.T) {
	commitErr := errors.New("etcd quorum unavailable")
	until := time.Now().Add(time.Minute)
	value, err := json.Marshal(storedSession{
		ExecSession:   store.ExecSession{ID: "session", Status: "running", ExpiresAt: until},
		OwnerIDStored: "owner", OwnerTokenStored: "token", OwnerLeaseUntil: &until,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := NewAuthStore(failedCommitKV{value: value, err: commitErr}, "/test")
	for name, run := range map[string]func() (bool, error){
		"renew": func() (bool, error) {
			return s.RenewExecSession(context.Background(), "session", "owner", "token", time.Minute)
		},
		"finish": func() (bool, error) {
			return s.FinishOwnedExecSession(context.Background(), "session", "owner", "token", "completed", nil, "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			committed, err := run()
			if committed || !errors.Is(err, commitErr) {
				t.Fatalf("committed=%v err=%v, want false and transaction error", committed, err)
			}
		})
	}
}
