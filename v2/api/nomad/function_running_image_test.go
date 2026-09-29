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
	"norn/v2/api/model"
)

func runningImageFixture() (*nomadapi.Job, *nomadapi.AllocationListStub, *nomadapi.Allocation, string) {
	image := "registry.example/widget@sha256:" + strings.Repeat("a", 64)
	app, region, kind, group := "widget", "global", "service", "web"
	version, index, count, healthy := uint64(3), uint64(19), 1, true
	job := &nomadapi.Job{ID: &app, Region: &region, Type: &kind, Version: &version, JobModifyIndex: &index, TaskGroups: []*nomadapi.TaskGroup{{Name: &group, Count: &count, Tasks: []*nomadapi.Task{{Name: group, Driver: "docker", Config: map[string]interface{}{"image": image}}}}}}
	stub := &nomadapi.AllocationListStub{ID: "alloc-1", JobID: app, JobVersion: version, TaskGroup: group, ClientStatus: nomadapi.AllocClientStatusRunning, DesiredStatus: nomadapi.AllocDesiredStatusRun, DeploymentStatus: &nomadapi.AllocDeploymentStatus{Healthy: &healthy}}
	allocation := &nomadapi.Allocation{ID: stub.ID, JobID: app, TaskGroup: group, ClientStatus: stub.ClientStatus, DesiredStatus: stub.DesiredStatus, Job: job, TaskStates: map[string]*nomadapi.TaskState{group: {State: "running"}}}
	return job, stub, allocation, image
}

func TestRunningImageProjectionRejectsMismatchedOrUnhealthyAllocations(t *testing.T) {
	job, stub, allocation, image := runningImageFixture()
	expected := map[string]bool{"web": true}
	if !runningJobImage(job, "widget", "global", image, expected) || !runningAllocationImage(stub, allocation, job, image, expected) {
		t.Fatal("valid running image was rejected")
	}
	wrong := *allocation
	wrong.Job, _, _, _ = runningImageFixture()
	wrong.Job.TaskGroups[0].Tasks[0].Config["image"] = "registry.example/widget@sha256:" + strings.Repeat("b", 64)
	if runningAllocationImage(stub, &wrong, job, image, expected) {
		t.Fatal("allocation with wrong image was admitted")
	}
	stale := *stub
	stale.JobVersion++
	if runningAllocationImage(&stale, allocation, job, image, expected) {
		t.Fatal("stale allocation version was admitted")
	}
	unhealthy := *stub
	no := false
	unhealthy.DeploymentStatus = &nomadapi.AllocDeploymentStatus{Healthy: &no}
	if runningAllocationImage(&unhealthy, allocation, job, image, expected) {
		t.Fatal("unhealthy allocation was admitted")
	}
}

func TestVerifyRunningAppImageReadsAllocationSnapshotAndStableJob(t *testing.T) {
	job, stub, allocation, image := runningImageFixture()
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("region") != "global" {
			t.Errorf("unscoped Nomad read: %s", r.URL.String())
		}
		switch r.URL.Path {
		case "/v1/job/widget":
			reads++
			_ = json.NewEncoder(w).Encode(job)
		case "/v1/job/widget/allocations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{stub})
		case "/v1/allocation/alloc-1":
			_ = json.NewEncoder(w).Encode(allocation)
		default:
			t.Errorf("unexpected Nomad read: %s", r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	spec := &model.InfraSpec{App: "widget", Processes: map[string]model.Process{"web": {Command: "serve"}}}
	if err := client.VerifyRunningAppImage(context.Background(), spec, image); err != nil || reads != 2 {
		t.Fatalf("running read-back err=%v job reads=%d", err, reads)
	}
	allocation.Job.TaskGroups[0].Tasks[0].Config["image"] = "registry.example/widget@sha256:" + strings.Repeat("b", 64)
	if err := client.VerifyRunningAppImage(context.Background(), spec, image); !errors.Is(err, ErrRunningAppImageUnproven) {
		t.Fatalf("changed allocation image err=%v", err)
	}
}
