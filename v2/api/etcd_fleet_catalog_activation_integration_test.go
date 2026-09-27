package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/database"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

func TestEtcdFleetCatalogActivationThroughWorker(t *testing.T) {
	t.Setenv("NORN_DATABASE_URL", "postgres://poisoned.invalid:1/never-open")
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/fleet-catalog-activation/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("fleet-catalog-activation-test-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStoreWithPolicy(client, prefix, authority, signer, store.AcceptancePolicy{ReplayTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	activate := etcdFleetCatalogActivation(operations)
	body, err := json.Marshal(etcdCatalogActivationRequest{ExpectedRevision: new(int64), Catalog: &database.Catalog{APIVersion: database.APIVersion}})
	if err != nil {
		t.Fatal(err)
	}
	call := func(key string, payload []byte, scopes ...string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/database/catalog/activations", bytes.NewReader(payload))
		request.Header.Set("Idempotency-Key", key)
		request = handler.WithAccessPrincipal(request, &handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, TokenID: "catalog-operator", Scopes: scopes})
		response := httptest.NewRecorder()
		activate(response, request)
		return response
	}
	if response := call("catalog-once", body, handler.ScopeAPIWrite); response.Code != http.StatusForbidden {
		t.Fatalf("scope=%d", response.Code)
	}
	first := call("catalog-once", body, handler.ScopePlatformOperate)
	if first.Code != http.StatusAccepted || strings.Contains(first.Body.String(), `"catalog":`) {
		t.Fatalf("accepted=%d %s", first.Code, first.Body)
	}
	var accepted model.Operation
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil || accepted.ID == "" {
		t.Fatalf("accepted body: %s %v", first.Body, err)
	}
	replay := call("catalog-once", body, handler.ScopePlatformOperate)
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), accepted.ID) {
		t.Fatalf("replay=%d %s", replay.Code, replay.Body)
	}
	changed := []byte(`{"expectedRevision":1,"catalog":{"apiVersion":"norn.database/v1alpha1","services":[],"bindings":[],"profiles":[]}}`)
	if response := call("catalog-once", changed, handler.ScopePlatformOperate); response.Code != http.StatusConflict {
		t.Fatalf("changed key=%d %s", response.Code, response.Body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := worker.NewOperationWorkerForKinds(operations, &pipeline.EtcdCatalogExecutor{Catalog: operations}, []string{pipeline.CatalogActivationKind})
	go w.Run(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := operations.GetOperation(context.Background(), accepted.ID)
		if err == nil && operation.Status == model.OperationSucceeded {
			active, err := operations.ActiveDatabaseCatalog(context.Background())
			if err != nil || active.Revision != 1 {
				t.Fatalf("active: %+v %v", active, err)
			}
			if response := call("catalog-once", body, handler.ScopePlatformOperate); response.Code != http.StatusOK {
				t.Fatalf("terminal replay=%d %s", response.Code, response.Body)
			}
			if response := call("catalog-stale", body, handler.ScopePlatformOperate); response.Code != http.StatusConflict {
				t.Fatalf("stale revision=%d %s", response.Code, response.Body)
			}
			if _, err := client.Put(context.Background(), prefix+"/v3/database-catalog/active", "malformed"); err != nil {
				t.Fatal(err)
			}
			if response := call("catalog-corrupt", changed, handler.ScopePlatformOperate); response.Code != http.StatusServiceUnavailable {
				t.Fatalf("corrupt active pointer=%d %s", response.Code, response.Body)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("catalog activation worker did not finish")
}
