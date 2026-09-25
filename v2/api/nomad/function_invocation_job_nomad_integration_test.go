package nomad

import (
	"context"
	"os"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

// TestFunctionInvocationJobNomadReadbackProjection requires a disposable
// Nomad 2.0.7 agent. It verifies the actual register/read-back projection;
// no allocation is started or awaited by this test.
func TestFunctionInvocationJobNomadReadbackProjection(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	if address == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR to a disposable Nomad 2.0.7 agent")
	}
	client, err := nomadapi.NewClient(&nomadapi.Config{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	request := functionInvocationJobRequest()
	job, want, err := BuildFunctionInvocationJob(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer client.Jobs().Deregister(request.JobID, true, (&nomadapi.WriteOptions{Region: "global"}).WithContext(ctx))
	adapter := &Client{api: client}
	if err := adapter.CreateFunctionInvocationJob(ctx, "global", job, want); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CreateFunctionInvocationJob(ctx, "global", job, want); err != ErrFunctionJobCreateConflict {
		t.Fatalf("duplicate create = %v, want conflict", err)
	}
	readBack, _, err := client.Jobs().Info(request.JobID, (&nomadapi.QueryOptions{Region: "global"}).WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ProjectFunctionInvocationJob(readBack)
	if err != nil {
		t.Fatalf("read-back projection rejected: %v", err)
	}
	if got != want {
		t.Fatalf("read-back digest = %+v, want %+v", got, want)
	}
	observed, err := adapter.LookupFunctionInvocationJob(ctx, "global", FunctionInvocationJobIdentity{JobID: request.JobID})
	if err != nil || observed.State != FunctionInvocationJobFound || observed.JobSpecDigest != want.Digest || !observed.HistoryComplete {
		t.Fatalf("exact job observation = %+v, %v", observed, err)
	}
}
