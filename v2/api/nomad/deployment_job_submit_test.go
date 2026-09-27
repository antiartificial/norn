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

func deploymentCASRequest() CASDeploymentJobRequest {
	jobID, region := "demo", "global"
	request := CASDeploymentJobRequest{App: jobID, Region: region, ExpectedJobModifyIndex: 42, DeploymentID: "deployment-1", SpecDigest: "spec-1", OperationID: "operation-1",
		ExecutionID: "nomad-deployment-1", JobDigest: strings.Repeat("a", 64)}
	request.Job = &nomadapi.Job{ID: &jobID, Region: &region, Meta: map[string]string{
		DeploymentIDMeta: request.DeploymentID, SpecDigestMeta: request.SpecDigest,
		DeploymentOperationIDMeta: request.OperationID, DeploymentExecutionIDMeta: request.ExecutionID, DeploymentJobDigestMeta: request.JobDigest,
	}}
	digest, err := DigestDeploymentJob(request.Job)
	if err != nil {
		panic(err)
	}
	request.JobDigest = digest
	request.Job.Meta[DeploymentJobDigestMeta] = digest
	return request
}

func TestRegisterDeploymentJobCASUsesExpectedRevision(t *testing.T) {
	request := deploymentCASRequest()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPut || r.URL.Path != "/v1/jobs" || r.URL.Query().Get("region") != request.Region {
			t.Errorf("unexpected Nomad request %s %s", r.Method, r.URL)
			return
		}
		var body nomadapi.JobRegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode registration: %v", err)
			return
		}
		if !body.EnforceIndex || body.JobModifyIndex != request.ExpectedJobModifyIndex || body.Job == nil || body.Job.Meta[DeploymentExecutionIDMeta] != request.ExecutionID || body.Submission == nil || body.Submission.Format != "json" {
			t.Errorf("registration is not bound to the expected revision and execution: %+v", body)
		}
		encoded, err := json.Marshal(request.Job)
		if err != nil || body.Submission == nil || body.Submission.Source != string(encoded) {
			t.Error("Nomad submission does not carry the exact registered job source")
		}
		_ = json.NewEncoder(w).Encode(&nomadapi.JobRegisterResponse{EvalID: "eval-1", JobModifyIndex: 43})
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	evalID, err := client.RegisterDeploymentJobCAS(context.Background(), request)
	if err != nil || evalID != "eval-1" || requests != 1 {
		t.Fatalf("CAS submit eval=%q calls=%d err=%v", evalID, requests, err)
	}
}

func TestRegisterDeploymentJobCASFailsBeforeUnboundWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected Nomad write") }))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	request := deploymentCASRequest()
	request.Job.Meta[DeploymentExecutionIDMeta] = "foreign-execution"
	if _, err := client.RegisterDeploymentJobCAS(context.Background(), request); err == nil {
		t.Fatal("unbound execution marker reached Nomad")
	}
}

func TestRegisterDeploymentJobCASRejectsChangedWorkloadBeforeWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected Nomad write") }))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	request := deploymentCASRequest()
	request.Job.TaskGroups = []*nomadapi.TaskGroup{{Tasks: []*nomadapi.Task{{Name: "web", Driver: "docker", Config: map[string]interface{}{"image": "changed"}}}}}
	if _, err := client.RegisterDeploymentJobCAS(context.Background(), request); err == nil {
		t.Fatal("changed workload reached Nomad")
	}
}

func TestRegisterDeploymentJobCASClassifiesAmbiguousResponse(t *testing.T) {
	const privateCanary = "NORN_DEPLOYMENT_PRIVATE_4c8e"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, privateCanary, http.StatusBadGateway)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RegisterDeploymentJobCAS(context.Background(), deploymentCASRequest()); !errors.Is(err, ErrDeploymentJobSubmitIndeterminate) || strings.Contains(err.Error(), privateCanary) {
		t.Fatalf("ambiguous response = %v", err)
	}
}
