package nomad

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestObserveMySQLSourceJobReturnsExactProvenanceAndLiveAllocations(t *testing.T) {
	request := mysqlSourceObservationFixture()
	client := newTestNomadClient(t, mysqlSourceObservationHandler(t, request, func(job *nomadapi.Job, allocations *[]*nomadapi.AllocationListStub) {}))
	got, err := client.ObserveMySQLSourceJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got.JobID != request.App || got.JobVersion != "7" || got.JobModifyIndex != "44" || len(got.AllocationIDs) != 2 || got.AllocationIDs[0] != "alloc-a" || got.AllocationIDs[1] != "alloc-b" {
		t.Fatalf("observation=%+v", got)
	}
}

func TestObserveMySQLSourceJobRejectsWrongProvenanceAndAllocationVersion(t *testing.T) {
	request := mysqlSourceObservationFixture()
	tests := []struct {
		name   string
		change func(*nomadapi.Job, *[]*nomadapi.AllocationListStub)
	}{
		{name: "wrong deployment", change: func(job *nomadapi.Job, _ *[]*nomadapi.AllocationListStub) { job.Meta[DeploymentIDMeta] = "other" }},
		{name: "stopped job", change: func(job *nomadapi.Job, _ *[]*nomadapi.AllocationListStub) { stopped := true; job.Stop = &stopped }},
		{name: "wrong live version", change: func(_ *nomadapi.Job, allocations *[]*nomadapi.AllocationListStub) { (*allocations)[1].JobVersion++ }},
		{name: "no live allocation", change: func(_ *nomadapi.Job, allocations *[]*nomadapi.AllocationListStub) {
			(*allocations)[1].ClientStatus = nomadapi.AllocClientStatusComplete
			(*allocations)[2].ClientStatus = nomadapi.AllocClientStatusComplete
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTestNomadClient(t, mysqlSourceObservationHandler(t, request, test.change))
			if _, err := client.ObserveMySQLSourceJob(context.Background(), request); !errors.Is(err, ErrMySQLSourceJobObservation) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func mysqlSourceObservationFixture() MySQLSourceJobObservationRequest {
	return MySQLSourceJobObservationRequest{App: "wordpress", DeploymentID: "deployment-1", SpecDigest: "sha256:" + repeat("a", 64), Region: "west", NomadRegion: "global", DatabaseBindingSchema: "norn.database-targets/v1", DatabaseBindingSHA256: repeat("b", 64), DatabaseCatalogRevision: "29"}
}

func mysqlSourceObservationHandler(t *testing.T, request MySQLSourceJobObservationRequest, change func(*nomadapi.Job, *[]*nomadapi.AllocationListStub)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version, index, stopped := uint64(7), uint64(44), false
		job := &nomadapi.Job{ID: &request.App, Region: &request.NomadRegion, Version: &version, JobModifyIndex: &index, Stop: &stopped, Meta: map[string]string{
			DeploymentIDMeta: request.DeploymentID, SpecDigestMeta: request.SpecDigest, DatabaseBindingSchemaMeta: request.DatabaseBindingSchema,
			DatabaseBindingSHA256Meta: request.DatabaseBindingSHA256, DatabaseCatalogRevisionMeta: request.DatabaseCatalogRevision,
		}}
		allocations := []*nomadapi.AllocationListStub{
			{ID: "old", JobID: request.App, JobVersion: 6, DesiredStatus: nomadapi.AllocDesiredStatusStop, ClientStatus: nomadapi.AllocClientStatusComplete},
			{ID: "alloc-b", JobID: request.App, JobVersion: 7, DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning},
			{ID: "alloc-a", JobID: request.App, JobVersion: 7, DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusPending},
		}
		change(job, &allocations)
		switch r.URL.Path {
		case "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.11.5"}}})
		case "/v1/job/wordpress":
			if r.URL.Query().Get("region") != request.NomadRegion {
				t.Fatalf("region=%q", r.URL.Query().Get("region"))
			}
			_ = json.NewEncoder(w).Encode(job)
		case "/v1/job/wordpress/allocations":
			if r.URL.Query().Get("all") != "true" || r.URL.Query().Get("region") != request.NomadRegion {
				t.Fatalf("query=%s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(allocations)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})
}

func repeat(value string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += value
	}
	return result
}
