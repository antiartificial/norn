package etcdstore

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

// This opt-in test crosses the private signed etcd admission, claim, effect
// attempt, Nomad registration/readback, allocation health, and effect-complete
// boundaries. It does not enable the public etcd deployment route.
func TestV3DeploymentWorkerEffectThroughEtcdAndDisposableNomad(t *testing.T) {
	address, image := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_DEPLOYMENT_IMAGE")
	if address == "" || image == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR and NORN_TEST_DEPLOYMENT_IMAGE for a disposable Docker-capable Nomad agent")
	}
	parsed, err := url.Parse(address)
	if err != nil || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || !model.IsContentAddressedImage(image) {
		t.Fatal("deployment integration requires loopback Nomad and a content-addressed image")
	}
	adapter, etcd, _ := deploymentEtcdStore(t)
	nomadClient, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	id := "norn-etcd-deploy-qual-" + uuid.NewString()[:8]
	request := deploymentAdmissionRequest(t, adapter.authority)
	request.Identity.Resource = "app/" + id
	request.Identity.Key = uuid.NewString()
	request.Operation.App = id
	request.Deployment.App = id
	request.Deployment.ImageTag = image
	request.Deployment.SpecDigest = "sha256:" + strings.Repeat("a", 64)
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: id, ControlEnvironment: "staging",
		Cluster: "norn-staging", FleetEnvironment: "staging/nyc3", Region: "west", NomadRegion: "global",
		Datacenters: []string{"dc1"}, Generation: 1}
	if _, err := adapter.putFleetAppTarget(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	request.Semantics["fleetAppTarget"] = target
	request.Regions[0].Datacenters = []string{"dc1"}
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_, claim, err := adapter.ClaimNextOperation(ctx, "deploy-worker", time.Minute, []string{"app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	effects, err := NewV3DeploymentEffectReservations(adapter)
	if err != nil {
		t.Fatal(err)
	}
	job, err := nomad.Translate(&model.InfraSpec{App: id, Processes: map[string]model.Process{"web": {Command: "sleep 60"}}}, image, nil)
	if err != nil {
		t.Fatal(err)
	}
	healthCheck, minHealthy := "task_states", time.Second
	job.TaskGroups[0].Update.HealthCheck = &healthCheck
	job.TaskGroups[0].Update.MinHealthyTime = &minHealthy
	input := nomad.DeploymentJobEffectInput{App: id, DeploymentID: accepted.Deployment.ID, Region: accepted.Regions[0].Name,
		NomadRegion: accepted.Regions[0].NomadRegion, ImageTag: image, SpecDigest: accepted.Deployment.SpecDigest}
	job.Meta[nomad.DeploymentIDMeta] = input.DeploymentID
	job.Meta[nomad.SpecDigestMeta] = input.SpecDigest
	job.Meta[nomad.DeploymentOperationIDMeta] = claim.OperationID()
	input.JobDigest, err = nomad.DigestDeploymentJob(job)
	if err != nil {
		t.Fatal(err)
	}
	executionID := deploymentEffectExecutionID(claim.OperationID(), input.Region, input.JobDigest)
	job.Meta[nomad.DeploymentExecutionIDMeta] = executionID
	job.Meta[nomad.DeploymentJobDigestMeta] = input.JobDigest
	t.Cleanup(func() {
		api, err := nomadapi.NewClient(&nomadapi.Config{Address: address})
		if err == nil {
			_, _, _ = api.Jobs().Deregister(id, true, &nomadapi.WriteOptions{Region: input.NomadRegion})
		}
	})
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r := effect.Reservation{Authority: adapter.authority, Resource: "app/" + id + "/deploy/" + input.Region,
		OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()},
		Stage:          "app.deploy.nomad.submit", Supervisor: "nomad-deployment", SupervisorExecutionID: executionID, LaunchPayload: payload}
	r.InputDigest, err = effect.ComputeInputDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	first, err := worker.EnsureDeploymentJobEffect(ctx, effects, nomadClient, r, job)
	if err != nil || first.State != worker.DeploymentJobEffectObserved {
		t.Fatalf("etcd/Nomad submit readback=%+v err=%v", first, err)
	}
	replayed, err := worker.EnsureDeploymentJobEffect(ctx, effects, nomadClient, r, job)
	if err != nil || replayed.State != worker.DeploymentJobEffectObserved || replayed.EffectID != first.EffectID {
		t.Fatalf("etcd/Nomad replay=%+v err=%v", replayed, err)
	}
	prematureLock, acquired, err := adapter.AcquireAppOperationLock(ctx, id)
	if err != nil || !acquired {
		t.Fatalf("premature terminal app lock acquired=%t err=%v", acquired, err)
	}
	prematureResult := *accepted.Deployment
	prematureResult.Status = model.StatusDeployed
	prematureRegions := []model.DeploymentRegion{{DeploymentID: prematureResult.ID, Region: input.Region, NomadRegion: input.NomadRegion,
		Status: model.StatusDeployed, DesiredWeight: accepted.Regions[0].TrafficWeight, ActiveWeight: accepted.Regions[0].TrafficWeight}}
	if err := adapter.finishClaimedDeployment(ctx, claim, prematureLock, prematureResult, prematureRegions, model.OperationSucceeded,
		"premature", map[string]interface{}{"externalEffectRecoveryPending": true}); err == nil || !strings.Contains(err.Error(), "successful deployment cannot retain unresolved recovery") {
		t.Fatalf("successful deployment used recovery hold to bypass effect proof: %v", err)
	}
	prematureErr := adapter.finishClaimedDeployment(ctx, claim, prematureLock, prematureResult, prematureRegions, model.OperationSucceeded, "premature", nil)
	prematureLock.Release()
	if prematureErr == nil {
		t.Fatal("unresolved Nomad effect permitted terminal deployment")
	}
	if resultRows, err := etcd.Get(ctx, adapter.deploymentRegionResultPrefix(accepted.Deployment.ID), clientv3.WithPrefix()); err != nil || len(resultRows.Kvs) != 0 {
		t.Fatalf("premature terminal wrote region result: rows=%d err=%v", len(resultRows.Kvs), err)
	}
	for {
		record, found, err := effects.UnresolvedForResource(ctx, adapter.authority, r.Resource)
		if err != nil || !found || record.Lifecycle != effect.LifecycleLaunched {
			t.Fatalf("launched effect unavailable: found=%t record=%+v err=%v", found, record, err)
		}
		decision, err := worker.CompleteDeploymentJobEffect(ctx, effects, nomadClient, record)
		if err != nil {
			t.Fatal(err)
		}
		if decision.State == worker.DeploymentJobEffectObserved {
			if _, found, err := effects.UnresolvedForResource(ctx, adapter.authority, r.Resource); err != nil || found {
				t.Fatalf("healthy effect retained gate: found=%t err=%v", found, err)
			}
			if operation, err := adapter.GetOperation(ctx, accepted.Operation.ID); err != nil || operation.Status.Terminal() {
				t.Fatalf("effect completion terminalized deployment operation: operation=%+v err=%v", operation, err)
			}
			active, err := etcd.Get(ctx, adapter.appAdmissionActivePrefix(id), clientv3.WithPrefix())
			if err != nil || len(active.Kvs) != 1 {
				t.Fatalf("effect completion released active app operation: entries=%d err=%v", len(active.Kvs), err)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Nomad job did not become healthy: %v", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	completed, _, _, err := effects.loadToken(ctx, effect.Token{EffectID: first.EffectID, Generation: 1})
	if err != nil || completed.Lifecycle != effect.LifecycleCompleted || completed.Completion == nil ||
		completed.Completion.Outcome != effect.OutcomeSucceeded || completed.Completion.Verification.InputDigest != r.InputDigest {
		t.Fatalf("durable healthy effect proof is unavailable: lifecycle=%q err=%v", completed.Lifecycle, err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, id)
	if err != nil || !acquired {
		t.Fatalf("deployment app lock acquired=%t err=%v", acquired, err)
	}
	defer lock.Release()
	result := *accepted.Deployment
	result.Status = model.StatusDeployed
	regions := []model.DeploymentRegion{{DeploymentID: result.ID, Region: input.Region, NomadRegion: input.NomadRegion,
		Status: model.StatusDeployed, DesiredWeight: accepted.Regions[0].TrafficWeight, ActiveWeight: accepted.Regions[0].TrafficWeight}}
	if err := adapter.finishClaimedDeployment(ctx, claim, lock, result, regions, model.OperationSucceeded, "deployed", nil); err == nil || !strings.Contains(err.Error(), "deployment-bound ingress proof") {
		t.Fatalf("Nomad health alone permitted positive active traffic: %v", err)
	}
	stillRunning, err := adapter.GetOperation(ctx, accepted.Operation.ID)
	if err != nil || stillRunning.Status != model.OperationRunning {
		t.Fatalf("route-proof refusal changed operation: operation=%+v err=%v", stillRunning, err)
	}
	if resultRows, err := etcd.Get(ctx, adapter.deploymentRegionResultPrefix(accepted.Deployment.ID), clientv3.WithPrefix()); err != nil || len(resultRows.Kvs) != 0 {
		t.Fatalf("route-proof refusal wrote region result: rows=%d err=%v", len(resultRows.Kvs), err)
	}
	active, err := etcd.Get(ctx, adapter.appAdmissionActivePrefix(id), clientv3.WithPrefix())
	if err != nil || len(active.Kvs) != 1 {
		t.Fatalf("route-proof refusal released app admission: entries=%d err=%v", len(active.Kvs), err)
	}
}
