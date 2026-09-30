package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

const (
	deploymentCrashRoleEnv   = "NORN_ETCD_DEPLOYMENT_CRASH_ROLE"
	deploymentCrashMarkerEnv = "NORN_ETCD_DEPLOYMENT_CRASH_MARKER"
	deploymentCrashStateEnv  = "NORN_ETCD_DEPLOYMENT_CRASH_STATE"
)

type deploymentCrashFixture struct {
	Prefix       string `json:"prefix"`
	Authority    string `json:"authority"`
	App          string `json:"app"`
	DeploymentID string `json:"deploymentId"`
	Image        string `json:"image"`
	SpecDigest   string `json:"specDigest"`
	Region       string `json:"region"`
	NomadRegion  string `json:"nomadRegion"`
}

type deploymentCrashState struct {
	OperationID string `json:"operationId"`
	OwnerID     string `json:"ownerId"`
	Generation  int64  `json:"generation"`
}

// crashAfterDeploymentRegister makes the parent kill a real worker only after
// Nomad has accepted the exactly-once job and the durable attempt marker has
// been written. The marker is a test synchronization boundary, never evidence
// used by the recovery path.
type crashAfterDeploymentRegister struct {
	worker.DeploymentJobRemote
	marker string
}

func (r crashAfterDeploymentRegister) RegisterDeploymentJobCAS(ctx context.Context, request nomad.CASDeploymentJobRequest) (string, error) {
	value, err := r.DeploymentJobRemote.RegisterDeploymentJobCAS(ctx, request)
	if err != nil {
		return value, err
	}
	if err := os.WriteFile(r.marker, []byte("registered\n"), 0o600); err != nil {
		return "", err
	}
	select {}
}

// TestV3DeploymentWorkerProcessCrashFailsClosedThroughEtcdAndDisposableNomad
// kills the actual owner of a real Nomad registration, then proves etcd's
// supported lease-expiry recovery fails app.deploy closed. Two successor
// processes cannot claim it automatically, and the original effect remains
// inspectable rather than being replayed or replaced.
func TestV3DeploymentWorkerProcessCrashFailsClosedThroughEtcdAndDisposableNomad(t *testing.T) {
	if role := os.Getenv(deploymentCrashRoleEnv); role != "" {
		runDeploymentCrashRole(t, role)
		return
	}
	address, image := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_DEPLOYMENT_IMAGE")
	if address == "" || image == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR and NORN_TEST_DEPLOYMENT_IMAGE for a disposable Docker-capable Nomad agent")
	}
	parsed, err := url.Parse(address)
	if err != nil || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || !model.IsContentAddressedImage(image) {
		t.Fatal("process-crash qualification requires loopback Nomad and a content-addressed image")
	}

	adapter, etcd, prefix := deploymentEtcdStore(t)
	id := "norn-etcd-crash-qual-" + uuid.NewString()[:8]
	request := deploymentAdmissionRequest(t, adapter.authority)
	request.Identity.Resource, request.Identity.Key = "app/"+id, uuid.NewString()
	request.Operation.App, request.Deployment.App = id, id
	request.Deployment.ImageTag = image
	request.Deployment.SpecDigest = "sha256:" + strings.Repeat("b", 64)
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: id, ControlEnvironment: "staging", Cluster: "norn-staging", FleetEnvironment: "staging/nyc3", Region: "west", NomadRegion: "global", Datacenters: []string{"dc1"}, Generation: 1}
	if _, err := adapter.putFleetAppTarget(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	request.Semantics["fleetAppTarget"] = target
	request.Regions[0].Datacenters = []string{"dc1"}
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	fixture := deploymentCrashFixture{Prefix: prefix, Authority: adapter.authority, App: id, DeploymentID: accepted.Deployment.ID, Image: image, SpecDigest: accepted.Deployment.SpecDigest, Region: accepted.Regions[0].Name, NomadRegion: accepted.Regions[0].NomadRegion}
	t.Cleanup(func() {
		api, apiErr := nomadapi.NewClient(&nomadapi.Config{Address: address})
		if apiErr == nil {
			_, _, _ = api.Jobs().Deregister(id, true, &nomadapi.WriteOptions{Region: fixture.NomadRegion})
		}
	})

	marker := filepath.Join(t.TempDir(), "nomad-register")
	stateFile := filepath.Join(t.TempDir(), "crashed-owner.json")
	crashed := deploymentCrashCommand(t, fixture, "crash", marker, stateFile)
	if err := crashed.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = crashed.Process.Kill(); _, _ = crashed.Process.Wait() })
	deadline := time.Now().Add(25 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("crashed worker did not reach post-Nomad-registration boundary")
		}
		time.Sleep(100 * time.Millisecond)
	}
	api, err := nomadapi.NewClient(&nomadapi.Config{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	registered, _, err := api.Jobs().Info(id, &nomadapi.QueryOptions{Region: fixture.NomadRegion})
	if err != nil || registered == nil || registered.Version == nil || *registered.Version != 0 {
		t.Fatalf("first process did not leave one real Nomad job: job=%+v err=%v", registered, err)
	}
	effects, err := NewV3DeploymentEffectReservations(adapter)
	if err != nil {
		t.Fatal(err)
	}
	var crashedState deploymentCrashState
	encodedState, err := os.ReadFile(stateFile)
	if err != nil || json.Unmarshal(encodedState, &crashedState) != nil || crashedState.OperationID == "" || crashedState.OwnerID == "" || crashedState.Generation < 1 {
		t.Fatalf("crashed worker claim state=%q err=%v", encodedState, err)
	}
	resource := "app/" + id + "/deploy/" + fixture.Region
	record, found, err := effects.UnresolvedForResource(ctx, adapter.authority, resource)
	if err != nil || !found || record.Lifecycle != effect.LifecycleReserved {
		t.Fatalf("post-register durable effect=%+v found=%t err=%v", record, found, err)
	}
	attempted, err := effects.SubmitAttempted(ctx, record.Token)
	if err != nil || !attempted {
		t.Fatalf("post-register submit marker=%t err=%v", attempted, err)
	}
	owner, err := etcd.Get(ctx, adapter.ownerKey(crashedState.OperationID))
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(crashedState.OwnerID, crashedState.Generation) {
		t.Fatalf("crashed worker did not own the live lease immediately before kill: owner=%+v err=%v", owner, err)
	}
	if err := crashed.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL first worker: %v", err)
	}
	if err := crashed.Wait(); err == nil {
		t.Fatal("first worker exited cleanly instead of being killed")
	}
	staleClaim, err := store.NewOperationClaim(crashedState.OperationID, crashedState.OwnerID, crashedState.Generation)
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(25 * time.Second)
	for {
		owner, ownerErr := etcd.Get(ctx, adapter.ownerKey(staleClaim.OperationID()))
		if ownerErr == nil && len(owner.Kvs) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("crashed worker owner lease persisted: owner=%+v err=%v", owner, ownerErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := adapter.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err := adapter.GetOperation(ctx, staleClaim.OperationID())
	if err != nil || operation.Status != model.OperationFailed || operation.Metadata["manualRecoveryRequired"] != true || operation.Metadata["externalEffectRecoveryPending"] != true {
		t.Fatalf("supported lease-expiry result=%+v err=%v", operation, err)
	}
	if err := adapter.DeferClaimedOperation(ctx, staleClaim, "unsafe stale replay", time.Now(), nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("stale claim mutated lease-recovered operation: %v", err)
	}

	successors := []*exec.Cmd{deploymentCrashCommand(t, fixture, "successor", "", ""), deploymentCrashCommand(t, fixture, "successor", "", "")}
	outputs := make([]bytes.Buffer, len(successors))
	for index, successor := range successors {
		successor.Stdout, successor.Stderr = &outputs[index], &outputs[index]
		if err := successor.Start(); err != nil {
			t.Fatalf("start successor %d: %v", index+1, err)
		}
	}
	for index, successor := range successors {
		if err := successor.Wait(); err != nil {
			t.Fatalf("successor %d failed: %v\n%s", index+1, err, outputs[index].String())
		}
	}
	versions, _, _, err := api.Jobs().Versions(id, false, &nomadapi.QueryOptions{Region: fixture.NomadRegion})
	if err != nil || len(versions) != 1 || versions[0] == nil || versions[0].Version == nil || *versions[0].Version != 0 {
		t.Fatalf("two successors created a duplicate Nomad revision: versions=%+v err=%v", versions, err)
	}
	record, found, err = effects.UnresolvedForResource(ctx, adapter.authority, resource)
	if err != nil || !found || record.Token.Generation != crashedState.Generation || record.Reservation.InputDigest == "" || record.Reservation.OperationClaim.Generation != crashedState.Generation {
		t.Fatalf("lease recovery changed the original unresolved effect=%+v found=%t err=%v", record, found, err)
	}
}

func deploymentCrashCommand(t *testing.T, fixture deploymentCrashFixture, role, marker, state string) *exec.Cmd {
	t.Helper()
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestV3DeploymentWorkerProcessCrashFailsClosedThroughEtcdAndDisposableNomad$", "-test.v")
	command.Env = append(os.Environ(), deploymentCrashRoleEnv+"="+role, "NORN_ETCD_DEPLOYMENT_CRASH_FIXTURE="+string(encoded), deploymentCrashMarkerEnv+"="+marker, deploymentCrashStateEnv+"="+state)
	return command
}

func runDeploymentCrashRole(t *testing.T, role string) {
	t.Helper()
	var fixture deploymentCrashFixture
	if err := json.Unmarshal([]byte(os.Getenv("NORN_ETCD_DEPLOYMENT_CRASH_FIXTURE")), &fixture); err != nil {
		t.Fatalf("decode crash fixture: %v", err)
	}
	if fixture.Prefix == "" || fixture.Authority == "" || fixture.App == "" || fixture.DeploymentID == "" || fixture.Image == "" || fixture.SpecDigest == "" || fixture.Region == "" || fixture.NomadRegion == "" {
		t.Fatal("crash fixture is incomplete")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"), ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-private-invocation-signing-key")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := NewV3OperationStore(client, fixture.Prefix, fixture.Authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := NewV3DeploymentEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := nomad.NewClient(os.Getenv("NORN_TEST_NOMAD_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	switch role {
	case "crash":
		_, claim, err := operations.ClaimNextOperation(ctx, "crashed-worker-process", 15*time.Second, []string{"app.deploy"})
		if err != nil || claim.OperationID() == "" {
			t.Fatalf("crash worker claim=%+v err=%v", claim, err)
		}
		state, err := json.Marshal(deploymentCrashState{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv(deploymentCrashStateEnv), state, 0o600); err != nil {
			t.Fatalf("write crash worker claim state: %v", err)
		}
		reservation, job, err := deploymentCrashPlan(fixture, claim)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := worker.EnsureDeploymentJobEffect(ctx, effects, crashAfterDeploymentRegister{DeploymentJobRemote: remote, marker: os.Getenv(deploymentCrashMarkerEnv)}, reservation, job); err != nil {
			t.Fatal(err)
		}
		t.Fatal("crash role returned after Nomad registration")
	case "successor":
		operation, claim, err := operations.ClaimNextOperation(ctx, "successor-process", time.Minute, []string{"app.deploy"})
		if err != nil {
			t.Fatal(err)
		}
		if operation == nil && claim.OperationID() == "" {
			return
		}
		t.Fatalf("unsupported successor automatically claimed lease-recovered operation=%+v claim=%+v", operation, claim)
	default:
		t.Fatalf("unknown crash role %q", role)
	}
}

func deploymentCrashPlan(fixture deploymentCrashFixture, claim store.OperationClaim) (effect.Reservation, *nomadapi.Job, error) {
	job, err := nomad.Translate(&model.InfraSpec{App: fixture.App, Processes: map[string]model.Process{"web": {Command: "sleep 60"}}}, fixture.Image, nil)
	if err != nil {
		return effect.Reservation{}, nil, err
	}
	healthCheck, minHealthy := "task_states", time.Second
	job.TaskGroups[0].Update.HealthCheck, job.TaskGroups[0].Update.MinHealthyTime = &healthCheck, &minHealthy
	input := nomad.DeploymentJobEffectInput{App: fixture.App, DeploymentID: fixture.DeploymentID, Region: fixture.Region, NomadRegion: fixture.NomadRegion, ImageTag: fixture.Image, SpecDigest: fixture.SpecDigest}
	job.Meta[nomad.DeploymentIDMeta], job.Meta[nomad.SpecDigestMeta], job.Meta[nomad.DeploymentOperationIDMeta] = input.DeploymentID, input.SpecDigest, claim.OperationID()
	input.JobDigest, err = nomad.DigestDeploymentJob(job)
	if err != nil {
		return effect.Reservation{}, nil, err
	}
	executionID := deploymentEffectExecutionID(claim.OperationID(), input.Region, input.JobDigest)
	job.Meta[nomad.DeploymentExecutionIDMeta], job.Meta[nomad.DeploymentJobDigestMeta] = executionID, input.JobDigest
	payload, err := json.Marshal(input)
	if err != nil {
		return effect.Reservation{}, nil, err
	}
	reservation := effect.Reservation{Authority: fixture.Authority, Resource: "app/" + fixture.App + "/deploy/" + input.Region, OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()}, Stage: "app.deploy.nomad.submit", Supervisor: "nomad-deployment", SupervisorExecutionID: executionID, LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		return effect.Reservation{}, nil, err
	}
	return reservation, job, nil
}

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
