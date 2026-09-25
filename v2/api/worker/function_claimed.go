package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

// ClaimedFunctionInvocationVerifier is the signed-public-record boundary for
// this worker. Implementations must reject an operation unless its accepted
// signature and every returned public field are authentic. The store-owned
// result avoids making the store package depend on worker effect types.
type ClaimedFunctionInvocationVerifier interface {
	VerifyClaimedPrivateInvocation(context.Context, model.Operation) (store.VerifiedPrivateInvocation, error)
}

// ClaimedFunctionInvocationPrivateStore opens the separately encrypted record
// named by the signed operation. It deliberately exposes no general operation
// mutation methods.
type ClaimedFunctionInvocationPrivateStore interface {
	OpenPrivateInvocation(context.Context, model.Operation, *store.PrivateInvocationKeyRing) (store.PrivateInvocationInput, error)
}

// ClaimedFunctionInvocationRuntime is the resolved, pinned runtime material.
// Database values are used only for the binding recheck; secret values remain
// private and are encoded directly into the Nomad variable.
type ClaimedFunctionInvocationRuntime struct {
	Spec           *model.InfraSpec
	ImageReference string
	Database       nomad.DatabaseRevision
	AppEnv         map[string]string
	SecretEnv      map[string]string
}

// ClaimedFunctionInvocationRuntimeResolver resolves the pinned spec, exact
// image, and exact database delivery selected by the signed invocation.
type ClaimedFunctionInvocationRuntimeResolver interface {
	ResolveClaimedFunctionInvocationRuntime(context.Context, FunctionInvocationEffectInput) (ClaimedFunctionInvocationRuntime, error)
}

// ClaimedFunctionInvocationWorker is intentionally separate from the general
// operation worker. It claims only accepted function invocations and never
// retries an unresolved remote one-shot submission.
type ClaimedFunctionInvocationWorker struct {
	Store    store.ExecutionStore
	Attempts FunctionInvocationAttemptStore
	Receipts store.FunctionInvocationReceiptStore
	Private  ClaimedFunctionInvocationPrivateStore
	Keys     *store.PrivateInvocationKeyRing
	Verifier ClaimedFunctionInvocationVerifier
	Runtime  ClaimedFunctionInvocationRuntimeResolver
	Remote   FunctionInvocationRemote
	ID       string
	Lease    time.Duration
}

// RunOnce recovers expired claims, then handles at most one function
// invocation. An unknown or unresolved state is returned without publishing a
// terminal receipt; the durable effect record remains the recovery evidence.
func (w *ClaimedFunctionInvocationWorker) RunOnce(ctx context.Context) error {
	if w == nil || w.Store == nil || w.Attempts == nil || w.Receipts == nil || w.Private == nil || w.Keys == nil || w.Verifier == nil || w.Runtime == nil || w.Remote == nil || w.ID == "" || w.Lease <= 0 {
		return fmt.Errorf("claimed function invocation worker is unavailable")
	}
	if err := w.Store.RecoverExpiredOperations(ctx); err != nil {
		return fmt.Errorf("recover expired function invocations: %w", err)
	}
	op, claim, err := w.Store.ClaimNextOperation(ctx, w.ID, w.Lease, []string{store.PrivateInvocationOperationKind})
	if err != nil || op == nil {
		return err
	}
	lock, acquired, err := w.Store.AcquireAppOperationLock(ctx, op.App)
	if err != nil {
		return fmt.Errorf("acquire function invocation app lock: %w", err)
	}
	if !acquired || lock == nil {
		return w.Store.DeferClaimedOperation(ctx, claim, "function invocation app lock is unavailable", time.Now().Add(5*time.Second), map[string]interface{}{"functionInvocationPending": true})
	}
	defer lock.Release()

	executionCtx, cancel := context.WithCancel(lock.Context())
	defer cancel()
	stopRenewal := make(chan struct{})
	renewed := make(chan error, 1)
	go func() {
		renewed <- renewFunctionInvocationClaim(executionCtx, w.Store, claim, w.Lease, cancel, stopRenewal)
	}()
	err = w.ExecuteClaimed(executionCtx, op, claim, lock)
	if effect.IsDeferred(err) && executionCtx.Err() == nil && lock.Context().Err() == nil {
		err = deferClaimedFunctionInvocation(executionCtx, w.Store, claim, lock, time.Now().Add(5*time.Second))
	} else if isFunctionInvocationPreflightError(err) && executionCtx.Err() == nil && lock.Context().Err() == nil {
		err = failClaimedFunctionInvocationPreflight(executionCtx, w.Store, claim, lock)
	}
	close(stopRenewal)
	if renewErr := <-renewed; renewErr != nil {
		return fmt.Errorf("renew function invocation claim: %w", renewErr)
	}
	if lock.Context().Err() != nil {
		return store.ErrOperationOwnershipLost
	}
	return err
}

type functionInvocationPreflightError struct{ reason string }

func (e functionInvocationPreflightError) Error() string { return e.reason }

func isFunctionInvocationPreflightError(err error) bool {
	var preflight functionInvocationPreflightError
	return errors.As(err, &preflight)
}

func failClaimedFunctionInvocationPreflight(ctx context.Context, executions store.ExecutionStore, claim store.OperationClaim, lock store.AppOperationLock) error {
	const message = "function invocation preflight failed"
	metadata := map[string]interface{}{"functionInvocationPreflightFailed": true}
	if fenced, ok := executions.(store.AppLockFencedExecutionStore); ok {
		return fenced.FinishClaimedOperationWithAppLock(ctx, claim, lock, model.OperationFailed, message, metadata)
	}
	return executions.FinishClaimedOperation(ctx, claim, model.OperationFailed, message, metadata)
}

func deferClaimedFunctionInvocation(ctx context.Context, executions store.ExecutionStore, claim store.OperationClaim, lock store.AppOperationLock, next time.Time) error {
	const message = "function invocation remote outcome is pending"
	metadata := map[string]interface{}{"functionInvocationPending": true}
	if fenced, ok := executions.(store.AppLockFencedExecutionStore); ok {
		return fenced.DeferClaimedOperationWithAppLock(ctx, claim, lock, message, next, metadata)
	}
	return executions.DeferClaimedOperation(ctx, claim, message, next, metadata)
}

// ExecuteClaimed verifies the accepted public intent before decrypting its
// private record, rechecks the pinned runtime, then runs the closed remote
// protocol and publishes only its redacted terminal projection.
func (w *ClaimedFunctionInvocationWorker) ExecuteClaimed(ctx context.Context, op *model.Operation, claim store.OperationClaim, lock store.AppOperationLock) error {
	if w == nil || w.Attempts == nil || w.Receipts == nil || w.Private == nil || w.Verifier == nil || w.Runtime == nil || w.Remote == nil || op == nil || lock == nil || ctx == nil || ctx.Err() != nil || lock.Context().Err() != nil || claim.OperationID() != op.ID || op.Kind != store.PrivateInvocationOperationKind || op.App == "" {
		return fmt.Errorf("claimed function invocation is invalid")
	}
	verified, err := w.Verifier.VerifyClaimedPrivateInvocation(ctx, *op)
	input := functionInvocationEffectInput(verified)
	if err != nil || !matchesClaimedFunctionInvocation(op, claim, input) {
		return functionInvocationPreflightError{"claimed function invocation signed payload is invalid"}
	}
	runtime, err := w.Runtime.ResolveClaimedFunctionInvocationRuntime(ctx, input)
	if err != nil || !validClaimedFunctionRuntime(input, runtime) {
		return functionInvocationPreflightError{"claimed function invocation runtime is invalid"}
	}
	process := runtime.Spec.Processes[input.Process]
	privateInput, err := w.Private.OpenPrivateInvocation(ctx, *op, w.Keys)
	if err != nil {
		return functionInvocationPreflightError{"claimed function invocation private record is unavailable"}
	}
	privateContent, files, err := EncodeFunctionInvocationRuntimeMaterialWithDatabase(privateInput, runtime.AppEnv, runtime.SecretEnv, process.Env, runtime.Spec, runtime.Database)
	if err != nil {
		return functionInvocationPreflightError{"claimed function invocation private runtime material is invalid"}
	}
	defer clearFunctionInvocationBytes(privateContent)
	identity, job, err := BuildFunctionInvocationJobPlan(input, process.Command, functionInvocationCPU(process), functionInvocationMemory(process), files...)
	if err != nil {
		return functionInvocationPreflightError{"claimed function invocation job plan is invalid"}
	}
	observed, err := RunFunctionInvocationRemote(ctx, w.Attempts, w.Remote, claim, identity, job, privateContent)
	if err != nil {
		return err
	}
	return finishClaimedFunctionInvocation(ctx, w.Receipts, lock, claim, input, identity, observed)
}

func functionInvocationEffectInput(verified store.VerifiedPrivateInvocation) FunctionInvocationEffectInput {
	payload := verified.Operation.Payload
	return FunctionInvocationEffectInput{
		Authority: verified.Authority, OperationID: verified.Operation.ID, App: verified.Operation.App,
		Process: stringFunctionPayload(payload, "process"), SpecDigest: stringFunctionPayload(payload, "specDigest"), ImageReference: stringFunctionPayload(payload, "imageReference"),
		DatabaseTarget: stringFunctionPayload(payload, "databaseTarget"), DatabaseRevision: stringFunctionPayload(payload, "databaseRevision"),
		PrivateRecordID: stringFunctionPayload(payload, "privateRecordId"), PrivateMaterialDigest: stringFunctionPayload(payload, "privateMaterialDigest"), PrivateKeyID: stringFunctionPayload(payload, "privateKeyId"),
	}
}

func stringFunctionPayload(payload map[string]interface{}, key string) string {
	value, _ := payload[key].(string)
	return value
}

func matchesClaimedFunctionInvocation(op *model.Operation, claim store.OperationClaim, input FunctionInvocationEffectInput) bool {
	if input.OperationID != claim.OperationID() || input.OperationID != op.ID || input.App != op.App || input.PrivateRecordID != op.ID || op.Payload == nil {
		return false
	}
	fields := map[string]string{"process": input.Process, "specDigest": input.SpecDigest, "imageReference": input.ImageReference, "databaseTarget": input.DatabaseTarget, "databaseRevision": input.DatabaseRevision, "privateRecordId": input.PrivateRecordID, "privateMaterialDigest": input.PrivateMaterialDigest, "privateKeyId": input.PrivateKeyID}
	for key, expected := range fields {
		actual, ok := op.Payload[key].(string)
		if !ok || actual != expected {
			return false
		}
	}
	return true
}

func validClaimedFunctionRuntime(input FunctionInvocationEffectInput, runtime ClaimedFunctionInvocationRuntime) bool {
	if runtime.Spec == nil || runtime.Spec.App != input.App || runtime.ImageReference != input.ImageReference {
		return false
	}
	process, ok := runtime.Spec.Processes[input.Process]
	if !ok || process.Function == nil {
		return false
	}
	return RecheckFunctionInvocationDatabaseBinding(input, runtime.Spec, runtime.Database) == nil
}

func functionInvocationCPU(process model.Process) int {
	if process.Resources == nil {
		return 0
	}
	return process.Resources.CPU
}

func functionInvocationMemory(process model.Process) int {
	if process.Function != nil && process.Function.Memory > 0 {
		return process.Function.Memory
	}
	if process.Resources == nil {
		return 0
	}
	return process.Resources.Memory
}

func finishClaimedFunctionInvocation(ctx context.Context, receipts store.FunctionInvocationReceiptStore, lock store.AppOperationLock, claim store.OperationClaim, input FunctionInvocationEffectInput, identity FunctionInvocationJobIdentity, observed nomad.FunctionInvocationTerminalObservation) error {
	if observed.ExitCode == nil || observed.StartedAt.IsZero() || observed.Duration < 0 || (observed.State != nomad.FunctionInvocationTerminalComplete && observed.State != nomad.FunctionInvocationTerminalFailed) {
		return fmt.Errorf("function invocation terminal observation is invalid")
	}
	duration := observed.Duration.Milliseconds()
	status, executionStatus, message := model.OperationFailed, "failed", "function invocation failed"
	if observed.State == nomad.FunctionInvocationTerminalComplete {
		status, executionStatus, message = model.OperationSucceeded, "complete", "function invocation completed"
	}
	execution := store.FuncExecution{ID: claim.OperationID(), App: input.App, Process: input.Process, Status: executionStatus, ExitCode: observed.ExitCode, StartedAt: observed.StartedAt, DurationMs: &duration}
	metadata := map[string]interface{}{"jobId": identity.JobID, "allocationId": observed.AllocationID, "exitCode": *observed.ExitCode, "durationMs": duration}
	return FinishFunctionInvocationReceipt(ctx, receipts, lock, claim, execution, status, message, metadata)
}

func renewFunctionInvocationClaim(ctx context.Context, executions store.ExecutionStore, claim store.OperationClaim, lease time.Duration, cancel context.CancelFunc, stop <-chan struct{}) error {
	ticker := time.NewTicker(lease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-stop:
			return nil
		case <-ticker.C:
			renewCtx, renewCancel := context.WithTimeout(ctx, lease/3)
			err := executions.RenewOperationClaim(renewCtx, claim, lease)
			renewCancel()
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				cancel()
				return err
			}
		}
	}
}

func clearFunctionInvocationBytes(bytes []byte) {
	for i := range bytes {
		bytes[i] = 0
	}
}
