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

func TestStopJobCASStopsExactObservedRevision(t *testing.T) {
	jobID, region := "source-db", "global"
	index := uint64(44)
	written := false
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.11.5"}}})
		case r.URL.Path == "/v1/job/source-db" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Region: &region, JobModifyIndex: &index, Stop: &written})
		case r.URL.Path == "/v1/job/source-db/allocations":
			status := nomadapi.AllocClientStatusRunning
			if written {
				status = nomadapi.AllocClientStatusComplete
			}
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: "alloc-1", JobID: jobID, ClientStatus: status}})
		case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPut:
			var request nomadapi.JobRegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if !request.EnforceIndex || request.JobModifyIndex != index || request.Job == nil || request.Job.Stop == nil || !*request.Job.Stop {
				t.Fatalf("stop CAS request=%+v", request)
			}
			written = true
			_ = json.NewEncoder(w).Encode(&nomadapi.JobRegisterResponse{})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.StopJobCAS(ctx, CASStopJobRequest{JobID: jobID, Region: region, JobModifyIndex: index, AllocationIDs: []string{"alloc-1"}}); err != nil {
		t.Fatal(err)
	}
}

func TestStopJobCASRejectsWrongRevisionBeforeMutation(t *testing.T) {
	jobID, region := "source-db", "global"
	current := uint64(45)
	wrote := false
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.11.5"}}})
		case "/v1/job/source-db":
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Region: &region, JobModifyIndex: &current})
		case "/v1/jobs":
			wrote = true
			http.Error(w, "must not write", http.StatusConflict)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	err := client.StopJobCAS(context.Background(), CASStopJobRequest{JobID: jobID, Region: region, JobModifyIndex: 44, AllocationIDs: []string{"alloc-1"}})
	if !errors.Is(err, ErrJobRevisionChanged) || wrote {
		t.Fatalf("err=%v wrote=%v", err, wrote)
	}
}

func TestStopJobCASRejectsCompetingAllocation(t *testing.T) {
	jobID, region := "source-db", "global"
	index := uint64(44)
	written := false
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.11.5"}}})
		case r.URL.Path == "/v1/job/source-db":
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Region: &region, JobModifyIndex: &index, Stop: &written})
		case r.URL.Path == "/v1/job/source-db/allocations":
			if !written {
				_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: "alloc-1", JobID: jobID, ClientStatus: nomadapi.AllocClientStatusRunning}})
				return
			}
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: "alloc-1", JobID: jobID, ClientStatus: nomadapi.AllocClientStatusComplete}, {ID: "competing", JobID: jobID, ClientStatus: nomadapi.AllocClientStatusRunning}})
		case r.URL.Path == "/v1/jobs":
			written = true
			_ = json.NewEncoder(w).Encode(&nomadapi.JobRegisterResponse{})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	err := client.StopJobCAS(context.Background(), CASStopJobRequest{JobID: jobID, Region: region, JobModifyIndex: index, AllocationIDs: []string{"alloc-1"}})
	if !errors.Is(err, ErrJobStopVerificationIndeterminate) {
		t.Fatalf("err=%v", err)
	}
}

func TestStopJobCASRejectsTerminalAllocationsWithoutStoppedJob(t *testing.T) {
	jobID, region := "source-db", "global"
	index := uint64(44)
	written := false
	stopped := false
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.11.5"}}})
		case "/v1/job/source-db":
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Region: &region, JobModifyIndex: &index, Stop: &stopped})
		case "/v1/job/source-db/allocations":
			status := nomadapi.AllocClientStatusRunning
			if written {
				status = nomadapi.AllocClientStatusComplete
			}
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: "alloc-1", JobID: jobID, ClientStatus: status}})
		case "/v1/jobs":
			written = true
			_ = json.NewEncoder(w).Encode(&nomadapi.JobRegisterResponse{})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	err := client.StopJobCAS(context.Background(), CASStopJobRequest{JobID: jobID, Region: region, JobModifyIndex: index, AllocationIDs: []string{"alloc-1"}})
	if !written || !errors.Is(err, ErrJobStopVerificationIndeterminate) {
		t.Fatalf("written=%v err=%v", written, err)
	}
}
