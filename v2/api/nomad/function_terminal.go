package nomad

import (
	"context"
	"errors"
	"sort"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

type FunctionInvocationTerminalState string

const (
	FunctionInvocationTerminalPending       FunctionInvocationTerminalState = "pending"
	FunctionInvocationTerminalComplete      FunctionInvocationTerminalState = "complete"
	FunctionInvocationTerminalFailed        FunctionInvocationTerminalState = "failed"
	FunctionInvocationTerminalIndeterminate FunctionInvocationTerminalState = "indeterminate"
)

var ErrFunctionTerminalLookupIndeterminate = errors.New("function terminal allocation lookup is indeterminate")

// FunctionInvocationTerminalExpectation is the exact lineage proved by the
// create/read-back step. Terminal observation is not allowed to widen it. If
// the first read-back precedes scheduling and has no allocation ID, the worker
// must refresh the complete job/evaluation/allocation proof through
// LookupFunctionInvocationJob and construct a new expectation; this observer
// never adopts a subsequently listed allocation by itself.
type FunctionInvocationTerminalExpectation struct {
	JobID          string
	JobVersion     uint64
	JobModifyIndex uint64
	EvaluationIDs  []string
	AllocationIDs  []string
}

// FunctionInvocationTerminalAllocation is the redacted allocation material
// used by the pure terminal projection. It contains no task event messages,
// environment, job, or template content.
type FunctionInvocationTerminalAllocation struct {
	ID           string
	JobID        string
	JobVersion   uint64
	EvaluationID string
	ClientStatus string
	TaskName     string
	TaskState    string
	TaskFailed   bool
	TaskRestarts uint64
	StartedAt    time.Time
	FinishedAt   time.Time
	ExitCode     *int
}

type FunctionInvocationTerminalObservation struct {
	State        FunctionInvocationTerminalState
	AllocationID string
	ExitCode     *int
	Duration     time.Duration
}

// ProjectFunctionInvocationTerminal is side-effect free. The closed function
// job has one task, no restarts or rescheduling, so any widened or incomplete
// lineage is indeterminate rather than guessed into a receipt.
func ProjectFunctionInvocationTerminal(expected FunctionInvocationTerminalExpectation, allocations []FunctionInvocationTerminalAllocation) FunctionInvocationTerminalObservation {
	if !validTerminalExpectation(expected) || len(allocations) > 1 {
		return FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalIndeterminate}
	}
	if len(allocations) == 0 {
		if len(expected.AllocationIDs) == 0 {
			return FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalPending}
		}
		return FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalIndeterminate}
	}
	a := allocations[0]
	if len(expected.AllocationIDs) != 1 || a.ID != expected.AllocationIDs[0] || a.JobID != expected.JobID || a.JobVersion != expected.JobVersion || !containsExact(expected.EvaluationIDs, a.EvaluationID) || a.TaskName != functionInvocationTaskName || a.TaskRestarts != 0 {
		return FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalIndeterminate}
	}
	switch a.ClientStatus {
	case nomadapi.AllocClientStatusPending, nomadapi.AllocClientStatusRunning:
		return FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalPending, AllocationID: a.ID}
	case nomadapi.AllocClientStatusComplete, nomadapi.AllocClientStatusFailed:
	default:
		return FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalIndeterminate}
	}
	if a.TaskState != "dead" || a.StartedAt.IsZero() || a.FinishedAt.IsZero() || a.FinishedAt.Before(a.StartedAt) || a.ExitCode == nil {
		return FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalIndeterminate}
	}
	result := FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalFailed, AllocationID: a.ID, ExitCode: cloneInt(a.ExitCode), Duration: a.FinishedAt.Sub(a.StartedAt)}
	if a.ClientStatus == nomadapi.AllocClientStatusComplete && !a.TaskFailed && a.ExitCode != nil && *a.ExitCode == 0 {
		result.State = FunctionInvocationTerminalComplete
	}
	return result
}

func validTerminalExpectation(expected FunctionInvocationTerminalExpectation) bool {
	return functionInvocationJobID.MatchString(expected.JobID) && expected.JobModifyIndex != 0 && len(expected.EvaluationIDs) > 0 && uniqueNonempty(expected.EvaluationIDs) && uniqueNonempty(expected.AllocationIDs)
}

func uniqueNonempty(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return false
		}
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func containsExact(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// ObserveFunctionInvocationTerminal obtains an exact, redacted allocation
// snapshot and verifies that the job revision did not change during the read.
func (c *Client) ObserveFunctionInvocationTerminal(ctx context.Context, region string, expected FunctionInvocationTerminalExpectation) (FunctionInvocationTerminalObservation, error) {
	if c == nil || c.api == nil || region != "global" || !validTerminalExpectation(expected) {
		return FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalIndeterminate}, ErrFunctionTerminalLookupIndeterminate
	}
	query := (&nomadapi.QueryOptions{Region: region}).WithContext(ctx)
	stubs, _, err := c.api.Jobs().Allocations(expected.JobID, true, query)
	if err != nil || !exactTerminalAllocationStubs(expected, stubs) {
		return FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalIndeterminate}, ErrFunctionTerminalLookupIndeterminate
	}
	observed := make([]FunctionInvocationTerminalAllocation, 0, len(stubs))
	for _, stub := range stubs {
		allocation, _, lookupErr := c.api.Allocations().Info(stub.ID, query)
		projected, ok := projectTerminalAllocation(stub, allocation, expected)
		if lookupErr != nil || !ok {
			return FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalIndeterminate}, ErrFunctionTerminalLookupIndeterminate
		}
		observed = append(observed, projected)
	}
	job, _, err := c.api.Jobs().Info(expected.JobID, query)
	if err != nil || job == nil || job.ID == nil || *job.ID != expected.JobID || job.Version == nil || *job.Version != expected.JobVersion || job.ModifyIndex == nil || *job.ModifyIndex != expected.JobModifyIndex {
		return FunctionInvocationTerminalObservation{State: FunctionInvocationTerminalIndeterminate}, ErrFunctionTerminalLookupIndeterminate
	}
	result := ProjectFunctionInvocationTerminal(expected, observed)
	if result.State == FunctionInvocationTerminalIndeterminate {
		return result, ErrFunctionTerminalLookupIndeterminate
	}
	return result, nil
}

func exactTerminalAllocationStubs(expected FunctionInvocationTerminalExpectation, stubs []*nomadapi.AllocationListStub) bool {
	ids := make([]string, 0, len(stubs))
	for _, stub := range stubs {
		if stub == nil || stub.ID == "" || stub.JobID != expected.JobID || stub.JobVersion != expected.JobVersion || !containsExact(expected.EvaluationIDs, stub.EvalID) {
			return false
		}
		ids = append(ids, stub.ID)
	}
	sort.Strings(ids)
	want := append([]string(nil), expected.AllocationIDs...)
	sort.Strings(want)
	if len(ids) != len(want) {
		return false
	}
	for i := range ids {
		if ids[i] != want[i] {
			return false
		}
	}
	return true
}

func projectTerminalAllocation(stub *nomadapi.AllocationListStub, allocation *nomadapi.Allocation, expected FunctionInvocationTerminalExpectation) (FunctionInvocationTerminalAllocation, bool) {
	if stub == nil || allocation == nil || allocation.ID != stub.ID || allocation.JobID != expected.JobID || allocation.EvalID != stub.EvalID || allocation.ClientStatus != stub.ClientStatus || len(allocation.TaskStates) != 1 {
		return FunctionInvocationTerminalAllocation{}, false
	}
	task, ok := allocation.TaskStates[functionInvocationTaskName]
	if !ok || task == nil {
		return FunctionInvocationTerminalAllocation{}, false
	}
	var exitCode *int
	for _, event := range task.Events {
		if event != nil && event.Type == nomadapi.TaskTerminated {
			code := event.ExitCode
			exitCode = &code
		}
	}
	return FunctionInvocationTerminalAllocation{ID: allocation.ID, JobID: allocation.JobID, JobVersion: stub.JobVersion, EvaluationID: allocation.EvalID, ClientStatus: allocation.ClientStatus, TaskName: functionInvocationTaskName, TaskState: task.State, TaskFailed: task.Failed, TaskRestarts: task.Restarts, StartedAt: task.StartedAt, FinishedAt: task.FinishedAt, ExitCode: exitCode}, true
}
