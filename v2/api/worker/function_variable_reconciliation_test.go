package worker

import (
	"fmt"
	"strings"
	"testing"
)

func TestReconcileFunctionVariableOnlyCreatesBeforeFirstWriteAttempt(t *testing.T) {
	expected := functionIdentity(t)
	private := []byte("request-body-private\nwith-a-secret")

	cases := []struct {
		name     string
		stage    FunctionVariableEffectStage
		observed FunctionVariableObservation
		want     FunctionVariableAction
	}{
		{"first conclusive absence", FunctionVariableEffectStage{Recorded: true}, FunctionVariableObservation{State: FunctionVariableNotFound}, FunctionVariableCreate},
		{"absence after attempted write", FunctionVariableEffectStage{Recorded: true, WriteAttempted: true}, FunctionVariableObservation{State: FunctionVariableNotFound}, FunctionVariableUnresolved},
		{"unrecorded first absence", FunctionVariableEffectStage{}, FunctionVariableObservation{State: FunctionVariableNotFound}, FunctionVariableUnresolved},
		{"indeterminate lookup", FunctionVariableEffectStage{Recorded: true}, FunctionVariableObservation{State: FunctionVariableIndeterminate}, FunctionVariableUnresolved},
		{"not found with evidence", FunctionVariableEffectStage{Recorded: true}, FunctionVariableObservation{State: FunctionVariableNotFound, Path: expected.VariablePath}, FunctionVariableUnresolved},
		{"not found with private evidence", FunctionVariableEffectStage{Recorded: true}, FunctionVariableObservation{State: FunctionVariableNotFound, PrivateContent: []byte{}}, FunctionVariableUnresolved},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReconcileFunctionVariable(expected, tc.stage, private, tc.observed)
			if got.Action != tc.want {
				t.Fatalf("decision = %+v, want %q", got, tc.want)
			}
		})
	}
}

func TestReconcileFunctionVariableRecoversOnlyExactPrivateVariable(t *testing.T) {
	expected := functionIdentity(t)
	private := []byte("request-body-private\nwith-a-secret")
	exact := FunctionVariableObservation{State: FunctionVariableFound, Path: expected.VariablePath, OwnerMarker: expected.OwnerMarker, PrivateContent: append([]byte(nil), private...)}

	cases := []struct {
		name string
		got  FunctionVariableObservation
		want FunctionVariableAction
	}{
		{"exact", exact, FunctionVariableRecovered},
		{"wrong path", FunctionVariableObservation{State: FunctionVariableFound, Path: "norn/function-invocation/other", OwnerMarker: expected.OwnerMarker, PrivateContent: private}, FunctionVariableUnresolved},
		{"wrong owner", FunctionVariableObservation{State: FunctionVariableFound, Path: expected.VariablePath, OwnerMarker: "other-owner", PrivateContent: private}, FunctionVariableUnresolved},
		{"wrong private bytes", FunctionVariableObservation{State: FunctionVariableFound, Path: expected.VariablePath, OwnerMarker: expected.OwnerMarker, PrivateContent: []byte("different-private-content")}, FunctionVariableUnresolved},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReconcileFunctionVariable(expected, FunctionVariableEffectStage{Recorded: true, WriteAttempted: true}, private, tc.got)
			if got.Action != tc.want {
				t.Fatalf("decision = %+v, want %q", got, tc.want)
			}
		})
	}
}

func TestReconcileFunctionVariableDoesNotExposePrivateContent(t *testing.T) {
	expected := functionIdentity(t)
	private := []byte("very-private-request-value")
	observed := FunctionVariableObservation{State: FunctionVariableFound, Path: expected.VariablePath, OwnerMarker: expected.OwnerMarker, PrivateContent: []byte("other-private-request-value")}
	decision := ReconcileFunctionVariable(expected, FunctionVariableEffectStage{Recorded: true}, private, observed)
	if decision.Action != FunctionVariableUnresolved {
		t.Fatalf("decision = %+v", decision)
	}
	for _, value := range []string{string(private), string(observed.PrivateContent)} {
		if strings.Contains(fmt.Sprintf("%+v", decision), value) {
			t.Fatalf("decision leaked private content: %+v", decision)
		}
	}
}

func TestReconcileFunctionVariableRejectsForgedIdentity(t *testing.T) {
	expected := functionIdentity(t)
	expected.OwnerMarker = "forged"
	got := ReconcileFunctionVariable(expected, FunctionVariableEffectStage{Recorded: true}, []byte("private"), FunctionVariableObservation{State: FunctionVariableNotFound})
	if got.Action != FunctionVariableUnresolved {
		t.Fatalf("decision = %+v", got)
	}
}
