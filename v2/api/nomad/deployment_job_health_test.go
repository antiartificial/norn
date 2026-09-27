package nomad

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestObserveDeploymentJobHealthRequiresExactHealthyRevision(t *testing.T) {
	request := deploymentCASRequest()
	request.ImageTag = "registry.example.test/demo@sha256:" + strings.Repeat("b", 64)
	name, kind, groupName, count, stopped := request.App, "service", "web", 1, false
	request.Job.Name, request.Job.Type, request.Job.Stop = &name, &kind, &stopped
	request.Job.TaskGroups = []*nomadapi.TaskGroup{{Name: &groupName, Count: &count, Tasks: []*nomadapi.Task{{Name: groupName, Driver: "docker", Config: map[string]interface{}{"image": request.ImageTag}}}}}
	digest, err := DigestDeploymentJob(request.Job)
	if err != nil {
		t.Fatal(err)
	}
	request.JobDigest = digest
	request.Job.Meta[DeploymentJobDigestMeta] = digest
	source, err := json.Marshal(request.Job)
	if err != nil {
		t.Fatal(err)
	}
	var current nomadapi.Job
	if err := json.Unmarshal(source, &current); err != nil {
		t.Fatal(err)
	}
	version, index := uint64(3), uint64(43)
	current.Version, current.JobModifyIndex = &version, &index
	healthy := true
	stub := &nomadapi.AllocationListStub{ID: "alloc-1", JobID: request.App, JobVersion: version, TaskGroup: groupName,
		ClientStatus: nomadapi.AllocClientStatusRunning, DesiredStatus: nomadapi.AllocDesiredStatusRun,
		DeploymentStatus: &nomadapi.AllocDeploymentStatus{Healthy: &healthy}}
	old := &nomadapi.AllocationListStub{ID: "old-alloc", JobID: request.App, JobVersion: version - 1, TaskGroup: "removed-group",
		ClientStatus: nomadapi.AllocClientStatusComplete, DesiredStatus: nomadapi.AllocDesiredStatusStop}
	allocation := &nomadapi.Allocation{ID: stub.ID, JobID: request.App, TaskGroup: groupName, Job: &current,
		ClientStatus: stub.ClientStatus, DesiredStatus: stub.DesiredStatus, TaskStates: map[string]*nomadapi.TaskState{groupName: {State: "running"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/job/" + request.App:
			_ = json.NewEncoder(w).Encode(&current)
		case "/v1/job/" + request.App + "/submission":
			_ = json.NewEncoder(w).Encode(&nomadapi.JobSubmission{Source: string(source), Format: "json"})
		case "/v1/job/" + request.App + "/plan":
			_ = json.NewEncoder(w).Encode(&nomadapi.JobPlanResponse{JobModifyIndex: index, Diff: &nomadapi.JobDiff{Type: "None"}})
		case "/v1/job/" + request.App + "/allocations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{stub, old})
		case "/v1/allocation/" + stub.ID:
			_ = json.NewEncoder(w).Encode(allocation)
		default:
			t.Errorf("unexpected Nomad request %s %s", r.Method, r.URL)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := client.ObserveDeploymentJobHealth(context.Background(), request)
	if err != nil || observation.State != DeploymentJobHealthReady || len(observation.AllocationIDs) != 1 || observation.AllocationIDs[0] != stub.ID {
		t.Fatalf("healthy revision observation=%+v err=%v", observation, err)
	}
	old.ClientStatus = nomadapi.AllocClientStatusRunning
	old.DesiredStatus = nomadapi.AllocDesiredStatusRun
	observation, err = client.ObserveDeploymentJobHealth(context.Background(), request)
	if !errors.Is(err, ErrDeploymentJobHealthIndeterminate) || observation.State != DeploymentJobHealthIndeterminate {
		t.Fatalf("live removed group observation=%+v err=%v", observation, err)
	}
	old.ClientStatus = nomadapi.AllocClientStatusComplete
	old.DesiredStatus = nomadapi.AllocDesiredStatusStop
	healthy = false
	observation, err = client.ObserveDeploymentJobHealth(context.Background(), request)
	if err != nil || observation.State != DeploymentJobHealthPending {
		t.Fatalf("unhealthy allocation observation=%+v err=%v", observation, err)
	}
	healthy = true
	stub.JobVersion++
	observation, err = client.ObserveDeploymentJobHealth(context.Background(), request)
	if err != nil || observation.State != DeploymentJobHealthPending {
		t.Fatalf("foreign-version allocation observation=%+v err=%v", observation, err)
	}
	stub.JobVersion = version
	current.TaskGroups[0].Tasks[0].Config["image"] = "changed"
	observation, err = client.ObserveDeploymentJobHealth(context.Background(), request)
	if !errors.Is(err, ErrDeploymentJobHealthIndeterminate) || observation.State != DeploymentJobHealthIndeterminate {
		t.Fatalf("changed job observation=%+v err=%v", observation, err)
	}
	current.TaskGroups[0].Tasks[0].Config["image"] = request.ImageTag
	canary := 1
	current.TaskGroups[0].Update = &nomadapi.UpdateStrategy{Canary: &canary}
	observation, err = client.ObserveDeploymentJobHealth(context.Background(), request)
	if !errors.Is(err, ErrDeploymentJobHealthIndeterminate) || observation.State != DeploymentJobHealthIndeterminate {
		t.Fatalf("unpromoted canary observation=%+v err=%v", observation, err)
	}
}
