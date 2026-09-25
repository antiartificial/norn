package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

type FunctionJobAttemptStore interface {
	RecordFunctionInvocationEffectStage(context.Context, store.OperationClaim, store.FunctionInvocationEffectAttemptStage, string, string) (store.FunctionInvocationEffectAttempt, error)
	MarkFunctionInvocationEffectAttempt(context.Context, store.OperationClaim, store.FunctionInvocationEffectAttemptStage, string, string) (store.FunctionInvocationEffectAttempt, error)
}

type FunctionJobRemote interface {
	LookupFunctionInvocationJob(context.Context, string, nomad.FunctionInvocationJobIdentity) (nomad.FunctionInvocationJobObservation, error)
	CreateFunctionInvocationJob(context.Context, string, *nomadapi.Job, nomad.FunctionInvocationJobDigest) error
}

var _ FunctionJobAttemptStore = (*store.DB)(nil)
var _ FunctionJobRemote = (*nomad.Client)(nil)

// EnsureFunctionInvocationJob performs one claim-bound create or recovery
// step. A prior attempted write followed by 404 remains unresolved. A create
// response is not itself recovery evidence; the exact Nomad job and history
// must be read back before this returns FunctionJobRecovered.
func EnsureFunctionInvocationJob(ctx context.Context, attempts FunctionJobAttemptStore, remote FunctionJobRemote, claim store.OperationClaim, region string, expected FunctionInvocationJobIdentity, job *nomadapi.Job) (FunctionJobDecision, error) {
	if attempts == nil || remote == nil || region != "global" || claim.OperationID() != expected.OperationID || !validFunctionInvocationJobIdentity(expected) {
		return FunctionJobDecision{}, fmt.Errorf("function job execution is unavailable")
	}
	projected, err := nomad.ProjectFunctionInvocationJob(job)
	if err != nil || projected.Digest != expected.JobSpecDigest {
		return FunctionJobDecision{}, fmt.Errorf("function job plan does not match durable identity")
	}
	binding, err := json.Marshal(struct {
		Region   string                        `json:"region"`
		Identity FunctionInvocationJobIdentity `json:"identity"`
	}{region, expected})
	if err != nil {
		return FunctionJobDecision{}, fmt.Errorf("function job binding is unavailable")
	}
	sum := sha256.Sum256(binding)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	recorded, err := attempts.RecordFunctionInvocationEffectStage(ctx, claim, store.FunctionInvocationJobAttempt, expected.JobID, digest)
	if err != nil {
		return FunctionJobDecision{}, err
	}
	decide := func(stage store.FunctionInvocationEffectAttempt) FunctionJobDecision {
		observed, lookupErr := remote.LookupFunctionInvocationJob(ctx, region, nomad.FunctionInvocationJobIdentity{JobID: expected.JobID})
		if lookupErr != nil {
			return FunctionJobDecision{Action: FunctionJobUnresolved, Reason: "Nomad job lookup is indeterminate"}
		}
		return ReconcileFunctionJob(expected, FunctionJobEffectStage{Recorded: true, SubmitAttempted: stage.Attempted}, FunctionJobObservation{
			State: FunctionJobLookupState(observed.State), JobID: observed.JobID, OwnerMarker: observed.OwnerMarker,
			JobSpecDigest: observed.JobSpecDigest, JobVersion: observed.JobVersion, ModifyIndex: observed.ModifyIndex,
			EvaluationIDs: observed.EvaluationIDs, AllocationIDs: observed.AllocationIDs, HistoryComplete: observed.HistoryComplete,
		})
	}
	decision := decide(recorded)
	if decision.Action != FunctionJobSubmit {
		return decision, nil
	}
	attempted, err := attempts.MarkFunctionInvocationEffectAttempt(ctx, claim, store.FunctionInvocationJobAttempt, expected.JobID, digest)
	if err != nil {
		return FunctionJobDecision{}, err
	}
	if attempted.MarkedNow {
		_ = remote.CreateFunctionInvocationJob(ctx, region, job, projected)
	}
	return decide(attempted), nil
}
