package nomad

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestManagedDeploymentJobsReadBackAndHealIndependently(t *testing.T) {
	requests := map[string]CASDeploymentJobRequest{}
	sources := map[string]string{}
	jobs := map[string]*nomadapi.Job{}
	stubs := map[string]*nomadapi.AllocationListStub{}
	allocations := map[string]*nomadapi.Allocation{}
	image := "registry.example.test/demo@sha256:" + strings.Repeat("b", 64)
	for _, deploymentID := range []string{"deployment-old", "deployment-new"} {
		request := managedDeploymentCASRequest()
		request.DeploymentID = deploymentID
		request.ImageTag = image
		var err error
		request.JobID, err = ManagedDeploymentJobID(request.App, request.PlacementRegion, deploymentID)
		if err != nil {
			t.Fatal(err)
		}
		request.Job.ID = &request.JobID
		request.Job.Meta[DeploymentIDMeta] = deploymentID
		name, kind, groupName, count, stopped := request.JobID, "service", "web", 1, false
		request.Job.Name, request.Job.Type, request.Job.Stop = &name, &kind, &stopped
		request.Job.TaskGroups = []*nomadapi.TaskGroup{{Name: &groupName, Count: &count,
			Tasks: []*nomadapi.Task{{Name: groupName, Driver: "docker", Config: map[string]interface{}{"image": image}}}}}
		request.JobDigest, err = DigestDeploymentJob(request.Job)
		if err != nil {
			t.Fatal(err)
		}
		request.Job.Meta[DeploymentJobDigestMeta] = request.JobDigest
		source, err := json.Marshal(request.Job)
		if err != nil {
			t.Fatal(err)
		}
		var current nomadapi.Job
		if err := json.Unmarshal(source, &current); err != nil {
			t.Fatal(err)
		}
		version, index := uint64(1), uint64(1)
		current.Version, current.JobModifyIndex = &version, &index
		healthy := true
		allocID := "allocation-" + deploymentID
		stub := &nomadapi.AllocationListStub{ID: allocID, JobID: request.JobID, JobVersion: version, TaskGroup: groupName,
			ClientStatus: nomadapi.AllocClientStatusRunning, DesiredStatus: nomadapi.AllocDesiredStatusRun,
			DeploymentStatus: &nomadapi.AllocDeploymentStatus{Healthy: &healthy}}
		requests[request.JobID] = request
		sources[request.JobID] = string(source)
		jobs[request.JobID] = &current
		stubs[request.JobID] = stub
		allocations[allocID] = &nomadapi.Allocation{ID: allocID, JobID: request.JobID, TaskGroup: groupName, Job: &current,
			ClientStatus: stub.ClientStatus, DesiredStatus: stub.DesiredStatus,
			TaskStates: map[string]*nomadapi.TaskState{groupName: {State: "running"}}}
	}
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/jobs" {
			var body nomadapi.JobRegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Job == nil || body.Job.ID == nil ||
				requests[*body.Job.ID].JobID == "" || !body.EnforceIndex || body.JobModifyIndex != 0 {
				t.Errorf("unbound managed job submission: %+v err=%v", body, err)
				return
			}
			writes++
			_ = json.NewEncoder(w).Encode(&nomadapi.JobRegisterResponse{EvalID: "eval-" + *body.Job.ID, JobModifyIndex: 1})
			return
		}
		for jobID := range requests {
			prefix := "/v1/job/" + jobID
			switch r.URL.Path {
			case prefix:
				_ = json.NewEncoder(w).Encode(jobs[jobID])
				return
			case prefix + "/submission":
				_ = json.NewEncoder(w).Encode(&nomadapi.JobSubmission{Source: sources[jobID], Format: "json"})
				return
			case prefix + "/plan":
				_ = json.NewEncoder(w).Encode(&nomadapi.JobPlanResponse{JobModifyIndex: 1, Diff: &nomadapi.JobDiff{Type: "None"}})
				return
			case prefix + "/allocations":
				_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{stubs[jobID]})
				return
			}
		}
		if allocation, ok := allocations[strings.TrimPrefix(r.URL.Path, "/v1/allocation/")]; ok && strings.HasPrefix(r.URL.Path, "/v1/allocation/") {
			_ = json.NewEncoder(w).Encode(allocation)
			return
		}
		t.Errorf("unexpected Nomad request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for jobID, request := range requests {
		if _, err := client.RegisterDeploymentJobCAS(context.Background(), request); err != nil {
			t.Fatalf("register %s: %v", jobID, err)
		}
		observed, err := client.LookupDeploymentJobRevision(context.Background(), request)
		if err != nil || observed.State != DeploymentJobFound || observed.JobModifyIndex != 1 {
			t.Fatalf("readback %s=%+v err=%v", jobID, observed, err)
		}
		health, err := client.ObserveDeploymentJobHealth(context.Background(), request)
		if err != nil || health.State != DeploymentJobHealthReady || len(health.AllocationIDs) != 1 || health.AllocationIDs[0] != stubs[jobID].ID {
			t.Fatalf("health %s=%+v err=%v", jobID, health, err)
		}
	}
	if writes != 2 {
		t.Fatalf("managed jobs submitted %d times, want two distinct creates", writes)
	}
}
