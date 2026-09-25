package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// FunctionInvocationEffectInput is the complete public binding a function
// worker needs before it can create or recover its Nomad job. Request bytes,
// request headers, and resolved credentials intentionally have no field here:
// they belong only in the encrypted private-material record and Nomad variable.
//
// This is a pure worker boundary. It does not imply that the HTTP function
// route is admitted or that a Nomad job can be submitted yet.
type FunctionInvocationEffectInput struct {
	Authority             string
	OperationID           string
	App                   string
	Process               string
	SpecDigest            string
	ImageReference        string
	DatabaseTarget        string
	DatabaseRevision      string
	PrivateRecordID       string
	PrivateMaterialDigest string
	PrivateKeyID          string
}

// FunctionInvocationJobIdentity is safe to persist in an effect reservation
// and to compare with Nomad. It binds the exact immutable public intent while
// keeping private request material in the variable named by VariablePath.
type FunctionInvocationJobIdentity struct {
	Authority             string `json:"authority"`
	OperationID           string `json:"operationId"`
	App                   string `json:"app"`
	Process               string `json:"process"`
	SpecDigest            string `json:"specDigest"`
	ImageReference        string `json:"imageReference"`
	DatabaseTarget        string `json:"databaseTarget"`
	DatabaseRevision      string `json:"databaseRevision"`
	PrivateRecordID       string `json:"privateRecordId"`
	PrivateMaterialDigest string `json:"privateMaterialDigest"`
	PrivateKeyID          string `json:"privateKeyId"`
	JobID                 string `json:"jobId"`
	VariablePath          string `json:"variablePath"`
	OwnerMarker           string `json:"ownerMarker"`
	JobSpecDigest         string `json:"jobSpecDigest"`
}

var functionEffectDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var functionEffectImage = regexp.MustCompile(`^[^\s@]+@sha256:[0-9a-f]{64}$`)

// FunctionInvocationNoDatabase is the signed target and revision for a
// function whose pinned InfraSpec has no runtime database. Both fields must
// use this value together; the executor still verifies that spec condition.
const FunctionInvocationNoDatabase = "none"

// NewFunctionInvocationJobIdentity derives stable remote names from the
// authority and operation identity. A retry therefore cannot create a second
// one-shot job under a new name.
func NewFunctionInvocationJobIdentity(input FunctionInvocationEffectInput, jobSpecDigest string) (FunctionInvocationJobIdentity, error) {
	if err := validateFunctionInvocationEffectInput(input, jobSpecDigest); err != nil {
		return FunctionInvocationJobIdentity{}, err
	}
	// Names never carry the application, process, request, or secret bytes.
	// This avoids exposing even otherwise-public labels in Nomad paths.
	sum := sha256.Sum256([]byte(input.Authority + "\x00" + input.OperationID))
	name := hex.EncodeToString(sum[:20])
	return FunctionInvocationJobIdentity{
		Authority: input.Authority, OperationID: input.OperationID, App: input.App, Process: input.Process,
		SpecDigest: input.SpecDigest, ImageReference: input.ImageReference,
		DatabaseTarget: input.DatabaseTarget, DatabaseRevision: input.DatabaseRevision,
		PrivateRecordID: input.PrivateRecordID, PrivateMaterialDigest: input.PrivateMaterialDigest,
		PrivateKeyID: input.PrivateKeyID, JobID: "norn-fn-" + name,
		VariablePath:  "nomad/jobs/norn-fn-" + name + "/invoke",
		OwnerMarker:   "norn.function-invoke/" + input.OperationID,
		JobSpecDigest: jobSpecDigest,
	}, nil
}

func validateFunctionInvocationEffectInput(input FunctionInvocationEffectInput, jobSpecDigest string) error {
	for _, field := range []struct{ name, value string }{
		{"authority", input.Authority}, {"operation ID", input.OperationID}, {"app", input.App},
		{"process", input.Process}, {"spec digest", input.SpecDigest}, {"image reference", input.ImageReference},
		{"database target", input.DatabaseTarget}, {"database revision", input.DatabaseRevision},
		{"private record ID", input.PrivateRecordID}, {"private material digest", input.PrivateMaterialDigest},
		{"private key ID", input.PrivateKeyID}, {"job spec digest", jobSpecDigest},
	} {
		if strings.TrimSpace(field.value) == "" || strings.ContainsAny(field.value, "\r\n\x00") {
			return fmt.Errorf("function invocation %s is incomplete", field.name)
		}
	}
	for _, digest := range []string{input.SpecDigest, input.PrivateMaterialDigest, jobSpecDigest} {
		if !functionEffectDigest.MatchString(digest) {
			return fmt.Errorf("function invocation digest is invalid")
		}
	}
	if !functionEffectImage.MatchString(input.ImageReference) {
		return fmt.Errorf("function invocation image reference is not digest-pinned")
	}
	if (input.DatabaseTarget == FunctionInvocationNoDatabase) != (input.DatabaseRevision == FunctionInvocationNoDatabase) {
		return fmt.Errorf("function invocation database target and revision disagree")
	}
	return nil
}

// FunctionJobLookupState distinguishes a conclusive not-found answer from an
// interrupted or otherwise ambiguous request. Only conclusive absence permits
// a first submission.
type FunctionJobLookupState string

const (
	FunctionJobNotFound      FunctionJobLookupState = "not-found"
	FunctionJobFound         FunctionJobLookupState = "found"
	FunctionJobIndeterminate FunctionJobLookupState = "indeterminate"
)

// FunctionJobObservation contains only the nonsecret portion of a Nomad job
// required for reconciliation. The real adapter must derive JobSpecDigest
// from a redacted, deterministic job description.
type FunctionJobObservation struct {
	State           FunctionJobLookupState
	JobID           string
	OwnerMarker     string
	JobSpecDigest   string
	JobVersion      *uint64
	ModifyIndex     uint64
	EvaluationIDs   []string
	AllocationIDs   []string
	HistoryComplete bool
}

// FunctionJobEffectStage is loaded from the durable effect record. Recorded
// must be true before this reconciler can authorize a first submit. The
// worker durably records SubmitAttempted before calling Nomad Register; a
// crash or later 404 can therefore never authorize a second one-shot submit.
type FunctionJobEffectStage struct {
	Recorded        bool
	SubmitAttempted bool
}

type FunctionJobAction string

const (
	// FunctionJobSubmit is permitted only after a conclusive absence check.
	FunctionJobSubmit FunctionJobAction = "submit"
	// FunctionJobRecovered proves the exact already-submitted job can be used
	// by the worker for subsequent allocation polling.
	FunctionJobRecovered FunctionJobAction = "recovered"
	// FunctionJobUnresolved keeps the effect and private material for operator
	// review. It must never trigger an automatic one-shot resubmission.
	FunctionJobUnresolved FunctionJobAction = "unresolved"
)

type FunctionJobDecision struct {
	Action        FunctionJobAction
	Reason        string
	JobVersion    uint64
	ModifyIndex   uint64
	EvaluationIDs []string
	AllocationIDs []string
}

// ReconcileFunctionJob is deterministic and side-effect free. In particular,
// any incomplete history, lookup ambiguity, prior submit, or remote identity
// mismatch fails closed instead of requesting another Nomad submit.
func ReconcileFunctionJob(expected FunctionInvocationJobIdentity, stage FunctionJobEffectStage, observed FunctionJobObservation) FunctionJobDecision {
	if !validFunctionInvocationJobIdentity(expected) {
		return FunctionJobDecision{Action: FunctionJobUnresolved, Reason: "reserved function job identity is invalid"}
	}
	if !stage.Recorded {
		return FunctionJobDecision{Action: FunctionJobUnresolved, Reason: "function job effect stage is not durably recorded"}
	}
	if observed.State == FunctionJobNotFound {
		if observed.JobID != "" || observed.OwnerMarker != "" || observed.JobSpecDigest != "" || observed.JobVersion != nil || observed.ModifyIndex != 0 || len(observed.EvaluationIDs) != 0 || len(observed.AllocationIDs) != 0 || observed.HistoryComplete {
			return FunctionJobDecision{Action: FunctionJobUnresolved, Reason: "not-found response includes remote job evidence"}
		}
		if stage.SubmitAttempted {
			return FunctionJobDecision{Action: FunctionJobUnresolved, Reason: "Nomad job is absent after a prior submit attempt"}
		}
		return FunctionJobDecision{Action: FunctionJobSubmit, Reason: "exact job is conclusively absent"}
	}
	if observed.State != FunctionJobFound {
		return FunctionJobDecision{Action: FunctionJobUnresolved, Reason: "Nomad job lookup is indeterminate"}
	}
	if observed.JobID != expected.JobID || observed.OwnerMarker != expected.OwnerMarker || observed.JobSpecDigest != expected.JobSpecDigest {
		return FunctionJobDecision{Action: FunctionJobUnresolved, Reason: "Nomad job identity does not match reserved effect"}
	}
	if observed.JobVersion == nil || observed.ModifyIndex == 0 || !observed.HistoryComplete || len(observed.EvaluationIDs) == 0 {
		return FunctionJobDecision{Action: FunctionJobUnresolved, Reason: "Nomad job history is incomplete"}
	}
	return FunctionJobDecision{Action: FunctionJobRecovered, Reason: "exact Nomad job is durably identifiable", JobVersion: *observed.JobVersion, ModifyIndex: observed.ModifyIndex,
		EvaluationIDs: append([]string(nil), observed.EvaluationIDs...), AllocationIDs: append([]string(nil), observed.AllocationIDs...)}
}

func validFunctionInvocationJobIdentity(identity FunctionInvocationJobIdentity) bool {
	derived, err := NewFunctionInvocationJobIdentity(FunctionInvocationEffectInput{
		Authority: identity.Authority, OperationID: identity.OperationID, App: identity.App, Process: identity.Process,
		SpecDigest: identity.SpecDigest, ImageReference: identity.ImageReference, DatabaseTarget: identity.DatabaseTarget,
		DatabaseRevision: identity.DatabaseRevision, PrivateRecordID: identity.PrivateRecordID,
		PrivateMaterialDigest: identity.PrivateMaterialDigest, PrivateKeyID: identity.PrivateKeyID,
	}, identity.JobSpecDigest)
	return err == nil && derived.JobID == identity.JobID && derived.VariablePath == identity.VariablePath && derived.OwnerMarker == identity.OwnerMarker
}

// FunctionJobReader is deliberately read-only. Submission remains outside
// this core until reservation, variable CAS, claim fencing, and terminal
// receipt publication are wired as one worker flow.
type FunctionJobReader interface {
	LookupFunctionJob(context.Context, string) (FunctionJobObservation, error)
}

func PlanFunctionJobReconciliation(ctx context.Context, reader FunctionJobReader, expected FunctionInvocationJobIdentity, stage FunctionJobEffectStage) (FunctionJobDecision, error) {
	if reader == nil || !validFunctionInvocationJobIdentity(expected) {
		return FunctionJobDecision{}, fmt.Errorf("function job reconciliation is unavailable")
	}
	observed, err := reader.LookupFunctionJob(ctx, expected.JobID)
	if err != nil {
		// Transport errors provide no absence proof.
		return FunctionJobDecision{Action: FunctionJobUnresolved, Reason: "Nomad job lookup failed"}, nil
	}
	return ReconcileFunctionJob(expected, stage, observed), nil
}
