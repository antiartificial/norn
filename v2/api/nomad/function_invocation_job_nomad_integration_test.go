package nomad

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"norn/v2/api/model"

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

// TestFunctionInvocationPrivateEnvironmentInNomad requires a disposable
// Nomad client with Docker. It proves the closed job's private env template
// consumes the padded variable value and preserves request bytes without
// putting them in the public job description.
func TestFunctionInvocationPrivateEnvironmentInNomad(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	if address == "" || os.Getenv("NORN_TEST_NOMAD_DOCKER") != "1" {
		t.Skip("set NORN_TEST_NOMAD_ADDR and NORN_TEST_NOMAD_DOCKER=1 for a disposable Docker-enabled Nomad client")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	request := functionInvocationJobRequest()
	request.Image = model.QualifiedWordPressVerifiedTLSImage
	request.Command = `printf '%s|%s|%s|%s|%s' "$NORN_REQUEST_BODY" "$NORN_REQUEST_METHOD" "$NORN_REQUEST_PATH" "$APP_SECRET" "$EMPTY_VALUE" | sha256sum; sleep 30`
	private := struct {
		Body   string            `json:"body"`
		Method string            `json:"method"`
		Path   string            `json:"path"`
		Env    map[string]string `json:"env"`
	}{Body: "line one\nline two with \"quotes\" and \\backslash #hash", Method: "", Path: "/x?q=é", Env: map[string]string{"APP_SECRET": "private\nvalue=with#hash", "EMPTY_VALUE": ""}}
	privateJSON, err := json.Marshal(private)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(private.Body + "|" + private.Method + "|" + private.Path + "|" + private.Env["APP_SECRET"] + "|" + private.Env["EMPTY_VALUE"]))
	wantHex := hex.EncodeToString(want[:])
	job, digest, err := BuildFunctionInvocationJob(request)
	if err != nil {
		t.Fatal(err)
	}
	encodedJob, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{private.Body, private.Path, private.Env["APP_SECRET"]} {
		if strings.Contains(string(encodedJob), secret) {
			t.Fatal("private request appeared in Nomad job JSON")
		}
	}
	identity := FunctionInvocationVariableIdentity{Path: request.VariablePath, OwnerMarker: request.OwnerMarker}
	ctx := context.Background()
	t.Cleanup(func() {
		_, _, _ = client.api.Jobs().Deregister(request.JobID, true, nil)
		_, _ = client.api.Variables().Delete(request.VariablePath, nil)
	})
	if err := client.CreateFunctionInvocationVariable(ctx, "global", identity, privateJSON); err != nil {
		t.Fatal(err)
	}
	if err := client.CreateFunctionInvocationJob(ctx, "global", job, digest); err != nil {
		t.Fatal(err)
	}
	checkAllocationOutput(t, client.api, request.JobID, "invoke", wantHex)
}
