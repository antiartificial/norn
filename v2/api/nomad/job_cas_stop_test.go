package nomad

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestStopJobCASStopsExactSignedRevision(t *testing.T) {
	request := casStopFixture()
	stopped := false
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version, index := request.JobVersion, request.JobModifyIndex
		if stopped {
			version++
			index++
		}
		switch {
		case r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.11.5"}}})
		case r.URL.Path == "/v1/job/source-db" && r.Method == http.MethodGet:
			job := casStopJob(request, version, index, stopped)
			_ = json.NewEncoder(w).Encode(job)
		case r.URL.Path == "/v1/job/source-db/allocations":
			status := nomadapi.AllocClientStatusRunning
			if stopped {
				status = nomadapi.AllocClientStatusComplete
			}
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{
				{ID: "old-terminal", JobID: request.JobID, JobVersion: request.JobVersion - 1, ClientStatus: nomadapi.AllocClientStatusComplete},
				{ID: "alloc-1", JobID: request.JobID, JobVersion: request.JobVersion, ClientStatus: status},
			})
		case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPut:
			var register nomadapi.JobRegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&register); err != nil {
				t.Fatal(err)
			}
			if !register.EnforceIndex || register.JobModifyIndex != request.JobModifyIndex || register.Job == nil || register.Job.Stop == nil || !*register.Job.Stop {
				t.Fatalf("stop CAS request=%+v", register)
			}
			stopped = true
			_ = json.NewEncoder(w).Encode(&nomadapi.JobRegisterResponse{JobModifyIndex: request.JobModifyIndex + 1})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.StopJobCAS(ctx, request); err != nil {
		t.Fatal(err)
	}
}

func TestStopJobCASRejectsProvenanceVersionAndAllocationSubstitution(t *testing.T) {
	tests := []struct {
		name   string
		change func(*nomadapi.Job, *[]*nomadapi.AllocationListStub)
	}{
		{name: "wrong metadata", change: func(job *nomadapi.Job, _ *[]*nomadapi.AllocationListStub) { job.Meta[DeploymentIDMeta] = "other" }},
		{name: "wrong job version", change: func(job *nomadapi.Job, _ *[]*nomadapi.AllocationListStub) { *job.Version++ }},
		{name: "allocation version substitution", change: func(_ *nomadapi.Job, allocations *[]*nomadapi.AllocationListStub) { (*allocations)[0].JobVersion++ }},
		{name: "allocation id substitution", change: func(_ *nomadapi.Job, allocations *[]*nomadapi.AllocationListStub) { (*allocations)[0].ID = "competing" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := casStopFixture()
			wrote := false
			client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				job := casStopJob(request, request.JobVersion, request.JobModifyIndex, false)
				allocations := []*nomadapi.AllocationListStub{{ID: "alloc-1", JobID: request.JobID, JobVersion: request.JobVersion, ClientStatus: nomadapi.AllocClientStatusRunning}}
				test.change(job, &allocations)
				switch r.URL.Path {
				case "/v1/agent/self":
					_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.11.5"}}})
				case "/v1/job/source-db":
					_ = json.NewEncoder(w).Encode(job)
				case "/v1/job/source-db/allocations":
					_ = json.NewEncoder(w).Encode(allocations)
				case "/v1/jobs":
					wrote = true
					http.Error(w, "must not write", http.StatusConflict)
				default:
					http.Error(w, "unexpected", http.StatusNotFound)
				}
			}))
			if err := client.StopJobCAS(context.Background(), request); err == nil || wrote {
				t.Fatalf("err=%v wrote=%v", err, wrote)
			}
		})
	}
}

func TestStopJobCASRejectsWrongStoppedRevisionAfterMutation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*nomadapi.Job)
	}{
		{name: "metadata", change: func(job *nomadapi.Job) { job.Meta[SpecDigestMeta] = "sha256:substituted" }},
		{name: "version", change: func(job *nomadapi.Job) { *job.Version++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := casStopFixture()
			stopped := false
			client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				version, index := request.JobVersion, request.JobModifyIndex
				if stopped {
					version++
					index++
				}
				switch r.URL.Path {
				case "/v1/agent/self":
					_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.11.5"}}})
				case "/v1/job/source-db":
					job := casStopJob(request, version, index, stopped)
					if stopped {
						test.change(job)
					}
					_ = json.NewEncoder(w).Encode(job)
				case "/v1/job/source-db/allocations":
					status := nomadapi.AllocClientStatusRunning
					if stopped {
						status = nomadapi.AllocClientStatusComplete
					}
					_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: "alloc-1", JobID: request.JobID, JobVersion: request.JobVersion, ClientStatus: status}})
				case "/v1/jobs":
					stopped = true
					_ = json.NewEncoder(w).Encode(&nomadapi.JobRegisterResponse{JobModifyIndex: request.JobModifyIndex + 1})
				default:
					http.Error(w, "unexpected", http.StatusNotFound)
				}
			}))
			if err := client.StopJobCAS(context.Background(), request); !errors.Is(err, ErrJobStopVerificationIndeterminate) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func casStopFixture() CASStopJobRequest {
	return CASStopJobRequest{JobID: "source-db", Region: "global", JobVersion: 7, JobModifyIndex: 44, AllocationIDs: []string{"alloc-1"},
		DeploymentID: "deployment-1", SpecDigest: "sha256:" + repeat("a", 64), DatabaseBindingSchema: "norn.database-targets/v1",
		DatabaseBindingSHA256: repeat("b", 64), DatabaseCatalogRevision: "29"}
}

func casStopJob(request CASStopJobRequest, version, index uint64, stopped bool) *nomadapi.Job {
	return &nomadapi.Job{ID: &request.JobID, Region: &request.Region, Version: &version, JobModifyIndex: &index, Stop: &stopped, Meta: map[string]string{
		DeploymentIDMeta: request.DeploymentID, SpecDigestMeta: request.SpecDigest, DatabaseBindingSchemaMeta: request.DatabaseBindingSchema,
		DatabaseBindingSHA256Meta: request.DatabaseBindingSHA256, DatabaseCatalogRevisionMeta: request.DatabaseCatalogRevision,
	}}
}
