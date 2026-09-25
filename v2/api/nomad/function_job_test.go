package nomad

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const functionJobPrivateCanary = "NORN_FUNCTION_JOB_PRIVATE_7c1d"

func functionJobIdentity() FunctionInvocationJobIdentity {
	return FunctionInvocationJobIdentity{JobID: "norn-fn-" + strings.Repeat("a", 40)}
}

func TestFunctionInvocationJobLookupRefusesExistingJobWithoutReturningDetails(t *testing.T) {
	identity := functionJobIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ID":"` + identity.JobID + `","Meta":{"secret":"` + functionJobPrivateCanary + `"}}`))
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
