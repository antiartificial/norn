package effect

import (
	"context"
	"errors"
	"testing"
)

type prepareRefusingSupervisor struct{ *memorySupervisor }

func (prepareRefusingSupervisor) Prepare(context.Context, Reservation) error {
	return errors.New("registered history is missing")
}

func TestPrepareRefusalDefersInsteadOfFailing(t *testing.T) {
	store := newMemoryStore()
	supervisor := newMemorySupervisor()
	executor := &Executor{Store: store, Supervisor: prepareRefusingSupervisor{supervisor}, Verifier: memoryVerifier{}}
	reservation := testReservation(t, "operation-1", "app/api/test", "execution-1", "go test ./...", 1)
	_, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: reservation})
	if !IsDeferred(err) {
		t.Fatalf("prepare refusal = %v, want deferred", err)
	}
	if store.reserveCalls != 0 || len(supervisor.launches) != 0 {
		t.Fatalf("prepare refusal reached reserve=%d launches=%v", store.reserveCalls, supervisor.launches)
	}
}

func TestRecoverCompletesOriginalExecutionWithoutLaunching(t *testing.T) {
	store := newMemoryStore()
	supervisor := newMemorySupervisor()
	executor := &Executor{Store: store, Supervisor: supervisor, Verifier: memoryVerifier{}}
	reservation := testReservation(t, "operation-1", "app/api/test", "execution-1", "go test ./...", 1)
	reserved, err := store.Reserve(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	identity := ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: "runtime-original"}
	if err := store.MarkLaunched(context.Background(), reserved.Record.Token, identity); err != nil {
		t.Fatal(err)
	}
	record := store.records[reserved.Record.Token.EffectID]

	supervisor.executions[reservation.SupervisorExecutionID] = supervisorExecution{identity: identity, phase: SupervisorRunning}
	if _, err := executor.Recover(context.Background(), record); !errors.Is(err, ErrEffectPending) {
		t.Fatalf("running recovery = %v, want pending", err)
	}
	supervisor.executions[reservation.SupervisorExecutionID] = supervisorExecution{identity: identity, phase: SupervisorSucceeded, output: []byte("ok")}
	result, err := executor.Recover(context.Background(), record)
	if err != nil || result.Outcome != OutcomeSucceeded || string(result.Output) != "ok" {
		t.Fatalf("recovery result=%+v err=%v", result, err)
	}
	completed := store.records[reserved.Record.Token.EffectID]
	if completed.Lifecycle != LifecycleCompleted || len(supervisor.launches) != 0 {
		t.Fatalf("recovery lifecycle=%s launches=%v", completed.Lifecycle, supervisor.launches)
	}
	reused, err := executor.Recover(context.Background(), completed)
	if err != nil || !reused.Reused || string(reused.Output) != "ok" {
		t.Fatalf("completed recovery = %+v, %v", reused, err)
	}
	if _, err := executor.Recover(context.Background(), Record{Reservation: reservation}); err == nil {
		t.Fatal("recovery without a durable token accepted")
	}
}

func TestRecoverOfNeverLaunchedRecordReportsResolution(t *testing.T) {
	store := newMemoryStore()
	supervisor := newMemorySupervisor()
	executor := &Executor{Store: store, Supervisor: supervisor, Verifier: memoryVerifier{}}
	reservation := testReservation(t, "operation-1", "app/api/test", "execution-1", "go test ./...", 1)
	reserved, err := store.Reserve(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Recover(context.Background(), reserved.Record)
	var pending *PendingError
	if !errors.As(err, &pending) || pending.Reason != ResolutionRecordedReason {
		t.Fatalf("never-launched recovery = %v", err)
	}
	if store.records[reserved.Record.Token.EffectID].Lifecycle != LifecycleResolved || !supervisor.tombstones[reservation.SupervisorExecutionID] {
		t.Fatal("never-launched recovery did not tombstone and resolve")
	}
}
