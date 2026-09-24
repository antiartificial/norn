package pipeline

import (
	"context"
	"errors"
	"testing"

	"norn/v2/api/effect"
	"norn/v2/api/nomad"
)

type restartNomadFake struct {
	stops   []nomad.RestartAllocation
	stopErr error
	status  nomad.RestartStatus
}

func (f *restartNomadFake) RestartSnapshot(context.Context, string) ([]nomad.RestartAllocation, error) {
	return nil, errors.New("unexpected snapshot")
}
func (f *restartNomadFake) StopRestartAllocation(_ context.Context, source nomad.RestartAllocation) error {
	f.stops = append(f.stops, source)
	return f.stopErr
}
func (f *restartNomadFake) RestartStatus(context.Context, string, []nomad.RestartAllocation) (nomad.RestartStatus, error) {
	return f.status, nil
}

func restartReservation(t *testing.T, sources []nomad.RestartAllocation) effect.Reservation {
	t.Helper()
	payload := []byte(`{"app":"demo","allocations":[`)
	for i, source := range sources {
		if i > 0 {
			payload = append(payload, ',')
		}
		payload = append(payload, []byte(`{"id":"`+source.ID+`","jobId":"demo","createIndex":`+"7"+`}`)...)
	}
	payload = append(payload, []byte(`]}`)...)
	r := effect.Reservation{Authority: "authority", Resource: restartResource("demo"), OperationClaim: effect.OperationClaim{OperationID: "op", OwnerID: "worker", Generation: 1}, Stage: nomadRestartStage, Supervisor: "nomad-restart", SupervisorExecutionID: "restart-1", LaunchPayload: payload}
	var err error
	r.InputDigest, err = effect.ComputeInputDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRestartLaunchStopsOnlyPersistedSourceAllocations(t *testing.T) {
	source := nomad.RestartAllocation{ID: "source-a", JobID: "demo", CreateIndex: 7}
	fake := &restartNomadFake{status: nomad.RestartStatus{App: "demo", Replaced: true}}
	supervisor := &nomadRestartSupervisor{client: fake}
	r := restartReservation(t, []nomad.RestartAllocation{source})
	identity, err := supervisor.Launch(context.Background(), r, effect.LaunchMaterial{})
	if err != nil || identity.RuntimeInstanceID != "nomad-restart:restart-1" || len(fake.stops) != 1 || fake.stops[0] != source {
		t.Fatalf("launch identity=%+v stops=%+v err=%v", identity, fake.stops, err)
	}
}

func TestRestartAmbiguousStopDefersAndNeverRepeatsStop(t *testing.T) {
	source := nomad.RestartAllocation{ID: "source-a", JobID: "demo", CreateIndex: 7}
	fake := &restartNomadFake{stopErr: errors.New("connection dropped"), status: nomad.RestartStatus{App: "demo", Replaced: false}}
	supervisor := &nomadRestartSupervisor{client: fake}
	r := restartReservation(t, []nomad.RestartAllocation{source})
	if _, err := supervisor.Launch(context.Background(), r, effect.LaunchMaterial{}); err == nil || len(fake.stops) != 1 {
		t.Fatalf("first launch err=%v stops=%+v", err, fake.stops)
	}
	observation, err := supervisor.Query(context.Background(), r, effect.ExecutionIdentity{})
	if err != nil || observation.Phase != effect.SupervisorUnknown || len(fake.stops) != 1 {
		t.Fatalf("reconciliation observation=%+v stops=%+v err=%v", observation, fake.stops, err)
	}
	if _, err := (nomadRestartVerifier{}).Verify(context.Background(), effect.Record{Reservation: r}, observation); err == nil {
		t.Fatal("ambiguous restart was accepted as a terminal outcome")
	}
}

func TestRestartDescriptorRejectsReplacementOrDuplicateSources(t *testing.T) {
	r := restartReservation(t, []nomad.RestartAllocation{{ID: "source-a", JobID: "demo", CreateIndex: 7}, {ID: "source-a", JobID: "demo", CreateIndex: 7}})
	if _, err := restartRequestFromReservation(r); err == nil {
		t.Fatal("duplicate source allocation accepted")
	}
	first := restartExecutionID(restartReservation(t, []nomad.RestartAllocation{{ID: "source-a", JobID: "demo", CreateIndex: 7}}))
	next := restartReservation(t, []nomad.RestartAllocation{{ID: "source-a", JobID: "demo", CreateIndex: 7}})
	next.OperationClaim.Generation = 2
	if second := restartExecutionID(next); first == second {
		t.Fatal("successor claim reused predecessor restart identity")
	}
}
