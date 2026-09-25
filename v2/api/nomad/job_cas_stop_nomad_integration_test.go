package nomad

import (
	"context"
	"os"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

// This qualification owns a disposable raw_exec job and purges it on exit.
// It is intentionally opt-in because it stops a real allocation.
func TestStopJobCASInDisposableNomad(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	if address == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR to a disposable Nomad agent")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	id := "norn-cas-stop-qual-" + time.Now().UTC().Format("20060102150405")
	region, kind, group, task, driver, command := "global", "batch", "work", "sleep", "raw_exec", "/bin/sleep"
	count := 1
	job := &nomadapi.Job{ID: &id, Name: &id, Region: &region, Type: &kind, Datacenters: []string{"dc1"}, TaskGroups: []*nomadapi.TaskGroup{{Name: &group, Count: &count, Tasks: []*nomadapi.Task{{Name: task, Driver: driver, Config: map[string]interface{}{"command": command, "args": []string{"30"}}}}}}}
	t.Cleanup(func() {
		_, _, _ = client.api.Jobs().Deregister(id, true, (&nomadapi.WriteOptions{Region: region}).WithContext(context.Background()))
	})
	if _, _, err := client.api.Jobs().Register(job, (&nomadapi.WriteOptions{Region: region}).WithContext(context.Background())); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var observed []*nomadapi.AllocationListStub
	var current *nomadapi.Job
	for {
		current, _, err = client.api.Jobs().Info(id, (&nomadapi.QueryOptions{Region: region}).WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		observed, _, err = client.api.Jobs().Allocations(id, true, (&nomadapi.QueryOptions{Region: region}).WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		if current.JobModifyIndex != nil && len(observed) == 1 && observed[0].ID != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("job did not produce one observed allocation")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err := client.StopJobCAS(ctx, CASStopJobRequest{JobID: id, Region: region, JobModifyIndex: *current.JobModifyIndex, AllocationIDs: []string{observed[0].ID}}); err != nil {
		t.Fatal(err)
	}
}
