package nomad

import (
	"context"
	"errors"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestObserveStoppedMySQLSourceJobRequiresExactTerminalRevision(t *testing.T) {
	base := mysqlSourceObservationFixture()
	request := CASStopJobRequest{JobID: base.App, Region: base.NomadRegion, JobVersion: 7, JobModifyIndex: 44,
		AllocationIDs: []string{"alloc-a", "alloc-b"}, DeploymentID: base.DeploymentID, SpecDigest: base.SpecDigest,
		DatabaseBindingSchema: base.DatabaseBindingSchema, DatabaseBindingSHA256: base.DatabaseBindingSHA256,
		DatabaseCatalogRevision: base.DatabaseCatalogRevision}
	stopped := func(job *nomadapi.Job, allocations *[]*nomadapi.AllocationListStub) {
		value := true
		version, index := uint64(8), uint64(45)
		job.Stop, job.Version, job.JobModifyIndex = &value, &version, &index
		for _, allocation := range *allocations {
			allocation.DesiredStatus = nomadapi.AllocDesiredStatusStop
			allocation.ClientStatus = nomadapi.AllocClientStatusComplete
		}
	}
	client := newTestNomadClient(t, mysqlSourceObservationHandler(t, base, stopped))
	if err := client.ObserveStoppedMySQLSourceJob(context.Background(), request); err != nil {
		t.Fatalf("exact stopped revision was rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*nomadapi.Job, *[]*nomadapi.AllocationListStub)
	}{
		{name: "job restarted", change: func(job *nomadapi.Job, _ *[]*nomadapi.AllocationListStub) { value := false; job.Stop = &value }},
		{name: "new revision", change: func(job *nomadapi.Job, _ *[]*nomadapi.AllocationListStub) { value := uint64(9); job.Version = &value }},
		{name: "unchanged modify index", change: func(job *nomadapi.Job, _ *[]*nomadapi.AllocationListStub) {
			value := uint64(44)
			job.JobModifyIndex = &value
		}},
		{name: "live allocation", change: func(_ *nomadapi.Job, allocations *[]*nomadapi.AllocationListStub) {
			(*allocations)[1].ClientStatus = nomadapi.AllocClientStatusRunning
		}},
		{name: "missing signed allocation", change: func(_ *nomadapi.Job, allocations *[]*nomadapi.AllocationListStub) {
			*allocations = (*allocations)[:2]
		}},
		{name: "wrong provenance", change: func(job *nomadapi.Job, _ *[]*nomadapi.AllocationListStub) { job.Meta[SpecDigestMeta] = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTestNomadClient(t, mysqlSourceObservationHandler(t, base, func(job *nomadapi.Job, allocations *[]*nomadapi.AllocationListStub) {
				stopped(job, allocations)
				test.change(job, allocations)
			}))
			if err := client.ObserveStoppedMySQLSourceJob(context.Background(), request); !errors.Is(err, ErrMySQLSourceStoppedObservation) {
				t.Fatalf("unsafe stopped observation: %v", err)
			}
		})
	}
}
