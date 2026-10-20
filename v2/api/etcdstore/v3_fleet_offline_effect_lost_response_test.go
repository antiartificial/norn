package etcdstore

import (
	"context"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type loseEffectCommitKV struct {
	clientv3.KV
	target string
	lost   bool
	cancel context.CancelFunc
}

func (kv *loseEffectCommitKV) Txn(ctx context.Context) clientv3.Txn {
	return &loseEffectCommitTxn{Txn: kv.KV.Txn(ctx), owner: kv}
}

type loseEffectCommitTxn struct {
	clientv3.Txn
	owner     *loseEffectCommitKV
	targetPut bool
}

func (txn *loseEffectCommitTxn) If(cmps ...clientv3.Cmp) clientv3.Txn {
	txn.Txn = txn.Txn.If(cmps...)
	return txn
}
func (txn *loseEffectCommitTxn) Then(ops ...clientv3.Op) clientv3.Txn {
	for _, op := range ops {
		if op.IsPut() && string(op.KeyBytes()) == txn.owner.target {
			txn.targetPut = true
		}
	}
	txn.Txn = txn.Txn.Then(ops...)
	return txn
}
func (txn *loseEffectCommitTxn) Commit() (*clientv3.TxnResponse, error) {
	response, err := txn.Txn.Commit()
	if err == nil && txn.targetPut && !txn.owner.lost {
		txn.owner.lost = true
		if txn.owner.cancel != nil {
			txn.owner.cancel()
		}
		return nil, context.Canceled
	}
	return response, err
}

func TestSignedEffectGrantRecoversCommittedLostResponse(t *testing.T) {
	h, b, a, c, envelope, now := signedEffectIssueFixture(t)
	if _, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, envelope, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx())
	defer cancel()
	wrapper := &loseEffectCommitKV{KV: h.s.kv, target: h.s.offlineSignedEffectGrantProofKey(b.PlanID), cancel: cancel}
	h.s.kv = wrapper
	grant, err := h.s.issueOfflineEffectGrantFromAcceptedWithClock(ctx, b.PlanID, a.ID, c.ID, offlineEffectIdentity(b, a), func() time.Time { return now.Add(time.Millisecond) })
	if err != nil || !wrapper.lost || grant.ID == "" {
		t.Fatalf("ambiguous grant response: %+v %v lost=%v", grant, err, wrapper.lost)
	}
}

func TestSignedEffectConsumeRecoversCommittedLostResponse(t *testing.T) {
	h, b, a, grant, c, _, now := signedConsumeFixture(t)
	ctx, cancel := context.WithCancel(h.ctx())
	defer cancel()
	wrapper := &loseEffectCommitKV{KV: h.s.kv, target: h.s.offlineSignedEffectConsumeProofKey(b.PlanID), cancel: cancel}
	h.s.kv = wrapper
	consumed, err := h.s.consumeOfflineEffectGrantFromAcceptedWithClock(ctx, b.PlanID, a.ID, grant.ID, c.ID, offlineEffectIdentity(b, a), func() time.Time { return now.Add(time.Millisecond) })
	if err != nil || !wrapper.lost || consumed.Phase != "consumed" {
		t.Fatalf("ambiguous consume response: %+v %v lost=%v", consumed, err, wrapper.lost)
	}
}
