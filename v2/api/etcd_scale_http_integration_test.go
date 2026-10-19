package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestEtcdScaleHTTPAcceptsPinnedManagedJobAndRejectsStaleWorker(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx := context.Background()
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-tests/scale-http/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-scale-http-signing-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	identities := etcdstore.NewAuthStore(client, prefix)
	rootID := uuid.NewString()
	now := time.Now().UTC()
	if err := identities.RecordAccessToken(ctx, &store.AccessToken{JTI: rootID, Subject: "operator", Scopes: []string{handler.ScopeAPIRead, handler.ScopeAPIWrite}, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	apps := t.TempDir()
	if err := os.Mkdir(filepath.Join(apps, "widgets"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := "name: widgets\ndeploy: true\nregions:\n  us-central:\n    nomadRegion: global\n    datacenters: [dc1]\nprocesses:\n  web:\n    command: sleep 60\n"
	if err := os.WriteFile(filepath.Join(apps, "widgets", "infraspec.yaml"), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	target := store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: "widgets", ControlEnvironment: "staging", Cluster: "fleet", FleetEnvironment: "staging/us-central", Region: "us-central", NomadRegion: "global", Datacenters: []string{"dc1"}, Generation: 1}
	if _, err := operations.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	const managedJob = "norn-job-pinned-deployment"
	currentJob := managedJob
	nomadWrites := 0
	nomadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			nomadWrites++
			http.Error(w, "unexpected write", http.StatusConflict)
			return
		}
		if strings.Contains(r.URL.Path, managedJob) && strings.HasSuffix(r.URL.Path, "/scale") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"JobID": managedJob, "TaskGroups": map[string]interface{}{"web": map[string]int{"Desired": 2, "Placed": 2, "Running": 2, "Healthy": 2}}})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(nomadServer.Close)
	cfg := &config.Config{Environment: "staging", AppsDir: apps, NomadAddr: nomadServer.URL}
	runtime, err := newEtcdScaleRuntime(cfg, operations)
	if err != nil {
		t.Fatal(err)
	}
	runtime.resolveTarget = func(_ context.Context, app, environment, region string) (string, string, error) {
		if app != "widgets" || environment != "staging" || region != "us-central" {
			return "", "", fmt.Errorf("wrong target")
		}
		return currentJob, "deployment-pinned", nil
	}
	router := chi.NewRouter()
	router.Post("/api/v1/apps/{id}/scale", etcdScaleApp(cfg, operations, identities, runtime))
	router.Get("/api/v1/apps/{id}/scale-status", etcdScaleStatus(cfg, operations, runtime))
	router.Get("/api/v1/operations/{id}", etcdFleetOperation(operations, false))
	principal := &handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, TokenID: rootID, Scopes: []string{handler.ScopeAPIWrite, handler.ScopeAPIRead}}
	request := func(method, path, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		req := handler.WithAccessPrincipal(httptest.NewRequest(method, path, strings.NewReader(body)), principal)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	const path = "/api/v1/apps/widgets/scale"
	first := request(http.MethodPost, path, `{"group":"web","region":"us-central","count":3}`, "same-intent")
	if first.Code != http.StatusAccepted {
		t.Fatalf("scale admission=%d %s", first.Code, first.Body.String())
	}
	var accepted model.Operation
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil || accepted.ID == "" || accepted.Payload["nomadJobId"] != managedJob {
		t.Fatalf("accepted target=%+v err=%v", accepted, err)
	}
	if got := request(http.MethodGet, "/api/v1/operations/"+accepted.ID, "", ""); got.Code != http.StatusOK {
		t.Fatalf("operation status=%d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, "/api/v1/apps/widgets/scale-status", "", ""); got.Code != http.StatusOK {
		t.Fatalf("scale status=%d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, path, `{"group":"web","region":"us-central","count":3}`, "same-intent"); got.Code != http.StatusOK {
		t.Fatalf("replay=%d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, path, `{"group":"web","region":"us-central","count":2}`, "same-intent"); got.Code != http.StatusConflict {
		t.Fatalf("conflicting replay=%d %s", got.Code, got.Body.String())
	}
	claimed, claim, err := operations.ClaimNextOperation(ctx, "scale-worker", time.Minute, []string{"app.scale"})
	if err != nil || claimed == nil || claim.OperationID() != accepted.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if _, err := runtime.ExecuteOperationWithAppLock(ctx, claimed, claim, nil); err == nil {
		t.Fatal("accepted mutation without app-lock fence")
	}
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, "widgets")
	if err != nil || !acquired {
		t.Fatalf("app lock=%t err=%v", acquired, err)
	}
	defer lock.Release()
	currentJob = "norn-job-other-deployment"
	if _, err := runtime.ExecuteOperationWithAppLock(ctx, claimed, claim, lock); err == nil || nomadWrites != 0 {
		t.Fatalf("stale route reached Nomad: err=%v writes=%d", err, nomadWrites)
	}
}
