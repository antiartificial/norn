package etcdstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

const functionInvocationEffectAttemptDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func functionInvocationEffectAttemptStore(t *testing.T) (*V3OperationStore, *clientv3.Client, string) {
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
	prefix := "/norn-conf/v3-function-effects/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-function-effects-signing-key")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, client, prefix
}

func claimFunctionInvocationEffectAttempt(t *testing.T, adapter *V3OperationStore, worker string, lease time.Duration) (model.Operation, store.OperationClaim) {
	t.Helper()
	keys, err := store.NewPrivateInvocationKeyRing("invocation-1", map[string][]byte{"invocation-1": bytes.Repeat([]byte{0x41}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	request := privateInvocationAcceptance(t, adapter.authority, uuid.NewString())
	request.Operation.MaxAttempts = 3
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := adapter.AcceptPrivateInvocation(context.Background(), request, store.PrivateInvocationInput{Body: "private-body"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	op, claim, err := adapter.ClaimNextOperation(context.Background(), worker, lease, []string{store.PrivateInvocationOperationKind})
	if err != nil || op == nil || op.ID != accepted.Operation.ID {
		t.Fatalf("claim op=%+v claim=%+v err=%v", op, claim, err)
	}
	return *op, claim
}

func TestV3FunctionInvocationEffectAttemptRecordsMarksAndLoadsEtcd(t *testing.T) {
	adapter, client, prefix := functionInvocationEffectAttemptStore(t)
	_, claim := claimFunctionInvocationEffectAttempt(t, adapter, "function-worker", time.Minute)
	target := "norn/function-invocation/0123456789abcdef"

	recorded, err := adapter.RecordFunctionInvocationEffectStage(context.Background(), claim, store.FunctionInvocationVariableAttempt, target, functionInvocationEffectAttemptDigest)
	if err != nil || recorded.Attempted || recorded.OperationID != claim.OperationID() || recorded.MarkedNow {
		t.Fatalf("recorded=%+v err=%v", recorded, err)
	}
	attempted, err := adapter.MarkFunctionInvocationEffectAttempt(context.Background(), claim, store.FunctionInvocationVariableAttempt, target, functionInvocationEffectAttemptDigest)
	if err != nil || !attempted.Attempted || !attempted.MarkedNow || attempted.AttemptedAt == nil || attempted.ClaimGeneration != claim.Generation() {
		t.Fatalf("attempted=%+v err=%v", attempted, err)
	}
	replayed, err := adapter.MarkFunctionInvocationEffectAttempt(context.Background(), claim, store.FunctionInvocationVariableAttempt, target, functionInvocationEffectAttemptDigest)
	if err != nil || !replayed.Attempted || replayed.MarkedNow || replayed.AttemptedAt == nil || !replayed.AttemptedAt.Equal(*attempted.AttemptedAt) {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	loaded, err := adapter.LoadFunctionInvocationEffectAttempt(context.Background(), claim.OperationID(), store.FunctionInvocationVariableAttempt)
	if err != nil || loaded == nil || loaded.MarkedNow || !loaded.Attempted || loaded.Target != target {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	values, err := client.Get(context.Background(), prefix+"/v3/function-invocation-effect-attempts/", clientv3.WithPrefix())
	if err != nil || len(values.Kvs) != 1 || bytes.Contains(values.Kvs[0].Value, []byte("private-body")) || bytes.Contains(values.Kvs[0].Value, []byte("envelope")) {
		t.Fatalf("public effect values=%d err=%v", len(values.Kvs), err)
	}
}

func TestV3FunctionInvocationEffectAttemptAllowsOneConcurrentRemoteCallerEtcd(t *testing.T) {
	adapter, client, prefix := functionInvocationEffectAttemptStore(t)
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-function-effects-second-client-key")
	if err != nil {
		t.Fatal(err)
	}
	secondClient, err := clientv3.New(clientv3.Config{Endpoints: client.Endpoints(), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondClient.Close() })
	second, err := NewV3OperationStore(secondClient, prefix, adapter.authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	_, claim := claimFunctionInvocationEffectAttempt(t, adapter, "function-worker", time.Minute)
	target := "norn/function-invocation/concurrent"
	if _, err := adapter.RecordFunctionInvocationEffectStage(context.Background(), claim, store.FunctionInvocationVariableAttempt, target, functionInvocationEffectAttemptDigest); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan store.FunctionInvocationEffectAttempt, 2)
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, current := range []*V3OperationStore{adapter, second} {
		wait.Add(1)
		go func(current *V3OperationStore) {
			defer wait.Done()
			<-start
			result, err := current.MarkFunctionInvocationEffectAttempt(context.Background(), claim, store.FunctionInvocationVariableAttempt, target, functionInvocationEffectAttemptDigest)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(current)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	markedNow := 0
	for result := range results {
		if !result.Attempted {
			t.Fatalf("result=%+v", result)
		}
		if result.MarkedNow {
			markedNow++
		}
	}
	if markedNow != 1 {
		t.Fatalf("remote callers=%d, want 1", markedNow)
	}
}

func TestV3FunctionInvocationEffectAttemptRejectsMismatchesAndFencesSuccessorEtcd(t *testing.T) {
	adapter, client, _ := functionInvocationEffectAttemptStore(t)
	_, first := claimFunctionInvocationEffectAttempt(t, adapter, "function-one", 100*time.Millisecond)
	target := "norn/function-invocation/successor"
	if _, err := adapter.MarkFunctionInvocationEffectAttempt(context.Background(), first, store.FunctionInvocationJobAttempt, target, functionInvocationEffectAttemptDigest); !errors.Is(err, store.ErrFunctionInvocationEffectMissing) {
		t.Fatalf("missing record error=%v", err)
	}
	if _, err := adapter.RecordFunctionInvocationEffectStage(context.Background(), first, store.FunctionInvocationVariableAttempt, target, functionInvocationEffectAttemptDigest); err != nil {
		t.Fatal(err)
	}
	changedDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if stored, err := adapter.RecordFunctionInvocationEffectStage(context.Background(), first, store.FunctionInvocationVariableAttempt, target, changedDigest); !errors.Is(err, store.ErrFunctionInvocationEffectConflict) || stored.InputDigest != functionInvocationEffectAttemptDigest {
		t.Fatalf("changed record=%+v err=%v", stored, err)
	}
	if stored, err := adapter.MarkFunctionInvocationEffectAttempt(context.Background(), first, store.FunctionInvocationVariableAttempt, "norn/function-invocation/changed", functionInvocationEffectAttemptDigest); !errors.Is(err, store.ErrFunctionInvocationEffectConflict) || stored.Target != target {
		t.Fatalf("changed mark=%+v err=%v", stored, err)
	}
	if err := adapter.RetryClaimedOperation(context.Background(), first, "retry after worker handoff", "handoff", time.Now().Add(-time.Second), nil); err != nil {
		t.Fatal(err)
	}
	_, successor, err := adapter.ClaimNextOperation(context.Background(), "function-two", time.Minute, []string{store.PrivateInvocationOperationKind})
	if err != nil || successor.Generation() != first.Generation()+1 {
		t.Fatalf("successor=%+v err=%v", successor, err)
	}
	if _, err := adapter.MarkFunctionInvocationEffectAttempt(context.Background(), first, store.FunctionInvocationVariableAttempt, target, functionInvocationEffectAttemptDigest); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("stale mark error=%v", err)
	}
	attempted, err := adapter.MarkFunctionInvocationEffectAttempt(context.Background(), successor, store.FunctionInvocationVariableAttempt, target, functionInvocationEffectAttemptDigest)
	if err != nil || !attempted.MarkedNow || attempted.ClaimGeneration != successor.Generation() {
		t.Fatalf("successor attempt=%+v err=%v", attempted, err)
	}
	// Explicitly revoke the replacement lease to prove the owner-key compare
	// fences even a caller that still holds the old claim value.
	owner, err := client.Get(context.Background(), adapter.ownerKey(successor.OperationID()))
	if err != nil || len(owner.Kvs) != 1 {
		t.Fatalf("owner=%+v err=%v", owner, err)
	}
	if _, err := client.Revoke(context.Background(), clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.RecordFunctionInvocationEffectStage(context.Background(), successor, store.FunctionInvocationJobAttempt, target, functionInvocationEffectAttemptDigest); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("revoked owner record error=%v", err)
	}
}
