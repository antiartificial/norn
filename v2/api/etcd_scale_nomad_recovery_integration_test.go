package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

// This opt-in test uses disposable loopback etcd and Nomad. A proxy drops
// Nomad's post-write scale read, so the first worker cannot claim success;
// a successor must reconcile the actual scheduler event and placement.
func TestEtcdManagedScaleRecoversRealNomadPlacement(t *testing.T) {
	endpoint, address := os.Getenv("NORN_TEST_ETCD_ENDPOINTS"), os.Getenv("NORN_TEST_DISPOSABLE_NOMAD_ADDR")
	if endpoint == "" || address == "" {
		t.Skip("disposable etcd and Nomad endpoints are required")
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("Nomad must be an explicit loopback HTTP agent")
	}
	ctx := context.Background()
	ec, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoint, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ec.Close() })
	prefix := "/norn-tests/scale-real-nomad/" + uuid.NewString()
	t.Cleanup(func() { _, _ = ec.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-real-nomad-scale-signing-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(ec, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}

	const app, region, deploymentID = "widgets", "us-central", "deployment-real-nomad"
	jobID := "norn-scale-recovery-" + uuid.NewString()
	direct, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	job := nomadapi.NewServiceJob(jobID, jobID, "global", 50).AddDatacenter("dc1")
	appPool := "app"
	job.NodePool = &appPool
	group := nomadapi.NewTaskGroup("web", 1).AddTask(
		nomadapi.NewTask("sleep", "raw_exec").SetConfig("command", "/bin/sleep").SetConfig("args", []string{"300"}),
	)
	group.Constraints = append(group.Constraints, nomadapi.NewConstraint("", nomadapi.ConstraintDistinctHosts, ""))
	job.AddTaskGroup(group)
	if _, err := direct.SubmitJob(job); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.StopJob(jobID, true); err != nil {
			t.Errorf("purge job: %v", err)
		}
	})
	deadline := time.Now().Add(45 * time.Second)
	for {
		deployment, err := direct.LatestDeploymentRegion(jobID, "global")
		if err == nil && deployment != nil && deployment.Status == "successful" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("initial deployment did not settle: %+v %v", deployment, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	var failScaleRead atomic.Bool
	failScaleRead.Store(true)
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failScaleRead.Load() && r.Method == http.MethodGet && r.URL.Path == "/v1/job/"+jobID+"/scale" {
			http.Error(w, "injected post-accept read outage", http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(proxied.Close)
	apps := t.TempDir()
	if err := os.Mkdir(filepath.Join(apps, app), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := "name: widgets\ndeploy: true\nregions:\n  us-central:\n    nomadRegion: global\n    datacenters: [dc1]\nprocesses:\n  web:\n    command: sleep 300\n    scaling:\n      min: 1\n      max: 2\n    placement:\n      distinctHosts: true\n    health:\n      path: /ready\n"
	if err := os.WriteFile(filepath.Join(apps, app, "infraspec.yaml"), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	target := store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: app, ControlEnvironment: "staging", Cluster: "fleet", FleetEnvironment: "staging/us-central", Region: region, NomadRegion: "global", Datacenters: []string{"dc1"}, Generation: 1}
	if _, err := operations.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	identities := etcdstore.NewAuthStore(ec, prefix)
	rootID := uuid.NewString()
	now := time.Now().UTC()
	if err := identities.RecordAccessToken(ctx, &store.AccessToken{JTI: rootID, Subject: "operator", Scopes: []string{handler.ScopeAPIRead, handler.ScopeAPIWrite}, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Environment: "staging", AppsDir: apps, NomadAddr: proxied.URL}
	runtime, err := newEtcdScaleRuntime(cfg, operations)
	if err != nil {
		t.Fatal(err)
	}
	specDigest, err := currentEtcdScaleSpecDigest(apps, app)
	if err != nil {
		t.Fatal(err)
	}
	runtime.resolveTarget = func(_ context.Context, gotApp, environment, gotRegion string) (etcdstore.FleetScaleTarget, error) {
		if gotApp != app || environment != "staging" || gotRegion != region {
			return etcdstore.FleetScaleTarget{}, fmt.Errorf("wrong route")
		}
		return etcdstore.FleetScaleTarget{JobID: jobID, DeploymentID: deploymentID, SpecDigest: specDigest}, nil
	}
	router := chi.NewRouter()
	router.Post("/api/v1/apps/{id}/scale", etcdScaleApp(cfg, operations, identities, runtime))
	principal := &handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, TokenID: rootID, Scopes: []string{handler.ScopeAPIWrite, handler.ScopeAPIRead}}
	acceptScale := func(count int, key string) model.Operation {
		t.Helper()
		body := fmt.Sprintf(`{"group":"web","region":"%s","count":%d}`, region, count)
		req := handler.WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/apps/widgets/scale", strings.NewReader(body)), principal)
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusAccepted {
			t.Fatalf("scale %d admission=%d %s", count, response.Code, response.Body.String())
		}
		var accepted model.Operation
		if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || accepted.ID == "" || accepted.Payload["nomadJobId"] != jobID {
			t.Fatalf("scale %d accepted target=%+v err=%v", count, accepted, err)
		}
		return accepted
	}
	accepted := acceptScale(2, "real-nomad-scale-up")
	id := accepted.ID
	claimed, first, err := operations.ClaimNextOperation(ctx, "first-worker", time.Minute, []string{"app.scale"})
	if err != nil || claimed == nil {
		t.Fatalf("first claim: %+v %v", claimed, err)
	}
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("first lock: %t %v", acquired, err)
	}
	result, err := runtime.ExecuteOperationWithAppLock(ctx, claimed, first, lock)
	if result != nil || err == nil {
		t.Fatalf("uncertain launch must defer: %+v %v", result, err)
	}
	lock.Release()
	owner, err := ec.Get(ctx, prefix+"/v3/owners/"+id)
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 {
		t.Fatalf("owner lease: %+v %v", owner.Kvs, err)
	}
	if _, err := ec.Revoke(ctx, clientv3.LeaseID(owner.Kvs[0].Lease)); err != nil {
		t.Fatal(err)
	}
	if err := operations.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := operations.GetOperation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != model.OperationQueued || recovered.Metadata["externalEffectRecoveryPending"] != true {
		t.Fatalf("expired claim was not queued for effect reconciliation: %+v", recovered)
	}
	before, err := operations.DesiredReplicaCounts(ctx, app, region)
	if err != nil || len(before) != 0 {
		t.Fatalf("uncertain external effect prematurely changed desired intent: %v %v", before, err)
	}
	failScaleRead.Store(false)
	deadline = time.Now().Add(45 * time.Second)
	for {
		status, err := direct.JobScaleStatusRegion(jobID, "global")
		if err == nil && status != nil && status.TaskGroups["web"].Running >= 2 && status.TaskGroups["web"].Healthy >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Nomad did not place healthy scaled allocations: %+v %v", status, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	claimed, second, err := operations.ClaimNextOperation(ctx, "successor-worker", time.Minute, []string{"app.scale"})
	if err != nil || claimed == nil || second.Generation() <= first.Generation() {
		t.Fatalf("successor claim: %+v %v", claimed, err)
	}
	lock, acquired, err = operations.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("successor lock: %t %v", acquired, err)
	}
	result, err = runtime.ExecuteOperationWithAppLock(ctx, claimed, second, lock)
	lock.Release()
	if err != nil || result == nil || !result.Finished() || !result.AppLockFenced() {
		t.Fatalf("successor did not reconcile the accepted scale: %+v %v", result, err)
	}
	final, err := operations.GetOperation(ctx, id)
	counts, countErr := operations.DesiredReplicaCounts(ctx, app, region)
	if err != nil || countErr != nil || final.Status != model.OperationSucceeded || final.Metadata["externalEffectRecoveryPending"] == true || counts["web"] != 2 {
		t.Fatalf("durable receipt and intent: %+v %v %v %v", final, counts, err, countErr)
	}
	status, err := direct.JobScaleStatusRegion(jobID, "global")
	if err != nil || status.TaskGroups["web"].Running < 2 || status.TaskGroups["web"].Healthy < 2 {
		t.Fatalf("actual Nomad placement: %+v %v", status, err)
	}
	matchedEvents := 0
	for _, event := range status.TaskGroups["web"].Events {
		if event.Meta != nil && fmt.Sprint(event.Meta["norn.operationId"]) == id {
			matchedEvents++
		}
	}
	if matchedEvents != 1 {
		t.Fatalf("Nomad has %d scale events for one accepted operation, want exactly one", matchedEvents)
	}
	running := waitForDistinctNomadPlacement(t, direct.API(), jobID, "web", 2, "")

	// A fresh store instance must observe the committed desired intent. This
	// guards against accidentally proving only an in-memory worker projection.
	reopened, err := etcdstore.NewV3OperationStore(ec, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.DesiredReplicaCounts(ctx, app, region)
	if err != nil || persisted["web"] != 2 {
		t.Fatalf("reopened durable scale intent: %+v %v", persisted, err)
	}

	// Drain one client that owns an allocation. Nomad must replace it on the
	// third disposable client while preserving the accepted count and the
	// distinct-host constraint; this is scheduler recovery, not provider scale.
	drainedNode := running[0].NodeID
	if _, err := direct.API().Nodes().UpdateDrain(drainedNode, &nomadapi.DrainSpec{Deadline: 30 * time.Second}, false, nil); err != nil {
		t.Fatal(err)
	}
	drainActive := true
	t.Cleanup(func() {
		var cleanupErr error
		if drainActive {
			_, cleanupErr = direct.API().Nodes().UpdateDrain(drainedNode, nil, true, nil)
		}
		if cleanupErr != nil {
			t.Errorf("clear node drain: %v", cleanupErr)
		}
	})
	waitForDistinctNomadPlacement(t, direct.API(), jobID, "web", 2, drainedNode)
	status, err = direct.JobScaleStatusRegion(jobID, "global")
	if err != nil || status.TaskGroups["web"].Desired != 2 {
		t.Fatalf("drain changed accepted replica intent: %+v %v", status, err)
	}
	matchedEvents = 0
	for _, event := range status.TaskGroups["web"].Events {
		if event.Meta != nil && fmt.Sprint(event.Meta["norn.operationId"]) == id {
			matchedEvents++
		}
	}
	if matchedEvents != 1 {
		t.Fatalf("drain caused %d scale events for one accepted operation, want exactly one", matchedEvents)
	}
	if _, err := direct.API().Nodes().UpdateDrain(drainedNode, nil, true, nil); err != nil {
		t.Fatal(err)
	}
	drainActive = false

	// Exercise an independently admitted downscale through the same HTTP
	// surface after scheduler recovery, then require durable intent and one
	// exact Nomad scaling event for that operation as well.
	down := acceptScale(1, "real-nomad-scale-down")
	claimed, downClaim, err := operations.ClaimNextOperation(ctx, "downscale-worker", time.Minute, []string{"app.scale"})
	if err != nil || claimed == nil || claimed.ID != down.ID {
		t.Fatalf("downscale claim: %+v %v", claimed, err)
	}
	lock, acquired, err = operations.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("downscale lock: %t %v", acquired, err)
	}
	deadline = time.Now().Add(45 * time.Second)
	for {
		result, err = runtime.ExecuteOperationWithAppLock(ctx, claimed, downClaim, lock)
		if err == nil && result != nil && result.Finished() && result.AppLockFenced() {
			break
		}
		if time.Now().After(deadline) {
			lock.Release()
			t.Fatalf("downscale did not converge: %+v %v", result, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	lock.Release()
	waitForDistinctNomadPlacement(t, direct.API(), jobID, "web", 1, "")
	reopened, err = etcdstore.NewV3OperationStore(ec, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err = reopened.DesiredReplicaCounts(ctx, app, region)
	downFinal, finalErr := reopened.GetOperation(ctx, down.ID)
	if err != nil || finalErr != nil || persisted["web"] != 1 || downFinal.Status != model.OperationSucceeded {
		t.Fatalf("durable downscale receipt and intent: operation=%+v counts=%+v err=%v finalErr=%v", downFinal, persisted, err, finalErr)
	}
	status, err = direct.JobScaleStatusRegion(jobID, "global")
	if err != nil || status.TaskGroups["web"].Desired != 1 {
		t.Fatalf("downscale scheduler intent: %+v %v", status, err)
	}
	downEvents := 0
	for _, event := range status.TaskGroups["web"].Events {
		if event.Meta != nil && fmt.Sprint(event.Meta["norn.operationId"]) == down.ID {
			downEvents++
		}
	}
	if downEvents != 1 {
		t.Fatalf("Nomad has %d scale events for accepted downscale, want exactly one", downEvents)
	}
}

func waitForDistinctNomadPlacement(t *testing.T, api *nomadapi.Client, jobID, group string, count int, excludedNode string) []*nomadapi.AllocationListStub {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		allocations, _, err := api.Jobs().Allocations(jobID, true, &nomadapi.QueryOptions{Region: "global"})
		running := make([]*nomadapi.AllocationListStub, 0, count)
		nodes := make(map[string]struct{}, count)
		for _, allocation := range allocations {
			if allocation != nil && allocation.TaskGroup == group && allocation.DesiredStatus == nomadapi.AllocDesiredStatusRun && allocation.ClientStatus == nomadapi.AllocClientStatusRunning {
				running = append(running, allocation)
				nodes[allocation.NodeID] = struct{}{}
			}
		}
		_, excluded := nodes[excludedNode]
		if err == nil && len(running) == count && len(nodes) == count && (excludedNode == "" || !excluded) {
			return running
		}
		if time.Now().After(deadline) {
			t.Fatalf("running allocations=%d distinct nodes=%d excluded node present=%t, want %d distinct placements (err=%v)", len(running), len(nodes), excluded, count, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
