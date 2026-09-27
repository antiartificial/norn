package worker

import (
	"context"
	"fmt"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/effect"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

// FunctionInvocationRemote is the closed Nomad surface used by a claimed
// function execution. No method accepts plaintext through a job description.
type FunctionInvocationRemote interface {
	FunctionVariableRemote
	FunctionJobRemote
	ObserveFunctionInvocationTerminal(context.Context, string, nomad.FunctionInvocationTerminalExpectation) (nomad.FunctionInvocationTerminalObservation, error)
}

type FunctionInvocationAttemptStore interface {
	FunctionVariableAttemptStore
}

// RunFunctionInvocationRemote performs the variable, job, and exact terminal
// observation in that order. It never creates a second one-shot job after an
// attempted write, and does not turn an ambiguous result into a failure receipt.
// The caller owns and clears privateContent and publishes the terminal receipt
// under the current claim only after this function returns a terminal state.
func RunFunctionInvocationRemote(ctx context.Context, attempts FunctionInvocationAttemptStore, remote FunctionInvocationRemote, claim store.OperationClaim, expected FunctionInvocationJobIdentity, job *nomadapi.Job, privateContent []byte) (nomad.FunctionInvocationTerminalObservation, error) {
	zero := nomad.FunctionInvocationTerminalObservation{}
	if attempts == nil || remote == nil || claim.OperationID() != expected.OperationID || !validFunctionInvocationJobIdentity(expected) {
		return zero, fmt.Errorf("function invocation remote execution is unavailable")
	}
	projected, err := nomad.ProjectFunctionInvocationJob(job)
	if err != nil || projected.Digest != expected.JobSpecDigest {
		return zero, fmt.Errorf("function invocation job plan is invalid")
	}
	variable, err := EnsureFunctionInvocationVariable(ctx, attempts, remote, claim, "global", expected, privateContent)
	if err != nil || variable.Action != FunctionVariableRecovered {
		return zero, pendingFunctionInvocation(expected, "private variable is not conclusively recovered")
	}
	jobResult, err := EnsureFunctionInvocationJob(ctx, attempts, remote, claim, "global", expected, job)
	if err != nil || jobResult.Action != FunctionJobRecovered {
		return zero, pendingFunctionInvocation(expected, "one-shot job is not conclusively recovered")
	}
	if len(jobResult.AllocationIDs) == 0 {
		// The first read may precede scheduling. The next claim re-runs exact
		// job/history reconciliation and may then bind the allocation ID.
		return zero, pendingFunctionInvocation(expected, "one-shot allocation has not been observed")
	}
	observed, err := remote.ObserveFunctionInvocationTerminal(ctx, "global", nomad.FunctionInvocationTerminalExpectation{
		JobID: expected.JobID, JobVersion: jobResult.JobVersion, JobModifyIndex: jobResult.ModifyIndex,
		EvaluationIDs: jobResult.EvaluationIDs, AllocationIDs: jobResult.AllocationIDs,
	})
	if err != nil || observed.State == nomad.FunctionInvocationTerminalIndeterminate || observed.State == nomad.FunctionInvocationTerminalPending {
		return zero, pendingFunctionInvocation(expected, "one-shot allocation is not conclusively terminal")
	}
	if observed.State != nomad.FunctionInvocationTerminalComplete && observed.State != nomad.FunctionInvocationTerminalFailed {
		return zero, pendingFunctionInvocation(expected, "one-shot allocation outcome is invalid")
	}
	if observed.ExitCode == nil || observed.StartedAt.IsZero() || observed.Duration < 0 || len(jobResult.AllocationIDs) != 1 || observed.AllocationID != jobResult.AllocationIDs[0] {
		return zero, pendingFunctionInvocation(expected, "one-shot terminal evidence is incomplete")
	}
	return observed, nil
}

func pendingFunctionInvocation(expected FunctionInvocationJobIdentity, reason string) error {
	return &effect.PendingError{EffectID: expected.OperationID, Resource: expected.JobID, Reason: reason}
}

var _ FunctionInvocationRemote = (*nomad.Client)(nil)
var _ FunctionInvocationAttemptStore = (*store.DB)(nil)
