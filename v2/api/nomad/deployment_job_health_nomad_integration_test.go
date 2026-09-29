package nomad

import (
	"context"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

func TestObserveDeploymentJobHealthInDisposableNomad(t *testing.T) {
	address, image := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_DEPLOYMENT_IMAGE")
	if address == "" || image == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR and NORN_TEST_DEPLOYMENT_IMAGE for a disposable Docker-capable Nomad agent")
	}
	parsed, err := url.Parse(address)
	if err != nil || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || !model.IsContentAddressedImage(image) {
		t.Fatal("deployment health test requires loopback Nomad and a content-addressed image")
	}
	client, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	id := "norn-deployment-health-qual-" + time.Now().UTC().Format("20060102150405")
	job := mustTranslate(t, &model.InfraSpec{App: id, Processes: map[string]model.Process{"web": {Command: "sleep 60"}}}, image, nil)
	healthCheck, minHealthy := "task_states", time.Second
	job.TaskGroups[0].Update.HealthCheck = &healthCheck
	job.TaskGroups[0].Update.MinHealthyTime = &minHealthy
	request := CASDeploymentJobRequest{Job: job, App: id, Region: "global", DeploymentID: "qualification-deployment",
		SpecDigest: "sha256:" + strings.Repeat("a", 64), OperationID: "qualification-operation", ExecutionID: "nomad-deployment-qualification", ImageTag: image}
	job.Meta[DeploymentIDMeta] = request.DeploymentID
	job.Meta[SpecDigestMeta] = request.SpecDigest
	job.Meta[DeploymentOperationIDMeta] = request.OperationID
	job.Meta[DeploymentExecutionIDMeta] = request.ExecutionID
	request.JobDigest, err = DigestDeploymentJob(job)
	if err != nil {
		t.Fatal(err)
	}
	job.Meta[DeploymentJobDigestMeta] = request.JobDigest
	t.Cleanup(func() {
		_, _, _ = client.api.Jobs().Deregister(id, true, (&nomadapi.WriteOptions{Region: request.Region}).WithContext(context.Background()))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if _, err := client.RegisterDeploymentJobCAS(ctx, request); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		observation, err := client.ObserveDeploymentJobHealth(ctx, request)
		if err != nil {
			t.Fatalf("deployment health readback=%+v err=%v", observation, err)
		}
		if observation.State == DeploymentJobHealthReady {
			if len(observation.AllocationIDs) != 1 {
				t.Fatalf("ready job has allocation IDs=%v", observation.AllocationIDs)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("deployment never became healthy: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}
