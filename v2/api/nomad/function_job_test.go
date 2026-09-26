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

const functionJobPrivateCanary = "NORN_FUNCTION_JOB_PRIVATE_7c1d"

func functionJobIdentity() FunctionInvocationJobIdentity {
	return FunctionInvocationJobIdentity{JobID: "norn-fn-" + strings.Repeat("a", 40)}
}

func observedFunctionJob(t *testing.T, identity FunctionInvocationJobIdentity) (*nomadapi.Job, FunctionInvocationJobDigest) {
	t.Helper()
	job, digest, err := BuildFunctionInvocationJob(FunctionInvocationJobRequest{JobID: identity.JobID, OwnerMarker: "norn.function-invoke/op-1", VariablePath: "nomad/jobs/norn-fn-" + strings.Repeat("a", 40) + "/invoke", Image: "registry.example/function@sha256:" + strings.Repeat("b", 64), Command: "./function", CPU: 250, MemoryMB: 192})
	if err != nil {
		t.Fatal(err)
	}
	version, modifyIndex, jobModifyIndex := uint64(1), uint64(55), uint64(54)
	job.Version, job.ModifyIndex, job.JobModifyIndex = &version, &modifyIndex, &jobModifyIndex
	return job, digest
}

func TestFunctionInvocationJobLookupReturnsCompleteExactObservation(t *testing.T) {
	identity := functionJobIdentity()
	job, digest := observedFunctionJob(t, identity)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/job/" + identity.JobID:
			_ = json.NewEncoder(w).Encode(job)
		case "/v1/job/" + identity.JobID + "/versions":
			_ = json.NewEncoder(w).Encode(nomadapi.JobVersionsResponse{Versions: []*nomadapi.Job{job}})
		case "/v1/job/" + identity.JobID + "/evaluations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.Evaluation{{ID: "eval-2", JobID: identity.JobID, JobModifyIndex: 54}, {ID: "eval-1", JobID: identity.JobID, JobModifyIndex: 54}})
		case "/v1/job/" + identity.JobID + "/allocations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: "alloc-2", JobID: identity.JobID, JobVersion: 1}, {ID: "alloc-1", JobID: identity.JobID, JobVersion: 1}})
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := client.LookupFunctionInvocationJob(context.Background(), "global", identity)
	if err != nil {
		t.Fatal(err)
	}
	if observed.State != FunctionInvocationJobFound || observed.JobID != identity.JobID || observed.OwnerMarker != "norn.function-invoke/op-1" || observed.JobSpecDigest != digest.Digest || observed.JobVersion == nil || *observed.JobVersion != 1 || observed.ModifyIndex != 55 || !observed.HistoryComplete || strings.Join(observed.EvaluationIDs, ",") != "eval-1,eval-2" || strings.Join(observed.AllocationIDs, ",") != "alloc-1,alloc-2" {
		t.Fatalf("observation = %+v", observed)
	}
}

func TestFunctionInvocationJobLookupRefusesInvalidHistoryAndDoesNotLeakResponse(t *testing.T) {
	identity := functionJobIdentity()
	job, _ := observedFunctionJob(t, identity)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/job/" + identity.JobID:
			_ = json.NewEncoder(w).Encode(job)
		case "/v1/job/" + identity.JobID + "/versions":
			other := *job
			other.Meta = map[string]string{functionInvocationOwnerMeta: "other-owner", "secret": functionJobPrivateCanary}
			_ = json.NewEncoder(w).Encode(nomadapi.JobVersionsResponse{Versions: []*nomadapi.Job{&other}})
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := client.LookupFunctionInvocationJob(context.Background(), "global", identity)
	if !errors.Is(err, ErrFunctionJobLookupIndeterminate) || observed.State != FunctionInvocationJobIndeterminate || strings.Contains(err.Error(), functionJobPrivateCanary) {
		t.Fatalf("lookup = %+v, %v", observed, err)
	}
}

func TestFunctionInvocationJobLookupRejectsRevisionChangeDuringHistoryReads(t *testing.T) {
	identity := functionJobIdentity()
	job, _ := observedFunctionJob(t, identity)
	infoReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/job/" + identity.JobID:
			infoReads++
			current := *job
			if infoReads == 2 {
				changed := *job.ModifyIndex + 1
				current.ModifyIndex = &changed
			}
			_ = json.NewEncoder(w).Encode(&current)
		case "/v1/job/" + identity.JobID + "/versions":
			_ = json.NewEncoder(w).Encode(nomadapi.JobVersionsResponse{Versions: []*nomadapi.Job{job}})
		case "/v1/job/" + identity.JobID + "/evaluations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.Evaluation{{ID: "eval-1", JobID: identity.JobID, JobModifyIndex: *job.JobModifyIndex}})
		case "/v1/job/" + identity.JobID + "/allocations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{})
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := client.LookupFunctionInvocationJob(context.Background(), "global", identity)
	if !errors.Is(err, ErrFunctionJobLookupIndeterminate) || observed.State != FunctionInvocationJobIndeterminate || infoReads != 2 {
		t.Fatalf("changed revision accepted: %+v, %v, info reads=%d", observed, err, infoReads)
	}
}

func TestFunctionInvocationJobLookupClassifiesOnly404AsAbsent(t *testing.T) {
	identity, requests := functionJobIdentity(), 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.Error(w, functionJobPrivateCanary, http.StatusBadGateway)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := client.LookupFunctionInvocationJob(context.Background(), "global", identity)
	if err != nil || missing.State != FunctionInvocationJobNotFound {
		t.Fatalf("missing = %+v, %v", missing, err)
	}
	got, err := client.LookupFunctionInvocationJob(context.Background(), "global", identity)
	if !errors.Is(err, ErrFunctionJobLookupIndeterminate) || got.State != FunctionInvocationJobIndeterminate || strings.Contains(err.Error(), functionJobPrivateCanary) {
		t.Fatalf("indeterminate = %+v, %v", got, err)
	}
}
