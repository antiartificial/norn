package nomad

import (
	"context"
	"errors"
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
	if err := BindDeploymentProvenance(job, DeploymentProvenance{DeploymentID: "qualification-deployment", SpecDigest: "sha256:" + repeat("a", 64), DatabaseBindingSchema: "norn.database-targets/v1", DatabaseBindingSHA256: repeat("b", 64), DatabaseCatalogRevision: "1"}); err != nil {
		t.Fatal(err)
	}
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
	if current.Version == nil {
		t.Fatal("job version is unavailable")
	}
	if err := client.StopJobCAS(ctx, CASStopJobRequest{JobID: id, Region: region, JobVersion: *current.Version, JobModifyIndex: *current.JobModifyIndex, AllocationIDs: []string{observed[0].ID},
		DeploymentID: "qualification-deployment", SpecDigest: "sha256:" + repeat("a", 64), DatabaseBindingSchema: "norn.database-targets/v1", DatabaseBindingSHA256: repeat("b", 64), DatabaseCatalogRevision: "1"}); err != nil {
		t.Fatal(err)
	}
	request := CASStopJobRequest{JobID: id, Region: region, JobVersion: *current.Version, JobModifyIndex: *current.JobModifyIndex, AllocationIDs: []string{observed[0].ID},
		DeploymentID: "qualification-deployment", SpecDigest: "sha256:" + repeat("a", 64), DatabaseBindingSchema: "norn.database-targets/v1", DatabaseBindingSHA256: repeat("b", 64), DatabaseCatalogRevision: "1"}
	if err := client.ObserveStoppedMySQLSourceJob(ctx, request); err != nil {
		t.Fatalf("guarded stop was not visible to recovery observer: %v", err)
	}
	restarted, _, err := client.api.Jobs().Info(id, (&nomadapi.QueryOptions{Region: region}).WithContext(ctx))
	if err != nil || restarted == nil {
		t.Fatalf("read stopped job before direct restart: %v", err)
	}
	resume := false
	restarted.Stop = &resume
	if _, _, err := client.api.Jobs().Register(restarted, (&nomadapi.WriteOptions{Region: region}).WithContext(ctx)); err != nil {
		t.Fatalf("direct restart of disposable job: %v", err)
	}
	if err := client.ObserveStoppedMySQLSourceJob(ctx, request); !errors.Is(err, ErrMySQLSourceStoppedObservation) {
		t.Fatalf("recovery observer accepted a directly restarted source: %v", err)
	}
}
