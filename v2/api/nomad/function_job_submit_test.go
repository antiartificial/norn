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

const functionSubmitPrivateCanary = "NORN_FUNCTION_JOB_PRIVATE_7c1d"

func TestCreateFunctionInvocationJobUsesZeroIndexCAS(t *testing.T) {
	job, digest, err := BuildFunctionInvocationJob(functionInvocationJobRequest())
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPut || r.URL.Path != "/v1/jobs" || r.URL.Query().Get("region") != "global" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			return
		}
		var body nomadapi.JobRegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if !body.EnforceIndex || body.JobModifyIndex != 0 || body.Job == nil || *body.Job.ID != *job.ID {
			t.Errorf("registration is not exact create-only CAS: %+v", body)
			return
		}
		if requests == 1 {
			_, _ = w.Write([]byte(`{"EvalID":"evaluation-1"}`))
			return
		}
		http.Error(w, nomadapi.RegisterEnforceIndexErrPrefix+": already exists", http.StatusConflict)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CreateFunctionInvocationJob(context.Background(), "global", job, digest); err != nil {
		t.Fatal(err)
	}
	if err := client.CreateFunctionInvocationJob(context.Background(), "global", job, digest); !errors.Is(err, ErrFunctionJobCreateConflict) {
		t.Fatalf("duplicate registration = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestCreateFunctionInvocationJobRejectsWrongDigestBeforeIO(t *testing.T) {
	job, digest, err := BuildFunctionInvocationJob(functionInvocationJobRequest())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected Nomad write") }))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	digest.Digest = "sha256:" + strings.Repeat("0", 64)
	if err := client.CreateFunctionInvocationJob(context.Background(), "global", job, digest); !errors.Is(err, ErrFunctionJobIdentity) {
		t.Fatalf("wrong digest = %v", err)
	}
	job, digest, _ = BuildFunctionInvocationJob(functionInvocationJobRequest())
	job.TaskGroups[0].Tasks[0].Env = map[string]string{"SECRET": "private"}
	if err := client.CreateFunctionInvocationJob(context.Background(), "global", job, digest); !errors.Is(err, ErrFunctionJobIdentity) {
		t.Fatalf("private env = %v", err)
	}
}

func TestCreateFunctionInvocationJobRedactsAmbiguousResponse(t *testing.T) {
	job, digest, err := BuildFunctionInvocationJob(functionInvocationJobRequest())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, functionSubmitPrivateCanary, http.StatusBadGateway)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = client.CreateFunctionInvocationJob(context.Background(), "global", job, digest)
	if !errors.Is(err, ErrFunctionJobCreateIndeterminate) || strings.Contains(err.Error(), functionSubmitPrivateCanary) {
		t.Fatalf("ambiguous registration error = %v", err)
	}
}
