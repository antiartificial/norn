package etcdstore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

func recoveryNomadFixture(t *testing.T, accepted store.AcceptedOperation, claim store.OperationClaim, authority string) (effect.Reservation, *nomad.Client) {
	t.Helper()
	r := deploymentEffectReservation(t, accepted, claim, authority)
	var input nomad.DeploymentJobEffectInput
	if err := json.Unmarshal(r.LaunchPayload, &input); err != nil {
		t.Fatal(err)
	}
	job, err := nomad.Translate(&model.InfraSpec{App: input.App, Processes: map[string]model.Process{"web": {Port: 8080}}}, input.ImageTag, nil)
	if err != nil {
		t.Fatal(err)
	}
	job.Meta[nomad.DeploymentIDMeta] = input.DeploymentID
	job.Meta[nomad.SpecDigestMeta] = input.SpecDigest
	job.Meta[nomad.DeploymentOperationIDMeta] = claim.OperationID()
	input.JobDigest, err = nomad.DigestDeploymentJob(job)
	if err != nil {
		t.Fatal(err)
	}
	r.SupervisorExecutionID = nomad.DeploymentEffectExecutionID(claim.OperationID(), input.Region, input.JobDigest)
	job.Meta[nomad.DeploymentExecutionIDMeta] = r.SupervisorExecutionID
	job.Meta[nomad.DeploymentJobDigestMeta] = input.JobDigest
	r.LaunchPayload, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r.InputDigest, err = effect.ComputeInputDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	name, kind, groupName, count, stopped := input.App, "service", "web", 1, false
	job.Name, job.Type, job.Stop = &name, &kind, &stopped
	// Translate already produced the job digest. Set group shape before recomputing
	// provenance, so the Nomad readback and signed descriptor bind the same job.
	job.TaskGroups = []*nomadapi.TaskGroup{{Name: &groupName, Count: &count, Tasks: []*nomadapi.Task{{Name: groupName, Driver: "docker", Config: map[string]interface{}{"image": input.ImageTag}}}}}
	input.JobDigest, err = nomad.DigestDeploymentJob(job)
	if err != nil {
		t.Fatal(err)
	}
	r.SupervisorExecutionID = nomad.DeploymentEffectExecutionID(claim.OperationID(), input.Region, input.JobDigest)
	job.Meta[nomad.DeploymentExecutionIDMeta], job.Meta[nomad.DeploymentJobDigestMeta] = r.SupervisorExecutionID, input.JobDigest
	r.LaunchPayload, _ = json.Marshal(input)
	r.InputDigest, _ = effect.ComputeInputDigest(r)
	source, _ := json.Marshal(job)
	var current nomadapi.Job
	_ = json.Unmarshal(source, &current)
	version, index, healthy := uint64(3), uint64(7), true
	current.Version, current.JobModifyIndex = &version, &index
	stub := &nomadapi.AllocationListStub{ID: "alloc-1", JobID: input.App, JobVersion: version, TaskGroup: groupName, ClientStatus: nomadapi.AllocClientStatusRunning, DesiredStatus: nomadapi.AllocDesiredStatusRun, DeploymentStatus: &nomadapi.AllocDeploymentStatus{Healthy: &healthy}}
	allocation := &nomadapi.Allocation{ID: stub.ID, JobID: input.App, TaskGroup: groupName, Job: &current, ClientStatus: stub.ClientStatus, DesiredStatus: stub.DesiredStatus, TaskStates: map[string]*nomadapi.TaskState{groupName: {State: "running"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/job/"+input.App+"/plan" && req.Method != http.MethodGet {
			t.Errorf("unexpected Nomad mutation %s %s", req.Method, req.URL.Path)
			http.Error(w, "mutation rejected", http.StatusMethodNotAllowed)
			return
		}
		switch req.URL.Path {
		case "/v1/job/" + input.App:
			_ = json.NewEncoder(w).Encode(&current)
		case "/v1/job/" + input.App + "/submission":
			_ = json.NewEncoder(w).Encode(&nomadapi.JobSubmission{Source: string(source), Format: "json"})
		case "/v1/job/" + input.App + "/plan":
			_ = json.NewEncoder(w).Encode(&nomadapi.JobPlanResponse{JobModifyIndex: index, Diff: &nomadapi.JobDiff{Type: "None"}})
		case "/v1/job/" + input.App + "/allocations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{stub})
		case "/v1/allocation/alloc-1":
			_ = json.NewEncoder(w).Encode(allocation)
		default:
			t.Errorf("unexpected Nomad request %s %s", req.Method, req.URL.Path)
			http.Error(w, "unexpected", 404)
		}
	}))
	t.Cleanup(server.Close)
	client, err := nomad.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return r, client
}
