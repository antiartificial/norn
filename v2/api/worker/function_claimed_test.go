package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

const claimedFunctionPrivateCanary = "NORN_CLAIMED_FUNCTION_PRIVATE_9f3d"

type claimedFunctionVerifierFake struct {
	input FunctionInvocationEffectInput
	err   error
	calls int
}

func (f *claimedFunctionVerifierFake) VerifyClaimedPrivateInvocation(context.Context, model.Operation) (store.VerifiedPrivateInvocation, error) {
	f.calls++
	return store.VerifiedPrivateInvocation{Authority: f.input.Authority, Operation: *claimedFunctionOperation(f.input)}, f.err
}

type claimedFunctionPrivateFake struct {
	input store.PrivateInvocationInput
	err   error
	calls int
}

func (f *claimedFunctionPrivateFake) OpenPrivateInvocation(context.Context, model.Operation, *store.PrivateInvocationKeyRing) (store.PrivateInvocationInput, error) {
	f.calls++
	return f.input, f.err
}

type claimedFunctionRuntimeFake struct {
	runtime ClaimedFunctionInvocationRuntime
	err     error
	calls   int
}

type claimedFunctionExecutionFake struct {
	*appLockFencedExecutionStoreFake
	op    *model.Operation
	claim store.OperationClaim
}

func (f *claimedFunctionExecutionFake) ClaimNextOperation(context.Context, string, time.Duration, []string) (*model.Operation, store.OperationClaim, error) {
	return f.op, f.claim, nil
}

func (f *claimedFunctionRuntimeFake) ResolveClaimedFunctionInvocationRuntime(context.Context, FunctionInvocationEffectInput) (ClaimedFunctionInvocationRuntime, error) {
	f.calls++
	return f.runtime, f.err
}

type claimedFunctionReceiptFake struct {
	calls     int
	execution store.FuncExecution
	status    model.OperationStatus
	message   string
	metadata  map[string]interface{}
}

func (f *claimedFunctionReceiptFake) FinishClaimedFunctionInvocation(_ context.Context, _ store.OperationClaim, execution store.FuncExecution, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	f.calls, f.execution, f.status, f.message, f.metadata = f.calls+1, execution, status, message, metadata
	return nil
}

func claimedFunctionInput(t *testing.T) FunctionInvocationEffectInput {
	t.Helper()
	spec := claimedFunctionSpec()
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	return FunctionInvocationEffectInput{
		Authority: "control.example", OperationID: "operation-claimed-7", App: spec.App, Process: "resize", SpecDigest: digest,
		ImageReference: "registry.example/widgets@sha256:" + strings.Repeat("b", 64), DatabaseTarget: FunctionInvocationNoDatabase, DatabaseRevision: FunctionInvocationNoDatabase,
		PrivateRecordID: "operation-claimed-7", PrivateMaterialDigest: "sha256:" + strings.Repeat("c", 64), PrivateKeyID: "function-kek-2026-09",
	}
}

func claimedFunctionSpec() *model.InfraSpec {
	return &model.InfraSpec{App: "widgets", Processes: map[string]model.Process{"resize": {Command: "./function", Function: &model.FunctionSpec{Memory: 192}, Resources: &model.Resources{CPU: 250, Memory: 128}}}}
}

func claimedFunctionOperation(input FunctionInvocationEffectInput) *model.Operation {
	return &model.Operation{ID: input.OperationID, Kind: store.PrivateInvocationOperationKind, App: input.App, Payload: map[string]interface{}{
		"process": input.Process, "specDigest": input.SpecDigest, "imageReference": input.ImageReference, "databaseTarget": input.DatabaseTarget, "databaseRevision": input.DatabaseRevision,
		"privateRecordId": input.PrivateRecordID, "privateMaterialDigest": input.PrivateMaterialDigest, "privateKeyId": input.PrivateKeyID,
	}}
}

func claimedFunctionWorker(input FunctionInvocationEffectInput, remote FunctionInvocationRemote, receipt store.FunctionInvocationReceiptStore) (*ClaimedFunctionInvocationWorker, *claimedFunctionPrivateFake, *claimedFunctionRuntimeFake) {
	private := &claimedFunctionPrivateFake{input: store.PrivateInvocationInput{Body: claimedFunctionPrivateCanary, Method: "POST", Path: "/private"}}
	runtime := &claimedFunctionRuntimeFake{runtime: ClaimedFunctionInvocationRuntime{Spec: claimedFunctionSpec(), ImageReference: input.ImageReference}}
	return &ClaimedFunctionInvocationWorker{Attempts: &functionRemoteAttempts{}, Receipts: receipt, Private: private, Verifier: &claimedFunctionVerifierFake{input: input}, Runtime: runtime, Remote: remote}, private, runtime
}

func TestClaimedFunctionInvocationWorkerPublishesOnlyRedactedTerminalReceipt(t *testing.T) {
	input := claimedFunctionInput(t)
	claim, err := store.NewOperationClaim(input.OperationID, "worker-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	remote := &functionCompositeRemote{functionVariableRemoteFake: &functionVariableRemoteFake{}, functionJobRemoteFake: &functionJobRemoteFake{}}
	receipt := &claimedFunctionReceiptFake{}
	worker, private, _ := claimedFunctionWorker(input, remote, receipt)
	lock := store.NewAppOperationLock(context.Background(), nil)
	defer lock.Release()
	if err := worker.ExecuteClaimed(context.Background(), claimedFunctionOperation(input), claim, lock); !errors.Is(err, effect.ErrEffectPending) {
		t.Fatalf("initial invocation = %v", err)
	}
	if receipt.calls != 0 || private.calls != 1 || remote.functionJobRemoteFake.creates != 1 {
		t.Fatalf("initial calls receipt=%d private=%d jobs=%d", receipt.calls, private.calls, remote.functionJobRemoteFake.creates)
	}
	remote.functionJobRemoteFake.observation.AllocationIDs = []string{"alloc-1"}
	exitCode := 0
	remote.terminal = nomad.FunctionInvocationTerminalObservation{State: nomad.FunctionInvocationTerminalComplete, AllocationID: "alloc-1", ExitCode: &exitCode, StartedAt: time.Unix(100, 0).UTC(), Duration: 3 * time.Second}
	if err := worker.ExecuteClaimed(context.Background(), claimedFunctionOperation(input), claim, lock); err != nil {
		t.Fatalf("terminal invocation = %v", err)
	}
	if receipt.calls != 1 || receipt.status != model.OperationSucceeded || receipt.execution.Status != "complete" || receipt.metadata["allocationId"] != "alloc-1" {
		t.Fatalf("receipt = %+v status=%s metadata=%v", receipt.execution, receipt.status, receipt.metadata)
	}
	if rendered := receipt.message + " " + receipt.execution.App + " " + receipt.execution.Process + " " + stringifyClaimedMetadata(receipt.metadata); strings.Contains(rendered, claimedFunctionPrivateCanary) {
		t.Fatalf("private content leaked into receipt: %q", rendered)
	}
}

func TestClaimedFunctionInvocationWorkerFailsClosedBeforePrivateRead(t *testing.T) {
	input := claimedFunctionInput(t)
	claim, _ := store.NewOperationClaim(input.OperationID, "worker-a", 1)
	remote := &functionCompositeRemote{functionVariableRemoteFake: &functionVariableRemoteFake{}, functionJobRemoteFake: &functionJobRemoteFake{}}
	worker, private, runtime := claimedFunctionWorker(input, remote, &claimedFunctionReceiptFake{})
	worker.Verifier = &claimedFunctionVerifierFake{input: input, err: errors.New("bad signature")}
	lock := store.NewAppOperationLock(context.Background(), nil)
	defer lock.Release()
	if err := worker.ExecuteClaimed(context.Background(), claimedFunctionOperation(input), claim, lock); err == nil || private.calls != 0 || runtime.calls != 0 || remote.functionVariableRemoteFake.creates != 0 {
		t.Fatalf("bad signed payload crossed a boundary: err=%v private=%d runtime=%d remote=%d", err, private.calls, runtime.calls, remote.functionVariableRemoteFake.creates)
	}
	worker.Verifier = &claimedFunctionVerifierFake{input: input}
	runtime.runtime.ImageReference = "registry.example/widgets@sha256:" + strings.Repeat("d", 64)
	if err := worker.ExecuteClaimed(context.Background(), claimedFunctionOperation(input), claim, lock); err == nil || private.calls != 0 || remote.functionVariableRemoteFake.creates != 0 {
		t.Fatalf("changed pinned image crossed private/remote boundary: err=%v private=%d remote=%d", err, private.calls, remote.functionVariableRemoteFake.creates)
	}
}

func TestClaimedFunctionInvocationPendingDeferCarriesNoPrivateMaterial(t *testing.T) {
	input := claimedFunctionInput(t)
	claim, err := store.NewOperationClaim(input.OperationID, "worker-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	var gotMessage string
	var gotMetadata map[string]interface{}
	executions := &executionStoreFake{deferClaim: func(_ context.Context, got store.OperationClaim, message string, _ time.Time, metadata map[string]interface{}) error {
		if got != claim {
			t.Fatal("defer received another claim")
		}
		gotMessage, gotMetadata = message, metadata
		return nil
	}}
	lock := store.NewAppOperationLock(context.Background(), nil)
	defer lock.Release()
	if err := deferClaimedFunctionInvocation(context.Background(), executions, claim, lock, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if pending, ok := gotMetadata["functionInvocationPending"].(bool); !ok || !pending || len(gotMetadata) != 1 || strings.Contains(gotMessage, claimedFunctionPrivateCanary) {
		t.Fatalf("unsafe pending receipt message=%q metadata=%v", gotMessage, gotMetadata)
	}
}

func TestClaimedFunctionInvocationPreflightFailsWithAppLockAndRedactedReceipt(t *testing.T) {
	input := claimedFunctionInput(t)
	claim, err := store.NewOperationClaim(input.OperationID, "worker-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	var finished bool
	executions := &appLockFencedExecutionStoreFake{executionStoreFake: &executionStoreFake{}, finishWithLock: func(_ context.Context, got store.OperationClaim, lock store.AppOperationLock, status model.OperationStatus, message string, metadata map[string]interface{}) error {
		if got != claim || lock == nil || status != model.OperationFailed || message != "function invocation preflight failed" || metadata["functionInvocationPreflightFailed"] != true {
			t.Fatalf("unexpected preflight receipt: claim=%+v status=%s message=%q metadata=%v", got, status, message, metadata)
		}
		finished = true
		return nil
	}}
	lock := store.NewAppOperationLock(context.Background(), nil)
	defer lock.Release()
	if err := failClaimedFunctionInvocationPreflight(context.Background(), executions, claim, lock); err != nil || !finished {
		t.Fatalf("preflight receipt: finished=%v err=%v", finished, err)
	}
	if !isFunctionInvocationPreflightError(functionInvocationPreflightError{"invalid private material"}) || isFunctionInvocationPreflightError(effect.ErrEffectPending) {
		t.Fatal("preflight errors crossed remote-effect boundary")
	}
}

func TestClaimedFunctionInvocationRunOnceTerminalizesPreflightBeforeRemoteEffect(t *testing.T) {
	input := claimedFunctionInput(t)
	claim, err := store.NewOperationClaim(input.OperationID, "worker-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	var finished bool
	executions := &claimedFunctionExecutionFake{op: claimedFunctionOperation(input), claim: claim}
	executions.appLockFencedExecutionStoreFake = &appLockFencedExecutionStoreFake{executionStoreFake: &executionStoreFake{lock: func(context.Context, string) (store.AppOperationLock, bool, error) {
		return store.NewAppOperationLock(context.Background(), nil), true, nil
	}}, finishWithLock: func(_ context.Context, got store.OperationClaim, _ store.AppOperationLock, status model.OperationStatus, _ string, _ map[string]interface{}) error {
		finished = got == claim && status == model.OperationFailed
		return nil
	}}
	remote := &functionCompositeRemote{functionVariableRemoteFake: &functionVariableRemoteFake{}, functionJobRemoteFake: &functionJobRemoteFake{}}
	w, private, _ := claimedFunctionWorker(input, remote, &claimedFunctionReceiptFake{})
	w.Store, w.Verifier, w.Keys, w.ID, w.Lease = executions, &claimedFunctionVerifierFake{input: input, err: errors.New("bad signature")}, &store.PrivateInvocationKeyRing{}, "worker-a", time.Second
	if err := w.RunOnce(context.Background()); err != nil || !finished || private.calls != 0 || remote.functionVariableRemoteFake.creates != 0 {
		t.Fatalf("preflight crossed effect boundary: finished=%v private=%d remote=%d err=%v", finished, private.calls, remote.functionVariableRemoteFake.creates, err)
	}
}

func stringifyClaimedMetadata(metadata map[string]interface{}) string {
	return strings.TrimSpace(strings.Join([]string{metadata["jobId"].(string), metadata["allocationId"].(string)}, " "))
}
