package worker

import (
	"context"
	"errors"
	"testing"

	"norn/v2/api/nomad"
)

type cleanupStoreFake struct {
	intent    *FunctionInvocationCleanup
	completed int
}

func (s *cleanupStoreFake) ClaimFunctionInvocationCleanup(context.Context) (*FunctionInvocationCleanup, error) {
	return s.intent, nil
}
func (s *cleanupStoreFake) CompleteFunctionInvocationVariableCleanup(_ context.Context, got FunctionInvocationCleanup) error {
	if s.intent == nil || got.Token != s.intent.Token {
		return errors.New("wrong cleanup token")
	}
	s.completed++
	return nil
}

type cleanupRemoteFake struct {
	observation nomad.FunctionInvocationVariableObservation
	deleteErr   error
	deletes     int
	deleteIndex uint64
}

func (r *cleanupRemoteFake) LookupFunctionInvocationVariable(context.Context, string, nomad.FunctionInvocationVariableIdentity) (nomad.FunctionInvocationVariableObservation, error) {
	return r.observation, nil
}
func (r *cleanupRemoteFake) DeleteFunctionInvocationVariable(_ context.Context, _ string, _ nomad.FunctionInvocationVariableIdentity, index uint64) error {
	r.deletes++
	r.deleteIndex = index
	return r.deleteErr
}

func cleanupFixture(state nomad.FunctionInvocationVariableState) (*FunctionInvocationCleanupConsumer, *cleanupStoreFake, *cleanupRemoteFake) {
	op := "op-123"
	identity := nomad.FunctionInvocationVariableIdentity{Path: "nomad/jobs/norn-fn-0123456789abcdef0123456789abcdef01234567/invoke", OwnerMarker: "norn.function-invoke/" + op}
	store := &cleanupStoreFake{intent: &FunctionInvocationCleanup{OperationID: op, Variable: identity, Token: "claim-1"}}
	remote := &cleanupRemoteFake{observation: nomad.FunctionInvocationVariableObservation{State: state, Path: identity.Path, OwnerMarker: identity.OwnerMarker, ModifyIndex: 42}}
	return &FunctionInvocationCleanupConsumer{Store: store, Remote: remote}, store, remote
}

func TestFunctionInvocationCleanupDeletesExactOwnedRevisionThenCompletes(t *testing.T) {
	consumer, store, remote := cleanupFixture(nomad.FunctionInvocationVariableFound)
	if err := consumer.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if remote.deletes != 1 || remote.deleteIndex != 42 || store.completed != 1 {
		t.Fatalf("deletes=%d index=%d completed=%d", remote.deletes, remote.deleteIndex, store.completed)
	}
}

func TestFunctionInvocationCleanupCompletesConclusiveAbsence(t *testing.T) {
	consumer, store, remote := cleanupFixture(nomad.FunctionInvocationVariableNotFound)
	if err := consumer.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if remote.deletes != 0 || store.completed != 1 {
		t.Fatalf("deletes=%d completed=%d", remote.deletes, store.completed)
	}
}

func TestFunctionInvocationCleanupRefusesWrongOwner(t *testing.T) {
	consumer, store, remote := cleanupFixture(nomad.FunctionInvocationVariableFound)
	remote.observation.OwnerMarker = "another-operation"
	if err := consumer.RunOnce(context.Background()); !errors.Is(err, ErrFunctionInvocationCleanupConflict) {
		t.Fatalf("error = %v", err)
	}
	if remote.deletes != 0 || store.completed != 0 {
		t.Fatalf("deletes=%d completed=%d", remote.deletes, store.completed)
	}
}

func TestFunctionInvocationCleanupDoesNotCompleteAmbiguousDelete(t *testing.T) {
	consumer, store, remote := cleanupFixture(nomad.FunctionInvocationVariableFound)
	remote.deleteErr = nomad.ErrFunctionVariableDeleteIndeterminate
	if err := consumer.RunOnce(context.Background()); !errors.Is(err, nomad.ErrFunctionVariableDeleteIndeterminate) {
		t.Fatalf("error = %v", err)
	}
	if remote.deletes != 1 || store.completed != 0 {
		t.Fatalf("deletes=%d completed=%d", remote.deletes, store.completed)
	}
}
