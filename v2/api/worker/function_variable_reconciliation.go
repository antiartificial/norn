package worker

import "crypto/subtle"

// FunctionVariableLookupState distinguishes a conclusive absence from an
// interrupted or otherwise ambiguous variable read. Only a conclusive absence
// before the first write attempt can authorize a create-only write.
type FunctionVariableLookupState string

const (
	FunctionVariableNotFound      FunctionVariableLookupState = "not-found"
	FunctionVariableFound         FunctionVariableLookupState = "found"
	FunctionVariableIndeterminate FunctionVariableLookupState = "indeterminate"
)

// FunctionVariableObservation is the exact Nomad variable observed by an
// adapter. PrivateContent is deliberately transient: adapters pass it straight
// to the worker reconciler and neither an effect record nor a decision retains
// it.
type FunctionVariableObservation struct {
	State          FunctionVariableLookupState
	Path           string
	OwnerMarker    string
	PrivateContent []byte
}

// FunctionVariableEffectStage is loaded from the durable effect reservation.
// WriteAttempted must be recorded before a remote create-only write begins.
// This prevents a response loss followed by an absent read from authorizing a
// second create attempt.
type FunctionVariableEffectStage struct {
	Recorded       bool
	WriteAttempted bool
}

type FunctionVariableAction string

const (
	// FunctionVariableCreate authorizes one create-only Nomad variable write.
	FunctionVariableCreate FunctionVariableAction = "create"
	// FunctionVariableRecovered proves the reserved private variable exists.
	FunctionVariableRecovered FunctionVariableAction = "recovered"
	// FunctionVariableUnresolved retains the effect for operator review.
	FunctionVariableUnresolved FunctionVariableAction = "unresolved"
)

// FunctionVariableDecision contains no private content. Reasons are fixed
// classifications so callers can safely surface them without redaction.
type FunctionVariableDecision struct {
	Action FunctionVariableAction
	Reason string
}

// ReconcileFunctionVariable is deterministic and side-effect free. The
// private content arguments are compared only in worker memory and never
// copied into the returned decision. A found variable must match the reserved
// path, owner marker, and private bytes exactly. Any mismatch, lookup
// ambiguity, or absence after a durable write attempt remains unresolved.
func ReconcileFunctionVariable(expected FunctionInvocationJobIdentity, stage FunctionVariableEffectStage, privateContent []byte, observed FunctionVariableObservation) FunctionVariableDecision {
	if !validFunctionInvocationJobIdentity(expected) {
		return FunctionVariableDecision{Action: FunctionVariableUnresolved, Reason: "reserved function variable identity is invalid"}
	}
	if !stage.Recorded {
		return FunctionVariableDecision{Action: FunctionVariableUnresolved, Reason: "function variable effect stage is not durably recorded"}
	}

	switch observed.State {
	case FunctionVariableNotFound:
		if observed.Path != "" || observed.OwnerMarker != "" || observed.PrivateContent != nil {
			return FunctionVariableDecision{Action: FunctionVariableUnresolved, Reason: "not-found response includes remote variable evidence"}
		}
		if stage.WriteAttempted {
			return FunctionVariableDecision{Action: FunctionVariableUnresolved, Reason: "Nomad variable is absent after a prior write attempt"}
		}
		return FunctionVariableDecision{Action: FunctionVariableCreate, Reason: "exact variable is conclusively absent"}
	case FunctionVariableFound:
		if observed.Path != expected.VariablePath || observed.OwnerMarker != expected.OwnerMarker {
			return FunctionVariableDecision{Action: FunctionVariableUnresolved, Reason: "Nomad variable identity does not match reserved effect"}
		}
		if len(privateContent) != len(observed.PrivateContent) || subtle.ConstantTimeCompare(privateContent, observed.PrivateContent) != 1 {
			return FunctionVariableDecision{Action: FunctionVariableUnresolved, Reason: "Nomad variable private content does not match reserved effect"}
		}
		return FunctionVariableDecision{Action: FunctionVariableRecovered, Reason: "exact Nomad private variable is durably identifiable"}
	default:
		return FunctionVariableDecision{Action: FunctionVariableUnresolved, Reason: "Nomad variable lookup is indeterminate"}
	}
}
