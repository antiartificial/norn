package main

import (
	"context"
	"fmt"
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

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
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
	job.AddTaskGroup(nomadapi.NewTaskGroup("web", 1).AddTask(
		nomadapi.NewTask("sleep", "raw_exec").SetConfig("command", "/bin/sleep").SetConfig("args", []string{"300"}),
	))
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
	runtime, err := newEtcdScaleRuntime(&config.Config{Environment: "staging", AppsDir: t.TempDir(), NomadAddr: proxied.URL}, operations)
	if err != nil {
		t.Fatal(err)
	}
	runtime.resolveTarget = func(_ context.Context, gotApp, environment, gotRegion string) (string, string, error) {
		if gotApp != app || environment != "staging" || gotRegion != region {
			return "", "", fmt.Errorf("wrong route")
		}
		return jobID, deploymentID, nil
	}
	id := uuid.NewString()
	op := model.Operation{ID: id, Kind: "app.scale", App: app, Ref: region + "/web", Status: model.OperationQueued, MaxAttempts: 1,
		Payload: map[string]interface{}{"app": app, "group": "web", "region": region, "nomadRegion": "global", "count": 2, "nomadJobId": jobID, "deploymentId": deploymentID}}
	acceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: op.Kind, Resource: "app/widgets", Key: id}, Operation: op, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"app": app, "count": 2}}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
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
	if err != nil || countErr != nil || final.Status != model.OperationSucceeded || counts["web"] != 2 {
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
}
