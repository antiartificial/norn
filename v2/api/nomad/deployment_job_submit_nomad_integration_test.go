package nomad

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestRegisterDeploymentJobCASInDisposableNomad(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	if address == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR to a disposable loopback Nomad agent")
	}
	parsed, err := url.Parse(address)
	if err != nil || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() {
		t.Fatal("disposable Nomad CAS test requires a loopback endpoint")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	id := "norn-deployment-cas-qual-" + time.Now().UTC().Format("20060102150405")
	region, kind, group, task, driver, command := "global", "batch", "work", "sleep", "raw_exec", "/bin/sleep"
	count := 1
	request := CASDeploymentJobRequest{App: id, Region: region, DeploymentID: "qualification-deployment", SpecDigest: "qualification-spec", OperationID: "qualification-operation",
		ExecutionID: "nomad-deployment-qualification", JobDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	request.Job = &nomadapi.Job{ID: &id, Name: &id, Region: &region, Type: &kind, Datacenters: []string{"dc1"}, Meta: map[string]string{
		DeploymentIDMeta: request.DeploymentID, SpecDigestMeta: request.SpecDigest,
		DeploymentOperationIDMeta: request.OperationID, DeploymentExecutionIDMeta: request.ExecutionID, DeploymentJobDigestMeta: request.JobDigest,
	}, TaskGroups: []*nomadapi.TaskGroup{{Name: &group, Count: &count, Tasks: []*nomadapi.Task{{Name: task, Driver: driver, Config: map[string]interface{}{"command": command, "args": []string{"15"}}}}}}}
	t.Cleanup(func() {
		_, _, _ = client.api.Jobs().Deregister(id, true, (&nomadapi.WriteOptions{Region: region}).WithContext(context.Background()))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	evalID, err := client.RegisterDeploymentJobCAS(ctx, request)
	if err != nil || evalID == "" {
		t.Fatalf("create-only CAS eval=%q err=%v", evalID, err)
	}
	job, _, err := client.api.Jobs().Info(id, (&nomadapi.QueryOptions{Region: region}).WithContext(ctx))
	if err != nil || job == nil || job.JobModifyIndex == nil || *job.JobModifyIndex == 0 || job.Meta[DeploymentExecutionIDMeta] != request.ExecutionID {
		t.Fatalf("registered deployment job=%+v err=%v", job, err)
	}
	observed, err := client.LookupDeploymentJobRevision(ctx, request)
	if err != nil || observed.State != DeploymentJobFound || observed.JobModifyIndex != *job.JobModifyIndex {
		t.Fatalf("readback revision=%+v err=%v", observed, err)
	}
	if _, err := client.RegisterDeploymentJobCAS(ctx, request); !errors.Is(err, ErrDeploymentJobSubmitConflict) {
		t.Fatalf("stale zero-index CAS result=%v", err)
	}
}
