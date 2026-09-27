package nomad

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestLookupDeploymentJobRevisionSeparatesAbsenceAndExactMarkers(t *testing.T) {
	request := deploymentCASRequest()
	index, version := uint64(43), uint64(7)
	request.Job.JobModifyIndex, request.Job.Version = &index, &version
	status := http.StatusNotFound
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/job/"+request.App || r.URL.Query().Get("region") != request.Region {
			t.Errorf("unexpected Nomad read %s %s", r.Method, r.URL)
		}
		if status != http.StatusOK {
			http.Error(w, "private Nomad response", status)
			return
		}
		_ = json.NewEncoder(w).Encode(request.Job)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := client.LookupDeploymentJobRevision(context.Background(), request)
	if err != nil || observed.State != DeploymentJobNotFound {
		t.Fatalf("missing job observation=%+v err=%v", observed, err)
	}
	status = http.StatusOK
	observed, err = client.LookupDeploymentJobRevision(context.Background(), request)
	if err != nil || observed.State != DeploymentJobFound || observed.JobModifyIndex != index || observed.Version != version {
		t.Fatalf("exact job observation=%+v err=%v", observed, err)
	}
	request.Job.Meta[DeploymentExecutionIDMeta] = "foreign-execution"
	observed, err = client.LookupDeploymentJobRevision(context.Background(), request)
	if !errors.Is(err, ErrDeploymentJobLookupIndeterminate) || observed.State != DeploymentJobIndeterminate {
		t.Fatalf("foreign job observation=%+v err=%v", observed, err)
	}
}

func TestLookupDeploymentJobRevisionRejectsStaleAndMalformedResponses(t *testing.T) {
	request := deploymentCASRequest()
	index, version := uint64(42), uint64(1)
	request.Job.JobModifyIndex, request.Job.Version = &index, &version
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(request.Job)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := client.LookupDeploymentJobRevision(context.Background(), request)
	if !errors.Is(err, ErrDeploymentJobLookupIndeterminate) || observed.State != DeploymentJobIndeterminate {
		t.Fatalf("stale job observation=%+v err=%v", observed, err)
	}
	request.Job = &nomadapi.Job{}
	observed, err = client.LookupDeploymentJobRevision(context.Background(), request)
	if !errors.Is(err, ErrDeploymentJobLookupIndeterminate) || observed.State != DeploymentJobIndeterminate {
		t.Fatalf("malformed job observation=%+v err=%v", observed, err)
	}
}
