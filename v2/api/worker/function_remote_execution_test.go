package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

type functionRemoteAttempts struct {
	stages map[store.FunctionInvocationEffectAttemptStage]store.FunctionInvocationEffectAttempt
}

func (f *functionRemoteAttempts) RecordFunctionInvocationEffectStage(_ context.Context, _ store.OperationClaim, stage store.FunctionInvocationEffectAttemptStage, target, digest string) (store.FunctionInvocationEffectAttempt, error) {
	if f.stages == nil {
		f.stages = map[store.FunctionInvocationEffectAttemptStage]store.FunctionInvocationEffectAttempt{}
	}
	prior, exists := f.stages[stage]
	if exists && (prior.Target != target || prior.InputDigest != digest) {
		return prior, store.ErrFunctionInvocationEffectConflict
	}
	if !exists {
		prior = store.FunctionInvocationEffectAttempt{Target: target, InputDigest: digest}
		f.stages[stage] = prior
	}
	return prior, nil
}

func (f *functionRemoteAttempts) MarkFunctionInvocationEffectAttempt(_ context.Context, _ store.OperationClaim, stage store.FunctionInvocationEffectAttemptStage, target, digest string) (store.FunctionInvocationEffectAttempt, error) {
	prior, exists := f.stages[stage]
	if !exists || prior.Target != target || prior.InputDigest != digest {
		return prior, store.ErrFunctionInvocationEffectConflict
	}
	prior.MarkedNow = !prior.Attempted
	prior.Attempted = true
	f.stages[stage] = prior
	return prior, nil
}

type functionCompositeRemote struct {
	*functionVariableRemoteFake
	*functionJobRemoteFake
	terminal      nomad.FunctionInvocationTerminalObservation
	terminalCalls int
}

func (f *functionCompositeRemote) ObserveFunctionInvocationTerminal(_ context.Context, _ string, _ nomad.FunctionInvocationTerminalExpectation) (nomad.FunctionInvocationTerminalObservation, error) {
	f.terminalCalls++
	return f.terminal, nil
}

func TestRunFunctionInvocationRemoteWaitsForExactAllocationThenReturnsTerminal(t *testing.T) {
	identity, job, err := BuildFunctionInvocationJobPlan(testFunctionJobPlanInput(), "./function", 250, 192)
	if err != nil {
		t.Fatal(err)
	}
	claim := functionVariableStepClaim(t, identity)
	attempts := &functionRemoteAttempts{}
	remote := &functionCompositeRemote{functionVariableRemoteFake: &functionVariableRemoteFake{}, functionJobRemoteFake: &functionJobRemoteFake{}}
	private := []byte(`{"body":"private","method":"POST","path":"/"}`)
	_, err = RunFunctionInvocationRemote(context.Background(), attempts, remote, claim, identity, job, private)
	if !errors.Is(err, effect.ErrEffectPending) || remote.functionVariableRemoteFake.creates != 1 || remote.functionJobRemoteFake.creates != 1 || remote.terminalCalls != 0 {
		t.Fatalf("initial attempt: err=%v variableCreates=%d jobCreates=%d terminalCalls=%d", err, remote.functionVariableRemoteFake.creates, remote.functionJobRemoteFake.creates, remote.terminalCalls)
	}
	remote.functionJobRemoteFake.observation.AllocationIDs = []string{"alloc-1"}
	exitCode := 0
	remote.terminal = nomad.FunctionInvocationTerminalObservation{State: nomad.FunctionInvocationTerminalComplete, AllocationID: "alloc-1", ExitCode: &exitCode, StartedAt: time.Unix(100, 0).UTC(), Duration: 3 * time.Second}
	got, err := RunFunctionInvocationRemote(context.Background(), attempts, remote, claim, identity, job, private)
	if err != nil || got.State != nomad.FunctionInvocationTerminalComplete || remote.functionVariableRemoteFake.creates != 1 || remote.functionJobRemoteFake.creates != 1 || remote.terminalCalls != 1 {
		t.Fatalf("recovery: result=%+v err=%v variableCreates=%d jobCreates=%d terminalCalls=%d", got, err, remote.functionVariableRemoteFake.creates, remote.functionJobRemoteFake.creates, remote.terminalCalls)
	}
}

func TestRunFunctionInvocationRemoteNeverSubmitsJobWithoutPrivateVariableProof(t *testing.T) {
	identity, job, err := BuildFunctionInvocationJobPlan(testFunctionJobPlanInput(), "./function", 250, 192)
	if err != nil {
		t.Fatal(err)
	}
	remote := &functionCompositeRemote{functionVariableRemoteFake: &functionVariableRemoteFake{lookupError: errors.New("unavailable")}, functionJobRemoteFake: &functionJobRemoteFake{}}
	_, err = RunFunctionInvocationRemote(context.Background(), &functionRemoteAttempts{}, remote, functionVariableStepClaim(t, identity), identity, job, []byte(`{"body":"`+strings.Repeat("x", 4)+`"}`))
	if !errors.Is(err, effect.ErrEffectPending) || remote.functionJobRemoteFake.creates != 0 {
		t.Fatalf("unproven variable reached job submit: err=%v creates=%d", err, remote.functionJobRemoteFake.creates)
	}
}
