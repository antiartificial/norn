package nomad

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

func terminalExpectation() FunctionInvocationTerminalExpectation {
	return FunctionInvocationTerminalExpectation{JobID: "norn-fn-" + strings.Repeat("a", 40), JobVersion: 1, JobModifyIndex: 55, EvaluationIDs: []string{"eval-1"}, AllocationIDs: []string{"alloc-1"}}
}

func terminalAllocation() FunctionInvocationTerminalAllocation {
	exit := 0
	started := time.Unix(100, 0).UTC()
	return FunctionInvocationTerminalAllocation{ID: "alloc-1", JobID: terminalExpectation().JobID, JobVersion: 1, EvaluationID: "eval-1", ClientStatus: nomadapi.AllocClientStatusComplete, TaskName: functionInvocationTaskName, TaskState: "dead", StartedAt: started, FinishedAt: started.Add(3 * time.Second), ExitCode: &exit}
}

func TestProjectFunctionInvocationTerminalRequiresExactLineage(t *testing.T) {
	expected := terminalExpectation()
	complete := terminalAllocation()
	got := ProjectFunctionInvocationTerminal(expected, []FunctionInvocationTerminalAllocation{complete})
	if got.State != FunctionInvocationTerminalComplete || got.AllocationID != "alloc-1" || got.ExitCode == nil || *got.ExitCode != 0 || !got.StartedAt.Equal(complete.StartedAt) || got.Duration != 3*time.Second {
		t.Fatalf("complete projection = %+v", got)
	}

	running := complete
	running.ClientStatus, running.TaskState, running.FinishedAt, running.ExitCode = nomadapi.AllocClientStatusRunning, "running", time.Time{}, nil
	if got := ProjectFunctionInvocationTerminal(expected, []FunctionInvocationTerminalAllocation{running}); got.State != FunctionInvocationTerminalPending {
		t.Fatalf("running projection = %+v", got)
	}

	failed := complete
	code := 7
	failed.ClientStatus, failed.TaskFailed, failed.ExitCode = nomadapi.AllocClientStatusFailed, true, &code
	if got := ProjectFunctionInvocationTerminal(expected, []FunctionInvocationTerminalAllocation{failed}); got.State != FunctionInvocationTerminalFailed || got.ExitCode == nil || *got.ExitCode != 7 {
		t.Fatalf("failed projection = %+v", got)
	}
	lost := complete
	lost.ClientStatus, lost.ExitCode = nomadapi.AllocClientStatusLost, nil
	if got := ProjectFunctionInvocationTerminal(expected, []FunctionInvocationTerminalAllocation{lost}); got.State != FunctionInvocationTerminalIndeterminate {
		t.Fatalf("lost allocation accepted: %+v", got)
	}
	missingExit := failed
	missingExit.ExitCode = nil
	if got := ProjectFunctionInvocationTerminal(expected, []FunctionInvocationTerminalAllocation{missingExit}); got.State != FunctionInvocationTerminalIndeterminate {
		t.Fatalf("missing exit code accepted: %+v", got)
	}

	cases := []FunctionInvocationTerminalAllocation{complete, complete, complete, complete}
	cases[0].EvaluationID = "eval-other"
	cases[1].ID = "alloc-other"
	cases[2].TaskRestarts = 1
	cases[3].FinishedAt = cases[3].StartedAt.Add(-time.Second)
	for i, candidate := range cases {
		if got := ProjectFunctionInvocationTerminal(expected, []FunctionInvocationTerminalAllocation{candidate}); got.State != FunctionInvocationTerminalIndeterminate {
			t.Fatalf("case %d accepted: %+v", i, got)
		}
	}
	if got := ProjectFunctionInvocationTerminal(expected, []FunctionInvocationTerminalAllocation{complete, complete}); got.State != FunctionInvocationTerminalIndeterminate {
		t.Fatalf("multiple allocations accepted: %+v", got)
	}
}

func TestObserveFunctionInvocationTerminalReturnsRedactedExactResult(t *testing.T) {
	expected := terminalExpectation()
	started, finished := time.Unix(100, 0).UTC(), time.Unix(104, 0).UTC()
	job, _ := observedFunctionJob(t, FunctionInvocationJobIdentity{JobID: expected.JobID})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/job/" + expected.JobID + "/allocations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: "alloc-1", JobID: expected.JobID, JobVersion: 1, EvalID: "eval-1", ClientStatus: nomadapi.AllocClientStatusComplete}})
		case "/v1/allocation/alloc-1":
			_ = json.NewEncoder(w).Encode(&nomadapi.Allocation{ID: "alloc-1", JobID: expected.JobID, EvalID: "eval-1", ClientStatus: nomadapi.AllocClientStatusComplete, TaskStates: map[string]*nomadapi.TaskState{functionInvocationTaskName: {State: "dead", StartedAt: started, FinishedAt: finished, Events: []*nomadapi.TaskEvent{{Type: nomadapi.TaskTerminated, ExitCode: 0, Message: "private-canary"}}}}})
		case "/v1/job/" + expected.JobID:
			_ = json.NewEncoder(w).Encode(job)
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ObserveFunctionInvocationTerminal(context.Background(), "global", expected)
	if err != nil || got.State != FunctionInvocationTerminalComplete || !got.StartedAt.Equal(started) || got.Duration != 4*time.Second || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("observation = %+v, %v", got, err)
	}
}

func TestObserveFunctionInvocationTerminalRejectsWidenedOrChangedLineage(t *testing.T) {
	expected := terminalExpectation()
	job, _ := observedFunctionJob(t, FunctionInvocationJobIdentity{JobID: expected.JobID})
	changed := *job.ModifyIndex + 1
	job.ModifyIndex = &changed
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/job/" + expected.JobID + "/allocations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: "alloc-1", JobID: expected.JobID, JobVersion: 1, EvalID: "eval-1", ClientStatus: nomadapi.AllocClientStatusRunning}})
		case "/v1/allocation/alloc-1":
			_ = json.NewEncoder(w).Encode(&nomadapi.Allocation{ID: "alloc-1", JobID: expected.JobID, EvalID: "eval-1", ClientStatus: nomadapi.AllocClientStatusRunning, TaskStates: map[string]*nomadapi.TaskState{functionInvocationTaskName: {State: "running"}}})
		case "/v1/job/" + expected.JobID:
			_ = json.NewEncoder(w).Encode(job)
		}
	}))
	defer server.Close()
	client, _ := NewClient(server.URL)
	got, err := client.ObserveFunctionInvocationTerminal(context.Background(), "global", expected)
	if !errors.Is(err, ErrFunctionTerminalLookupIndeterminate) || got.State != FunctionInvocationTerminalIndeterminate {
		t.Fatalf("changed job revision accepted: %+v, %v", got, err)
	}
}
