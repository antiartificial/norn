package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeFunctionJobReader struct {
	seen string
	got  FunctionJobObservation
	err  error
}

func (f *fakeFunctionJobReader) LookupFunctionJob(_ context.Context, jobID string) (FunctionJobObservation, error) {
	f.seen = jobID
	return f.got, f.err
}

func functionIdentity(t *testing.T) FunctionInvocationJobIdentity {
	t.Helper()
	id, err := NewFunctionInvocationJobIdentity(FunctionInvocationEffectInput{
		Authority: "control.example", OperationID: "operation-7", App: "widgets", Process: "resize",
		SpecDigest: "sha256:" + strings.Repeat("a", 64), ImageReference: "registry.example/widgets@sha256:" + strings.Repeat("b", 64),
		DatabaseTarget: "widgets-primary", DatabaseRevision: "14", PrivateRecordID: "operation-7",
		PrivateMaterialDigest: "sha256:" + strings.Repeat("c", 64), PrivateKeyID: "function-kek-2026-09",
	}, "sha256:"+strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestFunctionInvocationJobIdentityIsStableAndHasNoRequestFields(t *testing.T) {
	first, second := functionIdentity(t), functionIdentity(t)
	if first.JobID != second.JobID || first.VariablePath != second.VariablePath || first.JobID == "" {
		t.Fatalf("identity is not stable: first=%+v second=%+v", first, second)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"request-body-should-never-be-here", "PATCH", "/customers/private"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("public identity leaked %q: %s", private, encoded)
		}
	}
}

func TestPlanFunctionJobReconciliationUsesOnlyConclusiveAbsenceForSubmit(t *testing.T) {
	expected := functionIdentity(t)
	firstAttempt := FunctionJobEffectStage{Recorded: true}
	cases := []struct {
		name string
		got  FunctionJobObservation
		want FunctionJobAction
	}{
		{"absent", FunctionJobObservation{State: FunctionJobNotFound}, FunctionJobSubmit},
		{"indeterminate", FunctionJobObservation{State: FunctionJobIndeterminate}, FunctionJobUnresolved},
		{"remote mismatch", FunctionJobObservation{State: FunctionJobFound, JobID: expected.JobID, OwnerMarker: "other-owner", JobSpecDigest: expected.JobSpecDigest, ModifyIndex: 11, HistoryComplete: true, EvaluationIDs: []string{"eval-1"}}, FunctionJobUnresolved},
		{"incomplete history", FunctionJobObservation{State: FunctionJobFound, JobID: expected.JobID, OwnerMarker: expected.OwnerMarker, JobSpecDigest: expected.JobSpecDigest, ModifyIndex: 11}, FunctionJobUnresolved},
		{"exact remote job", FunctionJobObservation{State: FunctionJobFound, JobID: expected.JobID, OwnerMarker: expected.OwnerMarker, JobSpecDigest: expected.JobSpecDigest, ModifyIndex: 11, HistoryComplete: true, EvaluationIDs: []string{"eval-1"}, AllocationIDs: []string{"alloc-1"}}, FunctionJobRecovered},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeFunctionJobReader{got: tc.got}
			decision, err := PlanFunctionJobReconciliation(context.Background(), fake, expected, firstAttempt)
			if err != nil {
				t.Fatal(err)
			}
			if fake.seen != expected.JobID {
				t.Fatalf("lookup job ID = %q, want %q", fake.seen, expected.JobID)
			}
			if decision.Action != tc.want {
				t.Fatalf("decision = %+v, want %s", decision, tc.want)
			}
		})
	}
}

func TestPlanFunctionJobReconciliationFailsClosedOnLookupError(t *testing.T) {
	expected := functionIdentity(t)
	fake := &fakeFunctionJobReader{err: context.DeadlineExceeded}
	decision, err := PlanFunctionJobReconciliation(context.Background(), fake, expected, FunctionJobEffectStage{Recorded: true})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != FunctionJobUnresolved {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestReconcileFunctionJobRejectsForgedExpectedIdentity(t *testing.T) {
	expected := functionIdentity(t)
	expected.VariablePath = "norn/function-invocation/forged"
	decision := ReconcileFunctionJob(expected, FunctionJobEffectStage{Recorded: true}, FunctionJobObservation{State: FunctionJobNotFound})
	if decision.Action != FunctionJobUnresolved {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestPlanFunctionJobReconciliationDoesNotResubmitAfterLostResponseAnd404(t *testing.T) {
	expected := functionIdentity(t)
	fake := &fakeFunctionJobReader{got: FunctionJobObservation{State: FunctionJobNotFound}}
	decision, err := PlanFunctionJobReconciliation(context.Background(), fake, expected, FunctionJobEffectStage{Recorded: true, SubmitAttempted: true})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != FunctionJobUnresolved || !strings.Contains(decision.Reason, "prior submit") {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestPlanFunctionJobReconciliationPermitsFirstAttemptAnd404(t *testing.T) {
	expected := functionIdentity(t)
	fake := &fakeFunctionJobReader{got: FunctionJobObservation{State: FunctionJobNotFound}}
	decision, err := PlanFunctionJobReconciliation(context.Background(), fake, expected, FunctionJobEffectStage{Recorded: true, SubmitAttempted: false})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != FunctionJobSubmit {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestReconcileFunctionJobRequiresDurableStageForFirst404(t *testing.T) {
	expected := functionIdentity(t)
	decision := ReconcileFunctionJob(expected, FunctionJobEffectStage{}, FunctionJobObservation{State: FunctionJobNotFound})
	if decision.Action != FunctionJobUnresolved {
		t.Fatalf("decision = %+v", decision)
	}
}
