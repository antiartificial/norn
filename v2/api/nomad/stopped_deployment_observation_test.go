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

func TestObserveStoppedDeploymentRequiresExactStoppedRevisionAndAllocationSet(t *testing.T) {
	jobID, region, namespace, deploymentID, groupName := "app-deployment-old", "global", "default", "deployment-old", "web"
	specDigest := "sha256:" + strings.Repeat("a", 64)
	jobDigest := "sha256:" + strings.Repeat("b", 64)
	imageDigest := "sha256:" + strings.Repeat("c", 64)
	intentDigest := "sha256:" + strings.Repeat("d", 64)
	stopOperationID := "stop-effect-1"
	originalVersion, originalIndex, stoppedVersion, stoppedIndex := uint64(4), uint64(20), uint64(5), uint64(23)
	stopped, count := true, 2
	job := &nomadapi.Job{ID: &jobID, Region: &region, Namespace: &namespace, Version: &stoppedVersion,
		JobModifyIndex: &stoppedIndex, Stop: &stopped,
		Meta: map[string]string{DeploymentIDMeta: deploymentID, SpecDigestMeta: specDigest, DeploymentJobDigestMeta: jobDigest,
			DeploymentStopOperationMeta: stopOperationID, DeploymentStopIntentMeta: intentDigest},
		TaskGroups: []*nomadapi.TaskGroup{{Name: &groupName, Count: &count, Tasks: []*nomadapi.Task{{Name: groupName,
			Driver: "docker", Config: map[string]interface{}{"image": "registry.test/app@" + imageDigest}}}}}}
	stubs := []*nomadapi.AllocationListStub{
		{ID: "alloc-a", JobID: jobID, JobVersion: originalVersion, TaskGroup: groupName, DesiredStatus: nomadapi.AllocDesiredStatusStop, ClientStatus: nomadapi.AllocClientStatusComplete},
		{ID: "alloc-b", JobID: jobID, JobVersion: originalVersion, TaskGroup: groupName, DesiredStatus: nomadapi.AllocDesiredStatusStop, ClientStatus: nomadapi.AllocClientStatusLost},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/job/" + jobID:
			_ = json.NewEncoder(w).Encode(job)
		case "/v1/job/" + jobID + "/allocations":
			_ = json.NewEncoder(w).Encode(stubs)
		default:
			t.Errorf("unexpected Nomad request %s %s", r.Method, r.URL)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	request := StoppedDeploymentRequest{JobID: jobID, Region: region, Namespace: namespace, DeploymentID: deploymentID,
		SpecDigest: specDigest, JobDigest: jobDigest, ImageDigest: imageDigest, TaskGroup: groupName,
		StopOperationID: stopOperationID, IntentDigest: intentDigest,
		OriginalJobVersion: originalVersion, OriginalJobModifyIndex: originalIndex,
		StoppedJobVersion: stoppedVersion, StoppedJobModifyIndex: stoppedIndex, AllocationIDs: []string{"alloc-b", "alloc-a"}}

	got, err := client.ObserveStoppedDeployment(context.Background(), request)
	if err != nil || got.JobID != jobID || got.DeploymentID != deploymentID || got.JobVersion != stoppedVersion || got.JobModifyIndex != stoppedIndex || strings.Join(got.DrainedAllocationIDs, ",") != "alloc-a,alloc-b" {
		t.Fatalf("exact terminal readback=%+v err=%v", got, err)
	}
	unknownWriteResult := request
	unknownWriteResult.StoppedJobVersion, unknownWriteResult.StoppedJobModifyIndex = 0, 0
	recovered, err := client.ObserveStoppedDeploymentByIntent(context.Background(), unknownWriteResult)
	if err != nil || recovered.JobModifyIndex != stoppedIndex || recovered.JobVersion != stoppedVersion {
		t.Fatalf("lost stop response was not recovered by its exact operation marker: observation=%+v err=%v", recovered, err)
	}

	tests := []struct {
		name   string
		change func()
		reset  func()
	}{
		{name: "job restarted", change: func() { value := false; job.Stop = &value }, reset: func() { value := true; job.Stop = &value }},
		{name: "wrong stopped version", change: func() { value := uint64(6); job.Version = &value }, reset: func() { job.Version = &stoppedVersion }},
		{name: "wrong stop modify index", change: func() { value := uint64(24); job.JobModifyIndex = &value }, reset: func() { job.JobModifyIndex = &stoppedIndex }},
		{name: "wrong deployment provenance", change: func() { job.Meta[DeploymentIDMeta] = "other" }, reset: func() { job.Meta[DeploymentIDMeta] = deploymentID }},
		{name: "wrong old image", change: func() {
			job.TaskGroups[0].Tasks[0].Config["image"] = "registry.test/app@sha256:" + strings.Repeat("d", 64)
		}, reset: func() { job.TaskGroups[0].Tasks[0].Config["image"] = "registry.test/app@" + imageDigest }},
		{name: "allocation still running", change: func() { stubs[1].ClientStatus = nomadapi.AllocClientStatusRunning }, reset: func() { stubs[1].ClientStatus = nomadapi.AllocClientStatusLost }},
		{name: "allocation desired to run", change: func() { stubs[0].DesiredStatus = nomadapi.AllocDesiredStatusRun }, reset: func() { stubs[0].DesiredStatus = nomadapi.AllocDesiredStatusStop }},
		{name: "allocation version substituted", change: func() { stubs[0].JobVersion++ }, reset: func() { stubs[0].JobVersion = originalVersion }},
		{name: "unnamed live allocation", change: func() {
			stubs = append(stubs, &nomadapi.AllocationListStub{ID: "unlisted", JobID: jobID, JobVersion: originalVersion, TaskGroup: groupName, DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning})
		}, reset: func() { stubs = stubs[:2] }},
		{name: "duplicate allocation row", change: func() { stubs = append(stubs, stubs[0]) }, reset: func() { stubs = stubs[:2] }},
		{name: "missing allocation", change: func() { stubs = stubs[:1] }, reset: func() {
			stubs = []*nomadapi.AllocationListStub{
				{ID: "alloc-a", JobID: jobID, JobVersion: originalVersion, TaskGroup: groupName, DesiredStatus: nomadapi.AllocDesiredStatusStop, ClientStatus: nomadapi.AllocClientStatusComplete},
				{ID: "alloc-b", JobID: jobID, JobVersion: originalVersion, TaskGroup: groupName, DesiredStatus: nomadapi.AllocDesiredStatusStop, ClientStatus: nomadapi.AllocClientStatusLost},
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.change()
			defer test.reset()
			if _, err := client.ObserveStoppedDeployment(context.Background(), request); !errors.Is(err, ErrStoppedDeploymentObservation) {
				t.Fatalf("unsafe terminal readback accepted: %v", err)
			}
		})
	}
}

func TestStopDeploymentCASUsesPinnedRevisionAndOneUseMarker(t *testing.T) {
	jobID, region, namespace, deploymentID, groupName := "app-deployment-old", "global", "default", "deployment-old", "web"
	specDigest := "sha256:" + strings.Repeat("a", 64)
	jobDigest := "sha256:" + strings.Repeat("b", 64)
	imageDigest := "sha256:" + strings.Repeat("c", 64)
	intentDigest := "sha256:" + strings.Repeat("d", 64)
	stopOperationID := "stop-effect-1"
	originalVersion, originalIndex := uint64(4), uint64(20)
	stopped, count := false, 2
	job := &nomadapi.Job{ID: &jobID, Region: &region, Namespace: &namespace, Version: &originalVersion,
		JobModifyIndex: &originalIndex, Stop: &stopped,
		Meta: map[string]string{DeploymentIDMeta: deploymentID, SpecDigestMeta: specDigest, DeploymentJobDigestMeta: jobDigest},
		TaskGroups: []*nomadapi.TaskGroup{{Name: &groupName, Count: &count, Tasks: []*nomadapi.Task{{Name: groupName,
			Driver: "docker", Config: map[string]interface{}{"image": "registry.test/app@" + imageDigest}}}}}}
	stubs := []*nomadapi.AllocationListStub{
		{ID: "alloc-a", JobID: jobID, JobVersion: originalVersion, TaskGroup: groupName, DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning},
		{ID: "alloc-b", JobID: jobID, JobVersion: originalVersion, TaskGroup: groupName, DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning},
	}
	wrote := false
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.11.5"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/"+jobID:
			_ = json.NewEncoder(w).Encode(job)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/"+jobID+"/allocations":
			_ = json.NewEncoder(w).Encode(stubs)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
			var registration nomadapi.JobRegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
				t.Fatal(err)
			}
			if !registration.EnforceIndex || registration.JobModifyIndex != originalIndex || registration.Job == nil || registration.Job.Stop == nil || !*registration.Job.Stop ||
				registration.Job.Meta[DeploymentStopOperationMeta] != stopOperationID || registration.Job.Meta[DeploymentStopIntentMeta] != intentDigest {
				t.Fatalf("stop registration did not carry CAS and effect markers: %+v", registration)
			}
			wrote = true
			_ = json.NewEncoder(w).Encode(&nomadapi.JobRegisterResponse{JobModifyIndex: originalIndex + 2})
		default:
			t.Errorf("unexpected Nomad request %s %s", r.Method, r.URL)
		}
	}))
	request := StoppedDeploymentRequest{JobID: jobID, Region: region, Namespace: namespace, DeploymentID: deploymentID,
		StopOperationID: stopOperationID, IntentDigest: intentDigest, SpecDigest: specDigest, JobDigest: jobDigest,
		ImageDigest: imageDigest, TaskGroup: groupName, OriginalJobVersion: originalVersion, OriginalJobModifyIndex: originalIndex,
		AllocationIDs: []string{"alloc-a", "alloc-b"}}
	receipt, err := client.StopDeploymentCAS(context.Background(), request)
	if err != nil || !wrote || receipt.JobID != jobID || receipt.JobVersion != originalVersion+1 || receipt.JobModifyIndex != originalIndex+2 || receipt.IntentDigest != intentDigest {
		t.Fatalf("CAS stop receipt=%+v wrote=%t err=%v", receipt, wrote, err)
	}
}
