package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

func TestEtcdFleetAppTargetOperatorConfiguration(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	prefix := "/norn-test/fleet-app-target/" + uuid.NewString()
	defer client.Delete(context.Background(), prefix, clientv3.WithPrefix())
	signer, err := store.NewHMACAcceptanceSigner("fleet-target-operator-test-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := etcdstore.NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cluster.yaml")
	document := "apiVersion: norn.dev/fleet/v1\nkind: Cluster\nmetadata:\n  repository: example/norn-fleet\n  environment: staging\n  workflowURL: https://example.test/apply\ncluster:\n  name: norn-staging\n  provider: digitalocean\n  region: nyc3\nnodePools:\n  control:\n    size: s-2vcpu-4gb\n    min: 3\n    desired: 3\n    max: 5\n    replacement:\n      strategy: blueGreen\n      requireCapacityHeadroom: true\n      requireReadiness: true\n      drainTimeout: 15m\n"
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, "pilot")
	if err := os.Mkdir(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte("name: pilot\ndeploy: true\nregions:\n  west:\n    nomadRegion: global\n    datacenters: [dc1, dc2]\nprocesses:\n  web:\n    command: sleep 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Environment: "staging", EnvironmentExplicit: true, FleetConfig: path, AppsDir: appsDir}
	router := chi.NewRouter()
	router.Get("/api/v1/apps/{id}/fleet-target", etcdFleetAppTargetRead(cfg, operations))
	router.Put("/api/v1/apps/{id}/fleet-target", etcdFleetAppTargetConfigure(cfg, operations))
	url := "/api/v1/apps/pilot/fleet-target"
	put := func(body string, principal *handler.AccessPrincipal) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, url, strings.NewReader(body))
		if principal != nil {
			request = handler.WithAccessPrincipal(request, principal)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	operator := &handler.AccessPrincipal{TokenID: "operator-token", Scopes: []string{handler.ScopePlatformOperate}}
	body := `{"expectedRevision":0,"region":"west","nomadRegion":"global","datacenters":["dc2","dc1"]}`
	if response := put(body, nil); response.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated configure status=%d", response.Code)
	}
	if response := put(body, &handler.AccessPrincipal{TokenID: "ci-token", Scopes: []string{handler.ScopePlatformOperate}, CI: &handler.CIIdentity{}}); response.Code != http.StatusForbidden {
		t.Fatalf("CI configure status=%d", response.Code)
	}
	if response := put(`{"expectedRevision":0,"region":"west","nomadRegion":"global","datacenters":["dc3"]}`, operator); response.Code != http.StatusConflict {
		t.Fatalf("mismatched app placement status=%d body=%s", response.Code, response.Body.String())
	}
	created := put(body, operator)
	if created.Code != http.StatusCreated {
		t.Fatalf("configure status=%d body=%s", created.Code, created.Body.String())
	}
	var receipt struct {
		Target   store.FleetAppTarget `json:"target"`
		Revision int64                `json:"revision"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &receipt); err != nil || receipt.Revision <= 0 || receipt.Target.Cluster != "norn-staging" || receipt.Target.FleetEnvironment != "staging/nyc3" || receipt.Target.Generation != 1 || strings.Join(receipt.Target.Datacenters, ",") != "dc1,dc2" {
		t.Fatalf("configured target=%+v err=%v", receipt, err)
	}
	if response := put(body, operator); response.Code != http.StatusConflict {
		t.Fatalf("replayed create status=%d", response.Code)
	}
	read := httptest.NewRecorder()
	router.ServeHTTP(read, httptest.NewRequest(http.MethodGet, url, nil))
	if read.Code != http.StatusOK {
		t.Fatalf("target read status=%d", read.Code)
	}
	replacement := `{"expectedRevision":` + strconv.FormatInt(receipt.Revision, 10) + `,"region":"west","nomadRegion":"global","datacenters":["dc1","dc2"]}`
	if response := put(replacement, operator); response.Code != http.StatusOK {
		t.Fatalf("replace status=%d body=%s", response.Code, response.Body.String())
	}
	if response := put(replacement, operator); response.Code != http.StatusConflict {
		t.Fatalf("stale replace status=%d", response.Code)
	}
}
