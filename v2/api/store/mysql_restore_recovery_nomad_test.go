package store

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/nomad"
)

// The disposable raw_exec job is an exact Nomad stop/observation fixture.
// The claimed source runner stops it after the signed request is accepted.
// It does not stand in for a managed WordPress allocation or source SQL writer.
func disposableMySQLSourceJob(t *testing.T, parent context.Context, address string, revision int64) (MySQLSourceSnapshotJobIdentity, *nomad.Client) {
	t.Helper()
	raw, err := nomadapi.NewClient(&nomadapi.Config{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	id := "norn-mysql-recovery-qual-" + uuid.NewString()[:8]
	region, kind, group, task, driver := "global", "batch", "work", "sleep", "raw_exec"
	count := 1
	job := &nomadapi.Job{ID: &id, Name: &id, Region: &region, Type: &kind, Datacenters: []string{"dc1"},
		TaskGroups: []*nomadapi.TaskGroup{{Name: &group, Count: &count, Tasks: []*nomadapi.Task{{Name: task, Driver: driver,
			Config: map[string]interface{}{"command": "/bin/sleep", "args": []string{"60"}}}}}}}
	identity := validSourceSnapshotJobIdentity(id, revision, "1", "pending-allocation")
	if err := nomad.BindDeploymentProvenance(job, nomad.DeploymentProvenance{DeploymentID: identity.DeploymentID,
		SpecDigest: identity.SpecDigest, DatabaseBindingSchema: identity.DatabaseBindingSchema,
		DatabaseBindingSHA256: identity.DatabaseBindingSHA256, DatabaseCatalogRevision: identity.DatabaseCatalogRevision}); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, _, err := raw.Jobs().Deregister(id, true, (&nomadapi.WriteOptions{Region: region}).WithContext(ctx)); err != nil {
			t.Errorf("purge disposable source job %s: %v", id, err)
		}
	}
	t.Cleanup(cleanup)
	if _, _, err := raw.Jobs().Register(job, (&nomadapi.WriteOptions{Region: region}).WithContext(parent)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	for {
		current, _, jobErr := raw.Jobs().Info(id, (&nomadapi.QueryOptions{Region: region}).WithContext(ctx))
		allocations, _, allocErr := raw.Jobs().Allocations(id, true, (&nomadapi.QueryOptions{Region: region}).WithContext(ctx))
		if jobErr != nil || allocErr != nil {
			t.Fatalf("observe disposable source job: job=%v allocations=%v", jobErr, allocErr)
		}
		if current != nil && current.Version != nil && current.JobModifyIndex != nil && len(allocations) == 1 && allocations[0] != nil && allocations[0].ID != "" {
			identity.JobVersion = strconv.FormatUint(*current.Version, 10)
			identity.JobModifyIndex = strconv.FormatUint(*current.JobModifyIndex, 10)
			identity.AllocationIDs = []string{allocations[0].ID}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("disposable source job did not produce an allocation")
		case <-time.After(100 * time.Millisecond):
		}
	}
	return identity, observer
}
