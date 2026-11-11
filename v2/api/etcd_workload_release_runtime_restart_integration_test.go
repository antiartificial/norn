package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/effect"
	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
	workerruntime "norn/v2/api/worker"
)

func TestWorkloadReleaseReconcilerRecoversRouteLostResponseAfterRestartEtcd(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	newClient := func() *clientv3.Client {
		t.Helper()
		client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	client := newClient()
	prefix := "/norn-tests/workload-release-runtime-restart/" + uuid.NewString()
	t.Cleanup(func() {
		if client != nil {
			_, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix())
			_ = client.Close()
		}
	})
	signer, err := store.NewHMACAcceptanceSigner("norn-workload-release-runtime-restart-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: "norn-fleet-canary",
		Status: model.OperationQueued, Source: "runtime-restart-integration", StartedAt: time.Now().UTC(), MaxAttempts: 3}
	acceptance := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: authority,
			Actor: store.OperationActor{Issuer: "runtime-restart-integration", Subject: "operator"},
			Kind:  operation.Kind, Resource: "app/" + operation.App, Key: uuid.NewString()},
		Operation: operation, Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
		Audit: store.AcceptanceAuditContext{Source: "runtime-restart-integration"},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := operations.ClaimNextOperation(ctx, "workload-release-controller-before-crash", time.Minute, []string{operation.Kind})
	if err != nil || claimed == nil || claimed.ID != operation.ID {
		t.Fatalf("initial claim=%+v err=%v", claimed, err)
	}
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, operation.App)
	if err != nil || !acquired {
		t.Fatalf("initial app lock acquired=%v err=%v", acquired, err)
	}

	intent := workloadReleaseRuntimeRestartIntent()
	state := &workloadReleaseRuntimeRestartState{routeWrites: make(map[int]int), routeCalls: make(map[int]int),
		routeDigests: make(map[int]string), loseAfterStep: 1}
	firstProcess := workloadReleaseEtcdReceiptDriver{operations: operations, state: state, intent: intent, operationID: operation.ID}
	result, err := reconcileWorkloadRelease(ctx, claim, lock, firstProcess)
	var pending *effect.PendingError
	if !errors.As(err, &pending) || result != nil {
		t.Fatalf("pre-crash reconcile=(%+v,%v), want pending lost route response", result, err)
	}
	if state.routeWrites[1] != 1 || state.routeCalls[1] != 1 || state.lostResponseInjected != true {
		t.Fatalf("route effect before crash: writes=%v calls=%v lost=%t", state.routeWrites, state.routeCalls, state.lostResponseInjected)
	}
	record, receipt, err := firstProcess.loadReceipt(ctx)
	if err != nil || record == nil || receipt.RouteStep != 0 || receipt.RouteGeneration != intent.FirstRouteGeneration {
		t.Fatalf("durable predecessor after lost route response: record=%+v receipt=%+v err=%v", record, receipt, err)
	}

	// Revoke the operation lease and release the app lock to model a worker
	// process exiting without completing its in-flight route step.
	owner, err := client.Get(ctx, prefix+"/v3/owners/"+operation.ID)
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 {
		t.Fatalf("operation owner lease=%+v err=%v", owner.Kvs, err)
	}
	if _, err := client.Revoke(ctx, clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	if err := client.Close(); err != nil {
		t.Fatalf("close pre-crash etcd client: %v", err)
	}
	client = newClient()
	operations, err = etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	_, successor, err := operations.ClaimNextOperation(ctx, "workload-release-controller-after-crash", time.Minute, []string{operation.Kind})
	if err != nil || successor.OperationID() != operation.ID || successor.Generation() <= claim.Generation() {
		t.Fatalf("successor claim=%+v err=%v", successor, err)
	}
	lock, acquired, err = operations.AcquireAppOperationLock(ctx, operation.App)
	if err != nil || !acquired {
		t.Fatalf("successor app lock acquired=%v err=%v", acquired, err)
	}
	secondProcess := workloadReleaseEtcdReceiptDriver{operations: operations, state: state, intent: intent, operationID: operation.ID}
	result, err = reconcileWorkloadRelease(ctx, successor, lock, secondProcess)
	if err != nil || result == nil || result.Status != model.OperationSucceeded {
		t.Fatalf("post-restart reconcile=(%+v,%v), want succeeded", result, err)
	}
	completed, receipt, err := secondProcess.loadReceipt(ctx)
	if err != nil || completed == nil || receipt.Phase != pipeline.WorkloadReleaseComplete ||
		receipt.RouteStep != len(intent.RouteSteps)-1 || len(receipt.DrainedOldIDs) != len(intent.OldAllocationIDs) {
		t.Fatalf("terminal receipt=%+v record=%+v err=%v", receipt, completed, err)
	}
	for step := range intent.RouteSteps {
		if state.routeWrites[step] != 1 {
			t.Errorf("route step %d applied %d times, want once (calls=%d)", step, state.routeWrites[step], state.routeCalls[step])
		}
	}
	if state.routeCalls[1] != 2 || state.stopWrites != 1 {
		t.Fatalf("recovery did not adopt same route effect: route-1 calls=%d stop writes=%d", state.routeCalls[1], state.stopWrites)
	}
}

// This composes the durable target-create boundary with route/drain recovery:
// a successor must adopt the same ambiguous target create, bind its exact
// readback, and then finish the same release intent after another restart.
func TestWorkloadReleaseStageAndRouteRecoverSameIntentAcrossEtcdRestarts(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	newClient := func() *clientv3.Client {
		t.Helper()
		client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	client := newClient()
	prefix := "/norn-tests/workload-release-stage-route-restart/" + uuid.NewString()
	t.Cleanup(func() {
		if client != nil {
			_, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix())
			_ = client.Close()
		}
	})
	signer, err := store.NewHMACAcceptanceSigner("norn-workload-release-stage-route-restart-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	intent := workloadReleaseRuntimeRestartIntent()
	intent.TargetJobVersion, intent.TargetJobModifyIndex = 0, 0
	intent.FleetTargetGeneration = 1
	target := store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: intent.App,
		ControlEnvironment: intent.ControlEnvironment, Cluster: intent.FleetCluster, FleetEnvironment: intent.FleetEnvironment,
		Region: intent.Region, NomadRegion: intent.NomadRegion, NomadNamespace: intent.Namespace,
		Datacenters: append([]string(nil), intent.FleetDatacenters...), Generation: intent.FleetTargetGeneration}
	if _, err := operations.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	plan := pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: intent}
	planBytes, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: intent.App,
		Status: model.OperationQueued, Source: "stage-route-restart-integration", StartedAt: time.Now().UTC(), MaxAttempts: 4,
		Payload: map[string]interface{}{"workloadReleasePlan": string(planBytes)}}
	acceptance := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: authority,
			Actor: store.OperationActor{Issuer: "stage-route-restart-integration", Subject: "operator"},
			Kind:  operation.Kind, Resource: "app/" + operation.App, Key: uuid.NewString()},
		Operation: operation, Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
		Audit:     store.AcceptanceAuditContext{Source: "stage-route-restart-integration"},
		Semantics: map[string]interface{}{"planId": intent.ID},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	_, claim, err := operations.ClaimNextOperation(ctx, "stage-route-controller-before-crash", time.Minute, []string{operation.Kind})
	if err != nil || claim.OperationID() != operation.ID {
		t.Fatalf("initial claim=%+v err=%v", claim, err)
	}
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, operation.App)
	if err != nil || !acquired {
		t.Fatalf("initial app lock acquired=%v err=%v", acquired, err)
	}
	stageEffects, err := etcdstore.NewV3WorkloadReleaseEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := operations.WorkloadReleaseStageTargetReservation(ctx, claim, lock)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := stageEffects.Reserve(ctx, reservation)
	if err != nil || !reserved.Created {
		t.Fatalf("stage reservation=%+v err=%v", reserved, err)
	}
	allowed, err := operations.MarkWorkloadReleaseStageTargetAttempt(ctx, claim, lock, reserved.Record.Token.EffectID, "controller-before-crash")
	if err != nil || !allowed {
		t.Fatalf("first create marker allowed=%t err=%v", allowed, err)
	}
	createAttempts := 0
	if allowed { // the controller may submit only after the durable one-use marker
		createAttempts++
	}
	observer := &stageRouteRestartObserver{state: nomad.DeploymentJobNotFound}
	if _, err := operations.BindClaimedWorkloadReleaseTarget(ctx, claim, lock, reserved.Record.Token.EffectID, observer); !errors.Is(err, etcdstore.ErrWorkloadReleaseTargetNotFound) {
		t.Fatalf("initial absent readback err=%v", err)
	}
	owner, err := client.Get(ctx, prefix+"/v3/owners/"+operation.ID)
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 {
		t.Fatalf("owner lease=%+v err=%v", owner.Kvs, err)
	}
	if _, err := client.Revoke(ctx, clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	client = newClient()
	operations, err = etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	stageEffects, err = etcdstore.NewV3WorkloadReleaseEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	_, successor, err := operations.ClaimNextOperation(ctx, "stage-route-controller-after-crash", time.Minute, []string{operation.Kind})
	if err != nil || successor.OperationID() != operation.ID || successor.Generation() <= claim.Generation() {
		t.Fatalf("successor claim=%+v err=%v", successor, err)
	}
	lock, acquired, err = operations.AcquireAppOperationLock(ctx, operation.App)
	if err != nil || !acquired {
		t.Fatalf("successor app lock acquired=%v err=%v", acquired, err)
	}
	allowed, err = operations.MarkWorkloadReleaseStageTargetAttempt(ctx, successor, lock, reserved.Record.Token.EffectID, "controller-after-crash")
	if err != nil || allowed {
		t.Fatalf("successor reissued create marker allowed=%t err=%v", allowed, err)
	}
	if allowed { // a replayed marker must never authorize another external create
		createAttempts++
	}
	observer.state = nomad.DeploymentJobFound
	observer.version, observer.modifyIndex = 9, 47
	if _, err := operations.BindClaimedWorkloadReleaseTarget(ctx, successor, lock, reserved.Record.Token.EffectID, observer); err != nil {
		t.Fatalf("successor exact target binding: %v", err)
	}
	resolved, _, _, err := operations.ResolveClaimedWorkloadReleasePlan(ctx, successor, lock, reserved.Record.Token.EffectID)
	if err != nil || resolved.TargetJobVersion != observer.version || resolved.TargetJobModifyIndex != observer.modifyIndex {
		t.Fatalf("resolved intent=%+v err=%v", resolved, err)
	}
	if createAttempts != 1 {
		t.Fatalf("ambiguous create attempts=%d, want exactly one", createAttempts)
	}

	state := &workloadReleaseRuntimeRestartState{routeWrites: make(map[int]int), routeCalls: make(map[int]int),
		routeDigests: make(map[int]string), loseAfterStep: 1}
	firstRouteProcess := workloadReleaseEtcdReceiptDriver{operations: operations, state: state, intent: resolved, operationID: operation.ID}
	result, err := reconcileWorkloadRelease(ctx, successor, lock, firstRouteProcess)
	var pending *effect.PendingError
	if !errors.As(err, &pending) || result != nil {
		t.Fatalf("first route reconcile=(%+v,%v), want pending lost response", result, err)
	}
	owner, err = client.Get(ctx, prefix+"/v3/owners/"+operation.ID)
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 {
		t.Fatalf("route owner lease=%+v err=%v", owner.Kvs, err)
	}
	if _, err := client.Revoke(ctx, clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	client = newClient()
	operations, err = etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	_, finalClaim, err := operations.ClaimNextOperation(ctx, "stage-route-controller-recovered", time.Minute, []string{operation.Kind})
	if err != nil || finalClaim.OperationID() != operation.ID {
		t.Fatalf("final claim=%+v err=%v", finalClaim, err)
	}
	lock, acquired, err = operations.AcquireAppOperationLock(ctx, operation.App)
	if err != nil || !acquired {
		t.Fatalf("final app lock acquired=%v err=%v", acquired, err)
	}
	finalProcess := workloadReleaseEtcdReceiptDriver{operations: operations, state: state, intent: resolved, operationID: operation.ID}
	result, err = reconcileWorkloadRelease(ctx, finalClaim, lock, finalProcess)
	if err != nil || result == nil || result.Status != model.OperationSucceeded {
		t.Fatalf("final reconcile=(%+v,%v)", result, err)
	}
	if observer.calls != 2 || state.routeWrites[1] != 1 || state.routeCalls[1] != 2 || state.stopWrites != 1 {
		t.Fatalf("recovery effects: target readbacks=%d route writes=%v calls=%v stops=%d", observer.calls, state.routeWrites, state.routeCalls, state.stopWrites)
	}
}

// This driver qualification sends the production stage helper to a disposable
// Docker-capable Nomad agent. A loopback proxy drops the successful create
// response and hides the first readback; the successor must bind the job that
// Nomad actually accepted without submitting it a second time.
func TestWorkloadReleaseStageRecoversLostNomadResponseAfterEtcdRestart(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	address := strings.TrimSpace(os.Getenv("NORN_TEST_NOMAD_ADDR"))
	image := strings.TrimSpace(os.Getenv("NORN_TEST_DEPLOYMENT_IMAGE"))
	if endpoints == "" || address == "" || image == "" || os.Getenv("NORN_TEST_NOMAD_DOCKER") != "1" {
		t.Skip("set disposable NORN_TEST_ETCD_ENDPOINTS, NORN_TEST_NOMAD_ADDR, NORN_TEST_DEPLOYMENT_IMAGE, and NORN_TEST_NOMAD_DOCKER=1")
	}
	upstream, err := url.Parse(address)
	if err != nil || upstream.Scheme != "http" || net.ParseIP(upstream.Hostname()) == nil || !net.ParseIP(upstream.Hostname()).IsLoopback() {
		t.Fatal("workload release Nomad qualification requires a loopback HTTP agent")
	}
	plan, spec := workloadReleaseNomadStageFixture(t, image)
	intent := plan.Intent
	targetJobPlan, err := workerruntime.BuildWorkloadReleaseTargetJobPlan(plan, spec)
	if err != nil {
		t.Fatal(err)
	}
	actualNomad, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var registrations, hiddenReadbacks atomic.Int32
	var droppedCreate atomic.Bool
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		if request.Method == http.MethodPut && request.URL.Path == "/v1/jobs" {
			request.Header.Set("X-Norn-Test-Effect", "create")
		}
		if request.Method == http.MethodGet && request.URL.Path == "/v1/job/"+intent.TargetJobID {
			request.Header.Set("X-Norn-Test-Effect", "delayed-readback")
		}
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.Request == nil {
			return nil
		}
		switch response.Request.Header.Get("X-Norn-Test-Effect") {
		case "create":
			registrations.Add(1)
			if response.StatusCode >= 200 && response.StatusCode < 300 && droppedCreate.CompareAndSwap(false, true) {
				_ = response.Body.Close()
				return errors.New("test dropped the successful Nomad create response")
			}
		case "delayed-readback":
			if hiddenReadbacks.CompareAndSwap(0, 1) {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				response.Body = io.NopCloser(strings.NewReader(""))
				response.StatusCode = http.StatusNotFound
				response.Status = "404 Not Found"
				response.Header.Set("Content-Length", "0")
				response.Header.Del("Content-Encoding")
			}
		}
		return nil
	}
	proxyServer := httptest.NewServer(proxy)
	t.Cleanup(proxyServer.Close)
	schedulerClient, err := nomad.NewClient(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := &workloadReleaseNomadAdapter{client: schedulerClient}
	t.Cleanup(func() { _ = actualNomad.StopJob(intent.TargetJobID, true) })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	newEtcdClient := func() *clientv3.Client {
		t.Helper()
		client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	client := newEtcdClient()
	prefix := "/norn-tests/workload-release-nomad-stage/" + uuid.NewString()
	t.Cleanup(func() {
		if client != nil {
			_, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix())
			_ = client.Close()
		}
	})
	signer, err := store.NewHMACAcceptanceSigner("norn-workload-release-nomad-stage-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	target := store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: intent.App,
		ControlEnvironment: intent.ControlEnvironment, Cluster: intent.FleetCluster, FleetEnvironment: intent.FleetEnvironment,
		Region: intent.Region, NomadRegion: intent.NomadRegion, NomadNamespace: intent.Namespace,
		Datacenters: append([]string(nil), intent.FleetDatacenters...), Generation: intent.FleetTargetGeneration}
	if _, err := operations.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	planBytes, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: intent.App,
		Status: model.OperationQueued, Source: "disposable-nomad-stage-integration", StartedAt: time.Now().UTC(), MaxAttempts: 3,
		Payload: map[string]interface{}{"workloadReleasePlan": string(planBytes)}}
	acceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority,
		Actor: store.OperationActor{Issuer: "disposable-nomad-stage-integration", Subject: "operator"},
		Kind:  operation.Kind, Resource: "app/" + operation.App, Key: uuid.NewString()},
		Operation: operation, Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
		Audit:     store.AcceptanceAuditContext{Source: "disposable-nomad-stage-integration"},
		Semantics: map[string]interface{}{"planId": intent.ID}}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	_, claim, err := operations.ClaimNextOperation(ctx, "nomad-stage-before-crash", time.Minute, []string{operation.Kind})
	if err != nil || claim.OperationID() != operation.ID {
		t.Fatalf("initial claim=%+v err=%v", claim, err)
	}
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, operation.App)
	if err != nil || !acquired {
		t.Fatalf("initial app lock acquired=%v err=%v", acquired, err)
	}
	t.Cleanup(func() { lock.Release() })
	reservation, err := operations.WorkloadReleaseStageTargetReservation(ctx, claim, lock)
	if err != nil {
		t.Fatal(err)
	}
	stageEffects, err := etcdstore.NewV3WorkloadReleaseEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := stageEffects.Reserve(ctx, reservation)
	if err != nil || !reserved.Created {
		t.Fatalf("stage reservation=%+v err=%v", reserved, err)
	}
	inputs := func() *workerruntime.WorkloadReleaseTargetInputs {
		return &workerruntime.WorkloadReleaseTargetInputs{JobPlan: targetJobPlan,
			Secrets: map[string]string{}}
	}
	err = stageAndBindWorkloadReleaseTarget(ctx, scheduler, operations, claim, lock, reserved.Record.Token.EffectID,
		"nomad-stage-worker-before-crash", intent, inputs())
	var pending *effect.PendingError
	if !errors.As(err, &pending) || pending.Cause != etcdstore.ErrWorkloadReleaseTargetNotFound {
		t.Fatalf("first create/readback result=%v, want held pending after response loss and delayed visibility", err)
	}
	if scheduler.registerCalls != 1 || registrations.Load() != 1 || !droppedCreate.Load() || hiddenReadbacks.Load() != 1 {
		t.Fatalf("injected ambiguity did not follow a successful create: clientCalls=%d clientErr=%v proxyRegistrations=%d dropped=%t hidden=%d",
			scheduler.registerCalls, scheduler.registerErr, registrations.Load(), droppedCreate.Load(), hiddenReadbacks.Load())
	}
	observedJob, _, err := actualNomad.API().Jobs().Info(intent.TargetJobID,
		(&nomadapi.QueryOptions{Region: intent.NomadRegion, Namespace: intent.Namespace}).WithContext(ctx))
	if err != nil || observedJob == nil || observedJob.Meta[nomad.DeploymentExecutionIDMeta] != intent.TargetExecutionID {
		t.Fatalf("Nomad did not retain the target after its lost response: job=%+v err=%v", observedJob, err)
	}
	if err := operations.DeferClaimedOperationWithAppLock(ctx, claim, lock, "reconcile ambiguous Nomad target create",
		time.Now(), map[string]interface{}{"externalEffectRecoveryPending": true}); err != nil {
		t.Fatalf("durably defer unresolved target create: %v", err)
	}
	lock.Release()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	client = newEtcdClient()
	operations, err = etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	stageEffects, err = etcdstore.NewV3WorkloadReleaseEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	_, successor, err := operations.ClaimNextOperation(ctx, "nomad-stage-after-crash", time.Minute, []string{operation.Kind})
	if err != nil || successor.OperationID() != operation.ID || successor.Generation() <= claim.Generation() {
		t.Fatalf("successor claim=%+v err=%v", successor, err)
	}
	lock, acquired, err = operations.AcquireAppOperationLock(ctx, operation.App)
	if err != nil || !acquired {
		t.Fatalf("successor app lock acquired=%v err=%v", acquired, err)
	}
	recoveredReservation, err := operations.WorkloadReleaseStageTargetReservation(ctx, successor, lock)
	if err != nil {
		t.Fatal(err)
	}
	recoveredEffect, err := stageEffects.Reserve(ctx, recoveredReservation)
	if err != nil || recoveredEffect.Created || recoveredEffect.Record.Token != reserved.Record.Token {
		t.Fatalf("successor did not adopt original reservation: effect=%+v err=%v", recoveredEffect, err)
	}
	if err := stageAndBindWorkloadReleaseTarget(ctx, scheduler, operations, successor, lock, reserved.Record.Token.EffectID,
		"nomad-stage-worker-after-crash", intent, inputs()); err != nil {
		t.Fatalf("successor did not bind delayed Nomad readback: %v; observation=%+v lookupErr=%v request=%+v",
			err, scheduler.lookupObservation, scheduler.lookupErr, scheduler.lookupRequest)
	}
	if scheduler.registerCalls != 1 || registrations.Load() != 1 || hiddenReadbacks.Load() != 1 {
		t.Fatalf("successor replayed a completed or ambiguous external write: clientCalls=%d proxyRegistrations=%d hidden=%d", scheduler.registerCalls, registrations.Load(), hiddenReadbacks.Load())
	}
	resolved, binding, accepted, err := operations.ResolveClaimedWorkloadReleasePlan(ctx, successor, lock, reserved.Record.Token.EffectID)
	if err != nil || resolved.TargetJobVersion != scheduler.lookupObservation.Version || resolved.TargetJobModifyIndex == 0 ||
		binding.ClaimGeneration != successor.Generation() || accepted.Operation.ID != operation.ID {
		t.Fatalf("durable exact target binding=%+v intent=%+v accepted=%+v err=%v", binding, resolved, accepted.Operation, err)
	}
}

type stageRouteRestartObserver struct {
	state                nomad.DeploymentJobObservationState
	version, modifyIndex uint64
	calls                int
}

func workloadReleaseNomadStageFixture(t *testing.T, image string) (pipeline.WorkloadReleasePlan, *model.InfraSpec) {
	t.Helper()
	separator := strings.LastIndex(image, "@sha256:")
	if separator < 1 || len(image)-separator != len("@sha256:")+64 {
		t.Fatal("disposable workload image must be pinned by a SHA-256 digest")
	}
	app := "release-stage-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	bodyDigest := strings.Repeat("a", 64)
	spec := &model.InfraSpec{App: app, NomadNamespace: "default", Processes: map[string]model.Process{
		"web": {Port: 8080, Command: "sleep 3600", Resources: &model.Resources{CPU: 100, Memory: 128}},
	}, Endpoints: []model.Endpoint{{URL: "https://canary.example.test", Region: "local", Process: "web",
		TrafficProbe: &model.TrafficProbeSpec{Path: "/route-proof", BodySHA256: bodyDigest},
		RestrictedIngress: &model.RestrictedIngressSpec{PublicPaths: []string{"/route-proof", "/version"},
			RetryGETPaths: []string{"/version"}, PrivateReadinessPath: "/readyz"}}}}
	region := spec.ResolvedRegions()[0]
	specDigest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	deploymentID := "target-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	jobID, err := nomad.ManagedDeploymentJobID(app, region.Name, deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	imageDigest := "sha256:" + image[separator+len("@sha256:"):]
	intent := workloadReleaseRuntimeRestartIntent()
	intent.ID = "release-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	intent.App, intent.Region, intent.NomadRegion, intent.Namespace = app, region.Name, region.NomadRegion, spec.NomadNamespace
	intent.ControlEnvironment, intent.FleetCluster, intent.FleetEnvironment = "staging", "norn-test", "staging/local"
	intent.FleetDatacenters, intent.FleetTargetGeneration = append([]string(nil), region.Datacenters...), 1
	intent.Endpoint, intent.Process = spec.Endpoints[0].URL, "web"
	intent.SpecDigest, intent.SourceRevision = specDigest, "git:disposable-nomad-stage-test"
	intent.OldDeploymentID, intent.OldImageDigest, intent.OldSpecDigest = "old-"+deploymentID,
		"sha256:"+strings.Repeat("2", 64), "sha256:"+strings.Repeat("4", 64)
	intent.OldJobID, intent.OldJobDigest = app+"-old", "sha256:"+strings.Repeat("a", 64)
	intent.OldJobVersion, intent.OldJobModifyIndex, intent.OldAllocationIDs = 1, 2, []string{"old-allocation"}
	intent.TargetDeploymentID, intent.TargetJobID = deploymentID, jobID
	intent.TargetImage, intent.TargetImageDigest = image, imageDigest
	intent.TargetSpecDigest, intent.TargetJobDigest = specDigest, ""
	intent.TargetOperationID, intent.TargetExecutionID = "op-"+uuid.NewString(), "exec-"+uuid.NewString()
	intent.TargetJobVersion, intent.TargetJobModifyIndex, intent.TargetReplicas, intent.SurgeLimit = 0, 0, 1, 1
	intent.PolicyApprovalID, intent.PolicyDigest = uuid.NewString(), "sha256:"+strings.Repeat("6", 64)
	intent.PublicPaths, intent.RetryGETPaths = []string{"/route-proof", "/version"}, []string{"/version"}
	intent.PrivateReadinessPath, intent.TrafficProbePath, intent.TrafficProbeBodySHA256 = "/readyz", "/route-proof", bodyDigest
	intent.RouteSteps = []pipeline.WorkloadReleaseRouteStep{{OldWeight: 100}, {OldWeight: 50, TargetWeight: 50}, {TargetWeight: 100}}
	intent.TargetJobDigest, err = pipeline.DeriveWorkloadReleaseTargetJobDigest(intent, spec)
	if err != nil {
		t.Fatal(err)
	}
	return pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: intent}, spec
}

type workloadReleaseNomadAdapter struct {
	client            *nomad.Client
	registerCalls     int
	registerErr       error
	lookupObservation nomad.DeploymentJobObservation
	lookupErr         error
	lookupRequest     nomad.CASDeploymentJobRequest
}

func (a *workloadReleaseNomadAdapter) StageManagedJobSecretInputs(region string, requirements nomad.ManagedJobInputRequirements, values map[string]string) error {
	return a.client.StageManagedJobSecretInputs(region, requirements, values)
}

func (a *workloadReleaseNomadAdapter) RegisterDeploymentJobCAS(ctx context.Context, request nomad.CASDeploymentJobRequest) (string, error) {
	a.registerCalls++
	eval, err := a.client.RegisterDeploymentJobCAS(ctx, request)
	a.registerErr = err
	return eval, err
}

func (a *workloadReleaseNomadAdapter) LookupDeploymentJobRevision(ctx context.Context, request nomad.CASDeploymentJobRequest) (nomad.DeploymentJobObservation, error) {
	a.lookupRequest = request
	a.lookupObservation, a.lookupErr = a.client.LookupDeploymentJobRevision(ctx, request)
	return a.lookupObservation, a.lookupErr
}

func (o *stageRouteRestartObserver) LookupDeploymentJobRevision(_ context.Context, _ nomad.CASDeploymentJobRequest) (nomad.DeploymentJobObservation, error) {
	o.calls++
	return nomad.DeploymentJobObservation{State: o.state, Version: o.version, JobModifyIndex: o.modifyIndex}, nil
}

type workloadReleaseRuntimeRestartState struct {
	routeWrites          map[int]int
	routeCalls           map[int]int
	routeDigests         map[int]string
	loseAfterStep        int
	lostResponseInjected bool
	stopWrites           int
	privateInputDeletes  int
	privateInputDeleted  bool
}

type workloadReleaseEtcdReceiptDriver struct {
	operations  *etcdstore.V3OperationStore
	state       *workloadReleaseRuntimeRestartState
	intent      pipeline.WorkloadReleaseIntent
	operationID string
}

func (d workloadReleaseEtcdReceiptDriver) Verify(_ context.Context, claim store.OperationClaim, _ store.AppOperationLock) (pipeline.WorkloadReleaseIntent, error) {
	if claim.OperationID() != d.operationID || d.intent.Validate() != nil {
		return pipeline.WorkloadReleaseIntent{}, errors.New("test workload release intent is invalid")
	}
	return d.intent, nil
}

func (d workloadReleaseEtcdReceiptDriver) EnsureTargetBound(context.Context, store.OperationClaim, store.AppOperationLock, pipeline.WorkloadReleaseIntent) error {
	return nil
}

func (d workloadReleaseEtcdReceiptDriver) Load(ctx context.Context, intent pipeline.WorkloadReleaseIntent) (*store.WorkloadReleaseReceiptRecord, pipeline.WorkloadReleaseReceipt, error) {
	record, err := d.operations.LoadWorkloadReleaseReceipt(ctx, intent.ID)
	if err != nil || record == nil {
		return record, pipeline.WorkloadReleaseReceipt{}, err
	}
	var receipt pipeline.WorkloadReleaseReceipt
	if record.OperationID != d.operationID || json.Unmarshal(record.Receipt, &receipt) != nil || receipt.ValidateAgainst(intent) != nil {
		return nil, pipeline.WorkloadReleaseReceipt{}, errors.New("test driver loaded an invalid durable receipt")
	}
	return record, receipt, nil
}

func (d workloadReleaseEtcdReceiptDriver) Initialize(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	planned, err := json.Marshal(pipeline.WorkloadReleaseReceipt{IntentID: intent.ID, Phase: pipeline.WorkloadReleasePlanned})
	if err != nil {
		return err
	}
	_, err = d.operations.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, intent.ID, "", planned)
	return err
}

func (d workloadReleaseEtcdReceiptDriver) Advance(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent,
	expected string, next pipeline.WorkloadReleaseReceipt) (store.WorkloadReleaseReceiptRecord, error) {
	if expected != "" {
		_, current, err := d.Load(ctx, intent)
		if err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
		if err := next.ValidateSuccessor(intent, current); err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
	} else if err := next.ValidateAgainst(intent); err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	return d.operations.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, intent.ID, expected, encoded)
}

func (d workloadReleaseEtcdReceiptDriver) ObserveHealth(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	record, receipt, err := d.Load(ctx, intent)
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("durable receipt was unavailable during target health readback")
	}
	if len(receipt.TargetAllocations) == intent.TargetReplicas && healthyWorkloadTargets(receipt) == intent.TargetReplicas {
		return nil
	}
	receipt.TargetAllocations = []pipeline.WorkloadReleaseAllocationObservation{
		{AllocationID: "target-a", NodeID: "node-a", JobID: intent.TargetJobID, Namespace: intent.Namespace,
			TaskGroup: intent.Process, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
			ImageDigest: intent.TargetImageDigest, Healthy: true},
		{AllocationID: "target-b", NodeID: "node-b", JobID: intent.TargetJobID, Namespace: intent.Namespace,
			TaskGroup: intent.Process, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
			ImageDigest: intent.TargetImageDigest, Healthy: true},
	}
	_, err = d.Advance(ctx, claim, intent, record.ReceiptDigest, receipt)
	return err
}

func (d workloadReleaseEtcdReceiptDriver) PublishRouteStep(ctx context.Context, claim store.OperationClaim, _ store.AppOperationLock,
	intent pipeline.WorkloadReleaseIntent, step int) error {
	if step < 0 || step >= len(intent.RouteSteps) {
		return errors.New("route step is outside the admitted intent")
	}
	rendered, err := intent.RenderRouteStep(step)
	if err != nil {
		return err
	}
	d.state.routeCalls[step]++
	if prior, exists := d.state.routeDigests[step]; exists && prior != rendered.SHA256 {
		return errors.New("same route generation resolved to different content")
	}
	if _, exists := d.state.routeDigests[step]; !exists {
		d.state.routeDigests[step] = rendered.SHA256
		d.state.routeWrites[step]++
	}
	if step == d.state.loseAfterStep && !d.state.lostResponseInjected {
		d.state.lostResponseInjected = true
		return &effect.PendingError{Resource: "app/" + intent.App + "/workload-release/" + intent.Region,
			Reason: "simulated lost route response after external publication"}
	}
	record, receipt, err := d.Load(ctx, intent)
	if err != nil || record == nil {
		return errors.New("durable receipt was unavailable while reconciling route")
	}
	stepPolicy := intent.RouteSteps[step]
	receipt.RouteStep, receipt.RouteOldWeight, receipt.RouteTargetWeight = step, stepPolicy.OldWeight, stepPolicy.TargetWeight
	receipt.RouteGeneration, receipt.RouteSHA256 = intent.FirstRouteGeneration+uint64(step), "sha256:"+rendered.SHA256
	if step == len(intent.RouteSteps)-1 {
		receipt.Phase = pipeline.WorkloadReleaseDraining
	} else {
		receipt.Phase = pipeline.WorkloadReleaseRouting
	}
	_, err = d.Advance(ctx, claim, intent, record.ReceiptDigest, receipt)
	return err
}

func (d workloadReleaseEtcdReceiptDriver) StopAndReadback(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	if d.state.stopWrites == 0 {
		d.state.stopWrites++
	}
	record, receipt, err := d.Load(ctx, intent)
	if err != nil || record == nil {
		return errors.New("durable receipt was unavailable while reconciling drain")
	}
	receipt.OldJobStoppedVersion = intent.OldJobVersion + 1
	receipt.OldJobStoppedModifyIndex = intent.OldJobModifyIndex + 2
	receipt.DrainedOldIDs = append([]string(nil), intent.OldAllocationIDs...)
	_, err = d.Advance(ctx, claim, intent, record.ReceiptDigest, receipt)
	return err
}

func (d workloadReleaseEtcdReceiptDriver) Complete(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) (store.WorkloadReleaseReceiptRecord, error) {
	record, receipt, err := d.Load(ctx, intent)
	if err != nil || record == nil {
		return store.WorkloadReleaseReceiptRecord{}, errors.New("durable receipt was unavailable at completion")
	}
	receipt.Phase = pipeline.WorkloadReleaseComplete
	return d.Advance(ctx, claim, intent, record.ReceiptDigest, receipt)
}

func (d workloadReleaseEtcdReceiptDriver) DeleteCompletedPrivateInput(ctx context.Context, _ store.OperationClaim, _ store.AppOperationLock,
	intent pipeline.WorkloadReleaseIntent, completed store.WorkloadReleaseReceiptRecord) error {
	record, receipt, err := d.Load(ctx, intent)
	if err != nil || record == nil || receipt.Phase != pipeline.WorkloadReleaseComplete || record.ReceiptDigest != completed.ReceiptDigest {
		return errors.New("private input cleanup did not receive the durable completed receipt")
	}
	d.state.privateInputDeletes++
	d.state.privateInputDeleted = true
	return nil
}

func (d workloadReleaseEtcdReceiptDriver) loadReceipt(ctx context.Context) (*store.WorkloadReleaseReceiptRecord, pipeline.WorkloadReleaseReceipt, error) {
	return d.Load(ctx, d.intent)
}

func workloadReleaseRuntimeRestartIntent() pipeline.WorkloadReleaseIntent {
	return pipeline.WorkloadReleaseIntent{
		ID: "runtime-restart-intent", App: "norn-fleet-canary", Region: "nyc3", NomadRegion: "global", Namespace: "default",
		ControlEnvironment: "staging", FleetCluster: "norn-staging", FleetEnvironment: "staging/nyc3", FleetDatacenters: []string{"nyc3"}, FleetTargetGeneration: 3,
		Process: "web", Endpoint: "https://canary.example.test", PublicPaths: []string{"/route-proof", "/version"},
		RetryGETPaths: []string{"/version"}, PrivateReadinessPath: "/readyz", TrafficProbePath: "/route-proof",
		TrafficProbeBodySHA256: strings.Repeat("a", 64),
		SpecDigest:             "sha256:" + strings.Repeat("1", 64), SourceRevision: "git:runtime-restart-test", OldDeploymentID: "deployment-old",
		OldImageDigest: "sha256:" + strings.Repeat("2", 64), OldSpecDigest: "sha256:" + strings.Repeat("4", 64),
		OldJobID: "norn-fleet-canary-old", OldJobDigest: "sha256:" + strings.Repeat("a", 64), OldJobVersion: 7, OldJobModifyIndex: 41,
		TargetDeploymentID: "deployment-new", TargetImage: "registry.example.test/norn-fleet-canary@sha256:" + strings.Repeat("3", 64),
		TargetImageDigest: "sha256:" + strings.Repeat("3", 64), TargetJobID: "norn-fleet-canary-new",
		TargetJobDigest: "sha256:" + strings.Repeat("4", 64), TargetSpecDigest: "sha256:" + strings.Repeat("5", 64),
		TargetOperationID: "operation-target", TargetExecutionID: "nomad-deployment-target", OldAllocationIDs: []string{"old-a", "old-b"},
		TargetJobVersion: 8, TargetJobModifyIndex: 43, TargetReplicas: 2, SurgeLimit: 1,
		PolicyApprovalID: "11111111-1111-4111-8111-111111111111", PolicyDigest: "sha256:" + strings.Repeat("6", 64), FirstRouteGeneration: 11,
		RouteSteps: []pipeline.WorkloadReleaseRouteStep{{OldWeight: 100}, {OldWeight: 50, TargetWeight: 50}, {TargetWeight: 100}},
	}
}
