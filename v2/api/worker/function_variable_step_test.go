package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

type functionVariableAttemptsFake struct {
	mu        sync.Mutex
	recorded  bool
	attempted bool
	target    string
	digest    string
	marks     int
	err       error
}

func (f *functionVariableAttemptsFake) RecordFunctionInvocationEffectStage(_ context.Context, _ store.OperationClaim, stage store.FunctionInvocationEffectAttemptStage, target, digest string) (store.FunctionInvocationEffectAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return store.FunctionInvocationEffectAttempt{}, f.err
	}
	if stage != store.FunctionInvocationVariableAttempt {
		return store.FunctionInvocationEffectAttempt{}, errors.New("wrong stage")
	}
	if f.recorded && (f.target != target || f.digest != digest) {
		return store.FunctionInvocationEffectAttempt{}, store.ErrFunctionInvocationEffectConflict
	}
	f.recorded, f.target, f.digest = true, target, digest
	return store.FunctionInvocationEffectAttempt{Target: target, InputDigest: digest, Attempted: f.attempted}, nil
}

func (f *functionVariableAttemptsFake) MarkFunctionInvocationEffectAttempt(_ context.Context, _ store.OperationClaim, _ store.FunctionInvocationEffectAttemptStage, target, digest string) (store.FunctionInvocationEffectAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return store.FunctionInvocationEffectAttempt{}, f.err
	}
	if !f.recorded || target != f.target || digest != f.digest {
		return store.FunctionInvocationEffectAttempt{}, store.ErrFunctionInvocationEffectConflict
	}
	markedNow := !f.attempted
	f.attempted = true
	f.marks++
	return store.FunctionInvocationEffectAttempt{Target: target, InputDigest: digest, Attempted: true, MarkedNow: markedNow}, nil
}

type functionVariableRemoteFake struct {
	mu          sync.Mutex
	owner       string
	path        string
	private     []byte
	creates     int
	createError error
	lookupError error
}

func (f *functionVariableRemoteFake) LookupFunctionInvocationVariable(_ context.Context, _ string, _ nomad.FunctionInvocationVariableIdentity) (nomad.FunctionInvocationVariableObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookupError != nil {
		return nomad.FunctionInvocationVariableObservation{State: nomad.FunctionInvocationVariableIndeterminate}, f.lookupError
	}
	if f.path == "" {
		return nomad.FunctionInvocationVariableObservation{State: nomad.FunctionInvocationVariableNotFound}, nil
	}
	return nomad.FunctionInvocationVariableObservation{State: nomad.FunctionInvocationVariableFound, Path: f.path, OwnerMarker: f.owner, PrivateContent: bytes.Clone(f.private)}, nil
}

func (f *functionVariableRemoteFake) CreateFunctionInvocationVariable(_ context.Context, _ string, identity nomad.FunctionInvocationVariableIdentity, private []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if f.path != "" {
		return nomad.ErrFunctionVariableCreateConflict
	}
	f.path, f.owner, f.private = identity.Path, identity.OwnerMarker, bytes.Clone(private)
	return f.createError
}

func functionVariableStepClaim(t *testing.T, identity FunctionInvocationJobIdentity) store.OperationClaim {
	t.Helper()
	claim, err := store.NewOperationClaim(identity.OperationID, "worker-one", 1)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestEnsureFunctionInvocationVariableRecoversLostWriteResponseWithoutRecreate(t *testing.T) {
	identity := functionIdentity(t)
	private := []byte("private-request\x00and-db-secret")
	attempts := &functionVariableAttemptsFake{}
	remote := &functionVariableRemoteFake{createError: fmt.Errorf("opaque lost response: %s", private)}
	claim := functionVariableStepClaim(t, identity)
	for i := 0; i < 2; i++ {
		decision, err := EnsureFunctionInvocationVariable(context.Background(), attempts, remote, claim, "global", identity, private)
		if err != nil || decision.Action != FunctionVariableRecovered {
			t.Fatalf("run %d: decision=%+v err=%v", i, decision, err)
		}
		if strings.Contains(fmt.Sprint(decision), string(private)) {
			t.Fatal("private content escaped into decision")
		}
	}
	if remote.creates != 1 || attempts.marks != 1 || !strings.HasPrefix(attempts.digest, "sha256:") || strings.Contains(attempts.digest, string(private)) {
		t.Fatalf("creates=%d marks=%d digest=%q", remote.creates, attempts.marks, attempts.digest)
	}
}

func TestEnsureFunctionInvocationVariablePostAttemptAbsenceIsUnresolved(t *testing.T) {
	identity := functionIdentity(t)
	attempts := &functionVariableAttemptsFake{}
	remote := &functionVariableRemoteFake{}
	// First create, then simulate a lost remote variable while preserving the
	// durable attempted stage.
	decision, err := EnsureFunctionInvocationVariable(context.Background(), attempts, remote, functionVariableStepClaim(t, identity), "global", identity, []byte("private"))
	if err != nil || decision.Action != FunctionVariableRecovered {
		t.Fatalf("first step=%+v err=%v", decision, err)
	}
	remote.mu.Lock()
	remote.path, remote.owner, remote.private = "", "", nil
	remote.mu.Unlock()
	decision, err = EnsureFunctionInvocationVariable(context.Background(), attempts, remote, functionVariableStepClaim(t, identity), "global", identity, []byte("private"))
	if err != nil || decision.Action != FunctionVariableUnresolved || remote.creates != 1 {
		t.Fatalf("after absent read=%+v err=%v creates=%d", decision, err, remote.creates)
	}
}

func TestEnsureFunctionInvocationVariableRejectsWrongOwnerAndClaim(t *testing.T) {
	identity := functionIdentity(t)
	remote := &functionVariableRemoteFake{path: identity.VariablePath, owner: "someone-else", private: []byte("private")}
	attempts := &functionVariableAttemptsFake{}
	claim := functionVariableStepClaim(t, identity)
	decision, err := EnsureFunctionInvocationVariable(context.Background(), attempts, remote, claim, "global", identity, []byte("private"))
	if err != nil || decision.Action != FunctionVariableUnresolved || remote.creates != 0 {
		t.Fatalf("wrong owner=%+v err=%v creates=%d", decision, err, remote.creates)
	}
	stale, err := store.NewOperationClaim("other-operation", "worker-one", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureFunctionInvocationVariable(context.Background(), attempts, remote, stale, "global", identity, []byte("private")); err == nil {
		t.Fatal("mismatched claim was accepted")
	}
}

func TestEnsureFunctionInvocationVariableConcurrentCallersCreateOnce(t *testing.T) {
	identity := functionIdentity(t)
	attempts := &functionVariableAttemptsFake{}
	remote := &functionVariableRemoteFake{}
	claim := functionVariableStepClaim(t, identity)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			decision, err := EnsureFunctionInvocationVariable(context.Background(), attempts, remote, claim, "global", identity, []byte("private"))
			if err == nil && decision.Action != FunctionVariableRecovered && decision.Action != FunctionVariableUnresolved {
				err = fmt.Errorf("unexpected action %q", decision.Action)
			}
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if remote.creates != 1 {
		t.Fatalf("remote creates=%d, want one", remote.creates)
	}
}
