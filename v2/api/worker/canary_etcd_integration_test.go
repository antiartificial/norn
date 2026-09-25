package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// This runs the signed etcd operation through a real worker and fake Nomad
// HTTP boundary. The first promotion reaches Nomad but its HTTP response is
// lost; a successor claim must observe the exact deployment and must not POST
// a second promotion.
func TestBackendNeutralCanaryPromotionRecoversAmbiguousNomadWriteEtcd(t *testing.T) {
	t.Setenv("NORN_DATABASE_URL", "postgres://poisoned.invalid:1/never-open")
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx := context.Background()
	etcd, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = etcd.Close() })
	prefix := "/norn-tests/canary-worker/" + uuid.NewString()
	t.Cleanup(func() { _, _ = etcd.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-canary-worker-signing-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(etcd, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	effectStore, err := etcdstore.NewV3CanaryEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	var promoted atomic.Bool
	nomadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/deployment/promote/deployment-accepted":
			posts.Add(1)
			promoted.Store(true)
			// Simulate a committed Nomad mutation whose acknowledgement was
			// lost before Norn could persist the launch identity.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v1/deployment/deployment-accepted":
			if !promoted.Load() {
				_ = json.NewEncoder(w).Encode(&nomadapi.Deployment{ID: "deployment-accepted", JobID: "widgets", Status: "running",
					TaskGroups: map[string]*nomadapi.DeploymentState{"web": {DesiredCanaries: 1, PlacedCanaries: []string{"alloc-1"}, HealthyAllocs: 1}}})
				return
			}
			_ = json.NewEncoder(w).Encode(&nomadapi.Deployment{ID: "deployment-accepted", JobID: "widgets", Status: "successful",
				TaskGroups: map[string]*nomadapi.DeploymentState{"web": {PlacedCanaries: []string{"alloc-1"}, Promoted: true}}})
		default:
			http.Error(w, "unexpected Nomad request", http.StatusNotFound)
		}
	}))
	t.Cleanup(nomadServer.Close)
	nomadClient, err := nomad.NewClient(nomadServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := pipeline.NewNomadCanaryPromotionEffectsWithStore(effectStore, nomadClient)
	if err != nil {
		t.Fatal(err)
	}
	pipe := &pipeline.Pipeline{OperationStore: operations, CanaryPromotionEffects: effects}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.canary-promote", App: "widgets", Status: model.OperationQueued, MaxAttempts: 1,
		Payload: map[string]interface{}{"app": "widgets", "region": "us-central", "nomadRegion": "global", "deploymentId": "deployment-accepted"}}
	acceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: operation.Kind, Resource: operation.App, Key: uuid.NewString()},
		Operation: operation, Audit: store.AcceptanceAuditContext{Source: "canary-etcd-integration"}, Semantics: map[string]interface{}{"app": operation.App, "region": "us-central", "nomadRegion": "global", "deploymentId": "deployment-accepted"}}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	worker := NewOperationWorkerForKinds(operations, pipe, []string{"app.canary-promote"})
	if err := worker.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	deferred, err := operations.GetOperation(ctx, operation.ID)
	if err != nil || deferred.Status != model.OperationQueued || deferred.Metadata["externalEffectRecoveryPending"] != true || posts.Load() != 1 {
		t.Fatalf("ambiguous first run = %+v err=%v posts=%d", deferred, err, posts.Load())
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err := worker.runOnce(ctx); err != nil {
			t.Fatal(err)
		}
		current, err := operations.GetOperation(ctx, operation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status.Terminal() {
			if current.Status != model.OperationSucceeded || posts.Load() != 1 || current.Metadata["effectId"] == nil {
				t.Fatalf("recovered operation = %+v posts=%d", current, posts.Load())
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("ambiguous canary promotion did not reconcile before deadline")
}
