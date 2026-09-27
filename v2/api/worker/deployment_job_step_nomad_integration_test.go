package worker

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

// The disposable Nomad run checks the worker sequence against actual job
// registration and allocation health. The durable etcd store is qualified in
// its own integration tests; normal deployment dispatch remains unwired.
func TestDeploymentJobEffectThroughDisposableNomad(t *testing.T) {
	address, image := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_DEPLOYMENT_IMAGE")
	if address == "" || image == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR and NORN_TEST_DEPLOYMENT_IMAGE for a disposable Docker-capable Nomad agent")
	}
	parsed, err := url.Parse(address)
	if err != nil || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || !model.IsContentAddressedImage(image) {
		t.Fatal("deployment worker test requires loopback Nomad and a content-addressed image")
	}
	client, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	id := "norn-worker-deploy-qual-" + time.Now().UTC().Format("20060102150405")
	job := nomad.Translate(&model.InfraSpec{App: id, Processes: map[string]model.Process{"web": {Command: "sleep 60"}}}, image, nil)
	healthCheck, minHealthy := "task_states", time.Second
	job.TaskGroups[0].Update.HealthCheck = &healthCheck
	job.TaskGroups[0].Update.MinHealthyTime = &minHealthy
	input := nomad.DeploymentJobEffectInput{App: id, DeploymentID: "qualification-deployment", Region: "west", NomadRegion: "global",
		ImageTag: image, SpecDigest: "sha256:" + strings.Repeat("a", 64)}
	job.Meta[nomad.DeploymentIDMeta] = input.DeploymentID
	job.Meta[nomad.SpecDigestMeta] = input.SpecDigest
	job.Meta[nomad.DeploymentOperationIDMeta] = "qualification-operation"
	job.Meta[nomad.DeploymentExecutionIDMeta] = "nomad-deployment-qualification"
	input.JobDigest, err = nomad.DigestDeploymentJob(job)
	if err != nil {
		t.Fatal(err)
	}
	job.Meta[nomad.DeploymentJobDigestMeta] = input.JobDigest
	t.Cleanup(func() {
		api, err := nomadapi.NewClient(&nomadapi.Config{Address: address})
		if err == nil {
			_, _, _ = api.Jobs().Deregister(id, true, &nomadapi.WriteOptions{Region: "global"})
		}
	})
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r := effect.Reservation{Authority: "qualification-authority", Resource: "app/" + id + "/deploy/west",
		OperationClaim: effect.OperationClaim{OperationID: "qualification-operation", OwnerID: "worker", Generation: 1},
		Stage:          "app.deploy.nomad.submit", Supervisor: "nomad-deployment", SupervisorExecutionID: "nomad-deployment-qualification", LaunchPayload: payload}
	r.InputDigest, err = effect.ComputeInputDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	effects := &deploymentStepEffects{created: true}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	first, err := EnsureDeploymentJobEffect(ctx, effects, client, r, job)
	if err != nil || first.State != DeploymentJobEffectObserved || effects.launched != 1 {
		t.Fatalf("Nomad job submit/readback=%+v launched=%d err=%v", first, effects.launched, err)
	}
	for {
		decision, err := CompleteDeploymentJobEffect(ctx, effects, client, effects.record)
		if err != nil {
			t.Fatal(err)
		}
		if decision.State == DeploymentJobEffectObserved {
			if effects.completed != 1 || effects.completion.Verification.ResultDigest == "" ||
				effects.completion.Verification.RuntimeInstanceID != effects.record.Execution.RuntimeInstanceID {
				t.Fatalf("Nomad health did not bind effect completion: %+v", effects.completion)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Nomad job did not become healthy: %v", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
