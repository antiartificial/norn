package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

// FunctionVariableAttemptStore is the small durable boundary needed before a
// private Nomad variable can be created. Both control backends implement it.
type FunctionVariableAttemptStore interface {
	RecordFunctionInvocationEffectStage(context.Context, store.OperationClaim, store.FunctionInvocationEffectAttemptStage, string, string) (store.FunctionInvocationEffectAttempt, error)
	MarkFunctionInvocationEffectAttempt(context.Context, store.OperationClaim, store.FunctionInvocationEffectAttemptStage, string, string) (store.FunctionInvocationEffectAttempt, error)
}

type FunctionVariableRemote interface {
	LookupFunctionInvocationVariable(context.Context, string, nomad.FunctionInvocationVariableIdentity) (nomad.FunctionInvocationVariableObservation, error)
	CreateFunctionInvocationVariable(context.Context, string, nomad.FunctionInvocationVariableIdentity, []byte) error
}

var _ FunctionVariableRemote = (*nomad.Client)(nil)
var _ FunctionVariableAttemptStore = (*store.DB)(nil)

// EnsureFunctionInvocationVariable performs one claim-bound variable step.
// The caller owns privateContent and should clear it when the step returns.
// Neither the durable binding nor the returned decision contains those bytes.
// A failed or ambiguous remote write is reconciled by reading the exact path;
// absence after an attempted write never authorizes another create.
func EnsureFunctionInvocationVariable(ctx context.Context, attempts FunctionVariableAttemptStore, remote FunctionVariableRemote, claim store.OperationClaim, region string, expected FunctionInvocationJobIdentity, privateContent []byte) (FunctionVariableDecision, error) {
	if attempts == nil || remote == nil || !validFunctionInvocationJobIdentity(expected) || claim.OperationID() != expected.OperationID || strings.TrimSpace(region) == "" {
		return FunctionVariableDecision{}, fmt.Errorf("function variable execution is unavailable")
	}
	binding, err := json.Marshal(struct {
		Region   string                        `json:"region"`
		Identity FunctionInvocationJobIdentity `json:"identity"`
	}{region, expected})
	if err != nil {
		return FunctionVariableDecision{}, fmt.Errorf("function variable binding is unavailable")
	}
	sum := sha256.Sum256(binding)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	identity := nomad.FunctionInvocationVariableIdentity{Path: expected.VariablePath, OwnerMarker: expected.OwnerMarker}
	recorded, err := attempts.RecordFunctionInvocationEffectStage(ctx, claim, store.FunctionInvocationVariableAttempt, expected.VariablePath, digest)
	if err != nil {
		return FunctionVariableDecision{}, err
	}
	decide := func(stage store.FunctionInvocationEffectAttempt) FunctionVariableDecision {
		observed, lookupErr := remote.LookupFunctionInvocationVariable(ctx, region, identity)
		if lookupErr != nil {
			return FunctionVariableDecision{Action: FunctionVariableUnresolved, Reason: "Nomad private variable lookup is indeterminate"}
		}
		return ReconcileFunctionVariable(expected, FunctionVariableEffectStage{Recorded: true, WriteAttempted: stage.Attempted}, privateContent, FunctionVariableObservation{
			State: FunctionVariableLookupState(observed.State), Path: observed.Path, OwnerMarker: observed.OwnerMarker, PrivateContent: observed.PrivateContent,
		})
	}
	decision := decide(recorded)
	if decision.Action != FunctionVariableCreate {
		return decision, nil
	}
	attempted, err := attempts.MarkFunctionInvocationEffectAttempt(ctx, claim, store.FunctionInvocationVariableAttempt, expected.VariablePath, digest)
	if err != nil {
		return FunctionVariableDecision{}, err
	}
	if attempted.MarkedNow {
		// Both conflict and transport failure may mean another owner exists or
		// the write committed. The exact read below is the only recovery proof.
		_ = remote.CreateFunctionInvocationVariable(ctx, region, identity, privateContent)
	}
	return decide(attempted), nil
}
